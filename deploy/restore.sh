#!/bin/sh
# Restore the FabScreenTime database from a nightly backup (PLAN.md §8.4 DR drill).
#
#   sh restore.sh /root/backups/fabscreentime/fabscreentime-YYYY-MM-DD.db.gz
#
# Two non-obvious steps this encodes, both learned from an actual drill:
#   1. The stale -wal/-shm sidecars MUST be removed with the swapped-in file, or
#      SQLite replays the old WAL over the restored snapshot.
#   2. `docker cp` writes as root, but the container runs as uid 10001, so the
#      restored file must be chown'ed or the backend crash-loops with
#      "unable to open database file (14)".
set -eu

SRC="${1:?usage: restore.sh <backup.db.gz|backup.db>}"
DIR="${FST_DIR:-/opt/fabscreentime}"
PROJECT=fabscreentime
CONTAINER="${FST_CONTAINER:-fabscreentimed}"
VOLUME="${FST_VOLUME:-fabscreentime_fst_data}"
UID_GID="${FST_UID_GID:-10001:10001}"
PORT="${FST_PORT:-8090}"

[ -f "$SRC" ] || { echo "no such backup: $SRC" >&2; exit 1; }

echo "==> Stopping backend"
cd "$DIR/deploy"
docker compose -p "$PROJECT" stop

echo "==> Restoring $SRC"
TMP=/tmp/fst-restore.$$.db
case "$SRC" in
  *.gz) gunzip -c "$SRC" > "$TMP" ;;
  *)    cp "$SRC" "$TMP" ;;
esac
docker cp "$TMP" "$CONTAINER:/var/lib/fabscreentime/data.db"
find /tmp -maxdepth 1 -name "fst-restore.$$.db" -delete

# Drop stale WAL/SHM and fix ownership for the non-root runtime user.
docker run --rm -v "$VOLUME:/d" alpine:3.22 sh -c \
  'find /d -maxdepth 1 \( -name "data.db-wal" -o -name "data.db-shm" \) -delete; chown '"$UID_GID"' /d/data.db'

echo "==> Starting backend"
docker compose -p "$PROJECT" start

i=0
while [ "$i" -lt 30 ]; do
  if wget -qO- --timeout=3 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    echo "==> Restored and healthy."
    docker exec "$CONTAINER" sqlite3 /var/lib/fabscreentime/data.db \
      "select count(*) || ' samples, ' || (select count(*) from devices) || ' devices' from samples;"
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done
echo "!! Backend unhealthy after restore; logs:" >&2
docker compose -p "$PROJECT" logs --tail 30 >&2
exit 1
