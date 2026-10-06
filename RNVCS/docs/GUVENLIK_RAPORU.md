# RNVCS — Güvenlik İnceleme Raporu

**Tarih:** 2026-10-06 · **Kapsam:** repodaki tüm kaynak kod (core, panel-backend, panel-app, scripts,
migrations, CI/CD, systemd) · **Yöntem:** statik kod incelemesi (her dosya okundu) + derleme/vet/test
doğrulaması. Canlı sistemde sızma testi **yapılmadı**; bulgular koddan çıkarılan tespitlerdir ve kodda
hiçbir değişiklik yapılmadı. Bu belge **savunma amaçlıdır**: her madde "sorun / nerede / neden önemli /
nasıl düzeltilir" biçimindedir, çalışır istismar dizeleri içermez.

Şiddet: **KRİTİK** (kod çalıştırma / sistemin ele geçirilmesi), **YÜKSEK** (yetki aşımı, sır sızıntısı,
çağrıya müdahale), **ORTA** (denetim/gizlilik kaybı, zayıf koruma), **DÜŞÜK** (sıkılaştırma).

## Özet

| # | Bulgu | Şiddet | Bileşen |
|---|---|---|---|
| G-01 | Asterisk config/dialplan'e doğrulanmamış girdi yazılması (injection sınıfı) | KRİTİK | core writer paketleri |
| G-02 | AMI protokolüne doğrulanmamış girdi + AMI kullanıcısının geniş yetkisi | KRİTİK | core `internal/ami`, `conference.go` |
| G-03 | Login'de `panel_code` doğrulanmıyor → başka panelin voicemail/SIP erişimi | YÜKSEK | core `login.go`, `voicemail.go` |
| G-04 | panel-backend API'si kimlik doğrulamasız ve tüm arayüzlere açık | YÜKSEK | panel-backend |
| G-05 | SIP motoru gelen INVITE/RTP kaynağını doğrulamıyor | YÜKSEK | panel-backend `voip` |
| G-06 | Sırların düz metin saklanması (SIP/IAX parolaları, token, DB şifresi) | YÜKSEK | core, scripts |
| G-07 | Uçtan uca şifreleme yok (HTTP/SIP/RTP düz), CORS `*` | YÜKSEK | tümü |
| G-08 | Yönetim servisi `root` olarak, sıkılaştırmasız çalışıyor | YÜKSEK (çarpan) | systemd |
| G-09 | `event_log.detail` JSON'unun elle birleştirilmesi → denetim kaydı bozulabilir | ORTA | core |
| G-10 | Login'de kaba kuvvet/oran sınırı yok, hesap durumu sızıntısı | ORTA | core `login.go` |
| G-11 | Tüm roller tüm listeleri okuyabiliyor (yetkisiz bilgi ifşası) | ORTA | core |
| G-12 | Voicemail `folder` parametresi doğrulanmıyor; token URL'de | ORTA | core `voicemail.go` |
| G-13 | panel_app'te ham verinin `innerHTML` ile basılması (XSS) | ORTA | panel-app |
| G-14 | Oturum ömrü sabit 12 saat, idle timeout yok; BKT token'ı `localStorage` | DÜŞÜK | core, BKT |
| G-15 | CI/CD sıkılaştırma eksikleri | ORTA | `.gitea/` |
| G-16 | İlk admin şifresinin komut satırı/ekran üzerinden verilmesi | DÜŞÜK | scripts |
| G-17 | `EscapeLiteral`'ın yalnızca tek tırnak kaçışına dayanması | DÜŞÜK | core `pg` |

---

## G-01 — Asterisk config/dialplan'e doğrulanmamış girdi (injection) — KRİTİK

**Nerede:** `core/internal/{dialplan,confbridge,pjsip,iaxconf,voicemail}/writer.go`; çağıranlar
`internal/api/{users,panels,ring_groups,conference,iax_trunks}.go`.

