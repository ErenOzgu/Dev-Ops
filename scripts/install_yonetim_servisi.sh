#!/usr/bin/env bash
# =========================================================
# RNVCS Yönetim Servisi — Kurulum Script'i (Bölüm 10.1)
#
# CORE sunucusunda (core_kurulum.sh + core_migrate.sh TAMAMLANDIKTAN
# SONRA) çalıştırılır. Go REST API'yi derler, /opt/rnvcs altına kurar,
# DB şifresini (core_kurulum.sh'nin ürettiği /root/.rnvcs_db_password)
# okuyup bir provisioning anahtarı üretir, systemd SİSTEM servisi
# (kullanıcı servisi DEĞİL — bu servis CORE'da bir masaüstü oturumuna
# bağlı değil, panelin PipeWire durumu gibi bir kısıt yok) olarak kurar,
# ve eğer hiç kullanıcı yoksa ilk ADMIN kullanıcısını oluşturur.
#
# Kullanım: sudo bash install_yonetim_servisi.sh
# =========================================================

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Bu script root ile çalıştırılmalı. Örnek: sudo bash install_yonetim_servisi.sh"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="/opt/rnvcs"
BIN_PATH="$INSTALL_DIR/rnvcs-yonetim-servisi"
ENV_FILE="/etc/rnvcs/yonetim-servisi.env"

echo "==> [1/7] Go modülleri kontrol ediliyor (bu proje sıfır harici bağımlılıkla yazıldı, internet gerekmez)..."
cd "$SCRIPT_DIR"
go mod tidy

echo "==> [2/7] Derleniyor..."
go build -o /tmp/rnvcs-yonetim-servisi .

echo "==> [3/7] /opt/rnvcs/ altına kuruluyor..."
mkdir -p "$INSTALL_DIR"
install -m 755 /tmp/rnvcs-yonetim-servisi "$BIN_PATH"

echo "==> [4/7] Ortam değişkenleri dosyası hazırlanıyor ($ENV_FILE)..."
mkdir -p /etc/rnvcs
if [[ ! -f "$ENV_FILE" ]]; then
  DB_PASS="$(cat /root/.rnvcs_db_password 2>/dev/null || true)"
  if [[ -z "$DB_PASS" ]]; then
    echo "    UYARI: /root/.rnvcs_db_password bulunamadı — DB_DSN içine elle şifre gireceksin."
    DB_PASS="CHANGE_ME"
  fi
  PROV_KEY="$(openssl rand -hex 24)"
  # NOT: DSN, psql'in de kabul ettiği libpq URI formatında (postgresql://...)
  # yazılıyor — internal/pg paketi sorguları `psql` CLI ile çalıştırıyor.
  cat > "$ENV_FILE" <<EOF
RNVCS_DB_DSN=postgresql://rnvcs:$DB_PASS@localhost:5432/rnvcs?sslmode=disable
RNVCS_PROVISIONING_KEY=$PROV_KEY
EOF
  chmod 600 "$ENV_FILE"
  echo "    Provisioning anahtarı üretildi (panellere de verilecek): $ENV_FILE"
else
  echo "    $ENV_FILE zaten var, dokunulmadı."
fi

echo "==> [5/7] pjsip.conf / extensions.conf / iax.conf içine dinamik config include'ları ekleniyor (idempotent)..."
touch /etc/asterisk/pjsip_rnvcs_dynamic.conf
chmod 640 /etc/asterisk/pjsip_rnvcs_dynamic.conf
chown root:asterisk /etc/asterisk/pjsip_rnvcs_dynamic.conf 2>/dev/null || true
grep -qxF '#include pjsip_rnvcs_dynamic.conf' /etc/asterisk/pjsip.conf 2>/dev/null \
  || echo '#include pjsip_rnvcs_dynamic.conf' >> /etc/asterisk/pjsip.conf
asterisk -rx "pjsip reload" || true

touch /etc/asterisk/extensions_rnvcs_dynamic.conf
chmod 640 /etc/asterisk/extensions_rnvcs_dynamic.conf
chown root:asterisk /etc/asterisk/extensions_rnvcs_dynamic.conf 2>/dev/null || true
grep -qxF '#include extensions_rnvcs_dynamic.conf' /etc/asterisk/extensions.conf 2>/dev/null \
  || echo '#include extensions_rnvcs_dynamic.conf' >> /etc/asterisk/extensions.conf
asterisk -rx "dialplan reload" || true

touch /etc/asterisk/iax_rnvcs_dynamic.conf
chmod 640 /etc/asterisk/iax_rnvcs_dynamic.conf
chown root:asterisk /etc/asterisk/iax_rnvcs_dynamic.conf 2>/dev/null || true
grep -qxF '#include iax_rnvcs_dynamic.conf' /etc/asterisk/iax.conf 2>/dev/null \
  || echo '#include iax_rnvcs_dynamic.conf' >> /etc/asterisk/iax.conf
asterisk -rx "iax2 reload" || true

