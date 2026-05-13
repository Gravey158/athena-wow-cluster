# 001. Hardfork mode: mirror clone without GitHub fork relationship

Date: 2026-05-13

## Status

Accepted

## Context

We needed to clone `walkline/ToCloud9` into `Gravey158/athena-wow-cluster` and decide on the fork relationship. Three options were considered:

1. **GitHub Fork** — UI badge "forked from walkline/ToCloud9", encourages upstream PRs, GitHub UI tracks divergence.
2. **Regular clone + remote rewire** — `git clone`, `gh repo create`, push `origin` to ours, add `upstream` remote pointing to walkline.
3. **Mirror clone + push to fresh repo** — `git clone --mirror`, push all refs (branches + tags) to an empty new repo, add `upstream` remote separately.

The project goal (`.claude/goals/athena-wow-cluster.md`) explicitly states "Kein Upstream-PR-Pflicht" — this is a hardfork with its own maintenance lifecycle, not an extension that should default to upstream collaboration.

## Decision

**Option 3.**

Concrete steps performed on 2026-05-13:

```bash
git clone --mirror https://github.com/walkline/ToCloud9.git tc9-mirror
gh repo create Gravey158/athena-wow-cluster --public \
  --description "Kubernetes-native cross-realm WoW 3.3.5a cluster. Hardfork of walkline/ToCloud9 + AzerothCore-Fork with MariaDB source switch. Deploys to Talos K8s 'Athena' cluster."
cd tc9-mirror && git push --mirror https://github.com/Gravey158/athena-wow-cluster.git
# In working directory /Users/dennis/athena-wow-cluster:
git init -b master
git remote add origin   https://github.com/Gravey158/athena-wow-cluster.git
git remote add upstream https://github.com/walkline/ToCloud9.git
git remote set-url --push upstream DISABLED_no_pushes_to_upstream
git fetch origin && git checkout master
```

## Consequences

- **No "Open PR upstream" pressure** baked into the GitHub UI; the UI does not mark `Gravey158/athena-wow-cluster` as a fork.
- **Upstream tracking** is manual: cherry-picks or selective merges from `upstream/master` happen on the maintainer's terms, documented per-commit.
- **License obligations preserved**: `LICENSE` (MIT, Copyright 2021 walkline) is unchanged. `NOTICE` documents the hardfork relationship.
- **`refs/pull/*` rejected** by GitHub during mirror push — expected and harmless (GitHub policy: PR refs cannot be pushed to a different repo). We do not carry upstream PR history.
- **Severance is trivial** — removing the `upstream` remote later is one command if we want to fully decouple.
- **Branch parity at bootstrap**: `master`, `cmangos`, `libsidecar-cpp` were copied; tags `v0.0.1`–`v0.0.4` were copied.
