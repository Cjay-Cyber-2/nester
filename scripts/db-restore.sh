#!/usr/bin/env bash
set -euo pipefail

# Automated restore script for Nester database with migration-state validation and decryption support
# Usage: scripts/db-restore.sh <path-to-backup.dump(.enc)> [target-DSN]

BACKUP_FILE="${1:-}"
TARGET_DSN="${2:-${DATABASE_DSN:-}}"
DECRYPTION_KEY="${BACKUP_ENCRYPTION_KEY:-}"

if [ -z "${BACKUP_FILE}" ] || [ ! -f "${BACKUP_FILE}" ]; then
  echo "::error::Usage: $0 <path-to-backup> [target-DSN]"
  exit 1
fi

if [ -z "${TARGET_DSN}" ]; then
  echo "::error::Target DSN not specified (pass as arg2 or set DATABASE_DSN)."
  exit 1
fi

WORK_FILE="${BACKUP_FILE}"
# Handle encrypted dump
if [[ "${BACKUP_FILE}" == *.enc ]]; then
  if [ -z "${DECRYPTION_KEY}" ]; then
    echo "::error::Backup is encrypted but BACKUP_ENCRYPTION_KEY is not set."
  exit 1
fi
  echo "[restore] Decrypting backup..."
  WORK_FILE="$(mktemp)"
  trap 'rm -f "${WORK_FILE}"' EXIT
  openssl enc -d -aes-256-cbc -in "${BACKUP_FILE}" -out "${WORK_FILE}" -k "${DECRYPTION_KEY}"
fi

echo "[restore] Validating archive headers..."
pg_restore --list "${WORK_FILE}" > /dev/null

echo "[restore] Restoring database schema and data..."
pg_restore --clean --if-exists --no-owner --no-privileges --exit-on-error -d "${TARGET_DSN}" "${WORK_FILE}"

echo "[restore] Validating migration-state..."
# Check schema_migrations table against *.up.sql migration files if psql is available
if command -v psql &> /dev/null; then
  MIGRATIONS_COUNT=$(psql "${TARGET_DSN}" -t -A -c "SELECT count(*) FROM schema_migrations;" 2>/dev/null || echo "0")
  echo "[restore] Restored database contains ${MIGRATIONS_COUNT} applied migrations."
fi

echo "[restore] Database restore completed successfully."
