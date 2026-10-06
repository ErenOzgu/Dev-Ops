# RNVCS — Mimari ve Çalışma Prensibi

> Bu belge, repodaki **tüm kaynak kodun satır satır incelenmesiyle** (72 dosya, 2026-10-06) hazırlandı.
> Amaç: projeye yeni katılan birinin "bu sistem ne yapıyor, parçalar birbirine nasıl bağlı,
> bir istek nereden girip nereye çıkıyor" sorularına tek yerden cevap bulabilmesi.
> Güvenlik bulguları için `GUVENLIK_RAPORU.md`, eksikler ve iyileştirme listesi için
> `GUNCELLEME_ONERILERI.md`, uç nokta listesi için `API_REFERANSI.md` dosyalarına bakın.

---

## 1. Sistem Ne Yapar?

RNVCS, **Asterisk tabanlı bir sesli haberleşme (VCS) sistemi**dir. Sahadaki dokunmatik
"muhabere panelleri", bir merkez sunucu (CORE) üzerinden birbirini arar, çatal arama
(ring group) yapar, konferans kurar, sesli mesaj bırakır; INTERKOM ve IP Horn gibi
cihazlar da aynı sisteme bağlanır. Yöneticiler kullanıcı/panel/yetki tanımlarını bir web
arayüzünden (Bakım/Kontrol Terminali, BKT) yapar.

Üç yazılım bileşeni var:

| Bileşen | Nerede çalışır | Dil / Port | Görev |
|---|---|---|---|
| **core** (`rnvcs-yonetim-servisi`) | CORE sunucusu | Go, `:8091` | REST API + BKT web UI. PostgreSQL'deki tanımları Asterisk config dosyalarına yansıtır, login/oturum yönetir. |
| **panel-backend** (`rnvcs-panel-backend`) | Her panel cihazında | Go, `:8090` | Panelin yerel "telefon motoru": gerçek SIP user agent (REGISTER/INVITE/BYE), RTP/G.711 ses köprüsü (arecord/aplay), ses seviyesi, ses aygıtı keşfi. |
| **panel-app** (`panel_app.html`) | Panel cihazında kiosk tarayıcı | Tek dosya HTML/JS | Operatör arayüzü: login, dialpad, hızlı arama, gelen/aktif çağrı ekranı, konferans, sesli mesaj, "Sayfam" kutuları, TR/AZ/EN. |

Destekleyici parçalar: `migrations/` (PostgreSQL şeması), `scripts/` (kurulum/deploy/rollback/smoke
test), `infra/systemd/` (unit dosyaları), `.gitea/workflows/` (CI/CD), `docs/`.

---

## 2. Topoloji

