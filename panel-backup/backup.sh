#!/bin/bash
set -uo pipefail

# Determine mode
MODE="backup"
if [ -n "${RESTORE_USER:-}" ]; then MODE="restore"; fi

USER="${BACKUP_USER:-${RESTORE_USER:-admin}}"
NS="${BACKUP_NAMESPACE:-${RESTORE_NAMESPACE:-hosting-system}}"
COMPONENTS="${BACKUP_COMPONENTS:-${RESTORE_COMPONENTS:-web,db}}"
BACKUP_DIR="/backups"
BACKUP_ID="${BACKUP_ID:-${RESTORE_BACKUP_ID:-backup-${USER}-$(date +%s)}}"
BACKUP_PATH="${BACKUP_DIR}/${BACKUP_ID}"

echo "=== Panel Backup Tool ==="
echo "Mode: $MODE | User: $USER | Components: $COMPONENTS"
echo "Backup path: $BACKUP_PATH"

if [ "$MODE" = "backup" ]; then
    mkdir -p "$BACKUP_PATH"

    IFS=',' read -ra COMP_LIST <<< "$COMPONENTS"
    for comp in "${COMP_LIST[@]}"; do
        case "$comp" in
            web)
                echo "[backup] Backing up web files..."
                if [ -d /data/web ] && [ "$(ls -A /data/web 2>/dev/null)" ]; then
                    tar czf "${BACKUP_PATH}/web.tar.gz" -C /data/web . 2>/dev/null && \
                        echo "[backup] Web backup OK: $(du -sh ${BACKUP_PATH}/web.tar.gz | cut -f1)" || \
                        echo "[backup] Web backup failed"
                else
                    echo "[backup] /data/web empty or not mounted, skipping"
                fi
                ;;
            db)
                echo "[backup] Backing up databases..."
                if [ -n "${MARIADB_HOST:-}" ] && [ -n "${MARIADB_ROOT_PASSWORD:-}" ]; then
                    DBS=$(mysql -h "$MARIADB_HOST" -P "${MARIADB_PORT:-3306}" -u root -p"${MARIADB_ROOT_PASSWORD}" -N -e \
                        "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys')" 2>/dev/null || true)
                    if [ -n "$DBS" ]; then
                        for db in $DBS; do
                            echo "[backup]   Dumping $db..."
                            mysqldump -h "$MARIADB_HOST" -P "${MARIADB_PORT:-3306}" -u root -p"${MARIADB_ROOT_PASSWORD}" \
                                --single-transaction "$db" > "${BACKUP_PATH}/db-${db}.sql" 2>/dev/null && \
                                echo "[backup]   $db OK" || echo "[backup]   $db FAILED"
                        done
                    else
                        echo "[backup] No user databases found"
                    fi
                else
                    echo "[backup] MARIADB_HOST not set, skipping DB backup"
                fi
                ;;
            *)
                echo "[backup] $comp: not yet implemented"
                ;;
        esac
    done

    # Write manifest
    echo "{\"id\":\"${BACKUP_ID}\",\"user\":\"${USER}\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"components\":\"${COMPONENTS}\"}" > "${BACKUP_PATH}/manifest.json"
    echo "[backup] Complete. Contents:"
    ls -la "${BACKUP_PATH}/"

elif [ "$MODE" = "restore" ]; then
    if [ ! -d "$BACKUP_PATH" ]; then
        echo "[restore] ERROR: Backup not found: $BACKUP_PATH"
        exit 1
    fi
    echo "[restore] Restoring from: $BACKUP_PATH"
    cat "${BACKUP_PATH}/manifest.json" 2>/dev/null || true

    IFS=',' read -ra COMP_LIST <<< "$COMPONENTS"
    for comp in "${COMP_LIST[@]}"; do
        case "$comp" in
            web)
                if [ -f "${BACKUP_PATH}/web.tar.gz" ] && [ -d /data/web ]; then
                    echo "[restore] Restoring web files..."
                    tar xzf "${BACKUP_PATH}/web.tar.gz" -C /data/web && \
                        echo "[restore] Web files restored" || echo "[restore] Web restore failed"
                else
                    echo "[restore] No web backup or /data/web not mounted"
                fi
                ;;
            db)
                if [ -n "${MARIADB_HOST:-}" ] && [ -n "${MARIADB_ROOT_PASSWORD:-}" ]; then
                    for sqlfile in "${BACKUP_PATH}"/db-*.sql; do
                        [ -f "$sqlfile" ] || continue
                        dbname=$(basename "$sqlfile" .sql | sed 's/^db-//')
                        echo "[restore]   Restoring $dbname..."
                        mysql -h "$MARIADB_HOST" -P "${MARIADB_PORT:-3306}" -u root -p"${MARIADB_ROOT_PASSWORD}" \
                            "$dbname" < "$sqlfile" 2>/dev/null && \
                            echo "[restore]   $dbname OK" || echo "[restore]   $dbname FAILED"
                    done
                else
                    echo "[restore] MARIADB_HOST not set, skipping"
                fi
                ;;
            *)
                echo "[restore] $comp: not yet implemented"
                ;;
        esac
    done
    echo "[restore] Complete"
fi
