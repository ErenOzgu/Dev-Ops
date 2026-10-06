# RNVCS — Eksikler, Tutarsızlıklar ve Güncelleme Önerileri

**Tarih:** 2026-10-06 · Repodaki tüm kaynak kodun incelenmesiyle çıkarıldı. Güvenlik bulguları ayrı
dosyada (`GUVENLIK_RAPORU.md`); burada **doğruluk hataları, eksik parçalar, tutarsızlıklar ve
bakım/güncelleme önerileri** var. Her madde: ne / nerede / etki / öneri.

Öncelik: **P1** (işlevi bozan / veri kaybı riski), **P2** (kafa karışıklığı / bakım borcu), **P3** (iyileştirme).

---

## 1. Eksik migration'lar — şema ile kod uyuşmuyor  **P1**

**Durum:** `migrations/` klasöründe yalnızca `001_init` ve `004_annon_devices` var. Kod 002 ve 003'ün
içeriğine bağımlı:

* `sayfam_tiles` ve `sayfam_searches` tabloları: `sayfam_tiles.go` bunları kullanıyor, dosya başında
  `core_migration_003_sayfam_tiles.sql`'e atıf var — **bu dosya repoda yok**. Bu tablolar olmadan
  "Sayfam" sekmesinin tüm uçları 500 döner.
* `ring_group_members.user_id`: `ring_groups.go` USER tipi üyeyi `INSERT INTO ring_group_members
  (group_id, user_id, priority)` ile ekliyor, ama `001_init`'te tablo yalnızca `panel_id INT NOT NULL`
  içeriyor. Yani USER üyeli bir ring group oluşturma **DB hatasıyla başarısız olur** (ve dialplan
  yazılmadan önce kısmi kayıt kalabilir). Bir `002` migration'ı `user_id` kolonu eklemiş ve
  `panel_id`'yi nullable yapmış olmalı — repoda yok.
* `conference_sessions` 004'te var (iyi), ama `panel_session_history` hiçbir migration'da yok; yalnızca
  `main.go` açılışta `CREATE TABLE IF NOT EXISTS` ile yaratıyor.

**Etki:** `core_migrate.sh` ve CI'daki migration testi yalnızca var olan dosyaları uyguladığından, temiz
bir DB'de `ring_group_members.user_id` ve `sayfam_*` **oluşmaz** → ilgili özellikler çalışmaz. CI "yeşil"
görünür çünkü kod derlenir ama bu runtime yolları test edilmez.

**Öneri:**
1. Eksik şema değişikliklerini kalıcı, numaralı, idempotent migration dosyalarına dökün:
   `002_ring_group_user_members.sql` (`ALTER TABLE ring_group_members ADD COLUMN IF NOT EXISTS user_id ...;
   ALTER ... ALTER COLUMN panel_id DROP NOT NULL;` + uygun PK/constraint), `003_sayfam_tiles.sql`
   (`sayfam_tiles`, `sayfam_searches` + `uq_sayfam_searches(user_id, query)`).
2. `panel_session_history`'yi de bir migration'a taşıyın (şu an yalnızca kodun açılış DDL'ine bağlı;
   migration testi onu kapsamıyor).
3. `core/core_migration_004_annon_devices.sql` kök `migrations/`'ın birebir kopyası — çift kaynak.
   `core/`'daki kopyayı silip tek yeri (`migrations/`) kaynak kabul edin.

---

## 2. Sürüm numarası kaosu  **P1/P2**

Beş farklı yerde beş farklı "sürüm" var ve hiçbiri ötekiyle tutarlı değil (bkz. MIMARI §8):
kök `VERSION`=1.6.8, `core/VERSION`=1.7.0, `panel-backend/VERSION`=1.6.0, panel_app="v3.1",
RELEASE_NOTES son kayıt v1.6.0, CHANGELOG boş.

**Etki:** "Sahada hangi sürüm çalışıyor" sorusu cevaplanamıyor — ki yol haritasının (§2.2) çözmeye
çalıştığı asıl problem buydu. `/api/version` `core/VERSION`'ı döndürür (1.7.0), kök `VERSION` (release
tar'ının adını belirleyen) 1.6.8 — yani tag'lenen sürümle çalışan sürüm farklı görünebilir.

**Öneri:**
1. **Tek kaynak**: kök `VERSION`. `core/VERSION` ve `panel-backend/VERSION`'ı kaldırın; `go:embed` yerine
   derleme sırasında kök `VERSION`'ı gömün (ya da `release.yml` zaten yapmaya çalışıyor — aşağıya bakın).
