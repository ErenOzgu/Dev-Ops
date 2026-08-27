# v1.6.0 Deploy Adımları

## 1) panel-backend (CI: /home/rn/work/rnvcs/panel-backend/main.go) — daha önce verilen sed patch
(zaten uygulanmadıysa önce onu uygula, sonra install_servis.sh ile panele deploy et.)

## 2) CORE (rnvcs-yonetim-servisi) — bu klasördeki TÜM kaynağı CI'daki
yonetim-servisi ağacının üzerine kopyala (üzerine yaz), sonra:

    cd <kaynak-dizini>
    go build ./...            # derleme kontrolü (burada zaten temiz geçti)
    sudo bash install_yonetim_servisi.sh

install script artık otomatik olarak:
  - /usr/local/bin/voip_goster kurar
  - RNVCS_AMI_* env değişkenlerini üretir (yoksa)
  - Asterisk manager.conf'a localhost-only bir AMI kullanıcısı ekler
  - confbridge.conf'a varsayılan [rnvcs_bridge]/[rnvcs_user] profillerini ekler

## 3) Migration 004 — ELLE uygula (core_migrate.sh 003/004'ü otomatik aramıyor):

    cp core_migration_004_annon_devices.sql /tmp/
    chmod 644 /tmp/core_migration_004_annon_devices.sql
    sudo -u postgres psql -d rnvcs -v ON_ERROR_STOP=1 -f /tmp/core_migration_004_annon_devices.sql
    rm -f /tmp/core_migration_004_annon_devices.sql

## 4) Doğrulama
  - curl -s http://localhost:8091/api/version   -> {"version":"1.6.0"}
  - SSH ile CORE'a bağlanıp sadece `voip_goster` yaz -> pjsip contacts/endpoints listelenmeli
  - BKT > Paneller sekmesinde Cihaz Tipi dropdown'u görünmeli
  - BKT > Çatal Arama sekmesinde üye formatı artık "TİP:kod" (örn. USER:komutan,IP_HORN:horn1)
  - Konferans: POST /api/conference (henüz panel_app'te UI yok — sadece API hazır)

## Henüz YAPILMADI (ayrı iş — panel_app dosyasında)
  - Madde 5: IP Horn'a özel MP butonu/ikonu (panel_app_v3.html)
  - Madde 4: panel_app'te "Konferans" ekranı (çoklu seçim + "Konferans Başlat" butonu)
Bunlar panel_app_v3.html üzerinde ayrı bir adım — onay verirsen devam ederim.
