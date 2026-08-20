# RNVCS / VCS Panel — Test Planı ve Test Adımları

**Proje:** P2015 — Yedek VCS ve Panel Kurulumu (AZVCS)
**Kapsam:** Yedek CORE (rnvcs-yonetim-servisi, :8091), panel-backend (:8090), panel_app v2.1 (frontend), PostgreSQL şeması, Asterisk/PJSIP, IAX2 federasyon
**Hazırlayan:** Claude (Cowork) — proje dokümanları (panel_app_v2.1_1.html, speed_dials_1.go, core_migration_001_init.sql, proxmox_kurulum_handbook_2.md) incelenerek hazırlanmıştır.
**Tarih:** 2026-08-20

---

## 1. Sistemin Test Açısından Haritası

Sanallaştırma ve kurulum tamamlandığına göre, test stratejisini sistemin katmanlarına göre kurmak en doğrusu. Elimizdeki mimari şöyle:

| Katman | Bileşen | Teknoloji | Sorumluluk |
|---|---|---|---|
| Telefoni | Asterisk (PJSIP) | Asterisk 22.x | SIP register, çağrı yönlendirme, IAX2 trunk |
| CORE API | rnvcs-yonetim-servisi | Go, :8091 | Login/oturum, kullanıcı/panel/yetki, speed_dials, ring_groups, event_log, voicemail |
| Panel Backend | rnvcs-panel-backend | Go, :8090 | Ses aygıtları (PipeWire), çağrı durumu, ses seviyesi, oturum köprüleme |
| Veri | PostgreSQL (rnvcs) | SQL | users, panels, speed_dials, ring_groups, iax_trunks, event_log, asterisk_cdr |
| İstemci | panel_app v2.1 | HTML/JS (tek dosya) | Login, dialpad, hızlı arama, gelen/aktif çağrı, ses, sesli mesaj, cihazlar, çok dilli arayüz |

Test adımlarını da bu beş katmana göre ayırıyorum: **birim testler** (kod seviyesi), **servis/entegrasyon testleri** (API + DB + Asterisk), **fonksiyonel testler** (panel_app davranışı), **uçtan uca / son kullanıcı testleri** (gerçek cihazda gerçek arama) ve bunları tamamlayan **güvenlik, performans ve regresyon testleri**.

---

## 2. Birim Testler (Unit Test)

Amaç: Go kodundaki iş mantığını, dış bağımlılıklar (DB, Asterisk, ağ) olmadan izole şekilde doğrulamak.

### 2.1 rnvcs-yonetim-servisi (CORE, :8091)

`speed_dials.go` üzerinden örnek verirsek, kolayca izole test edilebilecek noktalar:

- `validSpeedTarget(t string)` — geçerli (`USER`, `RING_GROUP`, `TRUNK_REMOTE`, `NUMBER`, `PANEL`) ve geçersiz değerler (`""`, `"XYZ"`, küçük harf `"user"`) için tablo tabanlı test.
- Yetki mantığı: `isAdmin` hesaplaması (`Role == "ADMIN" || Role == "MAINTAINER"`) ve `req.Username != sess.Username && !isAdmin` dallanması — ADMIN, MAINTAINER, OPERATOR rolleriyle kendi/başkası kısayolu senaryoları.
- Varsayılan değer ataması: `req.Username == ""` → oturum sahibine düşme; `req.TargetType == ""` → `"NUMBER"` varsayılanı; `position == 0` → listenin sonuna ekleme mantığı.
- Aynı desen diğer handler dosyaları için de geçerli: login/oturum doğrulama, panel yetki matrisi (`user_panel_permissions` alanlarının — `can_login/can_call/can_anons/can_config` — doğru okunup uygulanması), ring_group üyelik mantığı.

