#!/usr/bin/env bash
# =========================================================
# RNVCS PANEL BACKEND — systemd (kullanıcı) servisi kurulumu
#
# Neden "kullanıcı" servisi (sistem servisi değil): PipeWire/WirePlumber
# de kullanıcı oturumu servisleridir (bkz. Bölüm 9.3 notu). Backend'in
# ses aygıtlarını görebilmesi için aynı kullanıcı oturumunun PipeWire
# soketine erişmesi gerekir — bu yüzden root/sistem servisi DEĞİL,
# panel kiosk kullanıcısının (örn. onur) kendi systemd --user servisi.
#
# Kullanım (bu script'in bulunduğu klasörde, go.mod/main.go/audiowatch
# aynı klasörde iken):
#   bash install_servis.sh
# (sudo GEREKMEZ — kasıtlı olarak normal kullanıcı olarak çalıştırılır)
# =========================================================

set -euo pipefail

if [[ $EUID -eq 0 ]]; then
  echo "Bu script'i root/sudo İLE çalıştırma — panel kiosk kullanıcısı olarak"
  echo "(örn. 'onur') normal kullanıcı oturumunda çalıştır: bash install_servis.sh"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="/opt/rnvcs"
BIN_PATH="$INSTALL_DIR/rnvcs-panel-backend"

echo "==> [1/5] Go binary derleniyor (go build)..."
cd "$SCRIPT_DIR"
go build -o /tmp/rnvcs-panel-backend .

echo "==> [2/5] /opt/rnvcs/ altına kuruluyor (sudo gerekli)..."
sudo mkdir -p "$INSTALL_DIR"
sudo install -m 755 /tmp/rnvcs-panel-backend "$BIN_PATH"

echo "==> [3/5] Kullanıcı için 'lingering' etkinleştiriliyor..."
echo "    (böylece servis, kullanıcı fiziksel olarak login olmasa bile"
echo "     sistem açılışında otomatik ayağa kalkar)"
sudo loginctl enable-linger "$(whoami)"

echo "==> [4/5] systemd --user unit dosyası yazılıyor..."
mkdir -p "$HOME/.config/systemd/user"
cat > "$HOME/.config/systemd/user/rnvcs-panel-backend.service" <<EOF
[Unit]
Description=RNVCS Panel Backend (audiowatch hot-plug + yerel API)
After=pipewire.service pipewire-pulse.service wireplumber.service
Wants=pipewire.service pipewire-pulse.service wireplumber.service

[Service]
ExecStart=$BIN_PATH
Restart=on-failure
RestartSec=2
Environment=XDG_RUNTIME_DIR=%t

[Install]
WantedBy=default.target
EOF

echo "==> [5/5] Servis etkinleştiriliyor ve başlatılıyor..."
systemctl --user daemon-reload
systemctl --user enable rnvcs-panel-backend.service
# NOT: "enable --now" servis zaten aktifse binary'yi YENİDEN BAŞLATMAZ (aynı
# hatayı Yönetim Servisi kurulumunda da yapmıştık — bkz. core kurulum notları).
# Bu yüzden burada AYRICA açıkça restart ediyoruz: script tekrar tekrar
# çalıştırılıp binary güncellendiğinde eski sürümün arkada takılı kalmaması için.
systemctl --user restart rnvcs-panel-backend.service

echo ""
echo "========================================================="
echo " Servis kuruldu ve çalışıyor."
echo ""
echo " Durum kontrolü : systemctl --user status rnvcs-panel-backend"
echo " Canlı loglar   : journalctl --user -u rnvcs-panel-backend -f"
echo " Yeniden başlat : systemctl --user restart rnvcs-panel-backend"
echo " Durdur         : systemctl --user stop rnvcs-panel-backend"
echo ""
echo " Reboot sonrası da (kiosk kullanıcı otomatik login olduğu için)"
echo " kendiliğinden ayağa kalkacak."
echo "========================================================="
