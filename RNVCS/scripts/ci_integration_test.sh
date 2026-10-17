#!/usr/bin/env bash
# =========================================================
# RNVCS — Entegrasyon testi (gerçek PostgreSQL'e karşı; Asterisk GEREKMEZ)
#
# Neyi doğrular:
#   1. Migration'lar iki kez uygulanabiliyor (idempotency)
#   2. core (Yönetim Servisi) derleniyor, ilk ADMIN oluşturulabiliyor, servis ayağa kalkıyor
#   3. Kimlik doğrulama: yanlış şifre / token'sız istek reddediliyor, doğru login token veriyor
#   4. Yönetim akışı: kullanıcı, panel ve yetki oluşturma; yetkili panelin login cevabında dönmesi
#   5. Denetim kaydı (event_log) ve logout sonrası token'ın geçersizleşmesi
#
# Gereksinimler: bash, go, psql, curl, jq ve erişilebilir bir PostgreSQL.
#
# Kullanım:
#   RNVCS_DB_DSN='postgresql://rnvcs:sifre@localhost:5432/rnvcs_test?sslmode=disable' \
#     ./scripts/ci_integration_test.sh
#
# Not: core 8091 portunu sabit dinler; test sırasında o port boş olmalıdır.
# Çıkış kodu: 0 = hepsi geçti, 1 = en az bir kontrol başarısız.
# =========================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${RNVCS_DB_DSN:?RNVCS_DB_DSN tanımlı olmalı (örn: postgresql://rnvcs:sifre@localhost:5432/rnvcs_test?sslmode=disable)}"
export RNVCS_DB_DSN
export RNVCS_PROVISIONING_KEY="${RNVCS_PROVISIONING_KEY:-ci-test-key}"

for tool in go psql curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "gerekli araç yok: $tool" >&2; exit 2; }
done

BASE="http://127.0.0.1:8091"
WORK="$(mktemp -d)"
CORE_PID=""
SUFFIX="$RANDOM$RANDOM"
ADMIN_USER="ci_admin_$SUFFIX"
ADMIN_PASS="Ci-Admin-pass-$SUFFIX"
OP_USER="ci_op_$SUFFIX"
OP_PASS="Ci-Op-pass-$SUFFIX"
PANEL_CODE="9$SUFFIX"

cleanup() {
  if [ -n "$CORE_PID" ]; then kill "$CORE_PID" 2>/dev/null || true; wait "$CORE_PID" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

PASS=0
FAIL=0
check() { # check "açıklama" komut...
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "[PASS] $name"; PASS=$((PASS+1)); else echo "[FAIL] $name"; FAIL=$((FAIL+1)); fi
}
code() { # code METHOD PATH [TOKEN] [JSON]  -> HTTP durum kodu
  local method="$1" path="$2" token="${3:-}" body="${4:-}"
  local args=(-s -m 10 -o /dev/null -w '%{http_code}' -X "$method" "$BASE$path")
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}
api() { # api METHOD PATH [TOKEN] [JSON]  -> gövde
  local method="$1" path="$2" token="${3:-}" body="${4:-}"
  local args=(-s -m 10 -X "$method" "$BASE$path")
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}
eq() { [ "$1" = "$2" ]; }

echo "== 1) Migration'lar (iki tur) =="
for round in 1 2; do
  for f in "$ROOT"/migrations/*.sql; do
    check "migration tur $round: $(basename "$f")" psql -q -v ON_ERROR_STOP=1 "$RNVCS_DB_DSN" -f "$f"
  done
done

echo "== 2) core derleme ve açılış =="
check "core derleniyor" bash -c "cd '$ROOT/core' && go build -o '$WORK/core' ."
check "ilk ADMIN oluşturuluyor" "$WORK/core" create-admin "$ADMIN_USER" "$ADMIN_PASS"
"$WORK/core" >"$WORK/core.log" 2>&1 &
CORE_PID=$!
up=0
for _ in $(seq 1 30); do
  if curl -sf -m 2 "$BASE/healthz" >/dev/null 2>&1; then up=1; break; fi
  kill -0 "$CORE_PID" 2>/dev/null || break
  sleep 0.5
done
check "servis ayağa kalktı (/healthz)" test "$up" = 1
if [ "$up" != 1 ]; then echo "--- core.log ---"; cat "$WORK/core.log"; exit 1; fi

echo "== 3) Kimlik doğrulama =="
check "/healthz status=ok" eq "$(api GET /healthz | jq -r .status)" ok
check "/api/version VERSION dosyasıyla aynı" eq "$(api GET /api/version | jq -r .version)" "$(tr -d '[:space:]' < "$ROOT/core/VERSION")"
check "token'sız /api/users -> 401" eq "$(code GET /api/users)" 401
check "yanlış şifre -> 401" eq "$(code POST /api/login '' "{\"username\":\"$ADMIN_USER\",\"password\":\"yanlis\"}")" 401
TOKEN="$(api POST /api/login '' "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" | jq -r '.token // empty')"
check "doğru şifre token döner" test -n "$TOKEN"
check "admin /api/users listesinde" eq "$(api GET /api/users "$TOKEN" | jq -r "[.[] | select(.username==\"$ADMIN_USER\")] | length")" 1

echo "== 4) Yönetim akışı =="
check "operatör kullanıcı oluşturuluyor" test -n "$(api POST /api/users "$TOKEN" "{\"username\":\"$OP_USER\",\"password\":\"$OP_PASS\",\"full_name\":\"CI Operator\",\"role\":\"OPERATOR\"}" | jq -r '.id // empty')"
# Asterisk yokken kayıt yine oluşur, cevapta 'warning' döner (/api/panels/sync ile onarılır) — bu beklenen davranıştır.
check "panel oluşturuluyor" test -n "$(api POST /api/panels "$TOKEN" "{\"panel_code\":\"$PANEL_CODE\",\"display_name\":\"CI Panel\",\"location\":\"Test\",\"device_type\":\"PANEL\"}" | jq -r '.id // empty')"
check "panel listede" eq "$(api GET /api/panels "$TOKEN" | jq -r "[.[] | select(.panel_code==\"$PANEL_CODE\")] | length")" 1
check "yetki veriliyor" eq "$(api POST /api/permissions "$TOKEN" "{\"username\":\"$OP_USER\",\"panel_code\":\"$PANEL_CODE\",\"can_login\":true,\"can_call\":true}" | jq -r .status)" ok
OP_LOGIN="$(api POST /api/login '' "{\"username\":\"$OP_USER\",\"password\":\"$OP_PASS\"}")"
check "operatör login oluyor, rolü OPERATOR" eq "$(echo "$OP_LOGIN" | jq -r .role)" OPERATOR
check "login cevabında yetkili panel dönüyor" eq "$(echo "$OP_LOGIN" | jq -r "[.panels[]? | select(.panel_code==\"$PANEL_CODE\")] | length")" 1

echo "== 5) Denetim kaydı ve logout =="
check "event_log'da LOGIN kaydı var" test "$(api GET /api/events "$TOKEN" | jq -r '[.[] | select(.event_type=="LOGIN")] | length')" -ge 1
OP_TOKEN="$(echo "$OP_LOGIN" | jq -r .token)"
check "logout başarılı" test "$(code POST /api/logout "$OP_TOKEN")" -lt 300
check "logout sonrası token geçersiz -> 401" eq "$(code GET /api/users "$OP_TOKEN")" 401

echo
echo "Sonuç: $PASS geçti, $FAIL başarısız"
[ "$FAIL" -eq 0 ]
