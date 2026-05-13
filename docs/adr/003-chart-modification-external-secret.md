# 003. Chart modification: opt-in external Secret for databases config

Date: 2026-05-13

## Status

Accepted

## Context

The inherited ToCloud9 chart at `chart/` generates a `{{ .Release.Name }}-databases-config` Secret in `templates/databases.yaml` by templating plaintext values from `.Values.databases.db_user` and `.Values.databases.db_password`. Every service template (`authserver.yaml`, `gameserver_ac.yaml`, etc.) reads connection strings from this Secret via `valueFrom: secretKeyRef`.

This works for `docker compose --profile ac` and bench-style deployments, but it leaks DB credentials into Helm-values files. For GitOps on Athena where everything is committed (and our repo is public), the plaintext leak is unacceptable.

Three options were considered (recorded in detail in conversation log on 2026-05-13):

- **A**: Chart modification — wrap the generated Secret + init ConfigMap in `{{- if not .Values.databases.externalSecret -}}` so the consumer can supply a pre-existing (sealed) Secret with the same keys.
- **B**: SOPS `helm-secrets` ArgoCD ConfigManagementPlugin, encrypted values file.
- **C**: Plaintext values in git, locked-down private repo just for secrets.

## Decision

**Option A.**

Concrete changes in this commit:

1. `chart/templates/databases.yaml` — wrap entire body in `{{- if not .Values.databases.externalSecret -}} ... {{- end }}`. Add a doc-comment at the top pointing at this ADR.
2. `chart/values.yaml` — add `databases.externalSecret: false` (backwards-compatible default; upstream consumers see no change).
3. `gitops/apps/wow-cluster/wow-values.yaml` — set `databases.externalSecret: true` for the Athena release.
4. `scripts/seal-mariadb-creds.sh` — extended to also seal a `wow-databases-config` Secret with the 9 keys the service templates expect (`user`, `password`, `schema_type`, `AUTH_DB_CONNECTION`, `CHAR_DB_CONNECTION`, `WORLD_DB_CONNECTION`, `AC_LOGIN_DATABASE_INFO`, `AC_WORLD_DATABASE_INFO`, `AC_CHARACTER_DATABASE_INFO`), using the same generated random `acore` password.
5. `gitops/apps/wow-cluster/SETUP.md` — updated workflow.

## Consequences

- **No plaintext DB credentials in git.** The seal script generates random passwords, prints them on stdout once (user saves), and writes only the SealedSecret YAML to git.
- **Upstream-merge friendliness**: the `if not externalSecret` wrapper is minimal and additive. An upstream merge that further changes `databases.yaml` would yield a small conflict at the wrap boundary, easy to resolve.
- **Secret-name coupling**: the Helm release name must be `wow` (we set it via `helm.releaseName: wow` in the ArgoCD Application) so the chart's templated reference `{{ .Release.Name }}-databases-config` matches the secret name we seal (`wow-databases-config`). If the release name ever changes, the seal-script and ArgoCD Application both need updating in lockstep.
- **Tradeoff vs. Option B**: no ArgoCD plugin to maintain, but a chart modification to maintain. Net: one local diff in this hardfork's chart, no infrastructure-level coupling to a secrets plugin.
- **The 9 keys are coupled to the chart's connection-string formats**: any future upstream change to those formats requires updating both the chart and the seal script.
