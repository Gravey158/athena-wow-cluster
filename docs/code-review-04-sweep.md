# Code-Review 04 — char/chat/servers-registry + gateway sweep (Sprint 6+7)

Date: 2026-05-13
Scope: third pass through the codebase, this time hitting the areas that the first two reviews left untouched — charserver internals, chatserver internals beyond the in-mem repo, servers-registry callback paths, and the gateway/session leaf files (battleground, mail, social) plus the gateway main and the events-broadcaster fanout.

Found **14 bugs (B55–B68)**. Real ones — these aren't refactor nits. The broadcaster bug (B68) is a cross-player DoS that one slow consumer can use to stall event delivery for every other player on the gateway; the SMSG_WHO + mail PropertySeed bugs (B62, B64) are wire-protocol mismatches that quietly corrupt UI state.

## Bugs

### B55 — `charserver/repo/online-characters_inmem.go` — name-key case mismatch leak (HIGH)

`charactersOnlineInMem.Add` stored:
```go
c.nameStorage[character.RealmID][strings.ToUpper(character.CharName)] = character
```
but `Remove` and `RemoveAllWithGatewayID` deleted by the raw `char.CharName`. The key never matched, so:

- the `nameStorage` map grew without bound (every logged-out character left a dead entry behind),
- `OneByRealmAndName` happily returned the stale `*Character` of a logged-out player as if they were still online — including their stale `*MsgSender` for whispers,
- under sustained churn the heap kept growing linearly with all-time logins.

Fixed by `strings.ToUpper`'ing the delete key to match. (Commit `f1bdb57`.)

### B56 — `charserver/cmd/main.go` — no SIGTERM handler

charserver simply called `grpcServer.Serve(lis)` and `panic(err)` on error. No `signal.Notify`, no `GracefulStop`. Container restarts hard-killed in-flight gRPC writes. Added the standard handler. (Commit `f1bdb57`.)

### B57 — `charserver/service/friends-cache.go` — `SetFriendsService` no-lock write

`SetFriendsService` wrote `o.friendsService = friendsService` unlocked. `HandleCharacterLoggedIn/Out` read it unlocked. The current main.go calls SetFriendsService before Listen() so the race is theoretical, but the ordering is fragile. Gated via the existing `cacheMutex` with a small `getFriendsService()` helper. (Commit `f1bdb57`.)

### B58 — `chatserver/service/*-listener.go` — three listeners used `context.TODO()`

All three chatserver NATS listeners — `CharactersListener`, `ChannelsListener`, `ServersRegistryListener` — ran their callback DB writes with `context.TODO()` despite the main.go already having a `mainContext` for graceful shutdown. SIGTERM could not abort:

- `RemoveCharactersWithRealm` (full realm purge)
- `GetOrCreateChannel` (read + INSERT)
- `JoinChannel`/`LeaveChannel` (member write-through)

Threaded `ctx` through all three constructors. (Commit `7b0a6d9`.)

### B59 — `charserver/cmd/main.go` + service listeners — missing mainContext

charserver/main.go had no `mainContext` (was added with just a signal handler in B56). Its `ServersRegistryListener` did `RemoveAllWithGatewayID(context.TODO(), ...)` in both NATS callbacks — same shape as B58. Added `mainContext`, threaded through, and (for consistency) also wired the orphan `service.NewCharactersListener` even though no current caller wires it up — the shared/events GatewayConsumer replaced it. (Commit `0b434ae`.)

### B60 — `servers-registry/service/{game,gateway}-server.go` — 7 `context.Background()` sites in callback paths

`gameServerImpl` and `gatewayImpl` both register callback observers with the healthchecker and metrics consumer. Those callbacks (`onServerUnhealthy`, `onMetricsUpdate`) invoke DB writes — `r.Remove`, `r.Update`, `distributeMapsToServers`, etc. — and previously all of them hardcoded `context.Background()`/`TODO()`. SIGTERM could not abort the realm-wide map redistribution path triggered by a single gameserver going unhealthy at shutdown.

Stored the ctx the constructors already received in B4 onto the struct, routed every callback through it. (Commit `e0deeac`.)

### B61 — `servers-registry/service/game-server.go:189` — dead `return append(result), nil`

go vet flagged `append with no values` — a bare `append(result)` returns its first arg unchanged. Replaced with `return result, nil`. (Commit `e0deeac`.)

### B62 — `gateway/session/social.go` — SMSG_WHO wrote race twice instead of race + gender (HIGH-impact UX)

WoW 3.3.5a `SMSG_WHO_LIST` per-item layout is:

```
CString name + CString guild + u32 level + u32 class + u32 race +
u8 gender + u32 zone
```

The code wrote:

```go
w.Uint32(item.Race)
w.Uint8(uint8(item.Race))   // bug: race-as-gender
w.Uint32(item.ZoneID)
```

The byte width is correct so the rest of the packet doesn't shift, but every `/who` result rendered the wrong gender icon — race=1 (Human) became gender=1 (Female), race=2 (Orc) wrapped to garbage. The proto already exposes `WhoQueryResponse_WhoItem.Gender`; just use it. (Commit `2149eb5`.)

### B63 — `gateway/session/battleground.go` — six A4-missed `gateway.RealmID` reads

