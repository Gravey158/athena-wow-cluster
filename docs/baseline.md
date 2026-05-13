# Baseline — Phase 0L Local Reproducible Stack

Status: ✅ working as of 2026-05-13

This document is the snapshot of what was validated locally via `docker compose` on macOS. It supersedes the original Phase 0 plan ("K8s baseline first") after the Athena cluster CPU exhaustion incident — local validation is now the entry gate to any Athena deployment.

## What was validated

Two ToCloud9 `gameserver-ac` instances + 1 single MySQL + 1 gateway + 8 Go microservices + NATS + Redis, all on a single Mac via OrbStack. A vanilla 3.3.5a ChromieCraft client running through Wine 11.0 successfully logged in as `ADMIN/admin`, created a Blood-Elf Rogue (Salan), and traveled across **three distinct Maps that landed on two different gameserver pods**:

```
mapID=0   (Eastern Kingdoms / Stormwind)  →  gameserver-ac-2  (192.168.97.17)
mapID=1   (Kalimdor)                      →  gameserver-ac    (192.168.97.13)
mapID=530 (Outland incl. Hellfire/Eversong) → gameserver-ac-2 (192.168.97.17)
```

The `servers-registry` microservice handled map-to-pod allocation dynamically — no per-pod map list needed in `worldserver.conf` (`Cluster.AvailableMaps=""`).

## Stack layout (16 containers)

| Service | Image origin | Idle CPU | RAM idle | Role |
|---|---|---|---|---|
| `database-ac` | `mysql:8.4` | 8.6% | 700 MiB | DB for auth/world/characters |
| `gameserver-ac` | local build (AC + ToCloud9 libsidecar) | **97%** | 2.13 GiB | C++ AzerothCore worldserver pod 1 |
| `gameserver-ac-2` | reuse same image | **97%** | 2.03 GiB | worldserver pod 2 |
| `gateway` | local build (Go) | 0.18% | 26.7 MiB | External player connection point (TCP 8085) |
| `gateway-second` | local build (Go) | 0.03% | 17.1 MiB | Secondary gateway (TCP 8045) |
| `authserver` | local build (Go) | 0.00% | 3.2 MiB | Login (TCP 3724) |
| `servers-registry` | local build (Go) | 0.01% | 21.8 MiB | gRPC map-to-pod registry |
| `charserver` | local build (Go) | 0.00% | 6.4 MiB | Char selection service |
| `chatserver` | local build (Go) | 0.04% | 6.1 MiB | Cluster-wide chat |
| `guildserver` | local build (Go) | 0.03% | 3.3 MiB | Guild ops |
| `guidserver` | local build (Go) | 0.00% | 3.1 MiB | Shared GUID pool |
| `groupserver` | local build (Go) | 0.00% | 4.9 MiB | Party/raid ops |
| `mailserver` | local build (Go) | 0.03% | 3.6 MiB | In-game mail |
| `matchmakingserver` | local build (Go) | 0.03% | 8.5 MiB | BG/arena queue |
| `nats` | `nats:2.10-alpine` | 0.15% | 10.6 MiB | Message bus |
| `redis` | `redis:7-alpine` (overridden, see issues) | 0.16% | 6.8 MiB | servers-registry cache |

**Total at idle (no players):** ~2.3 CPU sustained, ~5.5 GiB RAM. The two `gameserver-ac` pods dominate both axes by an order of magnitude over everything else.

## Issues found + how they were fixed

These are the gotchas that broke setup or runtime — recorded here so the K8s deploy doesn't trip on them again.

### 1. Client-data version mismatch (v16 vs walkline's current AC)

ToCloud9's `docker-compose.yaml` hardcodes `https://github.com/wowgaming/client-data/releases/download/v16/data.zip` (released 2023-01). walkline's AzerothCore fork (built 2026-04-29) expects newer VMap binary format. Result on first boot: gameserver-ac exited(1) with:

```
VMap file '/data/vmaps/000.vmtree' couldn't be loaded
... version of the VMap file and the version of this module are different
Failed to find map files for starting areas
```

**Fix:** patch `docker-compose.yaml` line 44 to point at `v19` (released 2025-12-03). K8s equivalent: pin the URL in the `gameserver_ac.initcontainer.download_url` value of `chart/values.yaml`.

### 2. Alpine BusyBox `unzip` can't handle ZIP64

`download-miscs` service uses `alpine/curl:8.14.1`, whose `unzip` is BusyBox-derived and silently truncates on archives with >2 GB uncompressed size. The v19 ZIP unzipped partially (only `Cameras/`, `dbc/`, `maps/`), missed `vmaps/` and `mmaps/` entirely, and exited with `unexpected end of file / inflate error`.

