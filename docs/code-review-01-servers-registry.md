# Code-Review 01 — servers-registry

Date: 2026-05-13
Scope: `apps/servers-registry/` (~2104 LoC Go)
Reviewer-context: aim is bug-hunting + slimming + foundation for ADR 004 (1:1 Map=Pod).

## Summary

The microservice is small, readable, well-structured around three concerns:

1. `repo/` — Gateway/GameServer persistence (in-memory + Redis dual-impl behind interface)
2. `service/` — business logic for registration/health/lookup
3. `mapbalancing/binpack/` — greedy bin-packing for map-to-server distribution

Found 4 bugs, 2 architectural issues, multiple slimming opportunities. The mapbalancing layer is the most relevant subsystem for the planned 1:1 Map=Pod architecture (ADR 004).

## Bugs

### B1. `cmd/main.go:65` — wrong error logged on Redis ping failure

```go
pingRes := rdb.Ping(context.Background())
if pingRes.Err() != nil {
    log.Fatal().Err(err).Msg("can't connect to redis")   // err is nil (left over from ParseURL)
}
```

`err` at that point is the result of `redis.ParseURL` (already nil here, since ParseURL succeeded). The actual error is `pingRes.Err()`. Logger emits `error: <nil>` and an unhelpful "can't connect to redis" message during real failures — debugging hostage.

**Fix:** `Err(pingRes.Err())`.

### B2. `mapbalancing/binpack/distributor.go:115-129` — silently drops maps when bins > servers

```go
if len(packing) > len(servers) {
    lenDiff := len(servers) - len(packing)   // NEGATIVE when packing > servers
    for i := 0; i < lenDiff; i++ {            // never executes (lenDiff < 0)
        for serversItr, j := 0, 0; j < lenDiff; j++ {   // also never executes
            ...
        }
    }
    packing = packing[:len(servers)]          // silently truncates excess bins
}
```

Variable naming inverted: it should be `lenDiff := len(packing) - len(servers)` for a "how many bins are surplus" semantic. As written, the loop body never runs and the maps that didn't fit are dropped on the floor without warning or error.

**Reproducer:** N maps with very high weights and few servers → bin-packing creates more bins than servers → maps from `packing[len(servers):]` vanish.

**Fix:** invert the subtraction, then the redistribution logic *might* be sensible (it tries to spread surplus map IDs across existing servers). The whole branch needs a clean rewrite + test coverage — there's already `distributor_test.go` with only 63 LoC, this case isn't covered.

### B3. `cmd/main.go:95` — Gateway service realms list hardcoded

```go
gatewayService, err := service.NewGateway(
    ...,
    []uint32{1},   // hardcoded — should be conf.RealmsID like gameServersService
)
```

`supportedRealms` (= `conf.RealmsID`) is propagated to `NewGameServer` but the literal `[]uint32{1}` to `NewGateway`. Multi-realm deployments would silently fail on gateway registration for realms ≠ 1.

**Fix:** pass `supportedRealms` to `NewGateway` too.

### B4. Background goroutines leak on shutdown

`healthChecker.Start()` and `metricsConsumer.Start()` are launched as bare `go` calls. They have no `context.Done()` watch in their start loops (need to verify by reading the `healthandmetrics` package). On `grpcServer.GracefulStop()` only the gRPC path stops; these goroutines either get killed on process exit (clean enough on K8s SIGTERM → exit) or continue performing IO with no consumers (less clean, possible panics on close-of-closed-redis-client).

**Fix:** thread `mainContext` through `Start(ctx)`; main waits for them to return before exiting. Today's behavior is benign at K8s lifecycle but bad pattern for testing + local-dev iteration.

## Architectural Issues

### A1. `Distribute()` re-assigns ALL maps on every call — no sticky assignment

```go
// cleanup prev maps distribution
for i := range servers {
    servers[i].AssignedMapsToHandle = []uint32{}   // every distribute() wipes state
}
```

When a pod is transiently down and comes back, the next `Distribute()` re-balances everything. Players currently on map M might be told "map M now lives on pod X" but their connection-state is still routed to pod Y. ToCloud9's player-redirect on map-change handles this gracefully (it's the very feature it's built on), but it's wasted churn — each restart causes O(maps) player redirections.