```
                         ┌──────────────────────────── CORE sunucusu (15.2.4.10 üretim / .11 yedek) ─────────────────────────────┐
                         │                                                                                                         │
  Panel cihazı (15.2.4.201)                                                                                                        │
 ┌──────────────────────────┐        HTTP :8091 (REST + BKT)      ┌──────────────────────────┐   psql CLI   ┌────────────────┐   │
 │ Kiosk tarayıcı           │ ───────────────────────────────────▶│ rnvcs-yonetim-servisi    │────────────▶ │ PostgreSQL     │   │
 │  panel_app.html          │                                     │  (Go, root olarak)        │              │  db: rnvcs     │   │
 │                          │  HTTP :8090 (yerel)                 │                          │              └────────────────┘   │
 │        │                 │ ──────────┐                         │  - /api/* handler'ları    │                                    │
 │        ▼                 │           │                         │  - internal/pjsip        │ dosya yazar  ┌────────────────┐   │
 │ rnvcs-panel-backend      │◀──────────┘                         │  - internal/dialplan     │─────────────▶│ /etc/asterisk/ │   │
 │  (Go, kiosk kullanıcısı) │                                     │  - internal/confbridge   │ "asterisk    │ *_rnvcs_dynamic│   │
 │  - SIP UA (UDP)          │   SIP/UDP :5060  +  RTP 10000-20000 │  - internal/iaxconf      │  -rx reload" │   .conf        │   │
 │  - RTP + arecord/aplay   │ ───────────────────────────────────▶│  - internal/voicemail    │              └───────┬────────┘   │
 │  - wpctl (PipeWire)      │                                     │  - internal/ami ─────────┼──AMI TCP 127.0.0.1:5038──┐       │
 └──────────────────────────┘                                     │  - internal/confwatch    │                          ▼       │
                                                                  └──────────────────────────┘              ┌────────────────┐   │
  INTERKOM / IP Horn (kendi SIP firmware'i) ── SIP/RTP ─────────────────────────────────────────────────────▶│ Asterisk 22    │   │
                                                                                                             │ PJSIP, IAX2,   │   │
  Uzak RNVCS sahası ── IAX2 :4569 (trunk, henüz dialplan'i yok) ───────────────────────────────────────────▶│ ConfBridge, VM │   │
                                                                                                             └───────┬────────┘   │
                                                                                                    ODBC (CDR) ───────┘           │
                                                                                                    → asterisk_cdr tablosu       │
                         │  Redis kurulu ama HİÇBİR bileşen kullanmıyor (bkz. GUNCELLEME_ONERILERI §7)                           │
                         └─────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

Önemli tasarım kararları (kodun yorumlarından ve davranışından çıkarıldı):

* **"Tek gerçek kaynak PostgreSQL"**: Panel, kullanıcı, SIP hesabı, ring group, trunk tanımı
  önce DB'ye yazılır; sonra ilgili Asterisk config dosyasına blok eklenir ve `asterisk -rx "... reload"`
  çağrılır. Asterisk realtime (res_config) **kullanılmıyor**, dosya üretimi yapılıyor.
* **Sıfır harici Go bağımlılığı**: Go modül proxy'sine erişimin garanti olmadığı izole ağ nedeniyle
  her iki Go modülü de sadece stdlib kullanır. Sonuçları: PostgreSQL'e `psql` alt süreciyle erişim
  (`internal/pg`), parola hash'i için elle yazılmış PBKDF2 (`internal/auth`), elle yazılmış
  AMI istemcisi, elle yazılmış SIP/RTP/G.711 yığını.
* **SIP kimliği KULLANICIYA ait** (v1.6.0 ile 10.19 modeline dönüldü): Operatör hangi panelden
  login olursa olsun panel-backend, operatörün `users.sip_username/sip_password`'üyle REGISTER olur.
  `device_type='PANEL'` kayıtları SIP register olmaz; sadece yetki etiketi + sesli mesaj kutusudur.
  INTERKOM/IP_HORN ise gerçek donanım olduğu için kendi PJSIP endpoint'ini alır.
* **Oturumlar bellek-içi**: Bearer token'lar `auth.SessionStore` map'inde tutulur; servis restart
  olunca herkes yeniden login olur. 12 saat sabit ömür, kullanıcı başına tek aktif oturum.

---

## 3. core — Yönetim Servisi (`core/`)

### 3.1 Başlangıç (`core/main.go`)

1. Zorunlu env: `RNVCS_DB_DSN` (libpq URI), `RNVCS_PROVISIONING_KEY`. Yoksa `log.Fatal`.
2. Opsiyonel env: `RNVCS_AMI_ADDR` (vars. `127.0.0.1:5038`), `RNVCS_AMI_USER`, `RNVCS_AMI_SECRET`,
   `RNVCS_ENFORCE_PANEL_LOGIN`, `RNVCS_VOICEMAIL_DIR`.
3. `pg.Open(dsn).Ping()` → `SELECT 1` ile bağlantı testi.
4. **Açılışta şema tamamlama**: `panel_session_history` tablosu ve indeksi `CREATE ... IF NOT EXISTS`
   ile yaratılır (migration dosyası repoda yok, bkz. §6).
5. Alt komut: `rnvcs-yonetim-servisi create-admin <kullanıcı> <şifre>` → ilk ADMIN'i yaratır ve çıkar.
6. `api.NewHandler(...)`, `confwatch.Start(...)` (arka plan konferans izleyicisi), `http.ServeMux`
   ile rotalar, `corsMiddleware` (`Access-Control-Allow-Origin: *`), `:8091` dinleme.
7. `/` → `go:embed` ile gömülü `web/bakim_terminali.html`. `VERSION` dosyası da gömülür → `/api/version`.
8. SIGINT/SIGTERM → 5 sn graceful shutdown.

### 3.2 Paket haritası

| Paket | Dosya | Sorumluluk |
|---|---|---|
| `internal/pg` | `pg.go` | `psql <DSN> -tA -F '\t' -c <SQL>` çalıştırır, stdout'u TAB ile böler. `EscapeLiteral` tek tırnağı ikiler. Her sorgu **yeni bir psql süreci**dir; transaction yok. |
| `internal/auth` | `password.go` | PBKDF2-HMAC-SHA256, 210.000 iterasyon, 16 byte salt, çıktı `$pbkdf2-sha256$i=N$salt$hash`. `ConstantTimeCompare` ile doğrulama. |
| | `session.go` | `SessionStore`: 24 byte rastgele hex token → `{UserID, Username, Role, PanelCode, Expires}`. `Create` aynı kullanıcının eski oturumunu siler. |
| `internal/api` | `handler.go` | `Handler{db, sessions, provisioningKey, ami*}`, `authenticate()` (Bearer), `writeJSON/writeErr`. |
| | `login.go` | `/api/login`, `/api/logout`, `/api/my-sip-credentials`, `/api/my-panel-sip-credentials`. |
| | `users.go` | Kullanıcı CRUD + `/api/users/sip`. SIP atanınca `pjsip.AppendEndpoint` + `dialplan.AppendDirectExtension`. |
| | `panels.go` | Panel/cihaz listeleme-oluşturma, `provisionPanelAsterisk`, `/api/panels/sync`. |
| | `permissions.go` | `user_panel_permissions` okuma/upsert. |
| | `ring_groups.go` | Ring group oluşturma (üye: USER/PANEL/INTERKOM/IP_HORN) → `dialplan.AppendRingGroup`. |
| | `speed_dials.go`, `sayfam_tiles.go` | Kullanıcıya bağlı kısayollar / "Sayfam" kutuları / arama geçmişi (CRUD + sıralama). |
| | `iax_trunks.go` | IAX2 trunk tanımı → `iaxconf.AppendTrunk`. |
| | `voicemail.go` | Asterisk voicemail spool'unu (`/var/spool/asterisk/voicemail/rnvcs-vm/<panel>/INBOX`) dosya sisteminden okur, ses dosyasını stream eder, siler. Tek test dosyası burada (`voicemail_test.go`). |
| | `call_records.go` | `asterisk_cdr ⋈ panel_session_history` zaman bazlı join → "kim kimi aradı". |
| | `conference.go` | `/api/conference` (ConfBridge odası yaz + AMI Originate), `/api/conference/kick`. |
| | `events.go` | `event_log` listesi (OPERATOR filtreli). |
| | `panel_config.go` | `X-Provisioning-Key` ile panel SIP bilgisi + kısayol çekme (eski model; panel_app bunu **kullanmıyor**). |
| `internal/pjsip` | `writer.go` | `/etc/asterisk/pjsip_rnvcs_dynamic.conf`'a `; RNVCS-USER:<ad>` işaretli endpoint/auth/aor üçlüsü yazar (idempotent: eski bloğu siler). `RemoveEndpoint`. `asterisk -rx "pjsip reload"`. |
| `internal/dialplan` | `writer.go` | `/etc/asterisk/extensions_rnvcs_dynamic.conf`'a `[rnvcs-panels]` extension blokları **append** eder (idempotent DEĞİL): `AppendRingGroup`, `AppendDirectExtension`, `AppendPanelExtension` (artık çağrılmıyor), `AppendDeviceExtension`. |
| `internal/confbridge` | `writer.go` | Aynı dosyaya `; RNVCS-CONF:<oda>` işaretli `ConfBridge()` extension'ı (idempotent). |
| `internal/iaxconf` | `writer.go` | `/etc/asterisk/iax_rnvcs_dynamic.conf`'a `[trunk]` bloğu append (idempotent değil). |
| `internal/voicemail` | `writer.go` | `/etc/asterisk/voicemail_rnvcs_dynamic.conf`: `AppendMailbox` (append) ve `RewriteAll` (tüm dosyayı DB'den yeniden üretir). PIN sabit `0000`. |
| `internal/ami` | `client.go` | Ham TCP AMI istemcisi: `Login`, `Originate`, `ConfbridgeList/Kick/PlayFile`, `Events`, `ListenEvents`. Event paketlerini atlayıp `Response:` bekler. |
| `internal/confwatch` | `watch.go` | AMI event'lerini dinler; `ConfbridgeJoin/Leave` sonrası odada **tek kişi** kaldıysa `conf-onlyone` anonsunu çalar, 3 sn sonra kişiyi atar (oda kapanır). Bağlantı koparsa 5 sn sonra yeniden bağlanır. |

### 3.3 Rol ve yetki modeli

Roller: `ADMIN`, `MAINTAINER`, `OPERATOR` (`users.role`, CHECK constraint var).

| İşlem | ADMIN | MAINTAINER | OPERATOR |
|---|:-:|:-:|:-:|
| Kullanıcı oluştur / düzenle / sil, SIP hesabı ata, IAX trunk tanımla | ✔ | ✖ | ✖ |
| Panel/cihaz tanımla, panels/sync, yetki ata, ring group tanımla | ✔ | ✔ | ✖ |
| Başkası adına kısayol / sayfam kutusu ekle-sırala | ✔ | ✔ | ✖ |
| Tüm panellerin voicemail'i, tüm event/CDR | ✔ | ✔ | sadece kendi oturum paneli / izinli panelleri |
| Listeleme (users, panels, ring-groups, speed-dials?username=, permissions, trunks) | ✔ | ✔ | ✔ (kısıt yok) |
| Konferans başlat / kick | ✔ | ✔ | ✔ |

`user_panel_permissions.can_login` yalnızca `RNVCS_ENFORCE_PANEL_LOGIN=1` ise login'de kontrol edilir
(varsayılan **kapalı**). `can_call`, `can_anons`, `can_config` **hiçbir yerde uygulanmaz**; sadece login
cevabında istemciye döner (bkz. GUNCELLEME_ONERILERI §4).

### 3.4 Kritik akışlar

**Operatör login (panelden):**
```
panel_app  POST CORE/api/login {username,password,panel_code:"sida04"}
   core    users tablosundan kullanıcıyı çek → enabled? → PBKDF2 doğrula
           can_login=true panelleri listele
           (enforce açıksa ve rol OPERATOR ise panel_code listede olmalı)
           SessionStore.Create → token (eski oturum düşer)
           event_log LOGIN; active_panel_sessions upsert; panel_session_history (açık satırları kapat, yeni aç)
   ← {token, username, role, panels[]}