**Fix:** use a Debian-based image (`debian:stable-slim` with `apt-get install unzip`) for the init/setup container. The downloaded archive itself was fine (`unzip -t` reports no errors). K8s equivalent: change the init container image from alpine to debian or rocky linux.

### 3. ChromieCraft realmlist.wtf pointed at olympus.x2-pandora.de

Standard. Backup made (`realmlist.wtf.backup-20260513-113359`), set to `set realmlist 127.0.0.1`. Plain text file at `Data/enUS/realmlist.wtf`.

### 4. AC idle CPU at 97% per pod

Default `MapUpdateInterval=10` (10 ms = 100 Hz tick rate) and `MapUpdate.Threads=1` means each gameserver pod burns one full CPU core even with zero players. With 2 pods this is 2 CPUs permanent — significant on a 12-CPU Athena worker pool.

**Mitigation for K8s deploy:** in `chart/files/worldserver.conf` overrides (or via `AC_MAPUPDATEINTERVAL` env var):
- `MapUpdateInterval = 100` (10 Hz instead of 100 Hz)
- `MapUpdate.Threads = 2` (so per-pod budget is 2 CPUs max, but typically much less)

Validate the tuning under load before declaring a target. Phase 4 perf work.

### 5. `ADMIN/admin` is GM-level 3 by default — but no relog needed

The `account_access` table is seeded with the ADMIN account at gmlevel=3 for RealmID=-1 (all realms). `.tele`, `.gm on`, `.cheat god 1` etc. work immediately after first login.

## ToCloud9 cluster mechanics (validated)

- `Cluster.Enabled=1` (via env `AC_CLUSTER_ENABLED=1`) — enables cluster mode in AC core.
- `Cluster.AvailableMaps=""` (empty) — each pod advertises itself as able to host **any** map. Combined with multiple pods sharing the same client-data volume, this means servers-registry can route any map to any pod that has capacity.
- The pod that "owns" a map is decided **on-demand at first player entry**; subsequent players on that map are routed to the same pod. When the last player leaves, the map can later be re-allocated.
- For dungeons / BGs / arenas: each Instance ID is independent — a Deadmines instance for player A and a Deadmines instance for player B can spawn on different pods. This is exactly the load-distribution we want (every loadscreen = potential pod switch).

## K8s implications

These are the concrete adjustments needed in `chart/values.yaml` and `gitops/apps/wow-cluster/` for a safe Athena re-deploy. **The git work from commits `c60b3b7`–`e896753` is good; these are deltas on top.**

### Image references

- `gameserver_ac.image.repository`: currently `ghcr.io/walkline/gameserver-ac`, but our local validates exactly the same upstream image works fine. Stay on upstream until we have a reason to switch (Phase 0.2 AC-fork is for cosmetic + MariaDB-source-switch optimisation, not blocking).
- `gameserver_ac.initcontainer.download_url`: hardcode `https://github.com/wowgaming/client-data/releases/download/v19/data.zip`.
- `gameserver_ac.initcontainer.image.repository`: change from `alpine` to `debian` (or `rockylinux`). Update its command to `apt-get install -y unzip curl` first.

### Resource requests (revised based on actual usage)

```yaml
resources:                      # global block in chart, all services use it
  requests:
    cpu: 50m
    memory: 128Mi
  limits:
    memory: 256Mi
```

This sizes for the Go-microservices, which dominate by count (9 of them × ~10 MiB each). For `gameserver_ac` specifically the chart template ignores per-service tuning, so gameserver pods will request 50m but burn 1 CPU each at runtime — this is acceptable because the cluster scheduler over-commits CPU by default.

If we hit scheduling problems again, the right fix is to **modify the chart template** to support per-service resource blocks, then override `gameserver_ac.resources` to `{requests: {cpu: 1, memory: 2Gi}, limits: {memory: 4Gi}}`. Track as a future ADR.

### Anti-affinity

Two cluster realities to encode:

1. **`talos-u3t-we5` hosts the heavyweight production tenants** (authentik-postgresql, argocd-application-controller, portal-backend). No athena-wow-cluster pod should land there.
2. **`talos-quv-i6z` already runs the legacy `gameserver-wow/ac-worldserver` pod** pinned by hostname. Our gameserver pods should soft-prefer the two Wyse workers (`talos-ph2-tfm`, `talos-u3t-we5`) but **`required` anti-affinity to `u3t-we5`** so we never collide there.

Add to chart templates (or to a values-based override block) — example for gameserver:

