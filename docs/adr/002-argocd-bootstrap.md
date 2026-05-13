# 002. ArgoCD bootstrap mechanic — how athena-wow-cluster apps register with Athena

Date: 2026-05-13

## Status

Accepted

## Context

The Athena cluster runs ArgoCD with the App-of-Apps pattern, all manifests sourced from `Gravey158/athena-cluster` (private). That repo's `CLAUDE.md` historically enforced:

> Don't touch `infrastructure/*`, `apps/services/*`, `clusters/athena/*` outside `customers/` — cluster-level state.

The `customers/`-only constraint dates from when Athena was used as a paying-customer hosting platform. The cluster is now run as a private server (no new customers; only Tobi's streaming remains as a legacy production tenant that must keep working). The constraint is therefore obsolete for non-streaming additions.

Three options were considered initially:

- **A**: Manual one-time `kubectl apply`, then a root `Application-of-Applications` in this repo. Zero touches in athena-cluster.
- **B**: Pointer commit in `athena-cluster/customers/`. Misuses the customers/ semantic (now obsolete anyway).
- **C**: Document a relaxation of the constraint in athena-cluster's CLAUDE.md.

After user input, a fourth, simpler option was chosen:

- **D**: Commit the pointer directly in `athena-cluster/clusters/athena/athena-wow-cluster.yaml`, alongside every other cluster app. Add a one-line annotation to athena-cluster's CLAUDE.md noting that the customers-only constraint is obsolete for new private workloads.

## Decision

**Option D.**

Implementation:

1. **In `athena-cluster/clusters/athena/argocd/athena-project.yaml`**: extend `spec.sourceRepos` with `https://github.com/Gravey158/athena-wow-cluster` so the `athena-cluster` AppProject can host the pointer Application.
2. **In `athena-cluster/clusters/athena/athena-wow-cluster.yaml`** (new): an ArgoCD `Application` resource targeting `gitops/clusters/athena/` in our repo with `directory.recurse: true`. ArgoCD then automatically picks up every sub-`Application` we add there (`arc-controller`, `wow-ci-resources`, `wow-ci-runner-set`, future `mariadb-operator`, `wow-cluster`, etc.).
3. **In `athena-cluster/CLAUDE.md`**: one-line annotation that the customers-only constraint is obsolete for new private workloads; `streaming/` and `infrastructure/` touches remain forbidden.

The pointer Application uses `project: athena-cluster` (the same project as every other cluster app), now that its `sourceRepos` includes our repo.

## Consequences

- After the initial cross-repo commit, **no further manual `kubectl apply` is needed for this project**. New ArgoCD Applications in `athena-wow-cluster/gitops/clusters/athena/` are picked up automatically on the next sync.
- The `athena-cluster` repo carries a single pointer file plus one CLAUDE.md line for this project. No further structural cross-repo touches.
- `Streaming/` (Tobi's legacy production) and `infrastructure/*` remain untouched per athena-cluster CLAUDE.md.
- If we ever fully sever the dependency, removing the three files in athena-cluster is a clean reverse.
- Sealed-secret seeding (e.g. GitHub PAT for ARC) is still a one-time user action per `gitops/apps/wow-ci/SETUP.md`; ArgoCD picks it up after commit.
