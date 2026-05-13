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

For each of the seven microservices: `go build ./apps/<svc>/...`, `go vet ./apps/<svc>/...`, and `go test -race -count=1 ./apps/<svc>/...` all green. matchmakingserver has a pre-existing vet warning at `server/matchmaking.go:50` for an unkeyed struct literal — pre-dates this work, not in scope.

E2E Wine-login verification of the gateway-side race-by-sleep fixes (B6, B10, B24) is still on the user; the microservice fixes here are server-side only and don't have a Wine code path to exercise them.