```yaml
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: kubernetes.io/hostname
              operator: NotIn
              values: [talos-u3t-we5]
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              app: wow-tocloud9-gameserver-ac
```

### Galera vs single-MySQL

Local used a single `mysql:8.4` pod. K8s plan calls for Galera-3 via mariadb-operator. Two open questions to resolve **before** deploying on Athena:

1. Does walkline's gameserver-ac binary (built against `libmysqlclient`) connect cleanly to MariaDB-11 Galera? Likely yes (wire-protocol compatible), but the AC fork explicitly has a MariaDB-source-switch branch which suggests there's a meaningful difference. We need to actually try.
2. Galera multi-writer semantics vs AC's heavy use of write-locks: AC writes characters/items/auth on every save tick. Need to confirm the writes don't conflict on Galera deadlock retry.

**Easy first deploy:** start with a **single-pod MariaDB** (`mariadb-operator` supports `replicas: 1`), validate AC works against MariaDB, then move to Galera 3 in a follow-up. Documented in ADR 003 already.

### Deploy strategy (incremental — anti-blast-radius)

After the 2026-05-13 cluster crash, the rule is: never deploy more than one new K8s Application at a time. Concrete sequence for re-attempting Athena:

1. **mariadb-operator-crds only** (sync-wave -29). Wait. Verify CRDs present.
2. **mariadb-operator controller** (sync-wave -27). Wait. Verify operator pod healthy.
3. **wow-cluster namespace + sealed-secrets + MariaDB CR with `replicas: 1`** (skip Galera-3 initially). Wait. Verify mariadb pod healthy, schemas seeded by Database CRs.
4. **wow-cluster Helm chart with `gameserver_ac.replicaCount: 1`** (single pod, not 2). Wait. Verify authserver + gateway responding on MetalLB IPs `10.10.30.72/.73`.
5. **Client login test from ChromieCraft against 10.10.30.72:3724**. Verify Salan can be created + enter world.
6. **THEN** scale gameserver_ac to 2 (so map-distribution can be validated on cluster). Verify same behaviour as local 0L.5.
7. **THEN** consider Galera-3 migration (separate ADR, requires backup-restore plan).

Each step gets `kubectl top nodes` watched; if any node exceeds 75% CPU during sync, pause + revert.

## Outstanding before next Athena deploy

- [ ] Update `chart/values.yaml` per "K8s implications" section above.
- [ ] Update `gitops/apps/wow-cluster/mariadb.yaml` to `replicas: 1` (Galera off) for first try.
- [ ] Update `gitops/apps/wow-cluster/wow-values.yaml` (the overlay) — image init container to debian, v19 URL.
- [ ] Add `affinity` block to chart templates or as a values override (chart-modification → ADR 004).
- [ ] Decide AC-fork-image strategy (open task #1) — keep using upstream walkline `gameserver-ac:v0.0.4` for now (it works locally), AC-fork build is Phase 0.2.

## Reproducing the local stack from scratch

```bash
# 1. Clone fresh ToCloud9 (no git association to our hardfork)
git clone https://github.com/walkline/ToCloud9.git ~/wow-local
cd ~/wow-local

# 2. Patch client-data version
sed -i.bak 's|client-data/releases/download/v16/data.zip|client-data/releases/download/v19/data.zip|g' docker-compose.yaml

# 3. Pre-pull build images (optional, speeds first run)
docker compose --profile setup-ac build

# 4. Setup phase: imports DB schemas + downloads client data
docker compose --profile setup-ac up -d
docker compose --profile setup-ac logs -f       # watch until db-import + download-miscs exit 0

# 5. If alpine's unzip truncated the ZIP, re-extract with debian:
docker run --rm -v wow-local_client_data:/data debian:stable-slim sh -c '
  apt-get update -qq && apt-get install -y unzip
  cd /data && rm -rf Cameras dbc maps mmaps vmaps && unzip -q data.zip && rm data.zip
'

# 6. Down + start ac profile
docker compose --profile setup-ac down
docker compose --profile ac up -d

# 7. Optional: add second gameserver-ac for load-distribution test
#    (edit docker-compose.yaml: add gameserver-ac-2 with image: wow-local-gameserver-ac:latest,
#     identical env, no build block). Then:
docker compose --profile ac up -d gameserver-ac-2

# 8. Client: ~/athena-cluster/ChromieCraft_3.3.5a/Data/enUS/realmlist.wtf → "set realmlist 127.0.0.1"
#    Launch via wine: cd ~/athena-cluster/ChromieCraft_3.3.5a && wine Wow.exe
#    Login as ADMIN / admin
```
