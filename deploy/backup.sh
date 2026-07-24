#!/bin/sh
# Nightly SQLite backup (PLAN.md §8.4, layer 1). Uses `sqlite3 .backup`, which is
# safe on a live WAL database — a plain file copy is not. Runs inside the running
# container (it ships sqlite3 and already has the volume mounted), then rotates.
#
# Layer 2 is the Proxmox vzdump/PBS snapshot of the whole VM, configured on the
# host — the two together cover file-level and machine-level recovery.
#
#   sh backup.sh [dest-dir] [keep-days]
set -eu

DEST="${1:-${FST_BACKUP_DIR:-/root/backups/fabscreentime}}"
KEEP="${2:-${FST_BACKUP_KEEP:-14}}"
CONTAINER="${FST_CONTAINER:-fabscreentimed}"
STAMP="$(date +%F)"
OUT="$DEST/fabscreentime-$STAMP.db"

mkdir -p "$DEST"

# .backup writes a consistent snapshot even while the backend is writing.
docker exec "$CONTAINER" sqlite3 /var/lib/fabscreentime/data.db \
  ".backup '/var/lib/fabscreentime/backup.tmp'"
docker cp "$CONTAINER:/var/lib/fabscreentime/backup.tmp" "$OUT"
docker exec "$CONTAINER" rm -f /var/lib/fabscreentime/backup.tmp

gzip -f "$OUT"
echo "backup: ${OUT}.gz ($(du -h "${OUT}.gz" | cut -f1))"

# Rotate: drop backups older than KEEP days.
find "$DEST" -name 'fabscreentime-*.db.gz' -mtime "+$KEEP" -delete 2>/dev/null || true
