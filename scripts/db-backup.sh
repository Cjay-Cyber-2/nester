#!/usr/bin/env bash
set -euo pipefail

# Automated encrypted backup script for Nester mainnet/production database
# Usage: DATABASE_DSN="postgres://..." BACKUP_ENCRYPTION_KEY="..." ./scripts/db-backup.sh

BACKUP_DIR="${BACKUP_DIR:-./backups}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RAW_DUMP="${BACKUP_DIR}/nester_${TIMESTAMP}.dump"
ENC_DUMP="${BACKUP_DIR}/nester_${TIMESTAMP}.dump.enc"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"

mkdir -p "${BACKUP_DIR}"

echo "[backup] Starting logical backup at ${TIMESTAMP}..."

if [ -z "${DATABASE_DSN:-}" ]; then
  echo "::error::DATABASE_DSN is not set."
  exit 1
fi

# Take custom-format (-Fc) pg_dump into temp file
TEMP_DUMP="$(mktemp)"
# Ensure cleanup on exit
trap 'rm -f "${TEMP_DUMP}"' EXIT

pg_dump --format=custom --no-owner --no-privileges "${DATABASE_DSN}" > "${TEMP_DUMP}"

# Validate backup artifact via pg_restore --list
echo "[backup] Validating backup structure..."
pg_restore --list "${TEMP_DUMP}" > /dev/null

# Tripwire secret check
if grep -q -E 'private[-_]?key|sentry[-_]?auth[-_]?token' "${TEMP_DUMP}" 2>/dev/null; then
  echo "::error::Backup tripwire triggered: potential secret detected in database dump!"
    exit 1
  fi

# Encrypt dump if BACKUP_ENCRYPTION_KEY or OPENSSL_KEY is provided
if [ -n "${BACKUP_ENCRYPTION_KEY:-}" ]; then
  echo "[backup] Encrypting backup with AES-256-CBC..."
  openssl enc -aes-256-cbc -salt -in "${TEMP_DUMP}" -out "${ENC_DUMP}" -k "${BACKUP_ENCRYPTION_KEY}"
  rm -f "${TEMP_DUMP}"
  echo "[backup] Encrypted backup stored at ${ENC_DUMP}"
else
  mv "${TEMP_DUMP}" "${RAW_DUMP}"
  echo "[backup] Backup stored at ${RAW_DUMP} (WARNING: unencrypted, set BACKUP_ENCRYPTION_KEY for production)"
fi

# Prune backups older than retention period
echo "[backup] Pruning backups older than ${RETENTION_DAYS} days..."
find "${BACKUP_DIR}" -name "nester_*.dump*" -type f -mtime +"${RETENTION_DAYS}" -exec rm -f {} \; || true

echo "[backup] Completed successfully."
