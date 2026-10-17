#!/usr/bin/env bash
# =========================================================
# RNVCS — docker compose duman testi
#
# Depo kökündeki docker-compose.yml ile PostgreSQL + core + panel arayüzünü ayağa kaldırır,
# imajın gerçekten çalıştığını (psql istemcisi, migration, API, kimlik doğrulama) doğrular
# ve işi bitince her şeyi (volume dahil) siler.
#
# Gereksinimler: docker (compose v2), curl, jq.
# Not: 8091 ve 8080 portları (127.0.0.1) boş olmalıdır.
# Çıkış kodu: 0 = hepsi geçti, 1 = en az bir kontrol başarısız.
# =========================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

for tool in docker curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "gerekli araç yok: $tool" >&2; exit 2; }
done

BASE="http://127.0.0.1:8091"
SUFFIX="$RANDOM$RANDOM"
ADMIN_USER="smoke_admin_$SUFFIX"
ADMIN_PASS="Smoke-pass-$SUFFIX"

PASS=0
FAIL=0
check() { # check "açıklama" komut...
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "[PASS] $name"; PASS=$((PASS+1)); else echo "[FAIL] $name"; FAIL=$((FAIL+1)); fi
}
eq() { [ "$1" = "$2" ]; }

cleanup() {
  if [ "$FAIL" -ne 0 ]; then
    echo "--- compose ps ---"; docker compose ps -a || true
    echo "--- compose logs ---"; docker compose logs --no-color --tail=80 || true
  fi
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== 1) Derleme ve açılış =="
docker compose down -v --remove-orphans >/dev/null 2>&1 || true
check "imaj derleniyor ve servisler başlıyor" docker compose up -d --build
up=0
for _ in $(seq 1 60); do
  if curl -sf -m 2 "$BASE/healthz" >/dev/null 2>&1; then up=1; break; fi
  sleep 1
done
check "core ayağa kalktı (/healthz)" test "$up" = 1
[ "$up" = 1 ] || exit 1
check "migration servisi başarıyla bitti" eq "$(docker inspect -f '{{.State.ExitCode}}' "$(docker compose ps -aq migrate)")" 0
core_healthy() {
  local cid; cid="$(docker compose ps -q core)"
  for _ in $(seq 1 30); do
    [ "$(docker inspect -f '{{.State.Health.Status}}' "$cid")" = healthy ] && return 0
    sleep 1
  done
  return 1
}
check "core konteyneri healthy (HEALTHCHECK)" core_healthy

echo "== 2) API =="
check "/api/version VERSION dosyasıyla aynı" eq "$(curl -s -m 5 "$BASE/api/version" | jq -r .version)" "$(tr -d '[:space:]' < RNVCS/core/VERSION)"
check "token'sız /api/users -> 401" eq "$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$BASE/api/users")" 401
check "ilk ADMIN oluşturuluyor" docker compose exec -T core rnvcs-core create-admin "$ADMIN_USER" "$ADMIN_PASS"
TOKEN="$(curl -s -m 5 -X POST "$BASE/api/login" -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" | jq -r '.token // empty')"
check "doğru şifre token döner" test -n "$TOKEN"
check "admin /api/users listesinde" eq "$(curl -s -m 5 -H "Authorization: Bearer $TOKEN" "$BASE/api/users" | jq -r "[.[] | select(.username==\"$ADMIN_USER\")] | length")" 1
check "bakım terminali açılıyor (/)" bash -c "curl -sf -m 5 '$BASE/' | grep -qi '<html'"

echo "== 3) Panel arayüzü =="
check "panel arayüzü sunuluyor (:8080)" bash -c "curl -sf -m 5 http://127.0.0.1:8080/ | grep -q 'VCS Panel'"

echo
echo "Sonuç: $PASS geçti, $FAIL başarısız"
[ "$FAIL" -eq 0 ]