Bu tür testler `h.db` gibi bağımlılıkları saklamayan (`Handler` struct'ı doğrudan `*sql.DB` tutuyor) fonksiyonlarda zor olabilir; pratik öneri: DB erişen fonksiyonları küçük, saf (pure) yardımcı fonksiyonlara ayırıp (doğrulama, SQL string üretimi, yanıt şekillendirme) sadece o kısımları unit test etmek, DB'ye dokunan kısmı entegrasyon testine bırakmak.

**Araç önerisi:** Go'nun standart `testing` paketi + `testify/assert`. Tablo tabanlı (`t.Run` alt testleriyle) test deseni bu kod tabanına çok uygun.

### 2.2 rnvcs-panel-backend (:8090)

- Ses seviyesi hesaplama/limitleme mantığı (ör. `vol up/down/mute` çağrılarının 0–100 sınırını aşmaması, mute/unmute geçişi).
- Çağrı durumu makinesi: `idle → ringing → active/dialing → idle` geçişlerinin geçersiz sıralamalarda hata vermesi (ör. `idle` iken `answer` çağrısı).
- PipeWire/WirePlumber çıktısını ayrıştıran kod varsa, sabit örnek çıktılarla (fixture) parse testleri — 3.11'de yaşanan "eski binary / eksik endpoint" sınıfı hataları unit seviyede yakalamanın en ucuz yolu budur.

### 2.3 Veritabanı migrasyonu

`core_migration_001_init.sql` idempotent yazılmış (`IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`). Bunun "birim testi" migrasyonu **art arda iki kez** boş bir DB'ye ve bir kez de eski şemalı bir DB'ye (10.19 kullanıcı-merkezli izleri olan) uygulayıp hatasız bittiğini doğrulamaktır:

```
createdb rnvcs_test
psql -d rnvcs_test -f core_migration_001_init.sql   # 1. çalıştırma
psql -d rnvcs_test -f core_migration_001_init.sql   # 2. çalıştırma — hata vermemeli
```

Bunu CI'a bir adım olarak eklemek, gelecekteki migrasyon hatalarını (3.6'da yaşanan kod-şema uyuşmazlığı gibi) üretime çıkmadan yakalar.

---

## 3. Servis / Entegrasyon Testleri

Amaç: Katmanlar birbirine gerçekten bağlıyken (gerçek Postgres, gerçek/simüle Asterisk) doğru çalıştığını doğrulamak. Birim testten farkı: DB'ye gerçekten yazıp okumak, HTTP üzerinden gerçek istek atmak.

### 3.1 CORE API (:8091) — uç nokta bazlı senaryolar

| Uç nokta | Test senaryoları |
|---|---|
| `POST /api/login` | doğru şifre → token; yanlış şifre → 401; `enabled=false` kullanıcı → red; panel yetkisi olmayan kullanıcı (RNVCS_ENFORCE_PANEL_LOGIN=1 iken) → red |
| `GET /api/speed-dials` | kendi kısayolları; `?username=` ile başkasınınkiler (yetkisiz OPERATOR → 403 bekleniyorsa kontrol; şu anki kodda GET için yetki kontrolü yok — bkz. §6.2) |
| `POST /api/speed-dials` | eksik `label`/`target_value` → 400; geçersiz `target_type` → 400; başkası adına ADMIN olmadan → 403; başarılı ekleme → `event_log`'a `CONFIG_CHANGE` satırı düştüğünü doğrula |
| `DELETE /api/speed-dials?id=` | var olmayan id → 404; başkasının kaydı, sahibi/ADMIN değilken → 403; sahip kendi kaydını siliyor → 200 |
| `PUT /api/speed-dials` (sıralama) | `ids` listesindeki id'lerden biri başka kullanıcıya aitse **sessizce atlanmalı** (SQL `AND user_id=` koşulu bunu sağlıyor) — bunun gerçekten böyle davrandığını ayrı bir testle doğrula |
| `GET /api/voicemail`, `DELETE /api/voicemail` | panel_code filtreleme, sesli mesaj silme sonrası liste güncelleniyor mu |

**Araç önerisi:** Go `net/http/httptest` ile handler'ları gerçek Postgres'e (test DB, her testten önce migration + seed) bağlayarak çalıştırmak; ya da Postman/Newman veya `hurl`/`bruno` ile dışarıdan HTTP koleksiyonu halinde otomatikleştirmek — CI'da `newman run` şeklinde çalıştırılabilir.