panel_app  POST BACKEND/api/session {yonetim_servisi_url:CORE, token}
 backend   GET CORE/api/my-sip-credentials (Bearer token) → {sip_username, sip_password}
           voip.Engine.Start: kalıcı UDP soket aç, REGISTER (401 → MD5 digest → 200), 0.7×Expires'ta yenile
   ← RegStatus
panel_app  1 sn'de bir GET BACKEND/api/call/status ; 15 sn'de bir GET CORE/api/my-panel-sip-credentials (401 → logout)
```

**Yeni kullanıcıya SIP hesabı atama (ADMIN, BKT):**
```
POST /api/users/sip {username, sip_username, sip_password}
  UPDATE users SET sip_username, sip_password (DÜZ METİN)
  pjsip.AppendEndpoint(sip_username, sip_password)   → pjsip_rnvcs_dynamic.conf + "pjsip reload"
  dialplan.AppendDirectExtension(sip_username, 30)  → extensions_rnvcs_dynamic.conf + "dialplan reload"
  event_log CONFIG_CHANGE
```

**Gelen çağrı (panel tarafı):**
```
Asterisk → INVITE (SDP) → panel-backend readLoop → handleInvite
   state!=idle → 486 Busy ; SDP yok → 488
   100 Trying, 180 Ringing (to-tag üret), state=ringing, fromDisplay=From'daki user
