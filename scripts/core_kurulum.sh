#!/usr/bin/env bash
# =========================================================
# RNVCS CORE (Merkezi PBX Sunucusu) — Kurulum Script'i
# Hedef: Ubuntu 26.04 LTS Server (minimized install)
# Kapsam (Bölüm 10 — Faz 1, TEK SUNUCU):
#   - Asterisk (PJSIP register, chan_iax2 trunk, ConfBridge/Page)
#   - PostgreSQL (users/panels/permissions/ring_groups/speed_dials/
#     iax_trunks/event_log + Asterisk CDR)
#   - Asterisk <-> PostgreSQL bağlantısı: unixODBC + res_odbc +
#     cdr_adaptive_odbc (native cdr_pgsql/res_config_pgsql modüllerinin
#     Ubuntu'nun binary asterisk paketinde her zaman derlenmiş
#     gelmemesi nedeniyle, ODBC yolu HER Asterisk paketinde bulunan
#     ve en taşınabilir/güvenilir yöntem olduğu için tercih edildi)
#   - Redis (online/presence/aktif-çağrı önbelleği)
#   - ufw (Bölüm 10.7 port seti)
#
# NOT: keepalived / Sanal IP (VRRP) BİLEREK DAHİL EDİLMEDİ.
#      Kullanıcı talebiyle yedeklilik şimdilik ertelendi, tek sunucu
#      kuruluyor. Redundansa geçilince ayrı bir script/adım olarak
#      eklenecek (RNVCS-STANDBY + keepalived).
#
# Kullanım: sudo bash core_kurulum.sh
# =========================================================

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Bu script root ile çalıştırılmalı. Örnek: sudo bash core_kurulum.sh"
  exit 1
fi

# ---------------------------------------------------------
# ETKİLEŞİMSİZ KURULUM (debconf tam ekran pencereleri "takılmasın")
# apt full-upgrade / paket kurulumları bazen mavi/mor tam ekran bir
# yapılandırma ekranı (initial blacklist/whitelist, hangi servisler
# yeniden başlatılsın vb.) açar; -y bunu geçemez ve kurulum bekliyormuş
# gibi durur. Aşağıdaki iki değişken debconf'u tamamen sessiz yapar ve
# tüm soruları paket varsayılanlarıyla otomatik yanıtlar.
#   NEEDRESTART_MODE=a : "hangi servisler restart edilsin" ekranını atlar
# ---------------------------------------------------------
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# ---------------------------------------------------------
# CD/USB KURULUM MEDYASI DEPOSUNU KAPAT
# ISO'dan kurulumdan sonra 'file:/cdrom' deposu apt kaynaklarında kalır;
# medya artık takılı olmadığından "no longer has a Release file" hatası
# verir ve 'set -e' altında script'i durdurur. İnternet depoları zaten
# çalıştığından bu kaydı güvenle kaldırıyoruz.
# ---------------------------------------------------------
if grep -qs 'cdrom' /etc/apt/sources.list 2>/dev/null; then
  sed -i '/cdrom/d' /etc/apt/sources.list
  echo "==> [0/10] cdrom deposu /etc/apt/sources.list'ten kaldırıldı."
