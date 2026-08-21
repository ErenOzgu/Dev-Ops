#!/usr/bin/env bash
# =========================================================
# RNVCS / VCS Panel — Smoke Test Betiği
# Amaç: Her deploy (tar paketi kurulumu) sonrası veya cron ile
#       periyodik olarak sistemin temel katmanlarının ayakta
#       ve doğru cevap verdiğini hızlıca doğrulamak.
#
# Kapsar: CORE API sağlığı, panel-backend ses uç noktaları,
#         PJSIP transport/register durumu, veritabanı bağlantısı,
#         600 echo dialplan sağlık kontrolü (opsiyonel, -e ile).
#
# Kullanım:
#   ./rnvcs_smoke_test.sh                 # varsayılan hedeflerle çalıştır
#   ./rnvcs_smoke_test.sh -e              # + 600 echo/dialplan testini de çalıştır
#   CORE_HOST=15.2.4.11 PANEL_HOST=15.2.4.201 ./rnvcs_smoke_test.sh
#
# Çıkış kodu: 0 = tüm testler geçti, 1 = en az bir test başarısız.
# Cron örneği (her 15 dakikada bir, log dosyasına yazarak):
#   */15 * * * * /opt/rnvcs/rnvcs_smoke_test.sh >> /var/log/rnvcs-smoketest.log 2>&1
# =========================================================

set -uo pipefail

# ---------- Ayarlar (env değişkeniyle override edilebilir) ----------
CORE_HOST="${CORE_HOST:-localhost}"
CORE_PORT="${CORE_PORT:-8091}"
PANEL_HOST="${PANEL_HOST:-localhost}"
PANEL_PORT="${PANEL_PORT:-8090}"
DB_NAME="${DB_NAME:-rnvcs}"
DB_USER="${DB_USER:-rnvcs}"
RUN_ECHO_TEST=0
TIMEOUT="${TIMEOUT:-5}"

while getopts "e" opt; do
  case "$opt" in
    e) RUN_ECHO_TEST=1 ;;
    *) ;;
  esac
done

TS() { date '+%Y-%m-%d %H:%M:%S'; }

PASS_COUNT=0
FAIL_COUNT=0
declare -a RESULTS=()

ok()   { RESULTS+=("[PASS] $1"); PASS_COUNT=$((PASS_COUNT+1)); echo "$(TS) [PASS] $1"; }
fail() { RESULTS+=("[FAIL] $1"); FAIL_COUNT=$((FAIL_COUNT+1)); echo "$(TS) [FAIL] $1"; }
info() { echo "$(TS) [INFO] $1"; }

echo "=================================================="
echo " RNVCS Smoke Test — $(TS)"
echo " CORE: ${CORE_HOST}:${CORE_PORT}   Panel: ${PANEL_HOST}:${PANEL_PORT}"
echo "=================================================="

# ---------- 1) CORE API sağlığı ----------
info "1) CORE API (/healthz) kontrol ediliyor..."
if resp=$(curl -sf --max-time "$TIMEOUT" "http://${CORE_HOST}:${CORE_PORT}/healthz" 2>&1); then
  ok "CORE /healthz cevap verdi (${resp:-boş gövde})"
else
  fail "CORE /healthz'e ulaşılamadı (${CORE_HOST}:${CORE_PORT})"
fi

# panel-backend, sadece fiziksel panel cihazında çalışan bir servistir (RNVCS mimarisi
# gereği CORE/yedek CORE üzerinde bulunmaz). Bu makinede rnvcs-panel-backend.service
# tanımlı değilse, panel-backend kontrollerini FAIL değil, bilinçli SKIP say.
PANEL_BACKEND_EXPECTED=1
if command -v systemctl >/dev/null 2>&1; then
  if ! systemctl list-unit-files 2>/dev/null | grep -q '^rnvcs-panel-backend\.service'; then
    PANEL_BACKEND_EXPECTED=0
  fi
fi

# ---------- 2) Panel-backend: ses seviyesi uç noktası ----------
if [ "$PANEL_BACKEND_EXPECTED" -eq 0 ]; then
  info "2) panel-backend /api/volume — bu makinede panel-backend servisi tanımlı değil, atlandı (beklenen: sadece panel cihazında çalışır)"
else
  info "2) panel-backend /api/volume kontrol ediliyor..."
  if resp=$(curl -sf --max-time "$TIMEOUT" "http://${PANEL_HOST}:${PANEL_PORT}/api/volume" 2>&1); then
    if echo "$resp" | grep -qi "volume"; then
      ok "panel-backend /api/volume beklenen alanı içeriyor"
    else
      fail "panel-backend /api/volume cevap verdi ama beklenen 'volume' alanı yok — eski/yanlış binary olabilir (bkz. handbook §3.11)"
    fi
  else
    fail "panel-backend /api/volume'e ulaşılamadı (${PANEL_HOST}:${PANEL_PORT}) — 404 dönüyorsa eski binary deploy edilmiş olabilir"
  fi
fi

# ---------- 3) Panel-backend: ses aygıtları uç noktası ----------
if [ "$PANEL_BACKEND_EXPECTED" -eq 0 ]; then
  info "3) panel-backend /api/audio-devices — bu makinede panel-backend servisi tanımlı değil, atlandı"
