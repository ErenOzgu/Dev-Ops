# RNVCS / VCS Panel — Yazılım Projesi Yol Haritası

**Proje:** P2015 — Yedek VCS ve Panel Kurulumu (AZVCS)
**Amaç:** Bugüne kadar "kurulum ve iş yapma" odaklı ilerleyen sistemi, sürümlenen, test edilen, kontrollü şekilde özellik eklenen bir **yazılım projesi**ne dönüştürmek.
**Kapsam:** Envanter (ne yapıldı) → boşluk analizi (ne eksik) → proje yapısı → versiyonlama → özellik akışı → CI/CD tasarımı → ortamlar → faz faz yol haritası → riskler.
**Tarih:** 2026-08-20

---

## 0. Nasıl Okunmalı

Bu belge dört soruya cevap veriyor: **Neredeyiz?** (Bölüm 1), **Ne eksik?** (Bölüm 2), **Bundan sonra nasıl çalışacağız?** (Bölüm 3–7: yapı, versiyon, özellik akışı, CI/CD, ortamlar), **Hangi sırayla?** (Bölüm 8–10). Önce 1 ve 2'yi okuyup mevcut durumu doğrulayın; 3'ten sonrası tasarım/karar gerektirir, ekip içinde gözden geçirilmesi gerekir.

---

## 1. Envanter — Bugüne Kadar Ne Yapıldı

### 1.1 Altyapı
- Proxmox host (sida04-1, 15.2.4.1) üzerinde üretim VCS'in (vcs04-1.az, VM 100, 15.2.4.10) yanına **yedek CORE** (VM 101, "ovcs041", 15.2.4.11) kuruldu — aynı donanım profili aynalandı (4 GiB RAM, 2 vCPU host-type, 60 GB disk, VirtIO, `bridge=LAN`).
- Ağ köprüsü sorunu (`vmbr0` hayalet, gerçek köprü `LAN`) çözüldü ve script'e gömülmedi ama el kitabına kalıcı not olarak düşüldü.
- İzole ağda internet erişimi USB ethernet passthrough + route/DNS düzeltmeleriyle sağlandı (geçici çözüm; kalıcı değil — bkz. §2.4).
- Panel cihazı (15.2.4.201, kod: sida04) statik IP'ye taşındı, netplan çakışması giderildi.

### 1.2 CORE (rnvcs-yonetim-servisi, Go, :8091)
- Asterisk 22.x (PJSIP) + PostgreSQL + Redis + unixODBC + Go servisi kuruldu, v1.5.0 sürümü çalışıyor.
- Uç noktalar: login/logout, panel bazlı yetki kontrolü (varsayılan kapalı, `RNVCS_ENFORCE_PANEL_LOGIN=1` ile açılabilir), kullanıcı-bazlı `speed_dials` CRUD (GET/POST/PUT/DELETE), `voicemail`, `event_log`, `/api/version`, `/api/my-panel-sip-credentials`.
- PJSIP transport eksikliği (kimse register olamıyordu) kalıcı olarak `pjsip.conf`'a `[transport-udp]` eklenerek çözüldü.

### 1.3 panel-backend (Go, :8090)
- Ses aygıtları (PipeWire/WirePlumber üzerinden), ses seviyesi (`/api/volume`), çağrı durumu/kontrolü (`/api/call/*`), oturum köprüleme (`/api/session`) uç noktaları mevcut.
- "Ses yok" sorununun asıl sebebinin eski/eksik binary olduğu bulundu, güncel binary deploy edildi.