panel_app chkCall görür "ringing" → gelen çağrı ekranı + Web Audio ringtone
Operatör "Cevapla" → POST /api/call/answer → Engine.Answer: RTP soketi aç, 200 OK + SDP (PCMU)
   rtpSendLoop: arecord 8kHz S16 → gain → μ-law → RTP 20ms paket
   rtpRecvLoop: RTP → μ-law decode → gain → aplay
BYE gelirse → 200 OK, resetCallState (arecord/aplay kill, soket kapat)
```

**Giden çağrı:** `POST /api/call/dial {target}` → INVITE (Asterisk'e), 1xx'ler geçilir, 401/407 ise bir
kez digest ile tekrar, 200 → ACK, SDP'den uzak RTP → köprü. Çalarken vazgeçme → aynı branch ile CANCEL.

**Konferans (madde 4):**
```
POST /api/conference {participants:[{type,code}...]}   (eski istemci: participant_usernames)
  room_code yoksa "konf<uid>-<ms base36>" üret
  katılımcıların SIP hedefini çöz (USER→users.sip_username, INTERKOM/IP_HORN→panel_code)
  confbridge.AppendConferenceRoom(room_code) → exten => room,1,ConfBridge(room) + dialplan reload
  AMI Dial → her hedef için Originate Channel=PJSIP/<hedef> Context=rnvcs-panels Exten=<room>
  conference_sessions INSERT, event_log
