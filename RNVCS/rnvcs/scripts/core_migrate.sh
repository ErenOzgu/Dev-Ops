#!/usr/bin/env bash
# =========================================================
# RNVCS-CORE — Migration Uygulama Script'i (v2 — otomatik keşif)
#
# migrations/ klasöründeki TÜM core_migration_*.sql dosyalarını
# isim sırasına göre otomatik bulup uygular. Yeni bir migration
# eklemek için tek yapılması gereken migrations/ altına numarası
# bir sonraki olan bir dosya koymak — bu script'e dokunmaya gerek yok.
#
# Kullanım: sudo bash core_migrate.sh
# =========================================================

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Bu script root ile çalıştırılmalı. Örnek: sudo bash core_migrate.sh"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="$SCRIPT_DIR/../migrations"
DB_NAME="rnvcs"

if [[ ! -d "$MIGRATIONS_DIR" ]]; then
  echo "HATA: $MIGRATIONS_DIR bulunamadı."
  exit 1
fi

shopt -s nullglob
FILES=("$MIGRATIONS_DIR"/core_migration_*.sql)
shopt -u nullglob

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "HATA: $MIGRATIONS_DIR içinde hiç core_migration_*.sql dosyası yok."
  exit 1
fi

# İsme göre sırala (001, 002, 003... doğal sırada gelir)
IFS=$'\n' SORTED=($(sort <<<"${FILES[*]}")); unset IFS

echo "==> Bulunan migration dosyaları (uygulama sırası):"
for f in "${SORTED[@]}"; do echo "    - $(basename "$f")"; done
echo ""

i=1
for f in "${SORTED[@]}"; do
  TMP="/tmp/rnvcs_$(basename "$f")"
  cp "$f" "$TMP"
  chmod 644 "$TMP"
  echo "==> [$i/${#SORTED[@]}] $(basename "$f") uygulanıyor..."
  sudo -u postgres psql -d "$DB_NAME" -v ON_ERROR_STOP=1 -f "$TMP"
  rm -f "$TMP"
  i=$((i+1))
done

echo "==> Asterisk yeniden başlatılıyor..."
systemctl restart asterisk
sleep 2

echo ""
echo "========================================================="
echo " MIGRATION ÖZETİ — tablo bazında doğrulama"
echo "========================================================="

check_table() {
  if sudo -u postgres psql -d "$DB_NAME" -tc \
      "SELECT 1 FROM information_schema.tables WHERE table_name='$1'" \
      | grep -q 1; then
    printf "  [OK]   %s (tablo: %s)\n" "$2" "$1"
  else
    printf "  [HATA] %s  (tablo bulunamadı: %s)\n" "$2" "$1"
  fi
}

check_table users                  "Kullanıcılar"
check_table panels                 "Paneller"
check_table user_panel_permissions "Kullanıcı-Panel yetkileri"
check_table ring_groups            "Çatal arama grupları"
check_table ring_group_members     "Çatal arama üyeleri"
check_table speed_dials            "Kısayollar"
check_table iax_trunks             "IAX trunk tanımları"
check_table event_log              "Olay kayıtları"
check_table asterisk_cdr           "Asterisk CDR"

echo " -- Asterisk Servisi --"
if systemctl is-active --quiet asterisk; then
  printf "  [OK]   Asterisk servisi (restart sonrası aktif)\n"
else
  printf "  [HATA] Asterisk servisi restart sonrası aktif değil\n"
fi

echo " -- ODBC Bağlantı Testi --"
ODBC_OUT="$(asterisk -rx "odbc show all" 2>/dev/null || true)"
if echo "$ODBC_OUT" | grep -qi "Connected: Yes"; then
  printf "  [OK]   Asterisk res_odbc 'rnvcs' bağlantısı canlı\n"
elif echo "$ODBC_OUT" | grep -qE "active connections: [1-9]"; then
  printf "  [OK]   Asterisk res_odbc 'rnvcs' bağlantısı canlı (aktif bağlantı var)\n"
else
  printf "  [HATA] Asterisk res_odbc bağlantısı doğrulanamadı\n"
fi

echo "========================================================="
echo " Migration tamamlandı ($((i-1)) dosya uygulandı)."
echo "========================================================="