### 1.4 panel_app (istemci, tek dosya HTML/JS, v2.0 → v2.1)
- Markalı giriş ekranı, canlı "Sistem Testi" paneli (sunucu bağlantısı, gecikme, backend, ses aygıtı).
- Dialpad, gelen/aktif çağrı ekranları, arka plana alma ("pill" bileşeni).
- **Kullanıcı bazlı dinamik hızlı arama** (v2.1'de eklendi): ekle/sil/sırala, CORE'a `speed_dials.user_id` ile bağlı.
- Sanal klavye (dokunmatik ekran için), dikey/yatay duyarlı düzen, **TR/AZ/EN çok dillilik**.

### 1.5 Veritabanı (PostgreSQL, migration 001)
- İdempotent tek migration dosyası: `users`, `panels`, `user_panel_permissions`, `active_panel_sessions`, `ring_groups`/`ring_group_members`, `speed_dials`, `iax_trunks`, `event_log`, `asterisk_cdr`.
- Kod-şema uyuşmazlıkları (eski kullanıcı-merkezli tasarım kalıntıları) idempotent `ALTER`'larla giderildi.

### 1.6 Test ve dokümantasyon (bu konuşmada eklendi)
- `vcs_test_plan.md`: katmanlı test planı (birim/servis/fonksiyonel/UAT/regresyon/güvenlik/performans).
- `rnvcs_smoke_test.sh`: deploy sonrası/cron ile koşturulabilir sağlık kontrolü betiği.
- `proxmox_kurulum_handbook.md`: kronolojik kurulum + sorun/çözüm kayıtları (bu projenin en değerli dokümanı — aşağıdaki boşluk analizinin çoğu buradan çıktı).

**Özet değerlendirme:** Sistem *çalışır durumda* ve kurulum bilgisi iyi belgelenmiş, ama bir **yazılım projesi** olarak henüz kaynak kontrolü, versiyonlama, otomatik derleme/dağıtım ve tekrarlanabilir bir özellik-geliştirme süreci yok. Aşağıdaki bölümler bunu kurmayı hedefliyor.

---

## 2. Boşluk Analizi — Ne Eksik

### 2.1 Kaynak kontrolü ve doküman dağınıklığı
Proje dosyaları arasında aynı dosyanın birden fazla, numaralandırılmamış kopyası var (`proxmox_kurulum_handbook.md` iki kez, `core_migration_001_init.sql` iki kez, `panel_app_v2.1.html` / `panel_app_v2.1_1.html`, `speed_dials.go` / `speed_dials_1.go`). Hangisinin güncel olduğu dosya adından tahmin ediliyor (`_1`, `_2` = daha yeni varsayımı) — bu, **git olmadan çalışmanın doğal sonucu**. İlk ve en kritik eksik bu: kod ve dokümanlar bir Git deposunda, tek doğruluk kaynağı olarak tutulmuyor.

### 2.2 Versiyonlama yok
`panel_app v2.1` gibi bir sürüm adı var ama bu sadece dosya adında/yorum satırında; koddan okunamıyor, CORE'un `/api/version`'ı ile ilişkilendirilmemiş, DB şema versiyonu (migration numarası) ile de bağlantılı değil. "Şu an üretimde tam olarak hangi sürüm çalışıyor" sorusuna bugün kesin cevap vermek zor.

### 2.3 CI/CD ve otomatik test yok
Release'ler elle hazırlanan bir tar dosyasıyla dağıtılıyor (kullanıcının kendi ifadesiyle). Derleme, test, paketleme, dağıtım — hepsi elle. `rnvcs_smoke_test.sh` dışında hiçbir otomatik test koşmuyor.

### 2.4 Ağ/altyapı kırılganlığı
İzole ağda internet erişimi geçici bir USB-ethernet + route hack'i ile sağlanıyor (§3.8, el kitabı). Bu, CI/CD makinesinin güvenilir internet erişimi ihtiyacıyla doğrudan çakışıyor — kalıcı bir çözüme (network şeması bunu bekleyen ayrı bir uplink, ya da tamamen offline/vendored derleme) ihtiyaç var.

### 2.5 Güvenlik
- `speed_dials.go`'da SQL sorguları parametreli sorgu yerine string birleştirme + `EscapeLiteral` ile kuruluyor (test planında §6.2'de işlendi) — kalıcı çözüm değil, kırılgan bir desen.
- `sip_password` ve kullanıcı şifreleri (`password_hash` dışında `sip_password` düz metin görünüyor migration şemasında) veritabanında düz metin/yarı düz metin tutuluyor olabilir — doğrulanmalı, gerekiyorsa şifrelenmeli.
- Rol bazlı yetkilendirme tutarsız: `GET /api/speed-dials?username=` için herhangi bir yetki kontrolü yok (herkes herkesin kısayollarını görebiliyor) — kasıtlı mı, atlanmış mı netleştirilmeli.
- Panel-CORE arası, panel-backend arası trafik şifreleme (TLS) durumu belirtilmemiş — muhtemelen düz HTTP; izole ağda risk düşük ama federasyon (IAX2, çoklu site) geldiğinde önem kazanır.

