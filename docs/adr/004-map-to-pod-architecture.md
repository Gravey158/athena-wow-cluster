# 004. 1:1 Map=Pod Architecture

Date: 2026-05-13
Status: Proposed (no implementation yet — design lock-in pending review)

## Context

The Goal-File (`.claude/goals/athena-wow-cluster.md`) defines cross-realm
distribution as a Phase 0/3 deliverable. The user's clarification:

> "cross-realm heißt für mich, dass die maps in separate instanzen sind, um den
>  load zu verteilen"
> "instanzen und raid instanzen sollen auch verteilt werden, um die last weiter
>  zu separieren, also alles wo ein loadscreen so oder so im spiel vorkommt"
> "pro map eine pod instanz 1:1"

Translated: every map (and every dungeon/raid/BG/arena instance) lives on its
own dedicated K8s pod. Anywhere the WoW client shows a loadscreen, the server
side is a fresh pod.

ToCloud9 today is closer to "load-balanced map allocation" than 1:1:

- `servers-registry/mapbalancing/binpack/distributor.go` distributes maps to
  the smallest number of pods using a bin-packing heuristic with per-map weights.
  Default behavior: with `Cluster.AvailableMaps=""`, all pods register as "free
  pool", maps are assigned dynamically across them.
- Pods are long-lived (K8s Deployment/StatefulSet). No "instance pod per
  dungeon" concept exists.

The existing distributor code already supports the 1:1 shape via the
`IsAllMapsAvailable()` + `readyServers` path (see `code-review-01-servers-registry.md`):
if a server registers with `AvailableMaps=[specific-map-id]`, it bypasses the
bin-packer and is just echoed back as authoritative for that map. The
foundation is 90% there.

Local validation (Phase 0L, `docs/baseline.md`) proved the multi-pod
distribution works for open-world maps: with two `gameserver-ac` pods registered
with empty `AvailableMaps`, the servers-registry dynamically routed Map 0
(Stormwind) to one pod and Map 530 (Outland) to the other. Each loadscreen
crossed a pod boundary.

The next step is shaping the K8s manifests + servers-registry semantics around
strict 1:1 instead of opportunistic load-balancing.

## Decision

**1:1 Map=Pod with two pod classes: permanent open-world pods and ephemeral
instance pods.**

### Pod classes

**Open-world (permanent)** — one pod per continent map ID:

| Map ID | Name | Pod |
|---|---|---|
| 0 | Eastern Kingdoms | `gameserver-map-0` |
| 1 | Kalimdor | `gameserver-map-1` |
| 530 | Outland (incl. Blood Elf + Draenei starting zones) | `gameserver-map-530` |
| 571 | Northrend | `gameserver-map-571` |

These are K8s `StatefulSet` or `Deployment` with `replicas: 1`, started at
cluster bring-up, never auto-deleted. Each pod's container env sets
`Cluster.AvailableMaps="<map-id>"` (single entry).

If a permanent pod crashes, K8s recreates it. Players on that map get the
existing `onWorldSocketClosed` reconnect flow (gateway side) — under 5 seconds
of disruption per the B11 timing fix.

**Instance (ephemeral)** — spawned on first player entry, despawned shortly
after last player leaves. Covers:

- Dungeon instances (~70 maps in 3.3.5a: Deadmines map 36, Stratholme map 329,
  Naxxramas map 533, etc.)
- Raid instances (sharing the same map ID as solo dungeons, separate
  instance ID)
- Battlegrounds (Map 30 AV, 489 WSG, 529 AB, 566 EotS, 607 SotA, 628 IoC,
  etc.) — one pod per BG instance
- Arenas (Map 559 NA, 562 BEA, 572 RoV, 617 DS, 618 RoL) — one pod per arena
  match
- Player-housing scenarios (no equivalent in 3.3.5a — not applicable)

These are spawned as K8s `Pod` resources (not Deployment, since each instance
is a one-off). Named like `gameserver-instance-{map-id}-{instance-id}`.

Lifecycle:
1. Player enters loadscreen → gateway asks servers-registry "available
   gameserver for map+instance X?"
2. servers-registry sees no pod → calls an **instance-orchestrator** service
3. instance-orchestrator creates the K8s Pod via Kubernetes API, waits for
   `Ready`, returns address
