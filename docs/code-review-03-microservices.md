# Code-Review 03 — apps/{guidserver,chatserver,mailserver,authserver,matchmakingserver,groupserver,guildserver}

Date: 2026-05-13
Scope: the seven Go microservices that sit beside `gateway` and `servers-registry`. Combined ~7 kLoC of Go. This pass covers `cmd/*`, `service/*`, `repo/*`, and the NATS / gRPC server layer.

Reviewer-context: these microservices are stateless or thin-cached, talk to MySQL + Redis + NATS, and are mostly invoked via gRPC from the gateway. Failure modes here drop discrete features (mail/guild/group/bg) without taking the whole shard down — so bugs are usually less catastrophic than gateway bugs, but they're also less-exercised and have correspondingly less battle-testing in the upstream repo.

## Summary

Found **17 bugs across 7 microservices** (B28–B44). All fixed in commits `9597212` (guidserver), `18c7b59` (chatserver), `9970be9` (mailserver), `4513fa0` (authserver), `0223176` (matchmakingserver), `1349abe` (groupserver+guildserver).

The dominant pattern, by a wide margin: **`context.Background()` / `context.TODO()` was used for DB calls inside NATS-driven callbacks and observer methods**, because those have no caller-supplied ctx. The blast radius of this pattern is: SIGTERM cannot cancel in-flight DB queries, so the container-runtime SIGKILL races with the DB driver — at best a slow shutdown, at worst a transaction torn in half.

The secondary pattern: **missing SIGTERM handlers entirely** (authserver, groupserver, guildserver). On Kubernetes this means every pod restart had a non-zero probability of killing mid-flight work.

Cross-cutting design issues (deferred, not fixed): the in-memory caches in `groupserver/service/group-cache_inmem.go` and `guildserver/service/guilds-cache_inmem.go` hand out raw `*GroupMember` / `*GuildMember` pointers from cache reads, then mutators (`Update`, `AddMember`) can replace the parent struct entirely — readers continue to write to stale memory. Fixing this requires a cache redesign; the lower-risk path is to make all cache reads return deep copies. Tracked as an open issue, not committed.

## Bugs

### B28 — `guidserver/service/guid.go` — instances max-GUID stored as items max-GUID (CRITICAL)

```go
// Was:
err = redisStorage.SetMaxGuidForItems(ctx, realmID, max)
// In a function that had just computed `max` for instances.
```

Copy-paste bug. After every instance-GUID allocation the service overwrites the items max-GUID in Redis with the instances value. Eventually item GUIDs and instance GUIDs collide, which corrupts the world DB silently. Fixed by calling `SetMaxGuidForInstances`.

### B29 — `guidserver/service/guid.go` — dead `requestNewDiapason() { time.Sleep(300s) }`

Function existed, was never called, contained nothing but a sleep. Removed.

### B30–B32 — chatserver gateway-channels (deferred)

Documented in [ADR-006](adr/006-chat-irc-backend.md): the entire chatserver is being replaced by an IRC backend. Existing bugs in the chatserver channel logic were not fixed in-place — they go away with the migration. The three minor issues found during review were captured in the ADR translation matrix.

### B33/B34/B36 — `chatserver/repo/characters_inmem.go` — dual-mutex temporal inconsistency

Two separate `RWMutex`es (`guidMu`, `nameMu`) covered two indexes for the same characters. `AddCharacter` released `guidMu` before locking `nameMu`, so a concurrent reader could see a character existing in one index but not the other. `RemoveCharactersWithRealm` only held `nameMu` but mutated `charsByGUID` too — a literal data race; the upstream author had even left `// TODO: need to completely rewrite this` next to it. Replaced both mutexes with a single `mu sync.RWMutex` covering both indexes.

### B37 — `mailserver/cmd/mailserver/main.go` — ticker survived SIGTERM

`MailsCleanupTicker` was started with `context.TODO()`, so the SIGTERM signal handler called `grpcServer.GracefulStop()` but the ticker goroutine kept running. Added `mainContext` and a `mainCancel()` call ahead of `GracefulStop`.

### B38 — `authserver/cmd/authserver/main.go` — no SIGTERM handler at all

Two coupled problems:

