#!/bin/bash
# Backup SQLite database to OCI Object Storage
set -euo pipefail

DB_PATH="/opt/milesahead/milesahead.db"
BACKUP_DIR="/opt/milesahead/backups"
BUCKET="milesahead-backups"
NAMESPACE="${OCI_NAMESPACE:-}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BACKUP_FILE="milesahead_${TIMESTAMP}.db"
RETENTION_DAYS=30

# Create backup directory if it doesn't exist
mkdir -p "$BACKUP_DIR"

# Create a consistent backup using SQLite's backup command
sqlite3 "$DB_PATH" ".backup '${BACKUP_DIR}/${BACKUP_FILE}'"

# Compress the backup
gzip "${BACKUP_DIR}/${BACKUP_FILE}"

# Upload to OCI Object Storage
oci os object put \
    --namespace "$NAMESPACE" \
    --bucket-name "$BUCKET" \
    --file "${BACKUP_DIR}/${BACKUP_FILE}.gz" \
    --name "backups/${BACKUP_FILE}.gz" \
    --no-multipart

# Clean up local backups older than retention period
find "$BACKUP_DIR" -name "milesahead_*.db.gz" -mtime +${RETENTION_DAYS} -delete

echo "Backup completed: ${BACKUP_FILE}.gz"