# Bölüm 10.22: panel voicemail (boş panele arama -> mesaj bırak) için
# voicemail_rnvcs_dynamic.conf include'u. Dosya boşsa bile rnvcs-vm context
# başlığını Yönetim Servisi yazacak (voicemail.RewriteAll / AppendMailbox).
touch /etc/asterisk/voicemail_rnvcs_dynamic.conf
chmod 640 /etc/asterisk/voicemail_rnvcs_dynamic.conf
chown root:asterisk /etc/asterisk/voicemail_rnvcs_dynamic.conf 2>/dev/null || true
grep -qxF '#include voicemail_rnvcs_dynamic.conf' /etc/asterisk/voicemail.conf 2>/dev/null \
  || echo '#include voicemail_rnvcs_dynamic.conf' >> /etc/asterisk/voicemail.conf
asterisk -rx "voicemail reload" || true

# Yönetim Servisi'nin sesli mesaj dosyalarını (WAV + metadata) okuyabilmesi
# için asterisk grubuna dahil edilmesi gerekir (Bölüm 10.22). Mesajlar
# /var/spool/asterisk/voicemail/... altında asterisk:asterisk sahipliğiyle
# oluşur; servis root çalışıyorsa zaten erişir, root DEĞİLSE bu grup şart.
RNVCS_SVC_USER="${RNVCS_SVC_USER:-root}"
if [ "$RNVCS_SVC_USER" != "root" ]; then
  usermod -aG asterisk "$RNVCS_SVC_USER" 2>/dev/null || true
fi

echo "==> [6/7] systemd servisi yazılıyor..."
cat > /etc/systemd/system/rnvcs-yonetim-servisi.service <<EOF
[Unit]
Description=RNVCS Yonetim Servisi (Go REST API)
After=network.target postgresql.service asterisk.service
Wants=postgresql.service asterisk.service

[Service]
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_PATH
Restart=on-failure
RestartSec=2
User=root

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable rnvcs-yonetim-servisi.service
# NOT: "enable --now", servis zaten aktifse YENİDEN BAŞLATMAZ (sadece
# etkin değilse başlatır) — bu yüzden script her çalıştığında yeni derlenen
# binary'nin devreye girmesini garantilemek için açıkça restart ediyoruz.
systemctl restart rnvcs-yonetim-servisi.service
sleep 1

echo "==> [6b/7] ufw: Bakım/Kontrol Terminali ve API portu (8091) açılıyor..."
if command -v ufw >/dev/null 2>&1; then
  ufw allow 8091/tcp comment 'RNVCS Yonetim Servisi (Bakim Terminali + API)' || true
else
  echo "    UYARI: ufw bulunamadı, port 8091'i manuel açman gerekebilir."
fi

echo "==> [7/7] İlk ADMIN kullanıcısı kontrol ediliyor..."
set -a
source "$ENV_FILE"
set +a

USER_COUNT="$(sudo -u postgres psql -d rnvcs -tc "SELECT count(*) FROM users" | tr -d '[:space:]')"
if [[ "$USER_COUNT" == "0" ]]; then
  ADMIN_PASS="$(openssl rand -hex 8)"
  "$BIN_PATH" create-admin admin "$ADMIN_PASS"
  echo ""
  echo "    ==============================================="
  echo "     İLK ADMIN KULLANICISI OLUŞTURULDU"
  echo "       Kullanıcı adı : admin"
  echo "       Şifre         : $ADMIN_PASS"
  echo "     Bu şifreyi güvenli bir yere kaydet, ilk login"
  echo "     sonrası değiştirmen önerilir."
  echo "    ==============================================="
else
  echo "    Zaten $USER_COUNT kullanıcı var, admin oluşturma atlandı."
fi

echo ""
echo "========================================================="
echo " KURULUM ÖZETİ"
echo "========================================================="

check_svc() {
  if systemctl is-active --quiet "$1"; then
    printf "  [OK]   %s (servis aktif)\n" "$2"
  else
    printf "  [HATA] %s  (servis aktif değil: systemctl status %s)\n" "$2" "$1"
  fi
}
check_svc rnvcs-yonetim-servisi "RNVCS Yönetim Servisi"

if curl -s -o /dev/null -w "%{http_code}" http://localhost:8091/healthz | grep -q 200; then
  printf "  [OK]   /healthz 200 dönüyor\n"
else
  printf "  [HATA] /healthz yanıt vermiyor (journalctl -u rnvcs-yonetim-servisi -e ile kontrol et)\n"
fi

CORE_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo "========================================================="
echo " Bakım/Kontrol Terminali (Web UI):"
echo "   http://${CORE_IP:-<CORE_IP>}:8091/"
echo "   (aynı ağdaki bir bilgisayardan tarayıcıyla açılabilir)"
echo "========================================================="
echo " Test için (ADMIN ile login + panel/kullanıcı listeleme):"
echo "   curl -s -X POST http://localhost:8091/api/login \\"
echo "     -d '{\"username\":\"admin\",\"password\":\"<yukaridaki_sifre>\"}' | jq"
echo "========================================================="