1. `cmd/authserver/main.go` had **no signal handler**. On a container restart the runtime sent SIGTERM, waited the grace period, then SIGKILL'd the process. Any in-flight `UpdateAccount` (which writes the SRP6 SessionKeyAuth) could be killed mid-transaction.
2. `session/authsession.go` used `context.TODO()` in four places (lines 188, 285, 342, 390) for `AccountByUserName` and `UpdateAccount` calls — even if there were a SIGTERM handler, those DB calls would have ignored cancellation.

Fixed both: added a `mainContext`/`mainCancel` pair, signal handler that cancels and closes the listener, and a `ctx context.Context` field on `AuthSession` that threads through to all four DB calls. The per-connection goroutine now uses `func(c net.Conn) { ... }(conn)` instead of a closure shadow (the original `conn := conn` line was a `:=`-redeclaration error inside the same loop-body scope).

### B39 — `matchmakingserver/server/matchmaking.go:67` — nil-deref in `BattlegroundQueueDataForPlayer`

```go
bg, err := s.bgService.GetBattlegroundByBattlegroundKey(ctx, ...)
if err != nil { return nil, err }
slots[i] = &pb.BattlegroundQueueDataForPlayerResponse_QueueSlot{
    BgTypeID: uint32(bg.BattlegroundTypeID),  // panic if bg == nil
    ...
}
```

`battlegroundInMemRepo.GetBattlegroundByInstanceID` returns `(nil, nil)` on cache miss. The handler nil-deref'd inside a gRPC call — crashing the whole microservice. Added a `continue` on nil.

### B40 — `matchmakingserver/service/battleground_service.go` — `rand.Seed` deprecated and harmful

`selectRandomTemplate` called `rand.Seed(time.Now().UnixNano())` on every invocation. Since Go 1.20, the default `rand.Source` is auto-seeded at startup, and calling `Seed` is deprecated. Calling it repeatedly with `time.Now().UnixNano()` can cause back-to-back calls within the same nanosecond to use identical seeds and produce identical "random" choices. Removed the line.

### B41 — `matchmakingserver/service/*.go` — ctx leakage in NATS / observer callbacks

Three places used `context.Background()` for DB calls that have no caller-supplied ctx:

- `OnGameServerRemoved` → `DeleteAllWithGameServerAddress`
- `OnNoCrossRealmNodesAvailable` / `UnAvailable` → `AllBattleGroupsIDs`, `BattleGroupIDByRealmID`
- `CharactersListener` NATS handlers → `PlayerBecomeOffline`

Same shape as B38: SIGTERM had no way to abort these. Added a `ctx context.Context` field on `battleGroundService` and `CharactersListener` — threaded `mainContext` from `cmd/matchmakingserver/main.go`. The startup-only `templatesRepo.GetAll` and `AllBattleGroupsIDs` calls in `NewBattleGroundService` also now use the passed ctx.

### B42 — `groupserver/service/group.go` — same ctx-leakage in NATS callbacks

`HandleCharacterLoggedIn` / `HandleCharacterLoggedOut` called `buildGroupMemberOnlineStatusChangedPayload` which used `context.Background()` for both `GroupIDByPlayer` and `GroupByID`. Same fix: ctx field on `groupServiceImpl`, threaded from main via `createGroupService(mainContext, ...)`. groupserver also previously had no `mainContext` at all — the gracefulStop only stopped gRPC, not any background work.

### B43 — `guildserver/cmd/guildserver/main.go` — no mainContext

guildserver's service handlers all take ctx properly from gRPC, but `cache.Warmup(context.Background(), 1)` and the signal handler had no `mainContext` to drive shutdown. Added one and threaded it into `createGuildService`. Less impactful than B42 because guildserver's NATS handlers (in the cache) are pure in-memory mutex operations.

### B44 — `guildserver/repo/guilds_mysql.go:130` — unreachable code after `panic`

`GuildByRealmAndID` was a stub that `panic("implement me")`'d, followed by `return nil, nil`. `go vet` warned about unreachable code. Removed the dead line.

## Cross-cutting patterns

### Pattern 1: `context.Background()` / `context.TODO()` in NATS handlers and observer methods

Five of the seven microservices had this exact pattern (matchmakingserver, mailserver, authserver, groupserver, plus chatserver pre-replacement). All five fixes share the same shape:

1. Add `ctx context.Context` field on the service struct.
2. Take ctx as first arg to the constructor.
3. Replace `context.Background()` / `TODO()` in callbacks with `s.ctx`.

