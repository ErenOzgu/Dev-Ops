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
  AMI_SECRET="$(openssl rand -hex 16)"
  # NOT: DSN, psql'in de kabul ettiği libpq URI formatında (postgresql://...)
  # yazılıyor — internal/pg paketi sorguları `psql` CLI ile çalıştırıyor.
  cat > "$ENV_FILE" <<EOF
RNVCS_DB_DSN=postgresql://rnvcs:$DB_PASS@localhost:5432/rnvcs?sslmode=disable
RNVCS_PROVISIONING_KEY=$PROV_KEY
RNVCS_AMI_ADDR=127.0.0.1:5038
RNVCS_AMI_USER=rnvcs
RNVCS_AMI_SECRET=$AMI_SECRET
EOF
  chmod 600 "$ENV_FILE"
  echo "    Provisioning anahtarı üretildi (panellere de verilecek): $ENV_FILE"
else
  echo "    $ENV_FILE zaten var, dokunulmadı."
  # Eski kurulumlarda AMI_* satırları hiç yoktu (Anons Sistemi FKT madde 4 —
  # konferans — ile eklendi). Env dosyası varsa ama bu satırlar eksikse
  # idempotent olarak tamamla (mevcut DB_DSN/PROVISIONING_KEY'e dokunmadan).
  if ! grep -q '^RNVCS_AMI_USER=' "$ENV_FILE"; then
    AMI_SECRET="$(openssl rand -hex 16)"
    {
      echo "RNVCS_AMI_ADDR=127.0.0.1:5038"
      echo "RNVCS_AMI_USER=rnvcs"
      echo "RNVCS_AMI_SECRET=$AMI_SECRET"
    } >> "$ENV_FILE"
    echo "    RNVCS_AMI_* satırları eksikti, eklendi (konferans özelliği için)."
  fi
fi

echo "==> [4b/7] Asterisk AMI (manager.conf) yapılandırılıyor (sadece localhost, konferans için — Anons Sistemi FKT madde 4)..."
AMI_USER_LINE="$(grep '^RNVCS_AMI_USER=' "$ENV_FILE" | cut -d= -f2-)"
AMI_SECRET_LINE="$(grep '^RNVCS_AMI_SECRET=' "$ENV_FILE" | cut -d= -f2-)"
if [[ -n "$AMI_USER_LINE" && -n "$AMI_SECRET_LINE" ]]; then
  if ! grep -q "^\[$AMI_USER_LINE\]" /etc/asterisk/manager.conf 2>/dev/null; then
    cat >> /etc/asterisk/manager.conf <<EOF

[$AMI_USER_LINE]
secret = $AMI_SECRET_LINE
deny = 0.0.0.0/0.0.0.0
permit = 127.0.0.1/255.255.255.255
read = system,call,originate
write = system,call,originate
EOF
    # [general] bölümünde enabled/bindaddr yoksa ekle (idempotent).
    grep -q '^enabled *= *yes' /etc/asterisk/manager.conf || sed -i '0,/\[general\]/s//[general]\nenabled = yes/' /etc/asterisk/manager.conf
    grep -q '^bindaddr' /etc/asterisk/manager.conf || sed -i '0,/\[general\]/s//[general]\nbindaddr = 127.0.0.1/' /etc/asterisk/manager.conf
    asterisk -rx "manager reload" || true
    echo "    manager.conf'a [$AMI_USER_LINE] eklendi (127.0.0.1'den erişim, dışarı KAPALI)."
  else
    echo "    manager.conf'ta [$AMI_USER_LINE] zaten var, dokunulmadı."
  fi
fi

echo "==> [4c/7] voip_goster script'i kuruluyor (SSH ile CORE'a bağlanıp 'voip_goster' yazınca register olmuş cihazları listeler)..."
cat > /usr/local/bin/voip_goster <<'VOIPEOF'
#!/usr/bin/env bash
asterisk -rx "pjsip show contacts"
echo "---"
asterisk -rx "pjsip show endpoints"
VOIPEOF
chmod +x /usr/local/bin/voip_goster

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

echo "==> [4d/7] Asterisk confbridge.conf hazırlanıyor (konferans için varsayılan profil)..."
if [[ ! -f /etc/asterisk/confbridge.conf ]] || ! grep -q '^\[rnvcs_bridge\]' /etc/asterisk/confbridge.conf 2>/dev/null; then
  cat >> /etc/asterisk/confbridge.conf <<EOF

[rnvcs_bridge]
type=bridge

[rnvcs_user]
type=user
admin=no
marked=no
EOF
  asterisk -rx "module reload app_confbridge.so" || true
  echo "    [rnvcs_bridge]/[rnvcs_user] profilleri eklendi."
else
  echo "    confbridge.conf'ta [rnvcs_bridge] zaten var, dokunulmadı."
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