4. gateway routes player to the new pod
5. Pod registers itself with servers-registry on startup (same code path as
   permanent pods, but with `AvailableMaps=[specific-instance-id]`)
6. When last player leaves: a grace timer (default 5 min) starts
7. If no rejoin within grace: instance-orchestrator deletes the Pod
8. If a rejoin: timer canceled, pod stays

### New microservice: instance-orchestrator

A small Go service that owns the K8s-API integration. Responsibilities:

- Watch servers-registry for "no available server for map X" events
- Create instance Pods via K8s API (`client-go`)
- Track in-flight instance Pods (their player count, idle time)
- Delete instance Pods that have been idle > grace period

Why a new service (not embedded in servers-registry):

- **Separation of concerns**: servers-registry is pure routing/discovery.
  K8s-API interaction is privileged + crashloop-risky; isolating it
  prevents one bad pod-create from taking down map routing for healthy
  pods.
- **RBAC scope**: only `instance-orchestrator` needs `pods.create / delete`
  permission on `wow-cluster` namespace. servers-registry stays read-only
  to K8s API (it doesn't need K8s access at all today; keep it that way).
- **Independent scaling**: orchestrator can have replicas (with leader
  election via K8s Lease) if pod-creation throughput matters. Registry
  stays single-instance (or 3 for HA).

Microservice spec:

- gRPC server with two RPCs: `EnsureInstanceForMap(realm, map, instance_id)`
  → returns gameserver pod address; `ReleaseInstance(addr)` → instructs
  delete (or starts grace timer).
- Internal state: in-memory map of `(realm, map, instance_id) → Pod ref`.
  Lost on restart; recoverable by listing existing instance pods + their
  labels.
- gateway calls `EnsureInstanceForMap` on every loadscreen, replacing the
  current `serversRegistryClient.AvailableGameServersForMapAndRealm`
  for instance maps. For open-world maps, gateway can keep using
  servers-registry directly (no orchestrator involvement).

### How servers-registry behavior changes

- Pods register with single-element `AvailableMaps` (already supported).
- The bin-packer (`mapbalancing/binpack/distributor.go`) is bypassed
  entirely — the `IsAllMapsAvailable()` check at line 37 of distributor.go
  short-circuits to `readyServers` path. The whole `binpack/` package can
  be removed in a later refactor (deferred — keep for now in case we
  need fallback bin-packing for resource-constrained clusters).
- `Cluster.AvailableMaps` env var on each pod is set by the K8s manifest
  (Helm-templated per-pod).

### Pod template — proposed Helm-chart changes

Modify `chart/templates/gameserver_ac.yaml` to support per-map deployments:

- Replace the single `Deployment` with a `range` over a map-list value:
  ```yaml
  {{- range .Values.gameserver_ac.openWorldMaps }}
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: gameserver-map-{{ . }}
  spec:
    replicas: 1
    template:
      spec:
        containers:
          - env:
              - name: AC_CLUSTER_AVAILABLEMAPS
                value: "{{ . }}"
              ...
  ---
  {{- end }}
  ```
- Add `chart/values.yaml`:
  ```yaml
  gameserver_ac:
    openWorldMaps: [0, 1, 530, 571]
    instances:
      # configuration for instance-orchestrator; not a static manifest.
      gracePeriodSeconds: 300
      maxConcurrent: 50
  ```

The instance pods are NOT in the Helm chart — they're created by
instance-orchestrator at runtime.

### Resource sizing (from `docs/baseline.md` + tuning measurements)

- **Open-world pod** (tuned `MapUpdateInterval=100`):
  - request: `cpu: 200m`, `memory: 1.5Gi`
  - limit: `cpu: 1000m`, `memory: 2Gi`
- **Instance pod** (single dungeon or BG instance, low population):
  - request: `cpu: 50m`, `memory: 600Mi`
  - limit: `cpu: 500m`, `memory: 1Gi`

Athena worker pool: 3 × ~4 CPU = ~12 CPU effective. Budget:

- 4 open-world pods: 0.8 CPU req / 4 CPU limit, 6 GiB req / 8 GiB limit
- ~10 active instances (typical raid night): 0.5 CPU req / 5 CPU limit, 6 GiB req / 10 GiB limit
- Galera 3 pods: 0.5 CPU req / 3 CPU limit, 3 GiB req / 6 GiB limit
- ToCloud9 microservices + gateway + NATS + Redis: ~0.3 CPU req / 1 CPU limit, 0.5 GiB
- **Total req: ~2.1 CPU + ~15.5 GiB** — fits comfortably on 12 CPU + ~30 GiB
- **Total limit: ~13 CPU + ~25 GiB** — over-commits CPU 1.1× (acceptable for
  bursty workloads), memory fits

### Lifecycle: detailed instance flow

1. Player on map 530 (Outland) zones into Hellfire Ramparts (map 543).
   Client → gateway: `CMsgWorldTeleport` (or `MsgMoveWorldPortAck` mid-portal).
2. Gateway looks up "who hosts map 543, instance_id N for realm 1?"
   - For raids, `instance_id` is the unique instance counter from
     `acore_characters.instance` table.
   - For dungeons (5-player), `instance_id` is per-group from same table.
   - For BGs, `instance_id` is from matchmakingserver assignment.
3. Gateway calls `instance-orchestrator.EnsureInstanceForMap(1, 543, N)`.
4. Orchestrator checks internal map: not found.
5. Orchestrator creates Pod `gameserver-instance-543-N` with env
   `AC_CLUSTER_AVAILABLEMAPS=543` + `AC_INSTANCE_ID=N`. Waits for
   readinessProbe (gRPC port up).
6. New pod registers with servers-registry, advertising single map.
7. Orchestrator returns pod address to gateway.
8. Gateway opens worldsocket to the new pod, sends client's
   `CMsgPlayerLogin`.
9. Player loads into the instance.
10. When player leaves (group disbands, BG ends), gateway sees
    `SMsgNewWorld` to a different map → notifies orchestrator
    `ReleaseInstance(addr)`.
11. Orchestrator starts grace timer. If another join in 5 min, cancel.
12. After grace: orchestrator deletes pod, GC reaps.

### DB-side considerations

- All instance pods write to the same `acore_characters` and `acore_world`
  Galera cluster. Concurrent writes from many pods need either:
  - **Galera multi-master**: works but locks/deadlocks under high write
    contention. AC writes character data on every save (~1 Hz), plus
    inventory + quest state changes. Galera certification will retry but
    introduces tail latency.
  - **Single-writer pattern**: one pod is "primary" for each character;
    others read-only. ToCloud9's shared-GUID design already implies a
    single-writer model. Verify in Phase 0.4 deployment whether this
    holds with multiple gameserver pods.
- The `acore_characters.instance` table tracks active instances and
  needs cleanup when an instance pod is reaped. Add a `last_seen`
  column + a periodic GC job (Phase 5 hardening).

### Anti-affinity

Open-world pods should spread across the worker nodes (Lenovo-04 +
Wyse-01 + Wyse-02), avoiding `talos-u3t-we5` (production-heavy node).
This is the same anti-affinity rule discussed in `docs/baseline.md`.

Instance pods should also spread, but with looser constraint —
`preferredDuringScheduling` rather than `required`, so instance creation
doesn't fail under capacity pressure.

## Consequences

### Positive

- **Predictable failure domain**: a crashed instance pod kills only that
  instance's players (5-25 people at worst). A crashed open-world pod
  kills only its continent (200-500 people at peak — still bad but
  bounded).
- **Resource sharding**: hot zones (Dalaran, Stormwind) get their own
  CPU/RAM. No noisy-neighbor between Stormwind and a low-pop continent.
- **Cluster.AvailableMaps=single-entry simplifies servers-registry**: no
  bin-packing, no rebalancing churn (architectural issue A1 in
  `code-review-01-servers-registry.md`).
- **Matches container-orchestration intent**: K8s is designed for
  ephemeral compute (Job + Pod), exactly what instances need.

### Negative

- **Pod startup time eats player UX**: an instance pod takes ~10-30 seconds
  to start (image pull from local registry + AC world-data load + Galera
  schema setup + grpc-registration). The player sees a long loading
  screen the first time anyone enters that instance. Mitigation: pre-warm
  pool of "spare" instance pods (e.g. 3 idle pods ready to claim a map)
  for popular instances; tunable via instance-orchestrator config.
- **More moving parts**: instance-orchestrator is a new microservice with
  K8s-API permissions, observability, error paths. Plus the K8s control
  plane sees a 5x-10x increase in Pod resource churn — needs monitoring.
- **Image build/cache cost**: each instance pod pulls the gameserver-ac
  image on startup. With 50+ instance creates/hour at peak, the worker
  nodes need adequate image cache. Mitigation: `imagePullPolicy: IfNotPresent`
  + container-image-cache configured (containerd's content store).
- **DB connection storms**: each instance pod opens 3 DB connections
  (auth/world/chars). 50 instances = 150 connections from gameservers
  alone. Galera connection pool sizing needs to handle this. PgBouncer-
  style connection pooler in front of Galera if pressure exceeds limits.

### Migration path

- **Phase 0.4 (current)**: keep upstream ToCloud9 default — pods register
  with empty `AvailableMaps`, bin-packer assigns maps. Validates the
  ToCloud9 cluster mode end-to-end without 1:1 complexity.
- **Phase 0.5 / 0.6**: switch to 1:1 open-world only. Helm chart templates
  one Deployment per `openWorldMaps` entry. Players can play, instance
  spawning still goes through the bin-packer fallback.
- **Phase 1**: instance-orchestrator MVP. K8s-API integration, pod
  creation, grace-period reaping. Integration test (real dungeon entry +
  cleanup).
- **Phase 1 cont.**: pre-warm pool for popular instances (Naxxramas,
  Trial of the Crusader, Ulduar).
- **Phase 2 / 3**: cross-realm-arena, cross-realm-BG → matchmakingserver
  picks instance-orchestrator pod assignment across realms.

## Open questions (need resolution before implementation)

1. **Where lives instance-orchestrator**: own microservice or merge into
   servers-registry? Decision driver: RBAC scope + crashloop isolation
   (above). Likely separate, but reassess if servers-registry already has
   K8s-API permission needs we haven't surfaced.

2. **Single-writer character data**: does ToCloud9's shared-GUID design
   plus AC's current save-tick really hold against parallel writes from
   multiple instance pods? Needs Phase 0.4/0.5 validation under load (50
   simulated bots in different instances).

3. **Pre-warm pool sizing**: 3 pods/popular-instance might be too few or
   too many. Will need profiling once Phase 1 lands.

4. **Cross-realm bridging**: how do orchestrator-managed instances span
   multiple realms (Phase 3)? If realm A and realm B both want a Ulduar
   instance, do they share one pod or get separate pods? Sharing means
   GUID/realm isolation must hold at gameserver level — non-trivial.
   Separate means twice the resource cost. Defer decision to Phase 3.

5. **Instance-orchestrator HA**: single replica or 3 with leader-election
   (K8s Lease)? Single is simpler; 3 with leader-election is more
   correct for "no instance creation during 5-min controller failover."

## Implementation pointers

- The `IsAllMapsAvailable()` branch in `apps/servers-registry/mapbalancing/binpack/distributor.go:37`
  is the single-line check that the 1:1 design hangs on. Already correct;
  no code change to servers-registry needed for the open-world half.
- The Helm chart change is mechanical: see "Pod template" section above.
- instance-orchestrator: ~500-800 LoC Go. Skeleton: `go-clientset` from
  k8s.io/client-go, NATS subscriber for "instance-needed" events from
  servers-registry, gRPC server for `EnsureInstanceForMap`. Reuse
  ToCloud9's existing `shared/healthandmetrics` for liveness + metrics.

## Why this is the right call

Two alternatives were considered:

- **Many maps per pod with intelligent bin-packing** (status quo): worse
  failure isolation (one pod crash kills multiple continents),
  scheduler thrash on rebalance, no per-instance compute sharding.
- **Single mega-pod with all maps** (pre-cluster ToCloud9, pre-Athena
  gameserver-wow): worst failure isolation, no horizontal scaling, fully
  defeats the cluster project's goals.

1:1 Map=Pod aligns with K8s primitives (Pod = lifecycle unit), aligns with
ToCloud9's existing `AvailableMaps` mechanism, and lines up with the
Phase 0 success criteria in the goal file (cross-realm BGs, arena cross-
realm, AH cluster-aware — all inherently want per-instance isolation).

Cost: one new microservice (instance-orchestrator), K8s-API integration,
moderate operational complexity from ephemeral pods.

Net assessment: worth doing, deferred concrete implementation until
Phase 0.4 / 0.5 has validated the simpler "multiple gameserver-ac pods"
baseline on Athena.