confwatch: odada tek kişi kalınca anons + kick
```

**Arama kayıtları:** Asterisk `cdr_adaptive_odbc` → `asterisk_cdr` (bitiş kolonu `enddate`, `end` rezerve).
`/api/call-records` her CDR satırı için `panel_session_history`'de "o anda o panelde login olan kişi"yi
alt sorguyla bulur.

### 3.5 Asterisk dosya düzeni (install script'in kurduğu)

| Dosya | Kim yazar | İçerik |
|---|---|---|
| `/etc/asterisk/pjsip_transport.conf` | `core_kurulum.sh` | `[transport-udp]`, `[transport-tcp]` 0.0.0.0:5060 |
| `/etc/asterisk/pjsip_rnvcs_dynamic.conf` | core (`pjsip`) | endpoint/auth/aor blokları, `context=rnvcs-panels`, `allow=ulaw,alaw,opus`, `direct_media=no` |
| `/etc/asterisk/extensions_rnvcs_dynamic.conf` | core (`dialplan`, `confbridge`) | `[rnvcs-panels]` extension'ları (direkt arama, ring group, cihaz, konferans) |
| `/etc/asterisk/iax_rnvcs_dynamic.conf` | core (`iaxconf`) | `[trunk] type=friend ... context=rnvcs-trunks` |
| `/etc/asterisk/voicemail_rnvcs_dynamic.conf` | core (`voicemail`) | `[rnvcs-vm]` + `panel => 0000,Ad` |
| `/etc/asterisk/manager.conf` | `install_yonetim_servisi.sh` | `[rnvcs]` AMI kullanıcısı, `permit 127.0.0.1`, `read/write=system,call,originate` |
| `/etc/asterisk/confbridge.conf` | `install_yonetim_servisi.sh` | `[rnvcs_bridge]`, `[rnvcs_user]` (kodda referans edilmiyor, varsayılan profil kullanılıyor) |
| `/etc/asterisk/res_odbc.conf`, `cdr_adaptive_odbc.conf`, `/etc/odbc.ini` | `core_kurulum.sh` | DSN `rnvcs-pgsql`, `table=asterisk_cdr`, `alias end => enddate` |

Hepsi ana dosyalara `#include` ile bağlanır; dinamik dosyalar `root:asterisk 640`.

---

## 4. panel-backend (`panel-backend/`)

* **Çalışma biçimi**: kiosk kullanıcısının `systemd --user` servisi (PipeWire soketine erişim için),
  `loginctl enable-linger` ile açılışta kalkar. `:8090`'da **kimlik doğrulamasız** HTTP API.
* **`audiowatch`**: `udevadm monitor --subsystem-match=sound` dinler, her add/remove'da `wpctl status`
  çıktısını parse eder (Audio > Sinks/Sources), değişiklik varsa `OnChange` → `/api/audio-devices`.
* **`internal/voip`** — elle yazılmış SIP UA:
  * `engine.go`: tek kalıcı UDP soket; `readLoop` yanıtları Call-ID'ye göre bekleyen goroutine'e
    yönlendirir (`respWaiters`), istekleri `handleRequest`'e verir. REGISTER + digest, yenileme döngüsü,
    INVITE/BYE/CANCEL/OPTIONS işleme, `Answer/Reject/HangupActive`, durum makinesi `idle|ringing|dialing|active`.
  * `outbound.go`: `Dial` (INVITE, 1xx bekleme, digest retry, ACK, SDP parse, yarış koruması `stillOurDial`).
  * `audio.go`: `arecord`/`aplay` alt süreçleri, 20 ms/160 örnek çerçeve, `RNVCS_MIC_GAIN` (4×) /
    `RNVCS_SPEAKER_GAIN` (1×) yazılımsal kazanç.
  * `g711.go`: μ-law kodek; `rtp.go`: 12 byte RTP başlığı; `sipmsg.go`: header parse / response inşası.