The A4 refactor (commit `6e2fb24`) moved package-global `gateway.RealmID` reads to per-session `s.realmID`. battleground.go was missed: 5 protobuf RealmID fields plus the crossrealm GUID derivation still read the package global. Mostly latent (single-realm gateway today), but the inconsistency would have broken multi-realm and was awkward when nothing else in `session/` looked like that. Fixed all six sites. (Commit `2e466fa`.)

### B64 — `gateway/session/mail.go` — SMSG_MAIL_LIST attachment block wrote RandomPropertyID twice

The mail attachment sub-block per WoW 3.3.5a:

```
... + u32 randomPropertyID + u32 SuffixFactor + u32 count + ...
```

The code wrote `RandomPropertyID` for both slots. Items with a non-zero `PropertySeed` (= SuffixFactor) rendered the wrong magical suffix. proto already had `PropertySeed`; just use it. Same kind of mistake as B62 — bytes counted right, semantics wrong. (Commit `2e466fa`.)

### B65 — `chatserver/server/chat-channels.go` — fire-and-forget goroutine used Background

`SendChannelMessage` spawned a `go func() { UpdateLastUsed(context.Background(), ...) }()` after sending. On SIGTERM the goroutine kept writing to MySQL past the container kill window. Added a `ctx` field on `ChatService` (set from main's `mainContext` via `NewChatService`) and used it for the bg write. (Commit `4c00007`.)

### B66 — `chatserver/service/channel-manager.go:947` — `ctx := context.Background()` for logout transfer

`TransferOwnershipOnLogout` created its own `Background()` ctx with a comment justifying it. Same problem as B65: a logging-out owner triggers per-channel `UpdateMemberFlags` writes that can't be cancelled. Promoted ctx to a function parameter; the only caller (`CharactersListener.HandleCharacterLoggedOut`) already has `c.ctx` from B58. (Commit `4c00007`.)

### B67 — `gateway/cmd/gateway/main.go` — no SIGTERM handler at all (HIGH)

The gateway, the highest-traffic process in the cluster, had no signal handler. `charsUpdsBarrier.Run(context.TODO())` ticked forever, `realmNamesService` used `Background()` for its init query, and per-connection `ListenAndProcess(context.Background())` had no shutdown path. SIGTERM landed and the runtime hard-killed every active player session.

Added the standard `mainContext`/`mainCancel` + signal handler that closes the listening socket and cancels the context. Threaded `mainContext` through:

- `charsUpdsBarrier.Run`
- `service.NewRealmNamesService` init
- `gamesocket.ListenAndProcess` — B54's GameSocket watcher goroutine picks up the parent cancel and fires `s.cancel()`, which unblocks every pending `SendOrCancel` across all sessions.

The Accept loop now exits cleanly on `mainContext.Err() != nil` after the signal handler closes the listener. (Commit `836359f`.)

### B68 — `gateway/events-broadcaster/broadcaster.go` — blocking sends across 32 fanout sites (HIGH)

Every broadcaster method did:

```go
ch <- Event{Type: ..., Payload: payload}
```

…as a blocking send to a per-session channel (buffer 100, set in `RegisterCharacter`). The broadcaster goroutine runs from NATS callbacks. If a single session's consumer wedged — slow gameSocket, stuck goroutine, anything — the channel buffer would fill and the broadcaster would block indefinitely. That single stuck player would stall NATS event delivery for **every other player** on the gateway: a cross-player DoS via slow consumer.

Wrapped all 32 sites with a `sendOrDrop(ch, ev)` helper that does a non-blocking `select`+`default` and logs the dropped event. The affected session loses one notification (and recovers from the next event); every other player continues to receive events normally.

(The sister broadcaster `events-broadcaster/chat-channels.go:191` already used the correct pattern — that one was right.)

(Commit `6cd2bfe`.)

## Cross-cutting

Same three patterns as review-03, more sites:

- `context.TODO()` / `context.Background()` for callbacks with no caller ctx — B58, B59, B60, B65, B66, B67.
- Missing SIGTERM handler — B56, B59 (charserver), B67 (gateway).
- Mutex coverage gaps on once-set state — B57.

Two new pattern classes from this sweep:

- **Wire-protocol field-position bugs** (B62 SMSG_WHO, B64 mail PropertySeed). Both copy-paste mistakes where the proto field name almost matched the field next to it. Worth a grep across other SMsg/CMsg writers for similar `w.Uint8(uint8(item.X))` patterns.
- **Blocking fan-out** (B68). Anywhere we have one producer goroutine writing to many consumer-owned channels, the default `ch <-` is dangerous because one slow consumer poisons all the others. The fix is uniform — non-blocking send + log on drop — and worth checking in any new fanout code.

## Deferred (post-Sprint-7)

- group/guild in-memory cache pointer-aliasing — still needs a redesign ADR.
- `apps/charserver/repo/online-characters_inmem.go:119,139` use `context.TODO()` for in-mem mutations. The repo is purely in-mem under mutex so ctx is cosmetic. Leave.
- `apps/matchmakingserver/service/crossrealm-nodes-tracker.go:32` and `battleground_service.go:1019` use `context.Background()` at startup or for in-mem template lookup. Cosmetic.
- B25 (gateway 1s sleep after CharDelete) and B27 (500ms post-redirect channel-rejoin) — still need AC ack opcodes.

## Verification

For each affected package: `go build ./apps/<svc>/...`, `go vet ./apps/<svc>/...`, and `go test -race -count=1 ./apps/<svc>/...` all green at commit time. The gateway's `gamesocket` race detected in the B52 sweep was fixed in B54 (commit `b466af0`) before this sprint.