**Sorun:** Kullanıcı/yönetici girdisi olan alanlar (`panel_code`, `sip_username`, `group_code`,
`room_code`, `trunk_name`, `remote_host`, `display_name` vb.) karakter kısıtı olmadan `fmt.Sprintf` ile
Asterisk konfigürasyon dosyalarına yazılıyor. Bir alanın içine satır sonu konursa, üretilen dosyaya
fazladan yapılandırma satırları girebilir. Asterisk dialplan'i uygulama çağırabildiğinden (ör. komut
çalıştıran uygulamalar), bu sınıf bir zafiyet en kötü durumda Asterisk süreci haklarıyla komut
çalıştırmaya kadar gidebilir. Tek girdi doğrulaması `voicemail.go`'daki `safePanelCode/safeMsgID`'dir ve
yalnızca dosya *okuma* yolunda kullanılır; *yazma* yollarında hiçbir doğrulama yok.

**Neden önemli:** `room_code` yolu (`/api/conference`) **herhangi bir login'li kullanıcıya** açık; diğer
alanlar ADMIN/MAINTAINER'a. Yani düşük yetkili bir operatör bile yazma yoluna girdi ulaştırabiliyor.

**Nasıl düzeltilir:**
1. Her writer'ın yazdığı tüm kullanıcı kaynaklı alanı, **izin verilen karakter kümesiyle** (ör.
   `^[A-Za-z0-9_-]{1,32}$`, `display_name` için harf/rakam/boşluk) doğrulayan ortak bir `validateIdent`
   fonksiyonundan geçirin; uymazsa 400 dönün. `voicemail.go`'daki `safePanelCode`'u tüm yazma yollarına taşıyın.
2. Ayrıca satır sonu / `[` / `;` / `=` karakterlerini reddedin (config dosyası ayraçları).
3. Mümkünse Asterisk realtime (res_config / ARA) kullanarak düz dosya üretmekten tümüyle vazgeçin;
   bu, enjeksiyon yüzeyini kaynağında kaldırır.
4. Asterisk tarafında komut çalıştıran dialplan uygulamalarının (`System`, `Shell`, `MixMonitor` vb.)
   gerekmiyorsa `modules.conf`/izinlerle devre dışı bırakılması savunmayı derinleştirir.

---

## G-02 — AMI'ye doğrulanmamış girdi + geniş AMI yetkisi — KRİTİK

**Nerede:** `core/internal/ami/client.go` (`sendAction` alanları `"%s: %s\r\n"` ile yazıyor),
`conference.go` (`room_code`, katılımcı `code`), `install_yonetim_servisi.sh` (`read/write = system,call,originate`).

**Sorun:** AMI, satır tabanlı (`Anahtar: Değer`, CRLF) bir protokoldür. `Originate`/`ConfbridgeKick` gibi
action'lara giden `room_code`, kanal adı gibi değerler doğrulanmadan yazıldığından, bir değerin içine
satır sonu konması fazladan AMI alanı/action'ı enjekte edebilir. AMI kullanıcısına `system` yetkisi
verildiği için bu, AMI üzerinden komut çalıştırmaya açılır. `room_code` yolu login'li her kullanıcıya açık.

**Nasıl düzeltilir:**
1. AMI'ye yazılan tüm değerleri aynı `validateIdent` (G-01) ile doğrulayın; CR/LF içerenleri reddedin.
2. AMI kullanıcısının yetkisini gerçekte kullanılana indirin: kod yalnızca `originate` ve ConfBridge
   action'larını kullanıyor → `read = call`, `write = originate` yeterli; **`system`'i kaldırın**.
3. AMI yalnızca `127.0.0.1`'e bağlı (iyi); bunu koruyun.

---

## G-03 — Login'de `panel_code` doğrulanmıyor — YÜKSEK

**Nerede:** `core/internal/api/login.go` (panel_code oturuma olduğu gibi yazılıyor, panels tablosuna
bakılmıyor; `RNVCS_ENFORCE_PANEL_LOGIN` varsayılan kapalı) + `voicemail.go` `panelAccessAllowed`
(OPERATOR'ün erişimi `sess.PanelCode == panelCode`'a dayanıyor).

