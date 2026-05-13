#!/usr/bin/env bash
#
# Seal a GitHub PAT into a SealedSecret for the wow-ci ARC runner.
#
# Required env:
#   GITHUB_PAT  -- the PAT value (classic 'repo' or fine-grained Actions+Administration)
#
# Output:
#   gitops/apps/wow-ci/github-pat-sealed-secret.yaml  (committed to git, picked up by ArgoCD)
#
# Usage:
#   GITHUB_PAT=ghp_xxx scripts/seal-github-pat.sh

set -euo pipefail

if [[ -z "${GITHUB_PAT:-}" ]]; then
  echo "ERROR: GITHUB_PAT env var not set." >&2
  echo "Usage: GITHUB_PAT=ghp_xxx $0" >&2
  exit 1
fi

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$REPO/gitops/apps/wow-ci/github-pat-sealed-secret.yaml"

if [[ -f "$OUT" ]]; then
  read -r -p "$OUT exists. Overwrite? [y/N] " ans
  [[ "$ans" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }
fi

kubectl -n wow-ci create secret generic arc-github-pat \
  --from-literal=github_token="$GITHUB_PAT" \
  --dry-run=client -o yaml \
  | kubeseal --controller-name=sealed-secrets-controller \
             --controller-namespace=kube-system \
             -o yaml \
  > "$OUT"

echo
echo "Wrote $OUT"
echo "Next:"
echo "  git add $OUT"
echo "  git commit -m 'chore(wow-ci): seal GitHub PAT for ARC runner'"
echo "  git push"
