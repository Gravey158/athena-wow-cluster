# 007. world_db Static-Data Microservice

Date: 2026-05-13
Status: Proposed (scaffold + creature_template POC committed; ObjectMgr-hook + remaining tables in follow-up)

## Context

After today's work, the per-pod AC worldserver baseline cost on the local
docker-compose stack settled at:

| Resource | per pod | comment |
|---|---|---|
| CPU idle | ~25% of 1 core | dominated by libsidecar gRPC + NATS threads after the [ADR-004] map-split + the `Map::Update` idle-skip patch ([commit `908df0e`](#)) |
| RAM | ~1.0 GiB | dominated by ObjectMgr static-data containers |

The CPU-idle bottleneck is solved — 1:1 Map=Pod is now CPU-feasible.
**RAM is the remaining hard floor.** Per worldserver process AC loads (at boot,
into in-memory `std::unordered_map`-style containers, never evicts):

| ObjectMgr container | rows in TC9 acore_world | RAM est. |
|---|---|---|
| `_creatureTemplateStore` | ~50,000 | ~120 MB |
| `_itemTemplateStore` | ~50,000 | ~80 MB |
| `_gameObjectTemplateStore` | ~30,000 | ~40 MB |
| `_questTemplates` | ~20,000 | ~80 MB |
| `_spellInfoStore` (parsed from DBC) | ~50,000 | ~200 MB |
| `_scriptInfoStore` (SAI, smart_scripts) | ~200,000 | ~50 MB |
| `_conditionStore` | ~100,000 | ~30 MB |
| ~80 smaller tables (vendor lists, npc_trainer, gossip, lore_text, …) | varies | ~150 MB |
| **total** | | **~750 MB** |

This data is **bitwise identical across every pod**. With 100 pods (1:1 Map=Pod
strict reading of [ADR-004]), 100 × 750 MB = **75 GiB of duplicated RAM** just
for the static-data tables — exceeds the entire 32 GiB Talos worker pool by 2x.

A 4-pod open-world design has 4 × 750 MB = 3 GiB duplicated; fine.
A 1:1 Map=Pod design needs this fixed before it's practical.

## Decision

Introduce a dedicated **`worlddbserver`** microservice that owns the static
read-mostly world_db. Worldserver processes consult it via gRPC instead of
loading the data themselves.

### Topology

```
                     ┌──────────────────────┐
                     │ acore_world (MySQL)  │
                     │ Galera replicated    │
                     └──────────┬───────────┘
                                │ read-only SELECT *
                                ▼
                  ┌─────────────────────────────┐
                  │ worlddbserver (Go)          │
                  │ - loads all static tables   │
                  │   into in-process structs   │
                  │ - exposes gRPC unary calls  │
                  │ - one replica is fine; can  │
                  │   scale up with leader-     │
                  │   elected cache-invalidator │
                  └─────────────┬───────────────┘
                                │ gRPC (bidi-stream for warmup,
                                │       unary for cache-miss)
            ┌───────────────────┼───────────────────┐
            ▼                   ▼                   ▼
       ┌────────┐          ┌────────┐          ┌────────┐
       │ gs pod │          │ gs pod │          │ gs pod │
       │ map=0  │   ...    │ map=571│   ...    │ inst:XX│
       │        │          │        │          │ (ephemeral)
       └────────┘          └────────┘          └────────┘
```

### gameserver-side: warm cache + read-through

The worldserver's `ObjectMgr` does not change its API. We modify the loaders
to populate the in-memory containers from gRPC instead of MySQL:

1. **Phase 1 (POC, this commit)**: keep MySQL load path AND introduce gRPC
   client. Run both side by side, log diffs. No RAM win yet — proves the
   service correctness.
2. **Phase 2**: drop the MySQL load path for the migrated tables. Worldserver
   pulls full snapshot from worlddbserver on boot. Same RAM, but config
   complexity reduced (worldserver no longer needs world_db credentials).
3. **Phase 3 (the RAM win)**: replace the full-snapshot-on-boot with an LRU
   cache (~5,000 entries default). ObjectMgr getters become async-ish: hit
   local LRU on hot path, fallback to gRPC on miss. Cache evicts cold entries.
   For a 1:1 Map=Pod with one map's working set, expected LRU residency:
   ~500 creature templates + ~1,500 item templates + ~200 gameobject templates
   = ~3 MB working-set RAM per pod, down from ~250 MB.

Phase 1 is the focus of THIS ADR. Phases 2 and 3 are sketched here but their
detailed design will be revisited in follow-up ADRs once Phase 1 measures the
gRPC roundtrip cost on hot paths.

### Service shape: one combined microservice, not many

A single `worlddbserver` covers all static tables. Rationale:

- These tables are read together at startup; one service can hold them all
  in shared memory cheaply (~750 MB once vs distributed).
- Cross-table joins exist (item_template references item_set, creature_template
  references creature_equip_template). Hosting them together makes joined
  lookups in-process (`item_template_with_set(entry)`).
- One gRPC client connection per worldserver is cheaper than 8.

Single service can be horizontally scaled later via leader-elected
`MapsReassigned`-style invalidation (existing pattern in `apps/servers-registry`)
if read throughput becomes a bottleneck — not anticipated for WoW-scale
loads (each player triggers maybe 10-100 lookups/sec at peak).

### Proto sketch

```proto
service WorldDBService {
  // Phase 1+2: bulk snapshot APIs for boot warmup
  rpc GetAllCreatureTemplates(Empty) returns (stream CreatureTemplate);
  rpc GetAllItemTemplates(Empty) returns (stream ItemTemplate);
  // ... per table

  // Phase 3: per-entry getters for LRU misses
  rpc GetCreatureTemplate(GetByEntryRequest) returns (CreatureTemplate);
  rpc GetItemTemplate(GetByEntryRequest) returns (ItemTemplate);
  // ...

  // Invalidation: GM commands / scripts mutate ~handful of templates.
  // Worldserver invalidates its LRU and broadcasts via NATS.
  rpc InvalidateCreatureTemplate(InvalidateRequest) returns (Empty);
}
```

NATS `worlddb.invalidated.creature_template.<entry>` for cluster-wide
invalidation. Pattern matches existing `events-guild`, `events-group`,
`events-servers-registry` subjects.

### What this does NOT do

- Does NOT replace `acore_characters` (mutable per-player state — stays in MySQL).
- Does NOT replace `acore_auth` (already small + the authserver microservice
  owns it).
- Does NOT touch DBC files (`Spell.dbc`, `Map.dbc`, etc.) — those are read
  from disk at boot, are tiny per pod (~200 MB), and `Spell.dbc` is referenced
  by ObjectMgr in ways that don't fit gRPC neatly. Future work; not on the
  critical path.

## Consequences

### Good

- Per-pod RAM for static-data drops from ~750 MB to ~3 MB (Phase 3),
  unlocking the 1:1 Map=Pod ceiling.
- DBA-side: schema changes go to one place. Today, every worldserver
  separately pulls `creature_template`; with the microservice, the schema
  upgrade flows through one service first.
- libsidecar already speaks gRPC to other TC9 microservices; the wiring is
  established.

### Bad

- New microservice = new failure mode. If worlddbserver is unreachable, every
  worldserver lookup that misses LRU stalls. Mitigation: aggressive LRU sizing
  + a stale-but-served circuit breaker that returns last-known-good on
  service-down.
- gRPC roundtrip on hot paths. Even Unix-domain socket gRPC is ~50-100 µs
  per call. With AC's 100ms tick budget, a single lookup is fine, but
  burst scenarios (player loots 10 items at once, addon spam-queries spell
  data) need batching. **Phase 1 will measure this.**
- AC-side patches to ObjectMgr touch dozens of files. Risk of subtle
  loading-order regressions during transition. Recovery: keep MySQL load
  path behind a feature flag through Phase 2.

### Risks worth calling out

- **Spell data**: `_spellInfoStore` is built from DBC parsing + custom DB
  patches. The ObjectMgr code that constructs SpellInfo references almost
  every other table during load. This is the highest-risk migration; it
  comes LAST after we've proven the pattern on simpler tables.
- **Quest data**: quest templates reference creature_template,
  gameobject_template, item_template, area_table_dbc, faction_dbc, …
  Same problem — quest is one of the LAST tables we migrate.

## Implementation order

1. `worlddbserver` scaffold (this ADR's accompanying commits): Go service
   in `apps/worlddbserver/` with `cmd`, `config`, `repo`, `server`, mirroring
   the existing TC9 service layout. Just `GetAllCreatureTemplates` (bulk).
2. AC-side: stub a `WorldDBClient` in libsidecar that talks to `worlddbserver`,
   pulls all creature_template at boot, populates ObjectMgr in parallel with
   MySQL load. Log diffs.
3. After verification: drop the MySQL load path for `creature_template` only,
   re-measure RAM (expected drop: ~120 MB per pod).
4. Repeat for `item_template`, `gameobject_template`, simpler tables.
5. `quest_template` + `spell_template` LAST, with feature flags.
6. (Optional) Phase 3 LRU once Phase 2 is everywhere.

This ADR is the design lock-in. Code lands incrementally.