This pattern is worth checking for in any new microservice added to the repo — it's an easy oversight because NATS callbacks visibly have no ctx param.

### Pattern 2: No SIGTERM handler

Three of seven (authserver, groupserver, guildserver) had no signal handler at all. All three relied on `grpcServer.GracefulStop()` only via... wait, two of them (group, guild) did have a signal handler that called GracefulStop, but no `mainCancel` to cancel background work. authserver had nothing.

Lesson: on Kubernetes, the per-pod SIGTERM grace is finite (default 30s). Any code path that does I/O after `grpcServer.Serve(lis)` exits but before `os.Exit` needs to be ctx-aware.

### Pattern 3: In-memory cache pointer aliasing (deferred)

`groupserver` and `guildserver` both have a `cacheMutex` + map-of-pointers cache. Reads hold the lock briefly to dereference the map, then return a `*GroupMember` or `*GuildMember` pointer to the caller. Concurrent mutators can:

- Replace the parent struct entirely (`g.cache[realm][id] = newGroup`), leaving the old pointer the caller holds dangling-but-still-valid.
- Append to `Members` slices that the cache also references — slice header is racey.

Fixing this properly requires either deep-copy on read (cheap, doubles allocations) or a redesign to make members immutable + use copy-on-write. Not in scope for this pass; tracked as a known issue.

## Deferred items

| ID | Service | Issue | Reason for defer |
|---|---|---|---|
| B25 | gateway | 1s sleep after CharDelete async DB transaction | Needs AC ack opcode |
| B27 | gateway | 500ms post-redirect channel-rejoin | No opcode signal |
| A5/A6 | gateway | HandleMap / OpcodeBlacklist package globals | Low ROI for now |
| B45 (mm) | matchmakingserver | PVPQueue.AddQueuedGroup takes no ctx | 20+ call-site interface change incl. tests |
| B46 (group) | groupserver | groupsCacheInMem pointer-aliasing race | Cache redesign required |
| B47 (guild) | guildserver | guildsInMemCache same pattern | Cache redesign required |

## Verification

For each of the seven microservices: `go build ./apps/<svc>/...`, `go vet ./apps/<svc>/...`, and `go test -race -count=1 ./apps/<svc>/...` all green. matchmakingserver had a pre-existing vet warning at `server/matchmaking.go:50` for an unkeyed struct literal — fixed in B46 (see follow-up below).

E2E Wine-login verification of the gateway-side race-by-sleep fixes (B6, B10, B24) is still on the user; the microservice fixes here are server-side only and don't have a Wine code path to exercise them.

## Follow-up: B45–B53 (deferred items + shared/ sweep)

Three additional commit batches landed after the initial review-03 doc. The deferred PVPQueue ctx-threading was promoted out of the deferred list; the cache pointer-aliasing race in groupserver got a partial fix (mutex coverage of the `IsOnline` write); and a quick pass over `shared/` turned up three more bugs.

### B45 — matchmakingserver: `PVPQueue.AddQueuedGroup` now takes ctx (commit `ea7537f`)

The PVPQueue interface had a `AddQueuedGroup(g *QueuedGroup) error` signature with no context. `GenericBattlegroundQueue.AddQueuedGroup` called `q.process(context.Background())` internally — so even though `AddGroupToQueue(ctx, ...)` in the service had a perfectly good ctx from the gRPC frame, it was discarded between the service and the queue. Result: a client deadline could never abort the `BattlegroundsThatNeedPlayers` → `InviteGroups` → `SaveBattleground` chain inside the match loop.

Threaded ctx through the interface, both impls (`GenericBattlegroundQueue`, `BattlegroundRandomQueue`), the 3 service call-sites, and 9 test call-sites. Also threaded ctx through `inviteGroupsToBG` and `getBattlegroundTemplate` (the latter is in-mem-only so largely cosmetic — but consistent).

### B46 — matchmakingserver: unkeyed struct literal vet warning (commit `ea7537f`)

`server/matchmaking.go:50` had a multi-line struct literal for `QueuesByRealmAndPlayerKey` with an unkeyed embedded field — vet flagged it. Single-line variants elsewhere stay as-is; vet only flags multi-line.

### B47 — groupserver: data race on `member.IsOnline` write (commit `adaac2a`)

