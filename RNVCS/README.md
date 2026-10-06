# RNVCS

Asterisk tabanlı **sesli haberleşme sistemi** (VCS). Sahadaki dokunmatik muhabere panelleri merkezi bir
sunucu üzerinden birbirini arar; çatal arama, konferans, sesli mesaj, INTERKOM/IP Horn ve (planlanan)
IAX2 federasyon desteklenir. Monorepo: CORE + panel-backend + panel_app + migrations + infra.

## Bileşenler

| Dizin | Nedir | Dil / Port |
|---|---|---|
| `core/` | Yönetim Servisi — REST API + Bakım/Kontrol Terminali (web UI). PostgreSQL tanımlarını Asterisk config'e yansıtır, login/oturum yönetir. | Go, `:8091` |
| `panel-backend/` | Panelin yerel telefon motoru — gerçek SIP user agent (REGISTER/INVITE), RTP/G.711 ses köprüsü, ses seviyesi, cihaz keşfi. | Go, `:8090` |
| `panel-app/` | Operatör arayüzü (`panel_app.html`) — tek dosya HTML/JS, kiosk tarayıcıda. TR/AZ/EN. | HTML/JS |
| `migrations/` | PostgreSQL şeması (idempotent, numaralı). | SQL |
| `scripts/` | Kurulum, migration, deploy, rollback, yedek, smoke test. | bash |
| `infra/systemd/` | systemd unit dosyaları (referans). | — |
| `.gitea/workflows/` | CI (lint+test+migration) ve release/deploy pipeline'ları. | Gitea Actions |
| `docs/` | Dokümantasyon (aşağıya bakın). | md |

## Dokümantasyon

| Belge | İçerik |
|---|---|
| [`docs/MIMARI.md`](docs/MIMARI.md) | Sistem nasıl çalışır: topoloji, paket haritası, kritik akışlar, veri modeli, kurulum/dağıtım. **Buradan başlayın.** |
| [`docs/API_REFERANSI.md`](docs/API_REFERANSI.md) | core ve panel-backend'in tüm uç noktaları, roller, gövdeler. |
| [`docs/GUVENLIK_RAPORU.md`](docs/GUVENLIK_RAPORU.md) | Güvenlik inceleme bulguları (savunma odaklı) ve giderme önerileri. |
| [`docs/GUNCELLEME_ONERILERI.md`](docs/GUNCELLEME_ONERILERI.md) | Eksikler, tutarsızlıklar, iyileştirme listesi (önceliklendirilmiş). |
| [`docs/rnvcs_yazilim_yol_haritasi.md`](docs/rnvcs_yazilim_yol_haritasi.md) | Faz faz yol haritası ve proje yapısı. |
| [`docs/vcs_test_plan.md`](docs/vcs_test_plan.md) | Katmanlı test planı. |
| [`docs/CICD_TASARIMI.md`](docs/CICD_TASARIMI.md) | CI/CD tasarımı ve kurulum adımları. |
| [`CLAUDE.md`](CLAUDE.md) | Geliştirici/ajan için repo kuralları ve değişmez ilkeler. |

## Hızlı başlangıç (geliştirici)

```bash
# Derleme + test (internet / harici Go bağımlılığı gerekmez)
cd core          && go build ./... && go vet ./... && go test ./...
cd ../panel-backend && go build ./... && go vet ./... && go test ./...

# core'u yerelde çalıştır (PostgreSQL + psql kurulu olmalı)
export RNVCS_DB_DSN='postgresql://rnvcs:sifre@localhost:5432/rnvcs?sslmode=disable'
export RNVCS_PROVISIONING_KEY=test
./rnvcs-yonetim-servisi create-admin admin admin123   # ilk ADMIN
./rnvcs-yonetim-servisi                                # http://localhost:8091/

# panel arayüzünü CORE olmadan görmek için
#   panel_app.html?demo
```

## Kurulum (sunucuda, özet)

CORE sunucusunda sırayla (ayrıntı: `docs/MIMARI.md §7`):
1. `sudo bash scripts/core_kurulum.sh` — Asterisk, PostgreSQL, ODBC/CDR, Redis, ufw.
2. `sudo bash scripts/core_migrate.sh` — şema.
3. `cd core && sudo bash install_yonetim_servisi.sh` — servisi derle/kur, ilk admin.

Panel cihazında: `cd panel-backend && bash install_servis.sh` (kiosk kullanıcısı, sudo'suz).

## Temel tasarım ilkeleri

- **Sıfır harici Go bağımlılığı** (izole ağ): DB `psql` CLI ile, parola PBKDF2 (stdlib), SIP/AMI/RTP elle yazılmış.
- **Tek gerçek kaynak PostgreSQL**: tanımlar DB'ye yazılır, sonra Asterisk config dosyalarına yansıtılıp `reload` edilir.
- **SIP kimliği kullanıcıya ait**: operatör hangi panelden girerse kendi SIP hesabıyla register olur.

> ⚠️ Üretime almadan önce `docs/GUVENLIK_RAPORU.md`'deki KRİTİK/YÜKSEK bulguları ve
> `docs/GUNCELLEME_ONERILERI.md`'deki P1 maddelerini (eksik migration'lar, rollback, sürüm kaynağı) gözden geçirin.