fi
for _f in /etc/apt/sources.list.d/*.list; do
  [ -f "$_f" ] && grep -qs 'cdrom' "$_f" && sed -i '/cdrom/d' "$_f"
done
true

echo "==> [1/10] Universe deposu açılıyor..."
add-apt-repository -y universe

echo "==> [2/10] Sistem güncelleniyor..."
apt update
apt full-upgrade -y

echo "==> [3/10] Genel kullanım araçları (unzip, nano, htop, git, curl, golang)..."
# golang-go: Yönetim Servisi (Go) SUNUCUDA derlendiği için Go derleyici
# gerekir. Proje sıfır harici bağımlılıkla (stdlib) yazıldığından derleme
# için internet gerekmez; yalnızca derleyicinin kurulu olması yeterlidir.
apt install -y unzip nano htop git curl golang-go

echo "==> [4/10] PostgreSQL kuruluyor..."
apt install -y postgresql postgresql-contrib
# Bazı Ubuntu imajlarında /usr/sbin/policy-rc.d, apt kurulumu sırasında
# servislerin OTOMATİK başlatılmasını engeller (invoke-rc.d ... denied,
# 'returned 101'). Bu yüzden PostgreSQL kurulduğu halde çalışmıyor olabilir
# ve ilerideki psql adımları "socket ... No such file" ile patlar. Servisi
# elle etkinleştirip başlatıyoruz (manuel systemctl policy-rc.d'den etkilenmez).
systemctl enable postgresql 2>/dev/null || true
systemctl start postgresql 2>/dev/null || true
# Soket hazır olana kadar bekle (en fazla ~30 sn)
echo "    PostgreSQL soketi bekleniyor..."
for _i in $(seq 1 30); do
  if sudo -u postgres psql -tc "SELECT 1" >/dev/null 2>&1; then break; fi
  sleep 1
done
if ! sudo -u postgres psql -tc "SELECT 1" >/dev/null 2>&1; then
  echo "    [HATA] PostgreSQL başlatılamadı. 'systemctl status postgresql' ile kontrol et."
  exit 1
fi

echo "==> [5/10] Asterisk (PJSIP + IAX2 + ConfBridge) kuruluyor..."
apt install -y asterisk asterisk-config

echo "==> [6/10] Asterisk <-> PostgreSQL köprüsü (unixODBC yolu) kuruluyor..."
# Native res_config_pgsql/cdr_pgsql modülleri Ubuntu paketinde garanti
# derlenmiş gelmiyor; unixODBC + odbc-postgresql + Asterisk'in kendi
# res_odbc.so / cdr_adaptive_odbc.so modülleri HER kurulumda var ve
# bu yüzden daha güvenilir. (Bkz. script başındaki not.)
apt install -y unixodbc unixodbc-dev odbc-postgresql

echo "==> [7/10] Redis kuruluyor (presence / aktif-çağrı önbelleği)..."
apt install -y redis-server
systemctl enable redis-server 2>/dev/null || true
systemctl start redis-server 2>/dev/null || true

echo "==> [8/10] PostgreSQL: rnvcs veritabanı ve kullanıcısı oluşturuluyor..."
DB_NAME="rnvcs"
DB_USER="rnvcs"
DB_PASS_FILE="/root/.rnvcs_db_password"

if [[ ! -f "$DB_PASS_FILE" ]]; then
  # NOT: eskiden burada "tr -dc ... </dev/urandom | head -c 24" kullanılıyordu.
  # head, 24 byte alınca pipe'ı erken kapatıyor ve tr SIGPIPE ile ölüyor;
  # set -o pipefail altında bu, script'i BURADA sessizce (hata mesajı
  # görünmeden) sonlandırıyordu. openssl rand ile bu risk yok.
  DB_PASS="$(openssl rand -hex 16)"
  echo "$DB_PASS" > "$DB_PASS_FILE"
  chmod 600 "$DB_PASS_FILE"
else
  DB_PASS="$(cat "$DB_PASS_FILE")"
fi

sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'" | grep -q 1 \
  || sudo -u postgres psql -c "CREATE ROLE $DB_USER WITH LOGIN PASSWORD '$DB_PASS';"

sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" | grep -q 1 \
  || sudo -u postgres psql -c "CREATE DATABASE $DB_NAME OWNER $DB_USER;"

echo "    DB şifresi $DB_PASS_FILE dosyasına yazıldı (root-only, 600)."
echo "    NOT: Bölüm 10'daki tam şemayı (users/panels/ring_groups/...) bu"
echo "    veritabanına yüklemek için ayrıca migration dosyaları hazırlanacak."

echo "==> [9/10] ODBC + Asterisk yapılandırma dosyaları yazılıyor..."

cat > /etc/odbc.ini <<EOF
[rnvcs-pgsql]
Description = RNVCS PostgreSQL (CDR + realtime)
Driver      = PostgreSQL Unicode
Servername  = localhost
Port        = 5432
Database    = $DB_NAME
Username    = $DB_USER
Password    = $DB_PASS
EOF
# NOT: /etc/odbc.ini SİSTEM GENELİ bir ODBC DSN dosyasıdır — chmod 600
# (root-only) yapılırsa "asterisk" sistem kullanıcısı (Asterisk daemon
# bu kullanıcı olarak çalışır) bu dosyayı okuyamaz ve DSN'i hiç
# bulamaz ("Data source name not found" / "odbc show all" içinde
# "Last fail connection attempt" görülür). unixODBC dosyaları genelde
# world-readable tutulur (şifre içermesi kabul edilen bir risktir,
# asıl güvenlik sınırı host'un kendi erişim kontrolüdür) — bu yüzden
# 644 (herkes okuyabilir, sadece root yazabilir) kullanıyoruz.
chmod 644 /etc/odbc.ini

mkdir -p /etc/asterisk

# PJSIP transport (UDP/TCP 5060) — Ubuntu'nun asterisk-config paketindeki
# pjsip.conf TAMAMEN yorum satırı (örnek/şablon) geliyor, aktif hiçbir
# transport tanımlamıyor. Bu olmadan Asterisk 5060'ta hiçbir şey dinlemez,
# panel/SIP cihazları "yanit zaman asimi" hatasıyla register OLAMAZ
# (2026-09-02'de bulundu — 10.1.82.66 kurulumunda panel register olamadı).
if [[ ! -f /etc/asterisk/pjsip_transport.conf ]]; then
  cat > /etc/asterisk/pjsip_transport.conf <<EOF
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0

[transport-tcp]
type=transport
protocol=tcp
bind=0.0.0.0
EOF
  grep -qxF '#include pjsip_transport.conf' /etc/asterisk/pjsip.conf 2>/dev/null \
    || sed -i '1i #include pjsip_transport.conf' /etc/asterisk/pjsip.conf
  echo "    pjsip_transport.conf oluşturuldu ([transport-udp]/[transport-tcp], 0.0.0.0:5060)."
else
  echo "    pjsip_transport.conf zaten var, dokunulmadı."
fi

# PJSIP transport (UDP/TCP 5060) — Ubuntu'nun asterisk-config paketindeki
# pjsip.conf TAMAMEN yorum satırı (örnek/şablon) geliyor, aktif hiçbir
# transport tanımlamıyor. Bu olmadan Asterisk 5060'ta hiçbir şey dinlemez,
# panel/SIP cihazları "yanit zaman asimi" hatasıyla register OLAMAZ
# (2026-09-02'de bulundu — 10.1.82.66 kurulumunda panel register olamadı).
if [[ ! -f /etc/asterisk/pjsip_transport.conf ]]; then
  cat > /etc/asterisk/pjsip_transport.conf <<EOF
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0

[transport-tcp]
type=transport
protocol=tcp
bind=0.0.0.0
EOF
  grep -qxF '#include pjsip_transport.conf' /etc/asterisk/pjsip.conf 2>/dev/null \
    || sed -i '1i #include pjsip_transport.conf' /etc/asterisk/pjsip.conf
  echo "    pjsip_transport.conf oluşturuldu ([transport-udp]/[transport-tcp], 0.0.0.0:5060)."
else
  echo "    pjsip_transport.conf zaten var, dokunulmadı."
fi
if [[ ! -f /etc/asterisk/res_odbc.conf.rnvcs-orig ]] && [[ -f /etc/asterisk/res_odbc.conf ]]; then
  cp /etc/asterisk/res_odbc.conf /etc/asterisk/res_odbc.conf.rnvcs-orig
fi
cat > /etc/asterisk/res_odbc.conf <<EOF
[rnvcs]
enabled => yes
dsn => rnvcs-pgsql
username => $DB_USER
password => $DB_PASS
pre-connect => yes
EOF

if [[ ! -f /etc/asterisk/cdr_adaptive_odbc.conf.rnvcs-orig ]] && [[ -f /etc/asterisk/cdr_adaptive_odbc.conf ]]; then
  cp /etc/asterisk/cdr_adaptive_odbc.conf /etc/asterisk/cdr_adaptive_odbc.conf.rnvcs-orig
fi
cat > /etc/asterisk/cdr_adaptive_odbc.conf <<EOF
[rnvcs_cdr]
connection=rnvcs
table=asterisk_cdr
; 'end' PostgreSQL'de rezerve kelime — CDR bitiş zamanını 'enddate' kolonuna
; yaz (Bölüm 10.22/v1.4.1). Tabloda 'end' kolonu OLMAMALI, yoksa çift yazıp patlar.
alias end => enddate
EOF

echo "    NOT: 'asterisk_cdr' tablosu ve Bölüm 10 şeması henüz DB'ye"
echo "    yüklenmedi — bu bir sonraki adımda (migration script'i) yapılacak."
echo "    Asterisk servisi restart edilmeden önce bu tablo oluşturulmalı,"
echo "    aksi halde CDR yazma denemeleri hata verir (Asterisk çalışmaya"
echo "    devam eder, sadece CDR insert başarısız olur)."

echo "==> [10/10] Güvenlik duvarı (Bölüm 10.7 port seti — TEK SUNUCU, redundans yok)..."
apt install -y ufw
ufw default deny incoming
ufw default allow outgoing
ufw allow OpenSSH
ufw allow 8091/tcp        # RNVCS Yönetim Servisi API + Bakım Terminali (BKT)
                          # panel_app ve uzaktan BKT bu porta bağlanır; açılmazsa
                          # panelde "failed to fetch" / BKT'de bağlantı hatası olur.
ufw allow 5060/tcp        # SIP (panel register)
ufw allow 5060/udp        # SIP
ufw allow 5061/tcp        # SIP TLS
ufw allow 4569/udp        # IAX2 (inter-server trunk)
ufw allow 10000:20000/udp # RTP medya
# 5432 (PostgreSQL) BİLEREK dışarı açılmadı — tek sunucu olduğu için
# şimdilik yalnızca localhost'tan erişim yeterli. STANDBY eklenince
# yalnızca STANDBY'ın IP'sine özel bir kural eklenecek (ufw allow
# from <STANDBY_IP> to any port 5432).
ufw --force enable

echo ""
echo "========================================================="
echo " KURULUM ÖZETİ — bileşen bazında doğrulama"
echo "========================================================="

check_pkg() {
  if dpkg -s "$1" &>/dev/null; then
    printf "  [OK]   %s\n" "$2"
  else
    printf "  [HATA] %s  (paket bulunamadı: %s)\n" "$2" "$1"
  fi
}
check_cmd() {
  if command -v "$1" &>/dev/null; then
    printf "  [OK]   %s\n" "$2"
  else
    printf "  [HATA] %s  (komut bulunamadı: %s)\n" "$2" "$1"
  fi
}
check_svc() {
  if systemctl is-active --quiet "$1"; then
    printf "  [OK]   %s (servis aktif)\n" "$2"
  else
    printf "  [HATA] %s  (servis aktif değil: systemctl status %s)\n" "$2" "$1"
  fi
}

echo " -- Genel Kullanım Araçları --"
check_cmd unzip "unzip"
check_cmd nano  "nano"
check_cmd htop  "htop"
check_cmd git   "git"
check_cmd curl  "curl"

echo " -- PostgreSQL --"
check_pkg postgresql          "PostgreSQL sunucu"
check_svc postgresql          "PostgreSQL servisi"
sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" 2>/dev/null | grep -q 1 \
  && printf "  [OK]   rnvcs veritabanı mevcut\n" \
  || printf "  [HATA] rnvcs veritabanı bulunamadı\n"

echo " -- Asterisk --"
check_pkg asterisk "Asterisk PBX"
check_svc asterisk "Asterisk servisi"
if command -v asterisk &>/dev/null; then
  if asterisk -rx "module show like res_odbc.so" 2>/dev/null | grep -q res_odbc.so; then
    printf "  [OK]   res_odbc.so modülü yüklü\n"
  else
    printf "  [HATA] res_odbc.so modülü yüklenemedi (asterisk -rx \"module show like odbc\")\n"
  fi
  if asterisk -rx "module show like chan_pjsip.so" 2>/dev/null | grep -q chan_pjsip.so; then
    printf "  [OK]   chan_pjsip.so modülü yüklü\n"
  else
    printf "  [HATA] chan_pjsip.so modülü yüklenemedi\n"
  fi
  if asterisk -rx "module show like chan_iax2.so" 2>/dev/null | grep -q chan_iax2.so; then
    printf "  [OK]   chan_iax2.so modülü yüklü (trunk için gerekli)\n"
  else
    printf "  [HATA] chan_iax2.so modülü yüklenemedi\n"
  fi
fi

echo " -- ODBC Köprüsü --"
check_pkg unixodbc          "unixODBC"
check_pkg odbc-postgresql   "odbc-postgresql sürücüsü"
if command -v isql &>/dev/null; then
  printf "  [OK]   isql (odbc test aracı)\n"
else
  printf "  [HATA] isql bulunamadı (unixodbc paketiyle gelmeli)\n"
fi

echo " -- Redis --"
check_pkg redis-server "Redis"
check_svc redis-server "Redis servisi"

echo " -- Güvenlik Duvarı --"
if ufw status | grep -q "Status: active"; then
  printf "  [OK]   ufw aktif ve Bölüm 10.7 portları açık\n"
else
  printf "  [HATA] ufw aktif değil, kontrol et: sudo ufw status\n"
fi

echo "========================================================="
echo " Kurulum tamamlandı."
echo ""
echo " DB şifresi : $DB_PASS_FILE (root-only)"
echo ""
echo " Sırada yapılması gerekenler:"
echo "   1) Bölüm 10 PostgreSQL şemasını (users/panels/permissions/"
echo "      ring_groups/ring_group_members/speed_dials/iax_trunks/"
echo "      event_log + asterisk_cdr tablosu) bu veritabanına migration"
echo "      olarak yükle."
echo "   2) /etc/asterisk/pjsip.conf içinde panel register tanımlarını"
echo "      (dinamik AOR / auth) hazırla."
echo "   3) Çatal arama (ring group) dialplan mantığını /etc/asterisk/"
echo "      extensions.conf içine Bölüm 10.2 akışına göre yaz."
echo "   4) Asterisk'i restart et: sudo systemctl restart asterisk"
echo "      (asterisk_cdr tablosu oluşmadan restart edilirse CDR insert"
echo "      hataları loglanır, servis yine de ayakta kalır)."
echo "   5) RNVCS Yönetim Servisi (Go, ARI/AMI) ve Bakım Terminali"
echo "      geliştirmesine başla (Bölüm 10.1 / 10.3 / 10.5)."
echo "========================================================="