* **`main.go`** uç noktaları: `/api/audio-devices`, `/healthz`, `/api/volume` (wpctl `@DEFAULT_AUDIO_SINK@`),
  `/api/session`, `/api/session/logout`, `/api/sip-status`, `/api/call/{status,dial,answer,reject,hangup}`.
  Asterisk adresi `RNVCS_ASTERISK_ADDR` ya da `yonetim_servisi_url` host'u + `:5060`.
* `fix_voip.py`: 2026-08-25'teki `respWaiters` düzeltmesini uygulayan **tek seferlik yama script'i**;
  değişiklik zaten kaynakta, dosya artık işlevsiz.

---

## 5. panel-app (`panel-app/panel_app.html`)

* Tek dosya (~180 KB; ~92 KB'ı satır 23'teki base64 arka plan görseli). Sürüm etiketi HTML içinde
  sabit "VCS Panel v3.1". `panel_app_3_1.html` birebir aynı, tek fark `CORE` varsayılanı
  (`rnvcs-core.local` vs `15.2.4.11`).
* Yapılandırma **URL query string** ile: `?panel=<kod>&core=<url>&backend=<url>&devid=<ad>&demo`.
  Varsayılanlar: `panel=sida04`, `core=http://rnvcs-core.local:8091`, `backend=http://localhost:8090`.
* Sekmeler: **VoIP** (dialpad, hızlı arama — filtre: Platformlar/USER, Merkezler/RING_GROUP, IP Horn;
  8'li sayfalama), **Telsiz** (statik görsel, işlevsiz), **Sayfam** (9×6 kutu, uzun basışla menü:
  etiket/taşıma/silme/bilgi; sunucuda `sayfam_tiles` olarak saklanır).
* Alt bar: HF/UHF/VHF mod butonları (görsel), ses, "Aktif Konferans" → konferans modalı
  (`/api/users` listesinden `has_sip_account` olanlar).
* Oturum `sessionStorage("vcs_sess")`; dil `sessionStorage("vcs_lang")`; sağ tık kapalı; ekran klavyesi.
* Sesli mesaj: 30 sn'de bir `/api/voicemail?panel_code=PANEL`; `<audio src=.../audio?...&token=...>`.

---

## 6. Veri Modeli (PostgreSQL `rnvcs`)

Repodaki migration'lar: `001_init` ve `004_annon_devices`. **002 ve 003 repoda yok**; kodun beklediği
ama hiçbir migration'da olmayan nesneler var (bkz. GUNCELLEME_ONERILERI §1).

| Tablo | Kaynak | Not |
|---|---|---|
| `users` | 001 | `password_hash` (PBKDF2), `role`, `enabled`, `sip_username`, `sip_password` (düz metin) |
| `panels` | 001 + 004 | `panel_code` UNIQUE, `sip_password` NOT NULL (PANEL tipi için üretilir ama kullanılmaz), `device_type` PANEL/INTERKOM/IP_HORN |
| `user_panel_permissions` | 001 | `can_login/can_call/can_anons/can_config` |
| `active_panel_sessions` | 001 | kullanıcı başına tek satır, **bearer token düz metin** |
| `panel_session_history` | **kod (main.go)** | login/logout aralıkları — CDR join için |
| `ring_groups`, `ring_group_members` | 001 | kod `ring_group_members.user_id` bekler → **001'de yok**, `panel_id NOT NULL` → USER üyesi eklenemez |
| `speed_dials` | 001 | `user_id` (yeni model), `panel_id` (eski, nullable) |
| `sayfam_tiles`, `sayfam_searches` | **yok** | kod `core_migration_003_sayfam_tiles.sql`'e referans verir, dosya repoda değil |
| `iax_trunks` | 001 | `secret` düz metin |
| `event_log` | 001 | `detail JSONB` — kod JSON'u string birleştirerek üretir |
| `asterisk_cdr` | 001 | `enddate` kolonu; `cdr_adaptive_odbc` yazar |
| `conference_sessions` | 004 | `ended_at` hiç set edilmiyor |

---

## 7. Kurulum ve Dağıtım Akışı

### 7.1 İlk kurulum (elle, CORE'da)
1. `scripts/core_kurulum.sh` (root): apt (Asterisk, PostgreSQL, unixODBC, Redis, golang-go), `rnvcs` DB/rol,
   şifre `/root/.rnvcs_db_password`, ODBC/CDR conf, PJSIP transport, ufw (22, 8091, 5060, 5061, 4569, 10000-20000/udp).
