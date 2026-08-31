#!/bin/bash
# Back up the Miles Ahead SQLite database.
#
# The database runs in WAL mode, so copying the .db file is NOT safe: committed
# pages live in milesahead.db-wal until a checkpoint, and a plain `cp` silently
# drops them. (Measured on 2026-08-31: a raw copy was missing 25 raw_articles
# rows that existed only in the WAL.) VACUUM INTO takes a consistent snapshot
# without locking out the running app.
#
# python3 is used rather than the sqlite3 CLI because the CLI is not installed
# on the server, while python3 ships SQLite 3.45.1 -- new enough for VACUUM INTO.
set -euo pipefail

DB_PATH="${DB_PATH:-/opt/milesahead/data/milesahead.db}"
BACKUP_DIR="${BACKUP_DIR:-/opt/milesahead/backups}"
RETENTION_DAYS="${RETENTION_DAYS:-30}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BACKUP_FILE="milesahead_${TIMESTAMP}.db"
DEST="${BACKUP_DIR}/${BACKUP_FILE}"

mkdir -p "$BACKUP_DIR"

# Snapshot, then verify the snapshot before we trust it. A backup that has never
# been read back is a guess, not a backup.
python3 - "$DB_PATH" "$DEST" <<'PY'
import sqlite3
import sys

src, dst = sys.argv[1], sys.argv[2]

con = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
try:
    con.execute("VACUUM INTO ?", (dst,))
finally:
    con.close()

check = sqlite3.connect(dst)
try:
    result = check.execute("PRAGMA integrity_check").fetchone()[0]
    if result != "ok":
        raise SystemExit(f"integrity_check failed on {dst}: {result}")
    tables = check.execute(
        "SELECT count(*) FROM sqlite_master WHERE type='table'"
    ).fetchone()[0]
    if tables == 0:
        raise SystemExit(f"{dst} contains no tables")
finally:
    check.close()

print(f"snapshot ok: {dst} ({tables} tables)")
PY

gzip -9 "$DEST"

# Prune old local copies. The off-box copy is handled separately by
# .github/workflows/backup.yml, which uploads to a GitHub release.
find "$BACKUP_DIR" -name 'milesahead_*.db.gz' -mtime "+${RETENTION_DAYS}" -delete

SIZE=$(du -h "${DEST}.gz" | cut -f1)
echo "Backup completed: ${BACKUP_FILE}.gz (${SIZE})"
