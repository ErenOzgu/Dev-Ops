# RNVCS Yönetim Servisi — Sürüm Notları

## v1.6.0 — 2026-08-25 (SIP kimliği kullanıcıya döndü + Anons Sistemi FKT)

**SIP kimliği: panel-bazlı → kullanıcı-bazlı (Bölüm 10.22'nin tersine, 10.19'a dönüş)**
- panel-backend artık `/api/my-sip-credentials` çağırıyor (`/api/my-panel-sip-credentials` değil) —
  login olan kullanıcı hangi panelden girerse girsin KENDİ sip_username/sip_password'üyle
  Asterisk'e REGISTER olur. Panel numaraları artık telefon numarası değil, sadece
  panel_code/yetki etiketi.
- `panels.go`: yeni PANEL tipi kayıtlar artık SIP register OLMUYOR (sadece sesli mesaj kutusu).
- `internal/pjsip/writer.go`: `AppendEndpoint` artık İDEMPOTENT (işaretli blok deseniyle
  eski kaydı silip yenisini yazıyor — tekrar tekrar çağrılırsa artık yinelenen blok oluşmuyor).
  Yeni `RemoveEndpoint` — kullanıcı silindiğinde SIP bloğu da temizleniyor (`users.go`).
- BKT'deki panel/senkron ipucu metinleri yeni modele göre güncellendi.

**Anons Sistemi FKT (`5DEAZNSİDA44300010100_Anons_Sistemi_FKT_Prosedürü_v1.1.docx`, Rev.1) — karşılanan maddeler:**
- **`voip_goster`**: `/usr/local/bin/voip_goster` artık `install_yonetim_servisi.sh` tarafından
  otomatik kuruluyor (`pjsip show contacts` + `pjsip show endpoints`).
- **Interkom / IP Horn provizyonu (madde 2)**: `panels.device_type` kolonu (PANEL/INTERKOM/
  IP_HORN — migration 004). INTERKOM/IP_HORN kayıtları gerçek donanım olduğundan kendi PJSIP
  endpoint'iyle register olur, voicemail fallback'siz sade `Dial()+Hangup()` extension'ı alır
  (`dialplan.AppendDeviceExtension`). BKT "Paneller" sekmesine Cihaz Tipi seçimi eklendi.
- **Ring group cihaz-agnostikliği (madde 3)**: `POST /api/ring-groups` artık `members: [{type,code}]`
  kabul ediyor (`type`: USER/PANEL/INTERKOM/IP_HORN) — bir çatal arama grubuna kullanıcıların
  yanı sıra Interkom/IP Horn da eklenebilir.
- **IP Horn butonu (madde 5)**: `speed_dials.target_type` artık INTERKOM/IP_HORN/CONFERENCE de
  kabul ediyor — ek şema değişikliği gerekmedi.
- **Konferans (madde 4)**: yeni `POST /api/conference` (`internal/api/conference.go`) — AMI
  (`internal/ami`, sıfır bağımlılıklı) üzerinden `Originate` ile katılımcıları çağırıp
  `internal/confbridge` ile yazılan dinamik `ConfBridge()` odasına bağlıyor. AMI SADECE
  localhost'a bind edilir (`RNVCS_AMI_ADDR/USER/SECRET` env değişkenleri, `install_yonetim_servisi.sh`
  `manager.conf`'u ve varsayılan `confbridge.conf` profillerini otomatik kuruyor). Oturumlar
  `conference_sessions` tablosunda tutuluyor (migration 004).

