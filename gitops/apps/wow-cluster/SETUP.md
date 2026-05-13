# wow-cluster — Setup notes

WoW realm namespace: Galera (mariadb-operator), DB schemas (auth/world/characters), DB user with grants.

## Sealed-Secret seeding (one-time, before ArgoCD can sync `wow-cluster-db`)

The MariaDB CR references two Secrets:

- `mariadb-root` (key `password`) — root password
- `mariadb-acore` (key `password`) — application user `acore`

Both must be sealed and committed to git before `wow-cluster-db` ArgoCD app can sync.

```bash
cd /Users/dennis/athena-wow-cluster
scripts/seal-mariadb-creds.sh
git add gitops/apps/wow-cluster/mariadb-creds-sealed-secret.yaml
git commit -m "chore(wow-cluster): seal MariaDB root + acore passwords"
git push
```

The script prints both passwords on stdout (save them — needed when populating chart values in Phase 0.4).

## Sanity check after sync

```bash
kubectl -n wow-cluster get mariadb wow-galera                            # waits for Ready
kubectl -n wow-cluster get pods -l app.kubernetes.io/instance=wow-galera # 3 pods
kubectl -n wow-cluster get database,user,grant                           # CRs report ready
kubectl -n wow-cluster get svc                                           # wow-galera + wow-galera-primary

# Smoke test: connect as acore user
kubectl -n wow-cluster run mysql-cli --rm -it --restart=Never \
  --image=mariadb:11.4 -- \
  mariadb -h wow-galera-primary -u acore -p"$ACORE_PWD" \
  -e "SHOW DATABASES; SELECT @@hostname, @@wsrep_cluster_size, @@wsrep_cluster_status;"
```

Expected: 3 databases (`acore_auth`, `acore_world`, `acore_characters`), `wsrep_cluster_size=3`, `wsrep_cluster_status=Primary`.

## Known TODOs / future work

- Galera tuning beyond `gcache.size=128M` and `wsrep_slave_threads=2` (Phase 4 perf).
- ServiceMonitor for Prometheus (Phase 1).
- Backup CR (mariadb-operator has `Backup` and `Restore` CRDs) to Longhorn → B2 (Phase 5 hardening).
- Anti-affinity to spread the 3 Galera pods across distinct nodes (currently relies on default soft anti-affinity — verify after deploy).