### 3.2 panel-backend (:8090) entegrasyonu

- `/api/audio-devices` — gerçek PipeWire soketine bağlanıp cihaz listesi dönüyor mu (3.11'deki "eski binary 404 dönüyordu" hatası tam olarak bu testin eksikliğinden kaynaklandı; bunu bir "smoke test" olarak CI/deploy sonrası otomatik koşturmak mantıklı).
- `/api/volume` GET/POST — gerçek sink üzerinde `up/down/mute` sonrası `wpctl status` çıktısıyla karşılaştırma.
- `/api/call/status`, `/api/call/dial`, `/api/call/answer`, `/api/call/hangup` — Asterisk AMI/ARI ile gerçek entegrasyon; en azından 600 echo test extension'ı üzerinden "dial → answer → active → hangup" tam döngüsü.
- `/api/session`, `/api/session/logout` — CORE token'ının panel-backend'e doğru aktarılması.

### 3.3 Asterisk / PJSIP / IAX2

- **PJSIP transport doğrulaması:** `sudo ss -lunp | grep 5060` çıktısının boş olmadığını her deploy sonrası otomatik kontrol eden bir betik — 3.13'te yaşanan "transport tanımsız, kimse register olamıyor" sınıfı hatayı sistematik yakalamanın yolu bu.
- **Register testi:** Bilinen bir panel/SIP kimlik bilgisiyle programatik REGISTER denemesi (ör. `sipsak` veya basit bir test SIP istemcisi ile) ve beklenen 200 OK yanıtının doğrulanması.
- **600 echo testi:** Otomatik çağrı başlatıp (AMI `Originate`) `600`'e bağlanıp `Answer→Echo→Hangup` akışının CDR'a (`asterisk_cdr`) doğru `disposition=ANSWERED` ile düştüğünü kontrol eden bir script — "ses yolu" regresyonlarını manuel dinlemeden yakalamak için.
- **IAX2 trunk (planlanan federasyon):** `iax_trunks` tablosundaki trunk tanımının Asterisk'e yansıdığını, prefix routing (`8+site+dahili`) kuralları yazıldığında giden/gelen çağrının doğru sunucuya gittiğini doğrulayan senaryo — bu özellik henüz tamamlanmadığı için test planı şimdiden yazılıp özellik bitince koşturulabilir.

### 3.4 Veritabanı bütünlüğü

- Yetki matrisi (`user_panel_permissions`) ile gerçek CORE davranışının tutarlılığı: `can_call=false` olan bir kullanıcı gerçekten arama başlatamıyor mu.
- `ON DELETE CASCADE` zincirlerinin (ör. kullanıcı silinince `speed_dials`, `active_panel_sessions` satırlarının da silinmesi) gerçekten çalıştığının doğrulanması.

---

## 4. Fonksiyonel Testler (panel_app v2.1 — istemci)

Bunlar "özellik çalışıyor mu" sorusuna UI seviyesinde cevap veren testler. panel_app tek dosyalık bir HTML/JS uygulaması olduğu için hem manuel kontrol listesi hem de tarayıcı otomasyonu (Playwright/Selenium) ile otomatikleştirilebilir.

### 4.1 Giriş ve sistem testi ekranı
- Doğru/yanlış kullanıcı adı-şifre ile giriş.
- "Testi Çalıştır" butonunun dört satırı (Sunucu Bağlantısı, Bağlantı Kalitesi, Panel Arka Servisi, Ses Aygıtı) gerçek durumu yansıtıyor mu — CORE veya panel-backend kapalıyken kırmızı/"Ulaşılamıyor" gösteriyor mu.
- Sanal klavye (dokunmatik ekran için) kullanıcı adı/şifre alanlarında doğru karakter giriyor mu, Shift/boşluk/backspace çalışıyor mu.

### 4.2 Dialpad ve arama
- Rakam/`*`/`#` tuşlarının ekrana doğru yazılması, geri silme (`⌫`).
- Boş numarayla "ARA" → hata mesajı (`needNum`).
- Geçerli numarayla arama → `dialing` → `active` durum geçişi, süre sayacının başlaması.
- Gelen çağrı ekranı (`#inc`): cevapla/reddet butonları, arayan numarasının doğru gösterilmesi.
- Aktif çağrı ekranı: "Arka Plana Al" ile küçültme, alttaki "pill" bileşeninin doğru peer/süre göstermesi, oradan tekrar açma, kapatma.

### 4.3 Hızlı Arama (Speed Dial) — kullanıcı bazlı CRUD
- "Düzenle" moduna geçiş, "＋Ekle" ile yeni kayıt (etiket + numara), boş alanla kaydetmeye çalışınca hata.
- Kayda tıklayınca (düzenleme modu kapalıyken) doğrudan arama başlaması.
- Silme (✕) ve sıralama (↑/↓) — sıralamanın `PUT /api/speed-dials` ile kalıcı olarak DB'ye yazıldığını sayfa yenilendikten sonra doğrulama.
- Farklı roller (OPERATOR, ADMIN) ile başkası adına kısayol ekleme denemesi — OPERATOR için engellenmeli.

### 4.4 Ses kontrolü ve cihazlar
- Gelen ses seviyesi Aç/Kıs/Sustur butonlarının göstergeyi (`%NN`) ve mute rozetini doğru güncellemesi.
- "Ses Aygıtları" modalının gerçek cihaz listesini (giriş/çıkış, transport tipi) gösterip 2 saniyede bir yenilenmesi; cihaz yokken uygun boş-durum mesajı.

### 4.5 Sesli Mesaj
- Rozet sayısının gerçek mesaj sayısıyla eşleşmesi, mesaj oynatma (audio player), silme sonrası listeden ve rozetten düşmesi.

### 4.6 Çok dilli arayüz (TR/AZ/EN)
- Dil değiştirme butonlarının hem login hem app ekranında tüm `data-i18n` etiketli metinleri değiştirmesi; dinamik metinlerin (Düzenle/Bitti, çağrı durumu yazıları) de yeni dile göre güncellenmesi; seçimin `sessionStorage`'da kalıcı olması.

### 4.7 Duyarlı tasarım (yatay/dikey)
- Dikey (portrait) modda hızlı aramanın üstte, dialpad'in altta küçük görünmesi; yatayda iki sütunlu klasik düzen.

**Araç önerisi:** Kritik akışlar (giriş, arama başlatma/bitirme, hızlı arama CRUD) için Playwright ile uçtan uca senaryo otomasyonu; geri kalanı elle kontrol listesi olarak her sürümde koşturulur.

---

## 5. Uçtan Uca / Son Kullanıcı Testleri (UAT)

Bunlar gerçek donanımda, gerçek operatörle yapılan, "sistem gerçekten çalışıyor mu" testleridir — Proxmox kurulum el kitabındaki §4 "Devam eden / doğrulanacak" listesiyle doğrudan örtüşüyor:

1. **Reboot kalıcılığı:** Yedek CORE VM'i yeniden başlatıldığında elle müdahale olmadan Asterisk'in 5060'ı dinlemesi, servislerin (Postgres, Redis, Go servisi) otomatik ayağa kalkması.
2. **Gerçek SIP register:** MicroSIP (VPN kapalı, Public Address 15.2.4.155) ile panel koduyla register olup 600 echo testinde kendi sesini duyması.
3. **Panelden gerçek arama:** Panel cihazından (15.2.4.201) başka bir dahiliye arama, karşı taraf cevaplayınca sesin iki yönlü de gelmesi, kapatma sonrası panelin `idle` durumuna dönmesi.
4. **Operatör senaryosu:** Bir operatör kullanıcısıyla giriş → kendi hızlı aramasını ekleme/kullanma → başka kullanıcı adına kısayol eklemeyi denediğinde engellenmesi.
5. **ADMIN senaryosu:** ADMIN/MAINTAINER ile başka kullanıcı adına kısayol ekleme/silme, panel yetki matrisini değiştirip etkisini gözlemleme.
6. **Arıza senaryosu (kesinti provası):** Üretim VCS (vcs04-1.az) devre dışı bırakılmadan, yedek CORE'un bağımsız çalıştığının ve gerektiğinde devreye alınabildiğinin tatbikatı — bu, "yedek" olma amacının asıl testi.
7. **Çok dilli kullanım:** Farklı dil tercihine sahip operatörlerle (AZ/TR/EN) gerçek kullanım — özellikle sayı/tarih formatlarının ve buton metinlerinin anlaşılır olduğu geri bildirimi.

UAT sonuçlarını basit bir tabloya (senaryo / beklenen / gerçekleşen / durum / not) kaydedip proje dosyalarına eklemenizi öneririm — ekip dışından biri kabul testine katılacaksa bu tablo, iletişimi kolaylaştırır.

---

## 6. Tamamlayıcı Test Türleri

### 6.1 Regresyon testi
Her yeni özellik/deploy sonrası en azından şu "smoke" seti hızlıca koşturulmalı: CORE `/healthz`, panel-backend `/api/volume` ve `/api/audio-devices`, PJSIP transport kontrolü, 600 echo testi, panel_app login. Bunu tek bir betikte toplayıp deploy sonrası otomatik çalıştırmak (§3.1–3.3'teki testlerin bir alt kümesi), 3.11 ve 3.13'teki gibi "sessizce bozulan" özellikleri hemen yakalar.

### 6.2 Güvenlik testi — dikkat edilmesi gereken bir nokta

`speed_dials.go` incelemesinde SQL sorguları `pg.EscapeLiteral` ile string birleştirilerek (parametreli sorgu/prepared statement yerine) kuruluyor (ör. `"...WHERE u.username = " + pg.EscapeLiteral(username)`). `pg.EscapeLiteral` doğru uygulandığı sürece SQL injection'a karşı koruma sağlar, ama bu desen hataya açık: yeni bir alan eklenirken biri `EscapeLiteral` çağırmayı unutursa doğrudan injection açığı oluşur. Test planına şunu eklemenizi öneririm:

- **Injection regresyon testi:** `label`, `target_value`, `username` gibi alanlara `'; DROP TABLE speed_dials; --` türü payload'lar göndererek CORE API'nin bunları güvenli şekilde işlediğini (hata vermeden veya güvenli biçimde reddederek) doğrulayan otomatik test seti — her yeni endpoint eklendiğinde bu sete bir örnek eklenmeli.
- Uzun vadede, bu handler'ları parametreli sorgulara (`database/sql` ile `$1,$2,...` placeholder) geçirmek, bu sınıf riski kaynağında ortadan kaldırır — bu bir kod iyileştirmesi önerisi, test planının kendisi değil, ama test planına not düşülmeye değer.

Diğer güvenlik kontrolleri: token/oturum süresinin dolması, yetkisiz endpoint erişimi (rol bazlı 403 kontrolleri — özellikle GET `/api/speed-dials?username=` için başka kullanıcı adı verildiğinde bugün yetki kontrolü yok, bunun kasıtlı mı olduğu netleştirilmeli), şifrelerin/`sip_password`'lerin API yanıtlarında sızmadığının doğrulanması.

### 6.3 Performans / yük testi
- CORE API'ye eşzamanlı çoklu panel/kullanıcı isteği (ör. `k6` veya `hey`/`wrk` ile `/api/login`, `/api/speed-dials` uç noktalarına yük testi) — özellikle birden çok panelin aynı anda bağlanacağı senaryo (federasyon planı düşünülünce önemli).
- Asterisk tarafında eşzamanlı çağrı kapasitesi testi (kaç panel aynı anda arama yapabiliyor, RTP port aralığı 10000–20000/udp yeterli mi).

---

## 7. Önerilen Uygulama Sırası

1. Migration idempotency testini ve CORE/panel-backend smoke testlerini (§3.1, §3.2, §6.1) bir betikte toplayıp ilk iş bunu çalışır hale getirin — en ucuz, en yüksek getirili adım budur; 3.11/3.13'teki hataların ikisi de bu seviyede otomatik yakalanabilirdi.
2. `speed_dials.go` gibi CRUD handler'ları için Go `testing` ile birim + entegrasyon testleri yazın (§2.1, §3.1) — kod zaten yazılmış, test yazmak görece hızlı.
3. Playwright ile panel_app'in kritik akışlarını (giriş, arama, hızlı arama CRUD) otomatikleştirin (§4) — manuel kontrol listesini paralelde tutun.
4. PJSIP/600 echo/IAX2 testlerini (§3.3) bir "telefoni sağlık kontrolü" betiği olarak birleştirin, her deploy sonrası koşturun.
5. UAT tablosunu (§5) hazırlayıp gerçek operatörlerle bir oturum planlayın — özellikle "reboot kalıcılığı" ve "arıza senaryosu" maddeleri, sistemin asıl amacı olan "yedeklik" iddiasını doğruladığı için önceliklidir.
6. Injection regresyon setini (§6.2) ve temel yük testini (§6.3) en son ekleyin; bunlar sistem stabil çalışmaya başladıktan sonra daha anlamlı sonuç verir.

---

## 8. Otomasyon Durumu ve Yapılacaklar

**Bugün itibarıyla otomatik:**

- **`rnvcs_smoke_test.sh`** yazıldı ve teslim edildi — CORE `/healthz`, panel-backend `/api/volume` ve `/api/audio-devices`, PJSIP 5060 transport dinleme durumu, PJSIP endpoint register özeti, PostgreSQL bağlantısı ve isteğe bağlı (`-e` bayrağı) 600 echo/dialplan sağlık testini tek komutla koşturuyor. Release tar paketine eklenip her kurulumdan sonra elle veya cron ile çalıştırılabilir; CI/CD henüz kurulmadığı için şimdilik bu betik "otomasyonun" karşılığı.

**CI/CD kurulmadan önce elle/cron ile çalıştırma önerisi:**

```
# CORE sunucusunda (15.2.4.11), her kurulum tar'ı açıldıktan sonra elle:
./rnvcs_smoke_test.sh -e

# Periyodik sağlık kontrolü için cron (örn. her 15 dakikada bir):
*/15 * * * * /opt/rnvcs/rnvcs_smoke_test.sh >> /var/log/rnvcs-smoketest.log 2>&1
```

**Yapılacaklar listesi (proje notu):**

1. **CI/CD sistemi kurulumu** — şu an release'ler elle hazırlanan bir tar dosyasıyla dağıtılıyor; ilerleyen vakitte bir CI/CD hattı (ör. GitHub Actions / GitLab CI / Jenkins, repo hangi platformda barınacaksa ona göre) kurulup şu adımlar otomatikleştirilmeli: migration idempotency testi (§2.3) → Go birim/entegrasyon testleri (§2.1, §3.1) → `rnvcs_smoke_test.sh` (deploy sonrası) → (ileride) Playwright UI testleri (§4). Bu, şu anda elle çalıştırılan smoke test betiğinin "her push/deploy'da otomatik tetiklenmesi" anlamına gelir.
2. Go proje yapısı (go.mod, klasörler) netleştiğinde §2.1'deki birim testleri ve §3.1'deki `httptest` tabanlı entegrasyon testleri yazılacak.
3. panel_app için Playwright test iskeleti (§4) — bir test ortamı (CORE + panel-backend ayakta, test kullanıcısı) hazır olduğunda kurulacak.
4. §6.2'deki injection regresyon seti ve §6.3'teki yük testi, CI/CD hattı kurulduktan sonra pipeline'a eklenecek.

---

*Bu belge proje dokümanlarına (`claude/vcs_test_plan.md` olarak) kaydedilmiştir; yeni özellikler (IAX2 federasyon, BKT çok dillilik) tamamlandıkça ilgili bölümler güncellenmelidir.*
