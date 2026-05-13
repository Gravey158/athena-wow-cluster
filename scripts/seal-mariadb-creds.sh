#!/usr/bin/env bash
#
# Generate random root + acore passwords for the wow-cluster MariaDB Galera,
# seal them into a single SealedSecret YAML. ALSO generate the
# 'wow-databases-config' SealedSecret with the 9 connection-string keys that
# the Helm chart's service templates expect (see ADR 003 in
# docs/adr/003-chart-modification-external-secret.md).
#
# Output:
#   gitops/apps/wow-cluster/mariadb-creds-sealed-secret.yaml
#     -- mariadb-root + mariadb-acore Secrets (consumed by MariaDB CR + User CR)
#   gitops/apps/wow-cluster/wow-databases-config-sealed-secret.yaml
#     -- wow-databases-config Secret (consumed by every ToCloud9 service)
#
# Usage:
#   scripts/seal-mariadb-creds.sh

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_MARIADB="$REPO/gitops/apps/wow-cluster/mariadb-creds-sealed-secret.yaml"
OUT_DBCONFIG="$REPO/gitops/apps/wow-cluster/wow-databases-config-sealed-secret.yaml"

for f in "$OUT_MARIADB" "$OUT_DBCONFIG"; do
  if [[ -f "$f" ]]; then
    read -r -p "$f exists. Overwrite? [y/N] " ans
    [[ "$ans" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }
  fi
done

ROOT_PWD=$(openssl rand -base64 32 | tr -d '=+/' | head -c 32)
ACORE_PWD=$(openssl rand -base64 32 | tr -d '=+/' | head -c 32)

echo
echo "=========================================================="
echo "  SAVE THESE -- needed for emergency root access:"
echo "=========================================================="
echo "  MARIADB_ROOT_PASSWORD=$ROOT_PWD"
echo "  MARIADB_ACORE_PASSWORD=$ACORE_PWD"
echo "=========================================================="
echo

# (1) MariaDB root + acore Secrets, consumed by the MariaDB and User CRs.
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
} > "$OUT_MARIADB"

echo "Wrote $OUT_MARIADB (mariadb-root + mariadb-acore)"

# (2) wow-databases-config Secret with connection strings, consumed by the
# ToCloud9 chart's service templates.
DB_HOST="wow-galera-primary.wow-cluster.svc.cluster.local"
DB_PORT="3306"
DB_USER="acore"
DB_AUTH="acore_auth"
DB_WORLD="acore_world"
DB_CHARS="acore_characters"

AUTH_CONN="${DB_USER}:${ACORE_PWD}@tcp(${DB_HOST}:${DB_PORT})/${DB_AUTH}"
CHAR_CONN="1:${DB_USER}:${ACORE_PWD}@tcp(${DB_HOST}:${DB_PORT})/${DB_CHARS}"
WORLD_CONN="${DB_USER}:${ACORE_PWD}@tcp(${DB_HOST}:${DB_PORT})/${DB_WORLD}"
AC_LOGIN="${DB_HOST};${DB_PORT};${DB_USER};${ACORE_PWD};${DB_AUTH}"
AC_WORLD="${DB_HOST};${DB_PORT};${DB_USER};${ACORE_PWD};${DB_WORLD}"
AC_CHARS="${DB_HOST};${DB_PORT};${DB_USER};${ACORE_PWD};${DB_CHARS}"

kubectl -n wow-cluster create secret generic wow-databases-config \
  --from-literal=user="${DB_USER}" \
  --from-literal=password="${ACORE_PWD}" \
  --from-literal=schema_type=ac \
  --from-literal=AUTH_DB_CONNECTION="${AUTH_CONN}" \
  --from-literal=CHAR_DB_CONNECTION="${CHAR_CONN}" \
  --from-literal=WORLD_DB_CONNECTION="${WORLD_CONN}" \
  --from-literal=AC_LOGIN_DATABASE_INFO="${AC_LOGIN}" \
  --from-literal=AC_WORLD_DATABASE_INFO="${AC_WORLD}" \
  --from-literal=AC_CHARACTER_DATABASE_INFO="${AC_CHARS}" \
  --dry-run=client -o yaml \
  | kubeseal --controller-name=sealed-secrets-controller \
             --controller-namespace=kube-system \
             -o yaml \
  > "$OUT_DBCONFIG"

echo "Wrote $OUT_DBCONFIG (wow-databases-config with 9 keys)"
echo
echo "Next:"
echo "  git add $OUT_MARIADB $OUT_DBCONFIG"
echo "  git commit -m 'chore(wow-cluster): seal MariaDB + databases-config secrets'"
echo "  git push"