**Fix idea:** preserve existing assignments where possible; only rebalance maps that have lost their server.

Cost of fix is low (a `prevAssignment map[uint32]serverID` lookup that's preferred when re-distributing), benefit grows with player count + frequency of pod restarts. Defer to Phase 4 perf-pass after ADR 004 lands.

### A2. Bin-packing assumes balanced load, not 1:1 Map=Pod target

The current model: "I have N servers, distribute M maps such that no server's total weight > avg + ceil". This is **the wrong shape** for the 1:1 Map=Pod intent.

For 1:1, what we want:

- Each open-world map has its own dedicated server, **named by map**.
- A pod's `AvailableMaps=[map-id]` is single-element (the path through `Distribute` line 36-48 already handles this — it bypasses the bin-packer entirely).

So the 1:1 mode is already supported in `Distribute()`'s shape, just needs:

1. Helm-chart per-pod-config so each pod gets a unique `Cluster.AvailableMaps=<one-id>` env.
2. servers-registry must NOT bin-pack — it should refuse to assign a "free" map to a pod that already has its dedicated map.

Cleaner refactor: **add a third distributor implementation** `OneMapPerServerDistributor` parallel to `binpackBalancer`, behind the `MapDistributor` interface. Switch via config flag.

## Slimming opportunities

- **Unused import / repo dual-impl:** the in-memory repo (`game-server_inmem.go`, `gateway-server-inmem.go`) is wired up but `main.go` always uses Redis impls. The in-memory ones look like test scaffolding promoted to production code (157 LoC across 2 files). Either delete or move to `_test.go`-adjacent.

- **`zerolog.DebugLevel` switch is at startup only:** `cmd/main.go:102-104` decorates the registry with a debug-logger middleware iff log level is debug at startup. Standard practice is to always wrap and let the underlying logger filter — saves a code path.

- **TODO from line 81 (custom maps weight list):** never implemented. With 1:1 Map=Pod the entire `MapsWeight` concept becomes obsolete (no weighting needed when assignment is dictated). Can be deleted alongside `binpack/weights.go` (127 LoC).

- **Sentinel `nats.Connect` settings duplicated across services:** `PingInterval(20*time.Second), MaxPingsOutstanding(5), Timeout(10*time.Second)` is identical across servers-registry, gateway, microservices. Move to a shared `pkg/natsconfig` constructor.

## Specific code-quality nits (low priority)

- `cmd/main.go:106` constructs `grpc.NewServer()` without options. Worth adding `grpc.MaxRecvMsgSize`, `grpc.MaxSendMsgSize` to avoid silently truncating large player-state messages during instance-spawn.
- `mapbalancing/binpack/distributor.go:24-27` copies the weights map but doesn't lock. If `weights` is modified concurrently, race. Locking is overkill — make `weights` immutable after construction (it already is in practice).
- `mapbalancing/binpack/distributor.go:96-98` sort + iterate twice — sort once before the loop, then linear pass.

## Implications for ADR 004 (1:1 Map=Pod)

The distribute-path code is **most of the way there** for 1:1:

```go
for _, server := range servers {
    if server.IsAllMapsAvailable() {                          // empty AvailableMaps = "free pool"
        serversToBalance = append(serversToBalance, server)
        continue
    }
    server.AssignedMapsToHandle = server.AvailableMaps        // 1:1 path: just echo back
    readyServers = append(readyServers, server)
    for _, availableMap := range server.AvailableMaps {
        delete(weightsCopy, availableMap)                     // claim removes from free pool
    }
}
```

For 1:1, **every** gameserver pod arrives with `AvailableMaps=[specific-map-id]` (Helm-chart per-pod env), goes through this `readyServers` path, and `serversToBalance` stays empty → bin-packer is never invoked.

What's missing:

1. Helm chart needs per-pod env override (current chart sets one global env for all replicas).
2. ADR 004 must specify the map-ID → pod-name mapping policy (static config? CRD? what about instance-IDs for dungeons that are dynamic?).
3. servers-registry shouldn't accept a registration for a map that's already claimed by another live pod (today it would silently overwrite the `AvailableMaps` setting).

These are all small code-deltas. Bigger lift is the dynamic instance-pod spawning side (separate microservice or K8s Job-creation API integration).