**Sorun:** OPERATOR login olurken `panel_code` değerini kendisi gönderir ve doğrulanmaz. `panelAccessAllowed`
bu değere güvendiği için, bir operatör login isteğinde başka bir panelin kodunu vererek o panelin sesli
mesajlarına (içerik dahil) ve `my-panel-sip-credentials` üzerinden o panelin SIP şifresine erişebilir.

**Nasıl düzeltilir:**
1. Login'de `panel_code` verildiyse, panelin var olduğunu **ve** kullanıcının o panelde `can_login`
   yetkisi olduğunu her zaman (enforce bayrağından bağımsız) doğrulayın; uymazsa oturuma yazmayın.
2. Voicemail/SIP erişim kontrolünü oturumdaki panel yerine, istek anında `user_panel_permissions`'tan
   tekrar kontrol edin.

---

## G-04 — panel-backend API'si kimlik doğrulamasız, `0.0.0.0`'a açık — YÜKSEK

**Nerede:** `panel-backend/main.go` (`:8090`, `Access-Control-Allow-Origin: *`, hiçbir uçta token kontrolü).

**Sorun:** `/api/call/dial|answer|reject|hangup`, `/api/session`, `/api/volume` uçları kimlik
doğrulaması olmadan çalışır. Panelin ağına erişebilen herhangi biri çağrı başlatabilir, gelen çağrıyı
cevaplayıp ses köprüsünü açabilir (dinleme), oturumu düşürebilir. `/api/session` ayrıca verilen
`yonetim_servisi_url`'e istek attığından SSRF aracı olabilir.

**Nasıl düzeltilir:**
1. Servisi `127.0.0.1`'e bağlayın (panel_app aynı cihazda çalışıyor) — uzak erişim gerekmiyor.
2. Yerel bir paylaşımlı sır / token ile uçları koruyun; CORS'u `*` yerine beklenen origin'e daraltın.
3. `/api/session`'ın `yonetim_servisi_url`'ini bir izin listesiyle sınırlayın (yalnızca bilinen CORS host).
4. Panel cihazında yerel güvenlik duvarıyla `:8090`'ı dışarıya kapatın.

---

## G-05 — SIP motoru gelen INVITE/RTP kaynağını doğrulamıyor — YÜKSEK