2. `release.yml`'deki `-ldflags "-X main.Version=..."` **etkisiz**: `core/main.go`'da `Version` adlı
   dışa aktarılmış değişken yok (`version`, küçük harf, `go:embed VERSION`'dan geliyor). Ya kodu
   `var Version string` yapıp ldflags'i çalışır hale getirin, ya da ldflags'i kaldırıp release'in kök
   `VERSION`'ı `core/VERSION`'a kopyalamasını sağlayın. Şu an ikisi birbiriyle çelişiyor.
3. panel_app.html'e kod içi `APP_VERSION` ekleyip (yol haritası §3.3) sürümü derleme/paketleme anında
   yazın; şu an "v3.1" elle gömülü sabit.
4. `CHANGELOG.md` boş — RELEASE_NOTES.md ile birleştirip Keep a Changelog formatında tek yerde tutun.

---

## 3. Rollback binary'leri geri koymuyor  **P1**

**Nerede:** `scripts/deploy_apply.sh` + `scripts/rollback.sh`.

**Durum:** `deploy_apply.sh` binary'leri `/opt/rnvcs/releases/<sürüm>/`'e açıyor **ama çalışan
binary'yi `/opt/rnvcs/rnvcs-yonetim-servisi`'ye kopyalıyor** (symlink değil). `current` symlink'i
yalnızca release klasörünü işaret ediyor. systemd unit `ExecStart=/opt/rnvcs/rnvcs-yonetim-servisi`
(sabit yol, `current` symlink'ini kullanmıyor). `rollback.sh` ise **yalnızca `current` symlink'ini**
eski klasöre çeviriyor — `/opt/rnvcs/rnvcs-yonetim-servisi` binary'si hâlâ yeni (bozuk) sürüm.

**Etki:** Rollback sonrası servis restart edildiğinde **yeni/bozuk binary çalışmaya devam eder** —
rollback aslında bir şey geri almıyor. Otomatik rollback (deploy-production.yml smoke test başarısızsa)
yanlış güvenlik hissi veriyor.

**Öneri:** İki modelden birini tutarlı uygulayın:
* **Symlink modeli:** systemd `ExecStart=/opt/rnvcs/current/rnvcs-yonetim-servisi` olsun; deploy ve
  rollback sadece `current` symlink'ini çevirsin, binary kopyalama olmasın. (Önerilen.)
* **Kopya modeli:** rollback da binary'leri eski release klasöründen `/opt/rnvcs/`'a geri kopyalasın.
Ayrıca `rollback.sh` DB'yi geri almıyor — migration ileri alındıysa şema yeni kalır. `backup_current.sh`
yedek alıyor ama rollback onu restore etmiyor; migration'ları geriye-uyumlu (additive) tutma disiplinini
(yol haritası §4.1) belgeleyip zorunlu kılın.

---

## 4. Uygulanmayan yetkiler: `can_call` / `can_anons` / `can_config`  **P2**

**Nerede:** `user_panel_permissions`'ta tanımlı, `login.go` cevabında dönüyor, ama **hiçbir uçta kontrol
edilmiyor**. `can_call=false` bir kullanıcı pekâlâ `/api/call/dial`'ı (panel-backend, kimlik doğrulamasız)
ya da dolaylı olarak çağrı akışını kullanabilir; `can_config` hiçbir yere bağlı değil.

**Etki:** Yetki matrisi arayüzde ve DB'de var ama işlevsel değil — yanıltıcı. (Test planı §3.4 "can_call=false
gerçekten arama başlatamıyor mu" diye kontrol edilmesini istemiş.)

**Öneri:** Ya bu alanları gerçekten uygulayın (çağrı başlatma yetkisini CORE üzerinden doğrulayın —
panel-backend'in dial'ı CORE'a sorması gerekir), ya da kullanılmıyorsa arayüzden/şemadan kaldırıp
karışıklığı giderin. En azından mevcut davranışı belgeleyin.

---

## 5. Tutarsız/ölü işlevler ve kod kopyaları  **P2**

| Konu | Nerede | Durum |
|---|---|---|
| `core_kurulum.sh`'de PJSIP transport bloğu **iki kez** kopyalanmış | `scripts/core_kurulum.sh:165-182` ve `184-206` | Birebir aynı blok, ikincisi gereksiz. İlki yetiyor. |
| `scripts/install_yonetim_servisi.sh` vs `core/install_yonetim_servisi.sh` | iki dosya | `core/`'daki güncel (AMI/manager.conf/confbridge adımları var), `scripts/`'teki eski. `scripts/`'teki kafa karıştırıyor — kaldırın ya da güncelini oraya taşıyın. |
| `scripts/install_servis.sh` vs `panel-backend/install_servis.sh` | iki dosya | Birebir aynı. Tek yer bırakın. |
| `panel_app.html` vs `panel_app_3_1.html` | iki dosya | Tek fark `CORE` varsayılanı. İki ayrı dosya tutmak yerine query param ile çözün (zaten `?core=` var), birini silin. |
| `panel-backend/fix_voip.py` | tek seferlik yama | Uyguladığı değişiklik zaten kaynakta. Ölü dosya, kaldırın. |
| `dialplan.AppendPanelExtension` | `dialplan/writer.go` | Artık hiçbir yerden çağrılmıyor (PANEL tipi SIP register olmuyor). Ölü kod. |
| `confbridge.conf` `[rnvcs_bridge]/[rnvcs_user]` | install script yazıyor | Kod `ConfBridge(room)` çağrısında bu profilleri kullanmıyor (varsayılan profil). Ya profili kullanın ya da yazmayın. |
| `AppendDirectExtension` idempotent değil | `dialplan/writer.go` | Aynı kullanıcıya SIP hesabı her atandığında `extensions_rnvcs_dynamic.conf`'a yinelenen blok eklenir; dosya şişer. pjsip/confbridge writer'larındaki işaretli-blok desenine geçirin. Aynı sorun `AppendRingGroup`, `AppendTrunk`, `AppendMailbox`(append) için de geçerli. |
| Kullanıcı silinince dialplan bloğu kalıyor | `users.go` DELETE | PJSIP bloğu siliniyor ama `extensions_rnvcs_dynamic.conf`'taki direkt-arama extension'ı silinmiyor (yorumda da itiraf ediliyor). Writer'a `RemoveExtension` ekleyin. |

---

## 6. Veri bütünlüğü / işlevsel boşluklar  **P2**

* **Ring group / trunk / panel için silme-düzenleme ucu yok.** Yalnızca oluşturma var. Yanlış tanım
  düzeltilemiyor (DB'den elle müdahale gerekir). CRUD'u tamamlayın.
* **Ring group kısmi kayıt:** üye bulunamaz/SIP yok ise 409 dönülüyor ama `ring_groups` satırı ve eklenen
  üyeler DB'de **kalıyor** (yalnızca dialplan hatası geri alınıyor). Üye doğrulamasını INSERT'ten **önce**
  yapın ya da tüm işlemi tek transaction'da geri alın (şu an `internal/pg` transaction desteklemiyor —
  bkz. §8).
* **`conference_sessions.ended_at` hiç set edilmiyor** — oturum hep "açık" görünür. confwatch oda
  kapatınca `ended_at`'i güncelleyin.
* **IAX2 federasyon yarım:** trunk DB'ye ve `iax.conf`'a yazılıyor ama giden `prefix` dialplan'i ve gelen
  `[rnvcs-trunks]` context'i yok (yol haritası §2.7 / §8 Faz 5). Özellik fiilen çalışmıyor.
* **Saat kayması riski:** `call_records` join'i CDR zamanı ile `panel_session_history` zamanını
  karşılaştırıyor; NTP yoksa yanlış kişiye atfedebilir (dokümanlarda not edilmiş). CORE'da NTP'yi
  kurulum script'ine ekleyin.

---

## 7. Bağımlılıklar ve altyapı  **P2/P3**

* **Redis kurulu ama kullanılmıyor.** `core_kurulum.sh` Redis kuruyor, `infra/systemd`'de unit var, ama
  hiçbir Go kodu Redis'e bağlanmıyor (oturumlar bellek-içi map). Ya presence/çoklu-instance için
  gerçekten kullanın (kod yorumları bunu "ileride" diyor) ya da kurulumdan çıkarıp kaynak tüketimini azaltın.
* **Go sürümü tutarsız:** `go.mod` `go 1.21`, CI `go-version: "1.23"`, bu ortamda 1.24.7 ile sorunsuz
  derleniyor. Hedef sürümü netleştirip hizalayın (go.mod'u 1.23'e çekmek mantıklı).
* **`psql` CLI bağımlılığı:** `internal/pg` her sorguda bir `psql` süreci başlatıyor. Çalışır ama:
  (a) transaction yok, (b) her sorgu process fork maliyeti, (c) DSN komut argümanında (ps'de görünür).
  Orta vadede `database/sql` + gömülebilen bir sürücüye (pgx tek dosya olarak vendor edilebilir) geçmek
  hem performansı hem güvenliği (parametreli sorgu, transaction) düzeltir. "Sıfır bağımlılık" ilkesi
  `go mod vendor` ile korunabilir.
* **ODBC/CDR kırılganlığı:** CDR `cdr_adaptive_odbc` + `alias end => enddate` ile çalışıyor; tablo şeması
  ile conf arasındaki bu ince bağ (`end` rezerve kelime) bir regresyon kaynağı. Migration ve conf'u aynı
  yerde belgeleyin (MIMARI'ye eklendi).

---

## 8. Mimari / sağlamlık önerileri  **P3**

* **Transaction yok:** `internal/pg` her `Exec`'i ayrı psql süreci olarak çalıştırıyor; çok adımlı
  işlemler (kullanıcı+SIP+extension, ring group+üyeler) yarıda kalırsa tutarsız durum oluşur. `psql`'e
  `BEGIN; ...; COMMIT;` tek `-c` bloğu ya da `database/sql` tx'e geçiş.
* **Hata yutma:** Pek çok `event_log` ve bazı kritik `Exec` çağrısı `_ = h.db.Exec(...)` ile hatayı
  yutuyor. En azından loglanmalı.
* **Test kapsamı çok dar:** Yalnızca `voicemail_test.go` (3 test) ve `watcher_manual_test.go` (1 test) var.
  Login/yetki/SQL üretimi/SIP digest gibi saf fonksiyonlar kolayca test edilebilir (test planı §2 bunu
  detaylandırmış). `httptest` tabanlı entegrasyon testleri ekleyin; CI zaten Postgres container'ı kaldırıyor.
* **`confwatch` her event'te yeni AMI bağlantısı açıyor** (`checkLoneParticipant` → `ami.Dial`); yoğun
  konferansta bağlantı fırtınası olabilir. Tek kalıcı bağlantıyı paylaşın.
* **panel_app tek dosya 180 KB**, 92 KB'ı base64 gömülü PNG. Görseli ayrı dosyaya alıp cache'lenebilir
  yapmak boyutu ve yükleme süresini düşürür (kiosk için önemli değilse düşük öncelik).
* **CLAUDE.md yok:** ajan/geliştirici için repo kuralları (sıfır bağımlılık, migration disiplini, writer
  idempotency kuralı) tek yerde değil. Eklendi (`/CLAUDE.md`).

---

## 9. Dokümantasyon tutarsızlıkları  **P2/P3**

* `CICD_TASARIMI.md` "deploy-production.yml bilinçli olarak repodan kaldırıldı" diyor ama dosya repoda
  **duruyor**. İkisini tutarlı hale getirin.
* `CHANGELOG.md` boş; yol haritası "her PR'da güncellenmeli" diyor — disiplin başlatılmamış.
* RELEASE_NOTES.md en son v1.6.0'da kalmış; `core/VERSION` 1.7.0 — 1.7.0'ın notları yok.
* `vcs_test_plan.md` panel_app "v2.1" diyor, mevcut dosya "v3.1" — doküman güncel koddan geri.
* Çok sayıda kod yorumu "Bölüm 10.x", "Anayasa 10.19" gibi repoda **olmayan** bir ana belgeye atıf
  yapıyor. O belge (muhtemelen proje anayasası) repoya eklenmeli ya da atıflar güncellenmeli; aksi halde
  "Bölüm 10.22" gibi referanslar yeni geliştirici için anlamsız.

---

## 10. Önerilen eylem sırası

1. **P1 — işlevi geri getiren:** eksik migration'ları yaz (§1), rollback'i düzelt (§3), sürüm kaynağını
   teke indir + ldflags'i düzelt (§2).
2. **P1/P2 — güvenlik (ayrı rapor):** G-01/G-02 girdi doğrulaması, panel-backend'i localhost+token'a al.
3. **P2 — temizlik:** kod kopyalarını ve ölü dosyaları kaldır (§5), yetki alanlarını uygula ya da kaldır
   (§4), silme/düzenleme uçlarını tamamla (§6).
4. **P3 — sağlamlık:** transaction, test kapsamı, Redis kararı, Go sürüm hizalama, doküman tutarlılığı.

Not: bu repo kopyasında `core` ve `panel-backend` `go build ./...`, `go vet ./...`, `go test ./...`
**temiz geçiyor** (Go 1.24.7). Yani mevcut kod derleniyor; yukarıdaki P1'ler derleme değil, runtime/şema
ve dağıtım düzeyinde sorunlar.