Gerekli manuel adım: `core_migration_004_annon_devices.sql`'i mevcut CORE'a elle uygula
(core_migrate.sh 003/004'ü otomatik aramıyor — bkz. dosyanın kendi başlığı).


Bundan sonra her zip bir sürüm numarasıyla etiketlenecek (`VERSION` dosyası +
zip dosya adı, ör. `rnvcs-yonetim-servisi-v1.3.0.zip`). Bakım Terminali'nin
başlığında da çalışan sürüm görünür (`GET /api/version`) — hangi zip'in
gerçekten deploy edildiği artık dosya tarihine bakmaya gerek kalmadan tek
bakışta anlaşılıyor.

## v1.4.1 — 2026-07-24 (CDR/arama kayıtları düzeltmesi)

- **DÜZELTME:** Arama kayıtları (CDR) PostgreSQL'e yazılamıyordu — `end`
  PostgreSQL'de rezerve kelime olduğundan `cdr_adaptive_odbc` INSERT'ü sözdizim
  hatası veriyordu. CDR bitiş-zamanı kolonu `enddate` olarak standartlaştırıldı;
  `/api/call-records` sorgusu da `enddate` kullanacak şekilde güncellendi.
  - Çalışan sunucuda gereken tek elle adım: `ALTER TABLE asterisk_cdr DROP COLUMN "end";`
    (yeni `enddate` kolonu ve `alias end => enddate` demo-fix script'iyle zaten eklendi).
  - Yeni kurulumlar için kalıcı: `core_migration_001_init.sql` artık `enddate`
    kolonu oluşturuyor, `core_kurulum.sh` cdr_adaptive_odbc.conf'a `alias end => enddate` yazıyor.
- Not: Panel voicemail modülü (`app_voicemail.so`) Debian/Ubuntu'da ayrı gelip
  ODBC ikiziyle çakışıyordu; çözüm sunucu tarafında `modules.conf`'a
  `noload => app_voicemail_odbc.so` (bu bir yazılım sürümü değişikliği değil).

## v1.4.0 — 2026-07-23 (Bölüm 10.22 — Panel-SIP modeli + Sesli Mesaj + Arama Kayıtları)

Bu sürüm mimari bir dönüş içerir: **numara artık kullanıcının değil PANELİN**
(örn. komutan paneli 1001, vardiya astsubayı paneli 1005). O panele kim
oturum açarsa, panelin numarası arandığında arama onun önüne düşer.

- **Panel-SIP modeli:** Panel backend login'de artık kullanıcının değil,
  oturum açtığı panelin SIP hesabıyla REGISTER oluyor (yeni uç:
  `GET /api/my-panel-sip-credentials`). Vardiya değişince (Ahmet çıkar,
  Mehmet girer) aynı numara yeni operatörün önüne düşer. Eski kullanıcı-SIP
  yolu (`/api/my-sip-credentials`) geriye dönük uyumluluk için duruyor ama
  panel akışında kullanılmıyor.
- **Panel oluşturma artık Asterisk'i hazırlıyor:** Yeni panel tanımlanınca
  otomatik olarak (a) panelin PJSIP endpoint'i (panel_code = SIP kullanıcı
  adı, `callerid` panel numarasına sabitli), (b) doğrudan-arama extension'ı,
  (c) sesli mesaj kutusu üretiliyor.
- **Sesli mesaj (voicemail):** Kimse login değilken panel numarası
  arandığında dialplan `VoiceMail()` fallback'ine düşüyor — arayan mesaj
  bırakıyor. Panele bir sonraki login olan operatöre "bu panele N mesaj
  bırakılmış, dinlemek ister misiniz?" modalı çıkıyor; mesaj panelin
  hoparlöründen çalınıyor, dinlendikten sonra silinebiliyor. Yeni uçlar:
  `GET /api/voicemail`, `GET /api/voicemail/audio`, `DELETE /api/voicemail`.
- **Arama kayıtları (kim kimi ne zaman aradı):** BKT'ye yeni "Arama
  Kayıtları" sekmesi. Asterisk CDR'ı, yeni `panel_session_history` tablosuyla
  zaman bazlı join'lenerek numaralara insan ismi ekleniyor — "1005'i Ahmet'in
  vardiyasında arayan → Ahmet" gibi. Sesli mesaja düşen aramalar işaretli.
  Yeni uç: `GET /api/call-records`.
- **`POST /api/panels/sync`:** 10.22 öncesi oluşturulmuş panelleri (SIP
  hesabı/arama tanımı yoktu) Asterisk'e hazır hale getiren onarım ucu. BKT
  Paneller sekmesindeki "Panelleri Asterisk'e Senkronla" butonu. Upgrade'den
  sonra BİR KEZ çalıştırılmalı.
- **Otomatik şema:** `panel_session_history` tablosu servis açılışında
  idempotent oluşturuluyor (Jenkins sadece yonetim-servisi zip'ini deploy
  ettiğinden ayrı migration adımı gerekmesin diye). `core_migration_003`
  dosyası parite için ayrıca var.
- Login/Logout artık `event_log`'a hem LOGIN hem LOGOUT olayı yazıyor
  (önceden LOGOUT yazılmıyordu — oturum geçmişi eksik kalıyordu).

**Bilinen sınırlamalar / notlar:**
- Panel `panel_app.html`'inde `PANEL_CODE` her panelde o panelin gerçek
  numarasına (1001, 1005, …) ayarlanmalı — kurulum-zamanı sabiti.
- Sesli mesajın tarayıcıda çalabilmesi için Asterisk `voicemail.conf`
  `[general]` bölümünde ses formatı listesinde `wav` (linear PCM) olmalı;
  yoksa API `.WAV`/`.gsm`'e düşer (bazı tarayıcılarda çalmayabilir).
- Yönetim Servisi root DEĞİL bir kullanıcı olarak çalışıyorsa, sesli mesaj
  dosyalarını okuyabilmek için `asterisk` grubuna eklenmeli (install script
  `RNVCS_SVC_USER` set edilmişse otomatik yapar).
- Saat kayması (NTP yoksa) arama kaydı join'ini teorik olarak yanlış kişiye
  denk getirebilir; CDR ve oturum geçmişi aynı CORE saatiyle yazıldığından
  pratik risk düşük.

## v1.3.0 — 2026-07-23

- **YENİ:** ADMIN yetkisindeki kullanıcılar artık Bakım Terminali'nden
  kullanıcı **silebiliyor** ve **düzenleyebiliyor** (`DELETE`/`PUT`/`PATCH
  /api/users?id=`). Kullanıcılar sekmesine "İşlemler" kolonu (Düzenle/Sil
  butonları) eklendi.
- Kendi kendini silme engellendi; silinen/rolü değiştirilen/pasife alınan
  kullanıcının aktif oturumları anında iptal ediliyor
  (`SessionStore.DeleteByUserID`).
- Silme işlemi `event_log` kayıtlarını SİLMİYOR — denetim izi korunuyor,
  sadece `user_id` NULL'a çekiliyor (cascade yok). `user_panel_permissions`,
  `active_panel_sessions`, `ring_group_members`, `speed_dials` kayıtları ise
  cascade ile siliniyor.
- CORS: `Access-Control-Allow-Methods` listesine `PUT, PATCH, DELETE`
  eklendi (önceki sürümde bu metodlar tarayıcıdan CORS preflight'ta
  reddediliyordu).
- **YENİ:** `GET /api/version` endpoint'i ve Bakım Terminali başlığında
  sürüm rozeti — bu sürümden itibaren "hangi zip çalışıyor" sorusu arayüzden
  doğrudan cevaplanabiliyor.

## v1.2.0 — 2026-07-22

- **YENİ:** `GET /api/permissions?username=&panel_code=` — Yetkiler
  sekmesindeki checkbox'lar artık gerçek DB durumunu gösteriyor (önceden her
  zaman statik "seçili" görünüyordu, bu da yanıltıcıydı).
- Kullanıcı/Panel seçimi değiştiğinde checkbox'lar otomatik güncelleniyor
  (`loadCurrentPermission()`).

## v1.1.0 — 2026-07-22

- **DÜZELTME (kök neden):** Yeni kullanıcı oluşturulduğunda (`POST
  /api/users`) artık otomatik olarak tüm aktif panellere varsayılan
  giriş+arama yetkisi (`user_panel_permissions`) tanımlanıyor. Önceki
  davranışta yeni kullanıcı hiçbir panelde login yetkisine sahip olmadan
  oluşturuluyordu; geçici çözüm olarak BKT'den yetkiyi kaldırıp tekrar
  eklemek gerekiyordu — bu artık gerekmiyor.

## v1.0.0 — önceki sürümler

- İlk sürüm: panel/kullanıcı/yetki yönetimi, SIP hesap ataması, provisioning
  endpoint'i, ring-group/speed-dial/IAX trunk yönetimi.