else
  info "3) panel-backend /api/audio-devices kontrol ediliyor..."
  if resp=$(curl -sf --max-time "$TIMEOUT" "http://${PANEL_HOST}:${PANEL_PORT}/api/audio-devices" 2>&1); then
    dev_count=$(echo "$resp" | grep -o '"kind"' | wc -l)
    if [ "$dev_count" -gt 0 ]; then
      ok "panel-backend /api/audio-devices ${dev_count} cihaz döndürdü"
    else
      fail "panel-backend /api/audio-devices boş liste döndürdü — PipeWire/WirePlumber çalışmıyor olabilir"
    fi
  else
    fail "panel-backend /api/audio-devices'e ulaşılamadı"
  fi
fi

# ---------- 4) PJSIP transport: 5060 dinleniyor mu ----------
info "4) PJSIP UDP 5060 transport kontrol ediliyor (bu betik CORE sunucusunda çalıştırılmalı)..."
if command -v ss >/dev/null 2>&1; then
  if ss -lunp 2>/dev/null | grep -q ":5060"; then
    ok "5060/udp dinleniyor (PJSIP transport aktif)"
  else
    fail "5060/udp DİNLENMİYOR — pjsip.conf'ta transport tanımı eksik/yorumlu olabilir (bkz. handbook §3.13)"
  fi
else
  info "ss komutu bulunamadı, 5060 kontrolü atlandı"
fi

# ---------- 5) Asterisk PJSIP endpoint durumu ----------
info "5) PJSIP endpoint register durumları kontrol ediliyor..."
if command -v asterisk >/dev/null 2>&1; then
  ep_out=$(sudo asterisk -rx "pjsip show endpoints" 2>&1)
  if echo "$ep_out" | grep -qi "Unavailable" && ! echo "$ep_out" | grep -qi "Avail "; then
    fail "Hiçbir PJSIP endpoint 'Available'/register durumunda görünmüyor"
  else
    avail_count=$(echo "$ep_out" | grep -ci "Avail")
    ok "PJSIP endpoint listesi alındı (yaklaşık ${avail_count} satırda 'Avail' geçiyor)"
  fi
else
  info "asterisk komutu bu makinede yok, endpoint kontrolü atlandı"
fi

# ---------- 6) Veritabanı bağlantısı ----------
info "6) PostgreSQL (${DB_NAME}) bağlantısı kontrol ediliyor..."
if command -v psql >/dev/null 2>&1; then
  if PGCONNECT_TIMEOUT="$TIMEOUT" psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1;" >/dev/null 2>&1; then
    ok "PostgreSQL bağlantısı ve basit sorgu başarılı"
  else
    fail "PostgreSQL'e bağlanılamadı (kullanıcı=${DB_USER}, db=${DB_NAME})"
  fi
else
  info "psql bulunamadı, DB kontrolü atlandı"
fi

# ---------- 7) (Opsiyonel) 600 echo / dialplan sağlık testi ----------
if [ "$RUN_ECHO_TEST" -eq 1 ]; then
  info "7) 600 echo dialplan testi çalıştırılıyor (-e ile istendi)..."
  if command -v asterisk >/dev/null 2>&1; then
    # Local kanaldan 600'e originate ederek dialplan'in cevap verip vermediğini kontrol eder.
    orig_out=$(sudo asterisk -rx "channel originate Local/600@rnvcs-panels application Echo" 2>&1)
    sleep 2
    if echo "$orig_out" | grep -qi "error\|no such"; then
      fail "600 echo testi başarısız: dialplan'e ulaşılamadı (${orig_out})"
    else
      # CDR'da son 30 saniyede 600'e giden ANSWERED bir kayıt var mı bak.
      if command -v psql >/dev/null 2>&1; then
        cdr_check=$(psql -U "$DB_USER" -d "$DB_NAME" -tAc \
          "SELECT count(*) FROM asterisk_cdr WHERE dst='600' AND disposition='ANSWERED' AND start > now() - interval '30 seconds';" 2>/dev/null)
        if [ "${cdr_check:-0}" -gt 0 ]; then
          ok "600 echo testi: CDR'da ANSWERED kayıt bulundu"
        else
          fail "600 echo testi: originate denendi ama CDR'da ANSWERED kayıt görülmedi (CDR gecikmesi olabilir, tekrar deneyin)"
        fi
      else
        info "psql yok, CDR doğrulaması atlandı; originate hatasız gönderildi"
        ok "600 echo testi: originate komutu hatasız gönderildi (CDR doğrulanamadı)"
      fi
    fi
  else
    info "asterisk komutu yok, echo testi atlandı"
  fi
else
  info "7) 600 echo testi atlandı (çalıştırmak için: ./rnvcs_smoke_test.sh -e)"
fi

# ---------- Özet ----------
echo "=================================================="
echo " ÖZET — $(TS)"
echo " Başarılı: ${PASS_COUNT}   Başarısız: ${FAIL_COUNT}"
echo "=================================================="
for r in "${RESULTS[@]}"; do echo " $r"; done

if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0
