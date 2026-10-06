#!/usr/bin/env bash
# =========================================================
# RNVCS — Geri Alma (Rollback) Betiği
# deploy_apply.sh'in bıraktığı /opt/rnvcs/.previous_release işaretine göre
# 'current' linkini bir önceki sürüme döndürür ve servisleri yeniden başlatır.
#
# Kullanım: sudo ./rollback.sh
# =========================================================
set -euo pipefail

CURRENT_LINK="/opt/rnvcs/current"
PREV_FILE="/opt/rnvcs/.previous_release"
SERVICE_CORE="rnvcs-yonetim-servisi"
SERVICE_PANEL_BACKEND="rnvcs-panel-backend"

if [ "$(id -u)" -ne 0 ]; then
  echo "Bu betik sudo ile çalıştırılmalı." >&2
  exit 1
fi

if [ ! -f "$PREV_FILE" ]; then
  echo "HATA: Önceki sürüm bilgisi bulunamadı (${PREV_FILE} yok). Elle müdahale gerekli." >&2
  exit 1
fi

PREV_TARGET="$(cat "$PREV_FILE")"
if [ ! -d "$PREV_TARGET" ]; then
  echo "HATA: Önceki sürüm klasörü artık yok: ${PREV_TARGET}" >&2
  exit 1
fi

echo "=== Geri alınıyor: ${PREV_TARGET} ==="
ln -sfn "$PREV_TARGET" "$CURRENT_LINK"

systemctl restart "${SERVICE_CORE}" 2>/dev/null || true
systemctl restart "${SERVICE_PANEL_BACKEND}" 2>/dev/null || true
sleep 2

echo "=== Geri alma tamamlandı. Şu an aktif: $(readlink -f "$CURRENT_LINK") ==="
echo "!!! UYARI: Bu otomatik bir acil durum geri almasıdır. Neden başarısız olduğunu"
echo "    araştırıp CHANGELOG/ROADMAP'e not düşmeyi unutmayın. !!!"
