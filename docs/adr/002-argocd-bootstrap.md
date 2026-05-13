# 002. ArgoCD bootstrap mechanic — how athena-wow-cluster apps register with Athena

Date: 2026-05-13

## Status

Proposed (decision pending; will become Accepted once user picks an option).

## Context

The Athena cluster runs ArgoCD with the App-of-Apps pattern, all manifests sourced from `Gravey158/athena-cluster` (private). That repo's `CLAUDE.md` enforces hard constraints:

> Don't touch `infrastructure/*`, `apps/services/*`, `clusters/athena/*` outside `customers/` — cluster-level state.

Our project `athena-wow-cluster` is a separate repo (`Gravey158/athena-wow-cluster`, public) holding its own manifests under `gitops/apps/<name>/` and ArgoCD Application resources under `gitops/clusters/athena/<name>.yaml`. We need to choose how those Applications get registered with ArgoCD on Athena without violating the touch restriction.

Three options:

### Option A: Manual one-time `kubectl apply`, then ArgoCD self-manages

```bash
kubectl apply -f gitops/clusters/athena/arc-controller.yaml
kubectl apply -f gitops/clusters/athena/wow-ci-resources.yaml
kubectl apply -f gitops/clusters/athena/wow-ci-runner-set.yaml
# Each new Application added later requires another manual apply.
```

- **Pro**: zero touches to `athena-cluster` repo. Each new App in this project is one `kubectl apply`.
- **Pro**: clean separation — ArgoCD Applications themselves are managed by the user (or via this repo's CI later) rather than by another repo's app-of-apps.
- **Con**: not GitOps-managed at the App-resource level. If you forget to apply a new App, it doesn't exist on the cluster. Could be fixed by an `Application-of-Applications` Application in this repo that points to `gitops/clusters/athena/` itself (recursive sync). That single root App is the only manual `kubectl apply` ever needed.
- **Con**: no central place in the cluster repo that lists "which apps run here".

### Option B: Pointer commit in `athena-cluster/customers/`

The athena-cluster CLAUDE.md leaves `customers/` as the only "free" subtree. We commit `athena-cluster/customers/athena-wow.yaml` — a single ArgoCD `Application` that points at `gitops/clusters/athena/` in this repo and recurses.

- **Pro**: standard GitOps; one place lists all cluster apps.
- **Con**: misuses the `customers/` namespace semantically (it's for paying-customer Stripe-provisioned workloads).
- **Con**: requires a commit in the athena-cluster repo — the only one for this project, but it's a precedent.

### Option C: Document a relaxation of the constraint

Edit `athena-cluster/CLAUDE.md` to add an explicit exception for `clusters/athena/athena-wow-cluster.yaml`, then commit the pointer there normally.

- **Pro**: most natural location, with the rest of the cluster's apps.
- **Con**: changes the cluster repo's contract, requires care.

## Decision

**Pending.** User input required.

Recommendation: **Option A, with a root App-of-Apps inside this repo.** Concrete shape:

- `gitops/clusters/athena/_root.yaml` — an ArgoCD `Application` whose source is `gitops/clusters/athena/` recursively, deploying every other `Application` resource there. Apply this *one* file manually; ArgoCD picks up every other Application from there.
- New Applications are added to `gitops/clusters/athena/<name>.yaml` in regular commits. No more `kubectl apply` needed.
- The `athena-cluster` repo and its constraints stay untouched.

## Consequences

To be filled in once decided.
