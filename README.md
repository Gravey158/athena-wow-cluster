# athena-wow-cluster

Kubernetes-native WoW 3.3.5a cross-realm cluster for the Athena Talos cluster.
Hardfork of [walkline/ToCloud9](https://github.com/walkline/ToCloud9) (MIT).

## What this is

ToCloud9's microservice architecture (authserver, game-load-balancer, n× worldserver,
chat / mail / guild / AH / social services) deployed on the Athena Talos K8s cluster,
backed by a Gravey158 AzerothCore fork (MariaDB source-switch) integrated via the
inherited `game-server/` hooks.

Goal: cross-realm BGs / arena / AH / global-channels for <1000 concurrent players on
commodity hardware (i3-7100T mini-PCs).

Non-goal: 10k+ players, multi-expansion (Cata / MoP), upstream-PR pressure.

## Status

**Phase 0 — Reproducible K8s baseline.**
The project goal lives in `.claude/goals/athena-wow-cluster.md` (machine-local, not in git).
`docs/baseline.md` will document current setup state (Phase 0.7).

## Layout

| Path | Origin | Purpose |
|---|---|---|
| `apps/`, `api/`, `gen/`, `shared/`, `sql/` | ToCloud9 | Go microservices, gRPC stubs, shared libs, DB migrations |
| `game-server/` | ToCloud9 | AzerothCore C++ integration patches |
| `chart/` | ToCloud9 (adapted) | Helm chart for K8s deployment |
| `docker-compose.yaml` | ToCloud9 | Local single-service iteration (NOT the Phase 0 baseline) |
| `docs/` | new | Project docs; `docs/adr/` for architecture decisions |
| `ops/` | new | Operational helpers (DB init, realmlist hacks, secret seeds) |
| `scripts/` | new | Maintenance scripts |
| `e2e/` | new | End-to-end tests (skeleton, Phase 5) |
| `.github/` | mixed | CI workflows (inherited) + issue / PR templates (new) |

## Upstream tracking

This is a hardfork, **not** a GitHub fork — no "Open PR upstream" UI pressure.

```
$ git remote -v
origin    https://github.com/Gravey158/athena-wow-cluster.git  (fetch)
origin    https://github.com/Gravey158/athena-wow-cluster.git  (push)
upstream  https://github.com/walkline/ToCloud9.git             (fetch)
upstream  DISABLED_no_pushes_to_upstream                       (push)
```

Selective cherry-picks from upstream are documented per-commit. See
[`docs/adr/001-hardfork-mode.md`](docs/adr/001-hardfork-mode.md) for the rationale.

## License

- ToCloud9 code: MIT, Copyright 2021 walkline — preserved in `LICENSE`.
- AzerothCore patches (`game-server/`): AGPL-3.0 (upstream license).
- Project additions: MIT unless explicitly stated.
- See `NOTICE` for attributions.
