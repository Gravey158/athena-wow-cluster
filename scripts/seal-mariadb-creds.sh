#!/usr/bin/env bash
#
# Generate random root + acore passwords for the wow-cluster MariaDB Galera,
# seal them into a single SealedSecret YAML.
#
# Output:
#   gitops/apps/wow-cluster/mariadb-creds-sealed-secret.yaml  (commit to git)
#
# Usage:
#   scripts/seal-mariadb-creds.sh

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$REPO/gitops/apps/wow-cluster/mariadb-creds-sealed-secret.yaml"

if [[ -f "$OUT" ]]; then
  read -r -p "$OUT exists. Overwrite? [y/N] " ans
  [[ "$ans" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }
fi

ROOT_PWD=$(openssl rand -base64 32 | tr -d '=+/' | head -c 32)
ACORE_PWD=$(openssl rand -base64 32 | tr -d '=+/' | head -c 32)

echo
echo "=========================================================="
echo "  SAVE THESE -- needed for chart/values overlay in 0.4:"
echo "=========================================================="
echo "  MARIADB_ROOT_PASSWORD=$ROOT_PWD"
echo "  MARIADB_ACORE_PASSWORD=$ACORE_PWD"
echo "=========================================================="
echo

{
  kubectl -n wow-cluster create secret generic mariadb-root \
    --from-literal=password="$ROOT_PWD" \
    --dry-run=client -o yaml \
    | kubeseal --controller-name=sealed-secrets-controller \
               --controller-namespace=kube-system \
               -o yaml
  echo "---"
  kubectl -n wow-cluster create secret generic mariadb-acore \
    --from-literal=password="$ACORE_PWD" \
    --dry-run=client -o yaml \
    | kubeseal --controller-name=sealed-secrets-controller \
               --controller-namespace=kube-system \
               -o yaml
} > "$OUT"

echo "Wrote $OUT (2 sealed secrets)"
echo "Next:"
echo "  git add $OUT"
echo "  git commit -m 'chore(wow-cluster): seal MariaDB root + acore passwords'"
echo "  git push"