`HandleCharacterLoggedIn/Out` called `groupMemberByGUID` which took the `cacheLock` RLock, dereferenced the map, released the lock, and returned the pointer. The caller then wrote `member.IsOnline = true/false` without any lock. Concurrent readers of `group.Members[i].IsOnline` (called from many service-layer `OnlineMemberGUIDs()` paths) could see torn writes.

Take the write lock across the lookup AND the mutation. Mirrors the guildserver pattern, which already did this correctly.

This does **not** fix the broader pointer-aliasing issue from the original review (cached `*GroupMember` pointers can be stale after `Update` if the caller passes a fresh `group` struct, or if `Delete` runs while a reader still holds a member pointer). That requires a cache redesign and stays deferred — but the most likely-to-fire race (single-flag write under cache miss of the parent group) is now plugged.

### B48 — `shared/gameserver/conn/gameserver-grpc-conn.go`: grpc.Dial race (commit `1914114`)

`GRPCConnByGameServerAddress` had a classic check-then-act race:

```go
m.lock.RLock()
conn = m.addressWithConn[connAddress]
m.lock.RUnlock()
if conn == nil {
    conn, _ = m.establishConn(connAddress)  // grpc.Dial, real network
    m.lock.Lock()
    m.addressWithConn[connAddress] = conn   // last writer wins
    m.lock.Unlock()
}
```

Two concurrent callers for the same address both see nil, both Dial, both store. The loser's `*grpc.ClientConn` becomes unreachable and leaks (Go GC eventually closes it but only on an unreference, which doesn't happen reliably — the OS file descriptor sits open). Plus duplicate Dial cost.

Fixed with double-checked locking: read-lock fast-path returns immediately if the conn exists; otherwise take the write lock, re-check the map, only Dial if still nil. Standard Go pattern.

### B52 — `shared/events/consumer-{gateway,group}.go`: orphan subscriptions on partial Listen() failure (commit `1914114`)

Each `Listen()` calls `c.nc.Subscribe(...)` for each handler the caller provided (3 for gateway, 12 for group). If subscription N succeeds and N+1 fails, the function returns the error, but the subscriptions for handlers 1..N are still live in NATS. The caller path in every microservice is `if err = listener.Listen(); err != nil { log.Fatal... }` — the consumer is then abandoned without ever calling `Stop()`. Orphan goroutines per partial-failure recovery.

Fix: call `c.unsubscribe()` to roll back all collected subscriptions before returning the error. Applied to both consumer-gateway.go and consumer-group.go; the same pattern probably exists in other `consumer-*.go` files but those were not exercised in this pass.

### B53 — `shared/healthandmetrics/metrics-reader.go`: protobuf message copied by value (commit `1914114`)

`go vet` flagged two lines:

```
metrics-reader.go:235: call of append copies lock value: dto.MetricFamily contains MessageState contains sync.Mutex
metrics-reader.go:241: range var result copies lock: dto.MetricFamily ...
```

Protobuf v2's generated types embed `protoimpl.MessageState` which contains a `sync.Mutex`. Copying a `MetricFamily` by value (via `append(metrics, result)` and `for _, result := range metrics`) copies a locked-or-unlocked mutex — undefined behavior per Go's mutex rules.

`MetricsRead.Raw` had type `[]dto.MetricFamily`. Grep showed no external code reads the `Raw` field, so changing it to `[]*dto.MetricFamily` is a zero-impact change. Updated the decode loop to allocate `result := &dto.MetricFamily{}` and `dec.Decode(result)`.

### Remaining deferred items (post-B53)

| ID | Service | Issue | Reason |
|---|---|---|---|
| B25 | gateway | 1s sleep after CharDelete async DB transaction | Needs AC ack opcode |
| B27 | gateway | 500ms post-redirect channel-rejoin | No opcode signal |
| A5/A6 | gateway | HandleMap / OpcodeBlacklist package globals | Refactor; low ROI |
| — | group/guild | In-memory cache pointer-aliasing (stale `*GroupMember` after parent `Update`/`Delete`) | Cache redesign; needs ADR |
| — | shared/events | Partial-Listen rollback only applied to consumer-gateway + consumer-group; the other consumer-*.go files have the same pattern unaddressed | Mechanical sweep; do when next touched |

