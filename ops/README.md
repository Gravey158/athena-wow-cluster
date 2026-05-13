# ops/

Operational helpers — local single-service iteration and one-shot tooling.

## Important: not the Phase-0 baseline

The Phase-0 reproducible baseline is a K8s deployment on Athena via Helm + ArgoCD.
`ops/` is for things like:

- Running one Go microservice locally against a NATS subset for fast iteration.
- DB-init dump generation for the Galera-cluster bootstrap (Phase 0.3).
- Realmlist patch helpers for pointing a local `Wow.exe` at `wow-cluster`.
- One-shot debug containers.

The inherited `docker-compose.yaml` at the repo root falls into the same category —
it is NOT the deployment baseline; it is a single-service-iteration tool.

## Conventions

- Sub-dirs by purpose (e.g. `ops/db-init/`, `ops/local-microservice/`).
- Each sub-dir gets its own README with usage notes and a list of required env vars.
- Idempotent where possible; if not, document the cleanup step.
