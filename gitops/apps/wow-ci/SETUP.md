# wow-ci — Setup notes

Self-hosted GitHub Actions runner via [Actions Runner Controller](https://github.com/actions/actions-runner-controller) `gha-runner-scale-set` 0.14.1.

## One-time bootstrap (until ADR 002 resolves App-of-Apps mechanic)

```bash
# 1. Apply the three ArgoCD Applications. ArgoCD takes over after this.
kubectl apply -f gitops/clusters/athena/arc-controller.yaml
kubectl apply -f gitops/clusters/athena/wow-ci-resources.yaml
kubectl apply -f gitops/clusters/athena/wow-ci-runner-set.yaml
```

The first sync of `wow-ci-runner-set` will fail until the PAT secret exists (next section).

## GitHub PAT (Personal Access Token)

The runner authenticates to GitHub via a PAT. Classic PAT with `repo` scope (or fine-grained PAT with Actions: read+write + Administration: read+write on `Gravey158/athena-wow-cluster`).

1. Create a fine-grained PAT on https://github.com/settings/personal-access-tokens
   - Resource owner: `Gravey158`
   - Repository access: only `Gravey158/athena-wow-cluster`
   - Permissions (Repository):
     - Actions: Read and write
     - Administration: Read and write
     - Metadata: Read-only (auto)
   - Expiration: max (1 year) — rotation TODO at expiry
2. Save the token, you will not see it again.
3. Seal and commit:

```bash
GITHUB_PAT=ghp_xxx scripts/seal-github-pat.sh
git add gitops/apps/wow-ci/github-pat-sealed-secret.yaml
git commit -m "chore(wow-ci): add sealed GitHub PAT for ARC runner"
git push
```

ArgoCD picks it up; the runner-set listener pod will start; the AutoscalingRunnerSet pulls jobs from GitHub.

## Sanity check

```bash
kubectl -n wow-ci get pods
kubectl -n wow-ci get autoscalingrunnerset
kubectl -n arc-systems get pods       # controller pod
gh api repos/Gravey158/athena-wow-cluster/actions/runners --jq '.runners[] | {name, status, labels: [.labels[].name]}'
```

A new runner pod is created on-demand when a workflow with `runs-on: [self-hosted, wow-cluster]` is queued.

## PAT rotation

1. Generate new PAT.
2. Re-run `scripts/seal-github-pat.sh`.
3. Commit, push. ArgoCD updates the SealedSecret → kubeseal-controller updates the Secret → ARC listener restarts and picks up the new token.
4. Revoke the old PAT on GitHub.

## Known TODOs

- Anti-affinity to `ac-worldserver` pod on `talos-quv-i6z` (Lenovo-04) to avoid CPU/RAM contention during AC builds. Will add when verifying the worldserver pod label.
- Cilium NetworkPolicy for `wow-ci` ns — outbound only to `github.com`, `api.github.com`, `ghcr.io`, package registries. Phase 5 hardening.
- Custom runner image with AC build dependencies pre-installed, if standard runner + `setup-actions` proves too slow for AC builds (Phase 0.2 decision point).
