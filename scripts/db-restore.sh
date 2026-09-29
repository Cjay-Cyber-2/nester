#!/usr/bin/env bash
set -euo pipefail

# Nester database restore script
# Usage: scripts/db-restore.sh <path-to-backup.dump> [target DSN]

BACKUP_FILE="${1:-}"
TARGET_DSN="${2:-${DATABASE_DSN:-postgres://nester:nester_dev_password@localhost:5432/nester_dev?sslmode=disable}}"

if [ -z "$BACKUP_FILE" ] || [ ! -f "$BACKUP_FILE" ]; then
  echo "[-] ERROR: Please specify a valid backup file path." >&2
  echo "Usage: $0 <path-to-backup.dump> [target DSN]" >&2
  exit 1
fi

echo "[+] Validating backup file: $BACKUP_FILE ..."
pg_restore --list "$BACKUP_FILE" > /dev/null

echo "[+] Restoring database from $BACKUP_FILE into target ..."
pg_restore --dsn="$TARGET_DSN" --clean --if-exists --no-owner --no-privileges --exit-on-error "$BACKUP_FILE"

echo "[+] Database restore completed successfully."
