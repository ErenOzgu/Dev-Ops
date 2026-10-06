#!/usr/bin/env bash
# =========================================================
# RNVCS — Deploy Öncesi Veritabanı Yedeği
# Üretime her dağıtımdan hemen önce çalıştırılır (deploy-production.yml).
# Migration bir şeyi bozarsa elimizde deploy-öncesi bir DB anlık görüntüsü olsun diye.
#
# Kullanım: sudo ./backup_current.sh
# =========================================================
set -euo pipefail

BACKUP_DIR="/opt/rnvcs/db-backups"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_FILE="${BACKUP_DIR}/rnvcs-pre-deploy-${TIMESTAMP}.sql.gz"

if [ "$(id -u)" -ne 0 ]; then
  echo "Bu betik sudo ile çalıştırılmalı." >&2
  exit 1
fi

mkdir -p "$BACKUP_DIR"

echo "=== Deploy-öncesi DB yedeği alınıyor: ${BACKUP_FILE} ==="
sudo -u postgres pg_dump rnvcs | gzip > "$BACKUP_FILE"
echo "Yedek boyutu: $(du -h "$BACKUP_FILE" | cut -f1)"

echo "=== Son 10 yedek dışındakiler temizleniyor ==="
cd "$BACKUP_DIR"
ls -t rnvcs-pre-deploy-*.sql.gz 2>/dev/null | tail -n +11 | xargs -r rm -f

echo "=== Tamamlandı ==="