**Nerede:** `panel-backend/internal/voip/engine.go` (`handleInvite` gelen From/RTP'yi doğrulamadan kabul eder).

**Sorun:** Panel, Asterisk dışından gelen bir INVITE'ı da ayırt etmeden çalar/cevaplar; RTP akışının
kaynağı doğrulanmaz. G-04 ile birleşince (servis dışarı açık) sahte çağrı/medya enjeksiyonu mümkün olur.

**Nasıl düzeltilir:** Gelen SIP isteklerini yalnızca yapılandırılmış Asterisk adresinden kabul edin
(kaynak IP kontrolü); SIP motorunu yalnızca localhost/known-peer'a bağlayın; G-04'teki ağ kısıtını uygulayın.

---

## G-06 — Sırların düz metin saklanması — YÜKSEK

**Nerede:** `users.sip_password`, `panels.sip_password`, `iax_trunks.secret`,
`active_panel_sessions.session_token` düz metin (DB); `core_kurulum.sh` DB şifresini `CREATE ROLE ...
PASSWORD '$DB_PASS'` ile verir (süreç argümanı/loglar) ve `/etc/odbc.ini`'yi `chmod 644` yapar
(şifre dünya-okunur); `/root/.rnvcs_db_password` düz metin.

**Sorun:** DB'ye ya da sunucuya okuma erişimi olan biri tüm SIP/IAX kimliklerini ve aktif oturum
token'larını ele geçirir → kimlik taklidi, çağrı dinleme. Token düz metin olduğundan DB sızıntısı =
oturum devralma.

**Nasıl düzeltilir:**
1. Bearer token'ları DB'ye yazmak yerine yalnızca bellek-içi tutun (zaten `SessionStore` var) ya da
   DB'ye yazılacaksa hash'leyin.
2. SIP/IAX parolalarını uygulama düzeyinde şifreleyin (ör. `RNVCS_SECRET_KEY` ile AES-GCM) ya da en
   azından DB dosya/erişim izinlerini sıkılaştırın, DB'yi yalnız localhost'ta tutun (hâlihazırda öyle).
2. `/etc/odbc.ini` için 644 yerine `asterisk` grubuna `640` verip Asterisk'i o gruba ekleyin.
3. DB rol şifresini `psql` komut argümanı yerine `\password` / `PGPASSWORD`+stdin ile verin.

---

## G-07 — Düz HTTP/SIP/RTP, CORS `*` — YÜKSEK

**Nerede:** core ve panel-backend HTTP; SIP/RTP; her iki `corsMiddleware`.

**Sorun:** Login (parola), bearer token, SIP digest ve ses trafiği ağda düz gider. CORS `*` olduğundan
tarayıcı kaynaklı herhangi bir origin API'yi çağırabilir. Ağı dinleyen biri kimlik ve çağrı içeriğini elde eder.

**Nasıl düzeltilir:** CORE API'yi TLS arkasına alın (reverse proxy / 8091 TLS); CORS'u bilinen panel/BKT
origin'lerine daraltın; mümkünse SIP/RTP için SRTP/TLS transport. İzole ağda risk görece düşük olsa da
(dokümanlarda belirtildiği gibi) federasyon/çok-sahne geldiğinde bu şart olur.

---

## G-08 — Servis `root` olarak çalışıyor — YÜKSEK (çarpan)

**Nerede:** `install_yonetim_servisi.sh` ve `infra/systemd/rnvcs-yonetim-servisi.service.txt` → `User=root`,
hiçbir sandbox direktifi yok.

**Sorun:** G-01/G-02 gibi bir açık, root hakkıyla çalıştığından doğrudan tüm makineyi verir.

**Nasıl düzeltilir:** Ayrı bir `rnvcs` sistem kullanıcısıyla çalıştırın (Asterisk conf dosyaları için
`asterisk` grubu + dar dosya izinleri). systemd sıkılaştırması ekleyin: `NoNewPrivileges=yes`,
`ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `ReadWritePaths=/etc/asterisk /var/spool/asterisk/...`,
`CapabilityBoundingSet=` (redis unit'i bunun iyi bir örneği). `asterisk -rx` çağrısı için ayrı, dar bir sudo kuralı.

---

## G-09 — `event_log.detail` JSON'unun elle üretilmesi — ORTA

**Nerede:** tüm `event_log` INSERT'leri (ör. `users.go`, `panels.go`) `detail`'i
`'{"action":"...","target_username":"'+name+'"}'` gibi string birleştirmeyle kuruyor.

**Sorun:** Kullanıcı adı/alanlar içindeki tırnak/süslü parantez JSON'u bozar; `detail` JSONB olduğundan
INSERT sessizce başarısız olabilir (çoğu çağrı `_ = h.db.Exec(...)` ile hatayı yutuyor) → denetim kaydı
kaybı. Ayrıca saldırgan sahte JSON alanları yazdırabilir.

**Nasıl düzeltilir:** `detail`'i `json.Marshal` ile üretip `pg.EscapeLiteral` ile tek parça olarak geçirin;
`event_log` yazımının hatasını en azından loglayın.

---

## G-10 — Kaba kuvvet koruması ve bilgi sızıntısı (login) — ORTA

**Nerede:** `core/internal/api/login.go`.

**Sorun:** Deneme sayısı sınırı/gecikme yok → parola deneme saldırısı serbest. Ayrıca "kullanıcı yok" ile
"şifre yanlış" aynı mesajı verse de (iyi), `enabled=false` için ayrı 403 "kullanıcı devre dışı" dönerek
kullanıcı adının varlığını doğruluyor.

**Nasıl düzeltilir:** IP/kullanıcı başına oran sınırı + üstel gecikme; başarısız login'leri `event_log`'a
yazın; devre dışı hesapta da jenerik "kullanıcı adı veya şifre hatalı" dönün.

---

## G-11 — Tüm roller tüm listeleri okuyabiliyor — ORTA

**Nerede:** `users.go` (GET), `panels.go`, `ring_groups.go`, `speed_dials.go` (`?username=` ile
başkasınınki), `iax_trunks.go`, `permissions.go` — hepsinde yalnızca `authenticate` var, rol kontrolü yok.

**Sorun:** OPERATOR; tüm kullanıcı listesi (ad, rol, SIP durumu), tüm panel/trunk tanımları ve **başka
kullanıcıların kısayolları/sayfam kutuları/arama geçmişi**ni okuyabilir. (Test planı §6.2 bu boşluğu
"netleştirilmeli" diye not etmiş.)

**Nasıl düzeltilir:** GET uçlarında da rol/sahiplik kontrolü uygulayın; `?username=` ile başkasının
verisini okumayı ADMIN/MAINTAINER'a kısıtlayın.

---

## G-12 — Voicemail `folder` doğrulanmıyor; token URL'de — ORTA

**Nerede:** `core/internal/api/voicemail.go` (`folder` query'si `filepath.Join`'e doğrudan giriyor;
`/api/voicemail/audio` token'ı query param olarak kabul ediyor).

**Sorun:** `panel_code` ve `id` doğrulanıyor ama `folder` doğrulanmıyor → `..` ile spool kökü dışına
çıkıp başka dosya okuma/silme denenebilir. Token URL'de olduğundan erişim loglarına, tarayıcı
geçmişine, `Referer`'a sızabilir.

**Nasıl düzeltilir:** `folder`'ı `{INBOX, Old}` beyaz listesiyle sınırlayın; `filepath.Join` sonrası
sonucun spool kökü altında kaldığını `filepath.Rel` ile doğrulayın; `<audio>` için token yerine kısa
ömürlü, tek kullanımlık bir imza parametresi kullanın.

---

## G-13 — panel_app'te `innerHTML` ile ham veri (XSS) — ORTA

**Nerede:** `panel-app/panel_app.html` — gelen çağrı adı (`$("inc-from").textContent` güvenli) çoğu yerde
`textContent` kullanıyor, **ama** cihaz listesi `renderDev` `display_name`'i `innerHTML` ile basıyor
(`'<div class="dn">'+d.display_name+'</div>'`); BKT (`bakim_terminali.html`) ise `esc()` kullanıyor (iyi).

**Sorun:** `display_name` ve benzeri alanlar bir saldırganın etkileyebileceği kaynaklardan (SIP From,
cihaz adı) gelebilir; `innerHTML`'e ham girince kiosk tarayıcıda script çalışabilir.

**Nasıl düzeltilir:** panel_app'teki tüm dinamik `innerHTML` kullanımını `textContent` ya da BKT'deki
`esc()` benzeri bir kaçış fonksiyonundan geçirin.

---

## G-14 — Oturum yönetimi — DÜŞÜK

12 saat sabit ömür, idle timeout yok; logout dışında iptal yolu `DeleteByUserID`'e bağlı. BKT token'ı
`localStorage`'da kalıcı (XSS'te çalınır). **Öneri:** idle timeout + kısa ömür + yenileme; BKT token'ını
`sessionStorage`'a alın; mümkünse HttpOnly cookie modeline geçin.

---

## G-15 — CI/CD sıkılaştırma — ORTA

**Nerede:** `.gitea/workflows/`.
* `deploy-production.yml` hâlâ repoda, ama `CICD_TASARIMI.md` "bilinçli olarak kaldırıldı" diyor → belge/kod
  tutarsızlığı; `PROD_CORE_HOST` secret'ı varsa üretime otomatik yol açık kalır. Belirsizliği giderin
  (dosyayı gerçekten kaldırın ya da belgeyi düzeltin).
* `StrictHostKeyChecking=accept-new` + her çalışmada `ssh-keyscan` → TOFU, MITM'e açık. Host anahtarını
  secret olarak sabitleyin.
* `actions/checkout@v4`, `setup-go@v5` gibi mutable tag'ler; SHA'ya sabitleyin.
* `release.yml`'de smoke test `... || true` ile çalışıyor → başarısız olsa bile pipeline yeşil, yorum
  "FAIL ederse durur" diyor ama durmaz. `|| true`'yu kaldırın.
* `go-version: "1.23"` ama `go.mod` `go 1.21`; bilinçli değilse hizalayın.

---

## G-16 — İlk admin şifresi — DÜŞÜK

`create-admin <user> <pass>` şifreyi komut satırı argümanı olarak alır (`ps`/shell history'de görünür);
install script üretilen admin şifresini stdout'a basar (log'a düşer). **Öneri:** şifreyi stdin/env ile
alın; ilk login'de zorunlu değişim.

---

## G-17 — `EscapeLiteral` tek tırnağa dayanıyor — DÜŞÜK

`pg.EscapeLiteral` yalnızca `'` → `''` yapar. PostgreSQL varsayılanında (`standard_conforming_strings=on`)
bu güvenlidir, ancak bu ayar kapalı bir ortamda ters eğik çizgi kaçışıyla korumadan çıkılabilir; ayrıca
desen "bir alanda EscapeLiteral çağırmayı unutma" hatasına açık (test planı §6.2 de bunu not etmiş).
**Öneri:** Uzun vadede parametreli sorgulara geçin (psql yerine `database/sql` + `lib/pq`/`pgx`, ya da
`psql -v` bağlama); kısa vadede `EscapeLiteral`'ı ters eğik çizgiyi de ele alacak biçimde sağlamlaştırın
ve her girişte zorunlu kılan bir sarmalayıcı tip kullanın.

---

## Olumlu bulgular (doğru yapılanlar)

* Parola hash'i PBKDF2-HMAC-SHA256, 210k iterasyon, rastgele salt, `ConstantTimeCompare` — sağlam.
* SIP şifresi tarayıcıya hiç gitmiyor; panel-backend sunucu-sunucu çekiyor.
* AMI yalnızca `127.0.0.1`'e bağlı.
* Voicemail okuma yolunda path-traversal koruması (`safePanelCode/safeMsgID`) var ve test edilmiş.
* BKT (`bakim_terminali.html`) çıktı kaçışı (`esc()`) kullanıyor.
* Migration idempotency CI'da iki-kez-uygula testiyle doğrulanıyor.
* Kullanıcı silme/devre dışı bırakma aktif oturumu anında düşürüyor; denetim kaydı (event_log) kullanıcı
  silinse de korunuyor (FK NULL'lanıyor, cascade yok).

---

## Önerilen öncelik sırası

1. **G-01, G-02** (KRİTİK): tüm Asterisk-config ve AMI yazma yollarına ortak girdi doğrulaması; AMI
   yetkisini daraltma. Tek bir `validateIdent` + beyaz liste ile ikisi birden büyük ölçüde kapanır.
2. **G-04, G-03, G-06, G-08** (YÜKSEK): panel-backend'i localhost+token'a al; login'de panel_code doğrula;
   token'ı DB'den kaldır / parolaları şifrele; servisi root'tan çıkar + systemd sandbox.
3. **G-11, G-12, G-09, G-13** (ORTA): GET uçlarına yetki; folder beyaz listesi; event_log JSON'u Marshal;
   panel_app innerHTML kaçışı.
4. **G-07, G-15, G-10, G-14, G-16, G-17** (sıkılaştırma): TLS, CI düzeltmeleri, oran sınırı, parametreli
   sorgulara geçiş yol haritası.