2. `scripts/core_migrate.sh` (root): `migrations/core_migration_*.sql` sırayla `psql -v ON_ERROR_STOP=1`, Asterisk restart.
3. `core/install_yonetim_servisi.sh` (root, `core/` içinden): `go build`, `/opt/rnvcs/rnvcs-yonetim-servisi`,
   `/etc/rnvcs/yonetim-servisi.env` (DSN, provisioning key, AMI secret üretir), `manager.conf`, `confbridge.conf`,
   dinamik conf dosyaları + `#include`'lar, systemd unit (**User=root**), ilk `admin` kullanıcısı (rastgele şifre ekrana basılır).
   > `scripts/install_yonetim_servisi.sh` bunun **eski** kopyasıdır (AMI/manager.conf adımları yok).
4. Panelde: `panel-backend/install_servis.sh` (kiosk kullanıcısı, sudo'suz): `go build`, `/opt/rnvcs/rnvcs-panel-backend`,
   `systemd --user` unit, linger.

### 7.2 CI/CD (Gitea Actions, `.gitea/workflows/`)
* `ci.yml`: her push/PR → gofmt, go vet, `go test -race` (core + panel-backend); ayrı job'da Postgres 16
  container'ına migration'lar **iki kez** uygulanır (idempotency).
* `release.yml`: `v*.*.*` tag → derle (`-ldflags -X main.Version=...` — kodda böyle bir değişken yok,
  etkisiz), `rnvcs-vX.Y.Z.tar.gz` (binary'ler, migrations, panel-app, systemd, scripts, VERSION, CHANGELOG)
  → artefakt → SSH ile yedek CORE'a `scp` → `sudo deploy_apply.sh` → `sudo rnvcs_smoke_test.sh -e || true`.
* `deploy-production.yml`: `workflow_dispatch` ile üretime dağıtım. **`CICD_TASARIMI.md` bu dosyanın
  "bilinçli olarak repodan kaldırıldığını" söyler ama dosya repoda duruyor.**
* Hedefte: `deploy_apply.sh` tar'ı `/opt/rnvcs/releases/<sürüm>/`'e açar, migration'ları uygular
  (`ON_ERROR_STOP` yok), binary'leri `/opt/rnvcs/`'a kopyalar, `current` symlink'ini çevirir, servisleri
  restart eder, son 3 sürümü tutar. `rollback.sh` yalnızca `current` symlink'ini geri alır — **binary'leri
  geri koymaz** (bkz. GUNCELLEME_ONERILERI §3).

---

## 8. Sürüm Durumu (repo HEAD, 2026-10-06)

| Yer | Değer |
|---|---|
| `VERSION` (kök) | 1.6.8 |
| `core/VERSION` (binary'ye gömülen, `/api/version`) | 1.7.0 |
| `panel-backend/VERSION` | 1.6.0 (koda gömülmez, `/api/version` yok) |
| `panel_app.html` | "v3.1" (HTML içinde sabit metin) |
| `core/RELEASE_NOTES.md` | en son v1.6.0 |
| `docs/CHANGELOG.md` | boş |
| `docs/BASELINE_IMPORT_NOTLARI.md` | sahada 2026-08-21'de çalışan: core 1.5.0, panel-backend 1.6.0 |

Tek bir sürüm numarası yok; hangi sürümün nerede çalıştığı dosyadan anlaşılamıyor.

---

## 9. Hızlı Başlangıç (geliştirici)

```bash
# derleme / test (internet gerekmez)
cd core && go build ./... && go vet ./... && go test ./...
cd ../panel-backend && go build ./... && go vet ./... && go test ./...

# core'u yerelde çalıştırmak için (PostgreSQL + psql kurulu olmalı)
export RNVCS_DB_DSN='postgresql://rnvcs:sifre@localhost:5432/rnvcs?sslmode=disable'
export RNVCS_PROVISIONING_KEY=test
./rnvcs-yonetim-servisi create-admin admin admin123
./rnvcs-yonetim-servisi            # http://localhost:8091/

# panel_app'i CORE olmadan görmek için
#   panel_app.html?demo
```

Asterisk dosyalarına yazan uçlar (`/api/users/sip`, `/api/panels`, `/api/ring-groups`, `/api/conference` ...)
yerelde `/etc/asterisk/` yoksa 500 döner — bu normaldir.
