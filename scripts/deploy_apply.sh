#!/usr/bin/env bash
# =========================================================
# RNVCS — Uzaktan Dağıtım Uygulama Betiği
# CI/CD tarafından SSH ile tetiklenir; hedef makinede (CORE veya panel) çalışır.
# Görevi: gelen tar.gz'yi aç, migration'ları uygula, binary'leri değiştir,
#         servisleri yeniden başlat — mevcut sürümü geri alma için saklayarak.
#
# Kullanım: sudo ./deploy_apply.sh /opt/rnvcs/incoming/rnvcs-vX.Y.Z.tar.gz
# =========================================================
set -euo pipefail

TAR_PATH="${1:?Kullanım: deploy_apply.sh <tar-dosya-yolu>}"
RELEASES_DIR="/opt/rnvcs/releases"
CURRENT_LINK="/opt/rnvcs/current"
SERVICE_CORE="rnvcs-yonetim-servisi"
SERVICE_PANEL_BACKEND="rnvcs-panel-backend"

if [ "$(id -u)" -ne 0 ]; then
  echo "Bu betik sudo ile çalıştırılmalı." >&2
  exit 1
fi

TAR_NAME="$(basename "$TAR_PATH")"
RELEASE_NAME="${TAR_NAME%.tar.gz}"          # rnvcs-vX.Y.Z
RELEASE_DIR="${RELEASES_DIR}/${RELEASE_NAME}"

echo "=== [1/6] ${RELEASE_NAME} açılıyor ==="
mkdir -p "$RELEASES_DIR"
mkdir -p "$RELEASE_DIR"
tar xzf "$TAR_PATH" -C "$RELEASES_DIR"

if [ ! -f "${RELEASE_DIR}/VERSION" ]; then
  echo "HATA: ${RELEASE_DIR}/VERSION bulunamadı, paket bozuk olabilir." >&2
  exit 1
fi
NEW_VERSION="$(cat "${RELEASE_DIR}/VERSION")"
echo "Yeni sürüm: ${NEW_VERSION}"

echo "=== [2/6] Mevcut sürüm yedekleniyor (rollback için) ==="
if [ -L "$CURRENT_LINK" ]; then
  PREV_TARGET="$(readlink -f "$CURRENT_LINK")"
  echo "$PREV_TARGET" > /opt/rnvcs/.previous_release
  echo "Önceki sürüm kaydedildi: ${PREV_TARGET}"
else
  echo "İlk kurulum — önceki sürüm yok."
fi

echo "=== [3/6] Migration'lar uygulanıyor (idempotent, güvenle tekrar çalışır) ==="
if [ -d "${RELEASE_DIR}/migrations" ]; then
  for f in "${RELEASE_DIR}"/migrations/*.sql; do
    [ -f "$f" ] || continue
    echo "  -> $(basename "$f")"
    sudo -u postgres psql -d rnvcs -f "$f"
  done
else
  echo "  (bu pakette migrations/ yok, atlanıyor)"
fi

echo "=== [4/6] Binary izinleri ayarlanıyor ==="
chmod 755 "${RELEASE_DIR}/rnvcs-yonetim-servisi" 2>/dev/null || true
chmod 755 "${RELEASE_DIR}/rnvcs-panel-backend" 2>/dev/null || true

echo "=== [5/6] 'current' sembolik linki yeni sürüme çevriliyor ==="
ln -sfn "$RELEASE_DIR" "$CURRENT_LINK"

echo "=== [6/6] Servisler yeniden başlatılıyor ==="
if systemctl list-unit-files | grep -q "^${SERVICE_CORE}.service"; then
  systemctl restart "${SERVICE_CORE}"
  sleep 2
  systemctl is-active --quiet "${SERVICE_CORE}" && echo "  ${SERVICE_CORE}: OK" || {
    echo "  HATA: ${SERVICE_CORE} başlamadı!" >&2
    exit 1
  }
fi
if systemctl list-unit-files | grep -q "^${SERVICE_PANEL_BACKEND}.service"; then
  systemctl restart "${SERVICE_PANEL_BACKEND}"
  sleep 2
  systemctl is-active --quiet "${SERVICE_PANEL_BACKEND}" && echo "  ${SERVICE_PANEL_BACKEND}: OK" || {
    echo "  HATA: ${SERVICE_PANEL_BACKEND} başlamadı!" >&2
    exit 1
  }
fi

echo "=== Eski sürümleri temizle (son 3 sürümü tut) ==="
cd "$RELEASES_DIR"
ls -dt rnvcs-v*/ 2>/dev/null | tail -n +4 | xargs -r rm -rf

echo "=== TAMAMLANDI: ${NEW_VERSION} kuruldu ve servisler ayakta ==="