### 2.6 Operasyonel olgunluk
- **Yedekleme/felaket kurtarma:** "Yedek VCS" kavramı var ama üretim DB'sinin düzenli yedeklenip yedek sisteme aktarıldığına dair bir süreç belgelenmemiş; bir "gerçek arıza tatbikatı" (test planı §5.6) henüz yapılmadı.
- **İzleme/uyarı:** Sistem sağlığını izleyen bir mekanizma (Prometheus/Grafana, basit uptime kontrolü, push/e-posta uyarısı) yok; sorunlar (§3.11, §3.13'teki gibi) ancak biri fark ettiğinde ortaya çıkıyor.
- **Tek kişilik operasyon riski:** Kurulumun büyük kısmı tek bir operatör (onur) üzerinden yürütülmüş görünüyor — "bus factor" riski; el kitabı bunu kısmen azaltıyor ama runbook/otomasyon olmadan risk sürüyor.

### 2.7 Yarım kalan özellikler
- **IAX2 federasyon:** `iax_trunks` tablosu ve peer config var, giden prefix dialplan'i ve gelen `[rnvcs-trunks]` yönlendirmesi eksik; numara planı (`8+site+dahili` hane sayısı) netleşmemiş.
- **BKT (Bakım/Kontrol Terminali) çok dilliliği:** panel_app'te var, BKT'de henüz yok.
- **Panel kiosk kod parametresi:** kiosk'tan panel koduna parametre geçilemediği için HTML'de sabit `sida04` varsayılanı var — birden fazla panel dağıtıldığında bu elle her panelde değiştirilmesi gereken kırılgan bir nokta.

---

## 3. Yazılım Proje Yapısı

### 3.1 Repo stratejisi: Monorepo

Ekip küçük, bileşenler (CORE, panel-backend, panel_app, migration'lar, kurulum script'leri, dokümantasyon) birbirine sıkı bağlı ve genelde birlikte deploy ediliyor. Bu senaryoda **tek bir Git deposu (monorepo)** çoklu repo'dan daha az operasyonel yük getirir: tek PR ile CORE + migration + panel_app değişikliği atomik olarak yapılabilir, tek CI pipeline'ı tüm bileşenleri kapsar, tek sürüm numarası tüm paketi temsil eder.

Önerilen klasör yapısı:

```
rnvcs/
├── core/                        # rnvcs-yonetim-servisi (Go)
│   ├── cmd/
│   ├── internal/
│   │   ├── api/                 # speed_dials.go vb. handler'lar
│   │   └── pg/
│   └── go.mod
├── panel-backend/                # Go
│   └── go.mod
├── panel-app/
│   └── panel_app.html            # tek dosya frontend
├── migrations/
│   ├── 001_init.sql
│   └── 002_...sql                # her yeni şema değişikliği yeni numaralı dosya
├── infra/
│   ├── proxmox/                  # VM tanımı / cloud-init şablonları
│   ├── systemd/                  # servis unit dosyaları
│   └── ci/                       # pipeline tanım dosyaları
├── scripts/
│   ├── core_kurulum.sh
│   ├── core_migrate.sh
│   ├── install_yonetim_servisi.sh
│   └── rnvcs_smoke_test.sh
├── docs/
│   ├── proxmox_kurulum_handbook.md
│   ├── vcs_test_plan.md
│   ├── CHANGELOG.md
│   └── ROADMAP.md                # bu belge
├── VERSION
└── README.md
```

Bu yapıya geçiş, bugün proje dosyalarında dağınık duran `_1`/`_2` kopyalarının da tek doğruluk kaynağına toplanması anlamına gelir — §2.1'deki dağınıklık sorununu otomatik olarak çözer.

### 3.2 Branch stratejisi: GitHub Flow (basit trunk-based)

Küçük ekip ve sık deploy döngüsü için ağır süreçlere (GitFlow'un develop/release/hotfix karmaşası) gerek yok:

- `main` her zaman **deploy edilebilir** durumda tutulur.
- Her özellik/düzeltme kendi kısa ömürlü dalında yapılır: `feature/iax2-prefix-routing`, `fix/speed-dials-sql-injection`.
- Değişiklik `main`'e **pull request** ile girer; CI (Bölüm 6) yeşil olmadan merge edilmez.
- Sürüm etiketleri (`v1.6.0` gibi) doğrudan `main` üzerinden kesilir.
- Acil üretim düzeltmesi gerekiyorsa `main`'den kısa bir `hotfix/` dalı açılır, aynı PR sürecinden geçer (sadece hızlandırılmış review ile).

### 3.3 Versiyonlama: Semantic Versioning (SemVer)

Tek, sistem genelinde geçerli sürüm numarası: `MAJOR.MINOR.PATCH` (ör. `v1.6.0`).

- **MAJOR:** geriye dönük uyumsuz DB şema değişikliği veya API sözleşmesi kırılması (ör. panel_app'in eski sürümünün artık çalışmayacağı bir CORE değişikliği).
- **MINOR:** yeni özellik, geriye uyumlu (ör. IAX2 federasyon, BKT çok dillilik).
- **PATCH:** hata düzeltmesi, güvenlik yaması (ör. SQL injection deseninin düzeltilmesi).

`VERSION` dosyası repo kökünde tek gerçek kaynak olur; CI, derleme sırasında bunu Go binary'lerine `-ldflags "-X main.Version=$(cat VERSION)-$(git rev-parse --short HEAD)"` ile gömer. CORE'un zaten var olan `/api/version` uç noktası bu değeri döndürecek şekilde güncellenir. panel_app.html'e de bir `APP_VERSION` sabiti eklenip "Sistem Testi" ekranında gösterilmesi, sahada "hangi panelde hangi sürüm çalışıyor" sorusunu tek bakışta cevaplar.

### 3.4 CHANGELOG disiplini

`docs/CHANGELOG.md`, [Keep a Changelog](https://keepachangelog.com) formatında tutulur; her PR, "Unreleased" bölümüne bir satır eklemeden merge edilemez (CI'da basit bir kontrol: PR diff'i CHANGELOG.md'yi değiştirmiyorsa uyarı). Bu, hem release notlarını otomatik üretir hem de "bu sürümde ne değişti" sorusuna anında cevap verir — şu anki durumda (§2.2) bu bilgi hiçbir yerde toplu tutulmuyor.

### 3.5 Migration numaralandırma kuralı

`core_migration_001_init.sql` deseni korunur: her yeni şema değişikliği **yeni, sıralı numaralı** bir dosya olarak eklenir (`002_iax_prefix_routing.sql` gibi), var olan dosyalar bir daha **asla değiştirilmez** (idempotent yazım disiplini zaten var, bunu koruyun). CI, migration testini (§6, adım 3) her PR'da bu sırayla çalıştırır.

---

## 4. Yeni Özellik Nasıl Çıkacak — Uçtan Uca Akış

1. **İhtiyaç/talep:** Bir özellik ihtiyacı (ör. "IAX2 prefix routing") issue tracker'da (Bölüm 6'da önerilen Gitea/GitLab issue sistemi) bir kart olarak açılır. Küçük değişiklikler için bu adım tek satırlık bir issue yeterlidir; büyük değişiklikler (yeni bir alt sistem, şema değişikliği) için `docs/ROADMAP.md`'ye bir madde ve kısa bir tasarım notu eklenir.
2. **Dal açma:** `feature/<kısa-ad>` dalı `main`'den açılır.
3. **Geliştirme:** Kod yazılır; DB değişikliği gerekiyorsa yeni numaralı migration dosyası eklenir (asla eskisi değiştirilmez); yeni endpoint/özellik için ilgili birim/entegrasyon testleri de aynı PR içinde yazılır (test yazılmadan PR açılmaz kuralı önerilir).
4. **Yerel doğrulama:** Geliştirici kendi makinesinde `go test ./...` ve `rnvcs_smoke_test.sh` ile temel sağlık kontrolünü koşturur.
5. **Pull Request:** PR açılır, CHANGELOG.md güncellenir. PR şablonunda (bkz. §9 ilk adımlar) şu kontrol listesi bulunur: migration idempotent mi, yeni SQL parametreli mi (injection deseni tekrarlanmasın), yeni rol/yetki kontrolü eksik mi, i18n string'leri üç dile de eklendi mi (panel_app için).
6. **CI pipeline:** Otomatik lint + unit test + migration testi + entegrasyon testi çalışır (Bölüm 6). Kırmızıysa merge engellenir.
7. **Code review:** En az bir başka kişi (mümkünse; tek kişilik ekipse en azından bir gün bekleyip "soğuk kafayla" ikinci geçiş) PR'ı inceler.
8. **Merge → main:** PR onaylanıp CI yeşilken `main`'e merge edilir.
9. **Sürüm etiketleme:** `VERSION` dosyası güncellenir (MINOR/PATCH kararı §3.3'e göre verilir), `git tag vX.Y.Z` atılır.
10. **Otomatik paketleme:** CI, tag push'unu yakalayıp release tar'ını otomatik üretir (binary'ler + migration'lar + panel_app.html + systemd unit'ler + `VERSION` + `CHANGELOG.md`'nin ilgili bölümü) ve dahili bir artefakt deposuna koyar.
11. **Staging'e dağıtım:** Tar, önce staging ortamına (Bölüm 7) otomatik veya yarı-otomatik indirilir; `rnvcs_smoke_test.sh -e` otomatik koşturulur.
12. **UAT / manuel onay:** Kritik değişikliklerde (özellikle DB migration içeren veya telefoni davranışını etkileyen) bir kişi staging'de kısa bir elle test yapar.
13. **Yedek CORE'a dağıtım (canary):** Tar önce **yedek** CORE'a (15.2.4.11) kurulur — zaten üretim trafiği taşımadığı için düşük riskli bir "canary" ortamıdır. Smoke test tekrar koşturulur.
14. **Üretime dağıtım:** Sorun yoksa aynı tar, planlı bir bakım penceresinde üretim VCS'e (vcs04-1.az) uygulanır.
15. **Doğrulama:** Deploy sonrası smoke test + kısa bir manuel kontrol (bir arama denemesi) yapılır.

### 4.1 Geri alma (rollback) stratejisi

- Her deploy öncesi bir önceki tar ve DB yedeği (`pg_dump`) saklanır (en az son 3 sürüm).
- Migration'lar mümkün olduğunca **geriye uyumlu, ekleme bazlı** yazılır (kolon silme yerine deprecate etme, vb.) — bu, "yeni sürüm sorun çıkardı, eski koda dönelim ama DB zaten yeni şemada" çıkmazını önler.
- Smoke test deploy sonrası başarısız olursa, önceki tar'a otomatik/manuel geri dönüş prosedürü `docs/CHANGELOG.md` yanına kısa bir `docs/ROLLBACK.md` olarak yazılmalı.

---

## 5. Versiyon ve Release Bilgisi Nerede Tutulacak

| Bilgi | Nerede | Nasıl güncellenir |
|---|---|---|
| Sistem sürümü (tek gerçek kaynak) | `VERSION` dosyası + Git tag (`vX.Y.Z`) | Release sürecinde elle/CI ile |
| Çalışan CORE sürümü | `GET /api/version` (derleme zamanı gömülü) | Otomatik, derleme anında |
| Çalışan panel_app sürümü | Kod içi `APP_VERSION` sabiti, Sistem Testi ekranında gösterilir | Otomatik, derleme/paketleme anında |
| DB şema sürümü | En yüksek numaralı `migrations/NNN_*.sql` dosya adı | Otomatik (dosya adı = versiyon) |
| Değişiklik geçmişi | `docs/CHANGELOG.md` | Her PR'da elle, release'de otomatik gruplanır |
| Hangi ortamda hangi sürüm çalışıyor | `docs/DEPLOYMENTS.md` (basit tablo: ortam / sürüm / tarih / kim) veya CI'nin deploy log'u | Deploy sonrası otomatik/elle satır eklenir |
| Yol haritası / planlanan işler | `docs/ROADMAP.md` (bu belge) | Faz tamamlandıkça güncellenir |

---

## 6. CI/CD Pipeline Tasarımı

### 6.1 Araç seçimi

İzole/kısıtlı internetli ağ ve muhtemelen küçük bir ekip düşünülünce, ağır bir platform (tam GitLab CE + ayrı runner filosu) yerine **Gitea + Gitea Actions** önerilir: git barındırma + issue takibi + CI/CD tek, hafif bir binary'de; kendi sunucunuzda (Bölüm "OS" cevabındaki Ubuntu VM'de) kurulumu ve bakımı GitLab CE'ye göre çok daha az kaynak ve operasyon yükü ister. Ekip ilerde büyür ve daha zengin bir platforma ihtiyaç duyarsa GitLab CE'ye geçiş, git geçmişi taşınabilir olduğu için kolaydır.

Alternatif: Jenkins (daha esnek ama kurulum/bakımı daha ağır, plugin yönetimi ek yük) — sadece ekipte Jenkins deneyimi zaten varsa tercih edilmeli.

### 6.2 Pipeline aşamaları (her PR ve her `main` push'unda)

1. **Lint/format:** `gofmt -l .`, `go vet ./...` — biçim/temel hata kontrolü.
2. **Birim testler:** `go test ./... -race` (CORE + panel-backend).
3. **Migration testi:** Geçici bir Postgres konteyneri açılır, tüm `migrations/*.sql` sırayla uygulanır, **ikinci kez** aynı sırayla tekrar uygulanır (idempotency kanıtı — bkz. test planı §2.3).
4. **Entegrasyon testleri:** `httptest` tabanlı CORE/panel-backend API testleri, adım 3'teki geçici DB'ye karşı.
5. **(main'e merge sonrası, tag atıldığında) Derleme:** CORE ve panel-backend binary'leri `VERSION` + git commit hash gömülerek derlenir.
6. **Paketleme:** `rnvcs-vX.Y.Z.tar.gz` — binary'ler, migration'lar, `panel_app.html`, systemd unit dosyaları, `scripts/`, `VERSION`, `CHANGELOG.md` ilgili bölümü.
7. **Artefakt yayını:** Dahili bir depoya (Gitea Package Registry veya basitçe bir dahili dosya sunucusu) yüklenir.
8. **Staging'e otomatik dağıtım + smoke test:** Yeni tar staging VM'e kopyalanır, kurulur, `rnvcs_smoke_test.sh -e` otomatik koşturulur; başarısızsa pipeline kırmızı yanar ve dağıtım burada durur.
9. **Üretime dağıtım:** Manuel onay kapısı (bir kişinin "onaylıyorum" tıklaması) sonrası, önce yedek CORE'a, sonra üretime aynı akış (kopyala → kur → smoke test) uygulanır. Bu adım başlangıçta tamamen elle de kalabilir — otomasyonun en riskli, en son otomatikleştirilecek parçası budur.

### 6.3 Ağ bağımlılığı notu

Adım 2 ve 4, Go modüllerini indirmek için internet ister (`go mod download`). İzole ağda internet erişimi kalıcı değilse (§2.4), iki seçenek:
- CI makinesine kalıcı, ayrı bir internet uplink'i tanımlayın (üretim/panel ağından izole, sadece paket indirme için).
- Go modüllerini `vendor/` klasörüne gömüp (`go mod vendor`) build'i tamamen offline hale getirin — bu, dış bağımlılık riskini de azaltır (bir paket npm/go proxy'sinden kaldırılırsa build kırılmaz).

---

## 7. Ortamlar (Environments)

Bugün fiilen sadece **üretim** ve **yedek/DR** var; **geliştirme** ve **staging** kavramı yok. Önerilen dörtlü yapı:

| Ortam | Bugünkü karşılığı | Amaç | Kim/ne dağıtır |
|---|---|---|---|
| **Geliştirme (dev)** | Yok — önerilir: geliştiricinin kendi makinesi + docker-compose (Postgres, gerekirse Asterisk stub) | Hızlı yerel iterasyon, CI'ya göndermeden önce deneme | Geliştirici, elle |
| **Staging** | Yok — önerilir: **yeni, ayrı, küçük bir VM** (Proxmox'ta, CORE'un küçültülmüş bir kopyası) | CI'nin otomatik dağıtım + smoke test hedefi; üretime çıkmadan son doğrulama | CI, otomatik |
| **Yedek / DR** | ovcs041, 15.2.4.11 | Hem felaket kurtarma hedefi hem de "canary" — tam test edilmiş sürüm önce burada, gerçek panel donanımına yakın koşullarda kısa süre çalışır | CI (onaylı) veya elle |
| **Üretim** | vcs04-1.az, 15.2.4.10 | Gerçek trafik | Elle onaylı, planlı bakım penceresi |

**Önemli tasarım kararı:** Yedek CORE'u hem "DR hedefi" hem "canary/test ortamı" olarak kullanmak cazip görünse de, bunu yaparsanız DR sisteminiz her zaman "en yeni, henüz üretimde kanıtlanmamış" sürümü taşır — tam da bir felaket anında güvenmek isteyeceğiniz şey değil. Kaynak el veriyorsa **ayrı bir staging VM** kurup yedek CORE'u sadece "üretimde kanıtlanmış son sürüm"le güncel tutmak, DR amacına çok daha sadık kalır.

---

## 8. Faz Faz Yol Haritası

Takvim tarihi yerine **bağımlılık sırası** ve gevşek süre tahminleri veriyorum — ekip kapasitesine göre paralelleştirilebilir.

### Faz 0 — Temel (hemen, ~1 hafta)
- Git deposu oluşturma, mevcut tüm kodun/dokümanın (dağınık `_1`/`_2` kopyaları tekilleştirilerek) Bölüm 3.1'deki klasör yapısına taşınması.
- `VERSION` dosyası, boş `CHANGELOG.md`, `README.md` (kurulum/geliştirme kısa özet + el kitabına link).
- `.gitignore`, temel commit disiplini (anlamlı commit mesajları).
- **Çıktı:** Artık "hangi dosya güncel" sorusu bir daha sorulmuyor; tek doğruluk kaynağı var.

### Faz 1 — CI/CD Temeli (~2–4 hafta, kullanıcının kuracağı makineyle paralel)
- CI/CD makinesinin (Ubuntu Server 24.04 LTS, Bölüm başındaki öneri) kurulumu, Gitea + Gitea Actions (veya seçilen alternatif) kurulumu.
- Pipeline'ın ilk üç aşaması (lint, unit test, migration testi) bağlanması — bunlar en ucuz, en hızlı geri bildirim veren adımlar.
- `rnvcs_smoke_test.sh`'in pipeline'a "deploy sonrası" adımı olarak bağlanması.
- **Çıktı:** Her PR otomatik doğrulanıyor; 3.11/3.13 sınıfı hatalar (eski binary, eksik transport) merge öncesi yakalanabilir hale geliyor.

### Faz 2 — Güvenlik ve Tutarlılık (Faz 1 ile paralel yürütülebilir, ~2–3 hafta)
- `speed_dials.go` ve benzeri handler'lardaki string-birleştirme SQL deseninin parametreli sorgulara taşınması.
- Rol bazlı yetkilendirme boşluklarının kapatılması (özellikle `GET /api/speed-dials?username=`).
- Şifre/kimlik bilgisi saklama pratiğinin gözden geçirilmesi (`sip_password` vb.).
- Panel kiosk kod parametresi sorununun çözülmesi (kiosk'tan panel koduna parametre geçme yolu).
- **Çıktı:** Test planındaki §6.2 güvenlik bulguları kapatılmış oluyor.

### Faz 3 — Test Kapsamının Genişletilmesi (Faz 1 tamamlandıktan sonra başlar, ~3–4 hafta)
- Go birim + `httptest` entegrasyon testlerinin CORE/panel-backend'in geri kalanına yaygınlaştırılması.
- Playwright ile panel_app kritik akış testleri.
- Injection regresyon seti ve temel yük testinin pipeline'a eklenmesi.
- **Çıktı:** Test planındaki (`vcs_test_plan.md`) tüm katmanlar otomasyona bağlanmış oluyor.

### Faz 4 — Staging Ortamı ve Tam CI/CD (Faz 1–3'ün ardından, ~2–3 hafta)
- Ayrı staging VM'in kurulumu (Bölüm 7).
- Pipeline'ın 5–8. adımlarının (derleme, paketleme, staging'e otomatik dağıtım) bağlanması.
- Üretime dağıtım adımının en azından "tek tıkla, onaylı" hale getirilmesi (tam otomatik olması şart değil, insan onayı kalabilir).
- **Çıktı:** Release süreci elle tar hazırlamaktan, CI'nin ürettiği, staging'de doğrulanmış bir artefaktı onaylayıp dağıtmaya dönüşüyor.

### Faz 5 — Özellik Genişletme (Faz 0 tamamlanır tamamlanmaz feature branch'lerde paralel başlayabilir, süre özelliğe bağlı)
- **IAX2 federasyon:** numara planı netleştirme (`8+site+dahili` hane sayısı), giden prefix dialplan'i, gelen `[rnvcs-trunks]` yönlendirmesi, test planı §3.3'teki federasyon senaryolarının yazılması ve koşturulması.
- **BKT çok dillilik:** panel_app'teki i18n deseninin (`T` sözlüğü, `data-i18n` attribute'ları) BKT'ye taşınması.
- Çoklu panel dağıtımı planı (panel kodu parametrelemesi Faz 2'de çözüldüyse burası daha kolay).

### Faz 6 — Operasyonel Olgunluk (Faz 4'ün ardından, sürekli/tekrarlanan)
- Otomatik DB yedekleme (ör. günlük `pg_dump`, ayrı bir depolamaya) ve **gerçek bir felaket tatbikatı** (üretim kapalıyken yedek CORE'un devreye alınması, test planı §5.6).
- Temel izleme/uyarı: `rnvcs_smoke_test.sh`'in periyodik çalıştırılıp sonucun bir kanala (e-posta/mesajlaşma) düşmesi; ileride Prometheus+Grafana'ya büyütülebilir.
- `docs/DEPLOYMENTS.md` ve runbook'ların olgunlaştırılması — tek kişilik operasyon riskinin azaltılması.

---

## 9. Hemen Atılabilecek İlk Somut Adımlar

1. Bu dört bileşeni (CORE, panel-backend, panel_app, migration'lar) tek bir Git deposunda birleştirin; proje dosyalarındaki `_1`/`_2` çiftlerini karşılaştırıp hangisinin güncel olduğuna karar verip diğerini arşivleyin.
2. `VERSION` dosyasını `1.5.0` (bugünkü CORE sürümüyle uyumlu bir başlangıç noktası) olarak oluşturun, ilk `git tag v1.5.0`'ı atın — buradan sonrası SemVer'e göre ilerler.
3. Kullanıcının kuracağı CI/CD makinesi hazır olunca, önce sadece Faz 1'in ilk üç pipeline adımını (lint + unit test + migration testi) bağlayın — en düşük efor, en yüksek getiri.
4. `docs/CHANGELOG.md`'yi boş bir "Unreleased" bölümüyle başlatın, bundan sonraki her değişikliği buraya yazma alışkanlığını hemen şimdi başlatın (repo kurulmadan önce bile elle bir dosyada tutmaya başlayabilirsiniz).
5. Bu belgeyi (`docs/ROADMAP.md`) ekip içinde bir kez gözden geçirip faz sıralamasını/sürelerini kendi kapasitenize göre ayarlayın — burada verilen sıralama bağımlılıklara göre mantıklı ama takvim tamamen sizin elinizde.

---

## 10. Riskler ve Azaltımlar

| Risk | Etki | Azaltım |
|---|---|---|
| İzole ağda CI'nin internet erişimi kararsız | Pipeline'lar rastgele başarısız olur, güven kaybolur | Kalıcı uplink veya `go mod vendor` ile tam offline build |
| Tek kişilik operasyon (bus factor) | Bilgi kaybı, yavaş müdahale | El kitabı + bu yol haritası + runbook disiplini; CI otomasyonu bilgiyi kod/pipeline'a taşır |
| Yedek CORE hem DR hem test ortamı olarak kullanılırsa | Felaket anında güvenilmez bir sürüm devrede olabilir | Ayrı staging VM (Faz 4); yedek CORE sadece kanıtlanmış sürüm taşır |
| SQL injection deseni yeni endpoint'lerde tekrarlanır | Güvenlik açığı | Faz 2'de parametreli sorguya geçiş + PR checklist'inde zorunlu kontrol |
| Migration disiplini bozulursa (var olan dosya değiştirilirse) | Ortamlar arası şema tutarsızlığı | CI'daki migration idempotency testi + "var olan migration dosyasına dokunma" kuralının PR review'da kontrol edilmesi |
| DB yedeği/felaket tatbikatı hiç yapılmazsa | "Yedek" sistemin gerçekten işe yarayıp yaramadığı bilinmez | Faz 6'da takvime bağlanmış, en az yılda bir tekrarlanan bir tatbikat |

---

*Bu belge proje dokümanlarına `docs/ROADMAP.md` / `claude/rnvcs_yazilim_yol_haritasi.md` olarak kaydedilmiştir. Faz tamamlandıkça bu dosya güncellenmeli, `docs/CHANGELOG.md` ile birlikte projenin "durum panosu" olarak kullanılmalıdır.*
