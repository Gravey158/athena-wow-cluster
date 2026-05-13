# scripts/

Maintenance scripts. Shell or Go preferred; no Python build pipelines (consistency with non-goals).

Planned contents:

- `bump-chart-version.sh` — semver bump in `chart/Chart.yaml` + git tag.
- `guid-pool-sanity-check.go` — Phase 5 hardening, cross-check shared GUID pool state against Galera.
- `seed-test-account.sh` — insert a test account into the `auth` schema for E2E (Phase 0.6).

Each script must:

- Be idempotent (safe to re-run).
- Document required env vars in its header (no env-discovery via `printenv | grep`).
- Fail loudly on missing prerequisites (`set -euo pipefail` for shell).
