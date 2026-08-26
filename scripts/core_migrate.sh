#!/usr/bin/env bash
# =========================================================
# RNVCS-CORE — Migration Uygulama Script'i
#
# core_kurulum.sh TAMAMLANDIKTAN SONRA (rnvcs veritabanı/kullanıcısı
# hazır olduktan sonra) çalıştırılır. Bölüm 10.3 şemasını ve
# asterisk_cdr tablosunu rnvcs veritabanına migration olarak yükler.
#
# Kullanım (core_migration_001_init.sql aynı klasörde iken):
#   sudo bash core_migrate.sh
# =========================================================

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Bu script root ile çalıştırılmalı. Örnek: sudo bash core_migrate.sh"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATION_FILE="$SCRIPT_DIR/core_migration_001_init.sql"
MIGRATION_FILE_002="$SCRIPT_DIR/core_migration_002_user_sip_identity.sql"
DB_NAME="rnvcs"

if [[ ! -f "$MIGRATION_FILE" ]]; then
  echo "HATA: $MIGRATION_FILE bulunamadı. Bu script'i core_migration_001_init.sql ile aynı klasörde çalıştır."
  exit 1
fi

# NOT: migration dosyası genelde bir kullanıcının ev dizininde (örn.
# /home/onur/) duruyor. Ev dizinlerinin izinleri (genelde 750) "postgres"
# sistem kullanıcısının o dizine ERİŞMESİNİ engeller — dosyanın kendisi
# okunabilir olsa bile "postgres" oraya giremediği için "Permission
# denied" alınır. Çözüm: dosyayı herkesin erişebildiği /tmp altına
# kopyalayıp psql -f'i oradan çalıştırmak.
TMP_MIGRATION="/tmp/rnvcs_core_migration_001_init.sql"
cp "$MIGRATION_FILE" "$TMP_MIGRATION"
chmod 644 "$TMP_MIGRATION"

echo "==> [1/3] Migration 001 uygulanıyor ($DB_NAME veritabanına)..."
sudo -u postgres psql -d "$DB_NAME" -v ON_ERROR_STOP=1 -f "$TMP_MIGRATION"
rm -f "$TMP_MIGRATION"

# Bölüm 10.19: SIP kimliği panelden kullanıcıya taşındı (komutan/astsubay
# senaryosu — birden fazla operatör aynı panelde farklı SIP kimlikleriyle
# login olabiliyor). Bu migration speed_dials/ring_group_members'ı YENİDEN
# OLUŞTURUR (BREAKING CHANGE) — sadece pilot/test verisi varsa güvenlidir.
if [[ -f "$MIGRATION_FILE_002" ]]; then
  TMP_MIGRATION_002="/tmp/rnvcs_core_migration_002_user_sip_identity.sql"
  cp "$MIGRATION_FILE_002" "$TMP_MIGRATION_002"
  chmod 644 "$TMP_MIGRATION_002"
  echo "==> [2/3] Migration 002 uygulanıyor (Bölüm 10.19 — kullanıcı bazlı SIP kimliği, DİKKAT: speed_dials/ring_group_members yeniden oluşturulacak)..."
  sudo -u postgres psql -d "$DB_NAME" -v ON_ERROR_STOP=1 -f "$TMP_MIGRATION_002"
  rm -f "$TMP_MIGRATION_002"
else
  echo "==> [2/3] Migration 002 dosyası bulunamadı, atlanıyor ($MIGRATION_FILE_002 aynı klasörde değil)."
fi

echo "==> [3/3] Asterisk yeniden başlatılıyor (yeni asterisk_cdr tablosunu görmesi için)..."
systemctl restart asterisk
sleep 2

echo ""
echo "========================================================="
echo " MIGRATION ÖZETİ — tablo bazında doğrulama"
echo "========================================================="

check_table() {
  # $1 = tablo adı, $2 = ekranda gösterilecek etiket
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
  printf "  [HATA] Asterisk servisi restart sonrası aktif değil (systemctl status asterisk)\n"
fi

echo " -- ODBC Bağlantı Testi --"
if asterisk -rx "odbc show all" 2>/dev/null | grep -qi "Connected: Yes"; then
  printf "  [OK]   Asterisk res_odbc 'rnvcs' bağlantısı canlı\n"
else
  printf "  [HATA] Asterisk res_odbc bağlantısı doğrulanamadı (asterisk -rx \"odbc show all\")\n"
fi

echo "========================================================="
echo " Migration tamamlandı."
echo ""
echo " Sırada: /etc/asterisk/pjsip.conf ve /etc/asterisk/extensions.conf"
echo " içine ilk panel register + basit dialplan tanımını ekleyip"
echo " gerçek bir SIP register/çağrı testi yapmak (Bölüm 10.4)."
echo "========================================================="
