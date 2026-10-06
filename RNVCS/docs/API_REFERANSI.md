# RNVCS — API Referansı

Kaynak koddan (`core/internal/api/*.go`, `panel-backend/main.go`) çıkarıldı, 2026-10-06.
Tüm gövdeler JSON. Hata cevabı her yerde `{"error": "<mesaj>"}`.

Kimlik doğrulama (core): `Authorization: Bearer <token>` — token `/api/login`'den alınır, 12 saat geçerli,
servis restart'ında düşer. Rol kısaltmaları: **A** = ADMIN, **M** = MAINTAINER, **O** = OPERATOR, **∗** = login'li herkes,
**–** = kimlik doğrulaması yok.

---

## 1. core — Yönetim Servisi (`:8091`)

### 1.1 Genel

| Metot | Yol | Kim | Açıklama |
|---|---|---|---|
| GET | `/` | – | Bakım/Kontrol Terminali (gömülü HTML) |
| GET | `/healthz` | – | `{"status":"ok"}` |
| GET | `/api/version` | – | `{"version":"1.7.0"}` (`core/VERSION` gömülü) |

### 1.2 Oturum

| Metot | Yol | Kim | Gövde / Parametre | Cevap |
|---|---|---|---|---|
| POST | `/api/login` | – | `{username, password, panel_code?}` | `{token, username, role, panels:[{panel_code, display_name, can_call, can_anons, can_config}]}` — 401 hatalı, 403 devre dışı / (enforce açıksa) panelde login yetkisi yok |
| POST | `/api/logout` | ∗ | — | `{"status":"ok"}`; `active_panel_sessions` siler, `panel_session_history` kapatır, LOGOUT event |
| GET | `/api/my-sip-credentials` | ∗ | — | `{sip_username, sip_password}` — **kullanıcının** SIP hesabı (panel-backend bunu kullanır). 404: hesap yok |
| GET | `/api/my-panel-sip-credentials` | ∗ | — | `{sip_username, sip_password, display_name}` — oturumun `panel_code`'una ait **panelin** SIP şifresi. 404: oturum panele bağlı değil / panel yok |

`panel_code` login'de doğrulanmaz (panels tablosuna bakılmaz); oturuma olduğu gibi yazılır.
`RNVCS_ENFORCE_PANEL_LOGIN=1|true|yes` ise OPERATOR için `can_login` kontrolü yapılır.

### 1.3 Kullanıcılar

| Metot | Yol | Kim | Gövde / Parametre | Cevap |
|---|---|---|---|---|
| GET | `/api/users` | ∗ | — | `[{id, username, full_name, role, enabled, sip_username, has_sip_account}]` |
| POST | `/api/users` | A | `{username, password, full_name, role, sip_username?, sip_password?}` | 201 `{id}`. SIP verilirse PJSIP endpoint + direkt extension yazılır; tüm aktif PANEL'lere varsayılan `can_login+can_call` verilir |
| PUT/PATCH | `/api/users?id=N` | A | `{full_name?, role?, enabled?, password?, sip_username?, sip_password?}` | `{"status":"ok"}`; `enabled=false` veya rol değişince hedefin oturumu düşer |
| DELETE | `/api/users?id=N` | A | — | `{"status":"ok"}`; kendini silemez; `event_log.user_id` NULL'lanır; PJSIP bloğu silinir (dialplan bloğu **silinmez**) |
| POST | `/api/users/sip` | A | `{username, sip_username, sip_password}` | `{"status":"ok"}` — SIP hesabı ata/güncelle + Asterisk config |

### 1.4 Paneller / Cihazlar

| Metot | Yol | Kim | Gövde / Parametre | Cevap |
|---|---|---|---|---|
| GET | `/api/panels?device_type=PANEL\|INTERKOM\|IP_HORN` | ∗ | — | `[{id, panel_code, display_name, location, enabled, device_type}]` |
| POST | `/api/panels` | A, M | `{panel_code, display_name, location, device_type?}` | 201 `{id, warning?}`. PANEL → voicemail kutusu; INTERKOM/IP_HORN → rastgele SIP şifresi + PJSIP endpoint + cihaz extension |
| POST | `/api/panels/sync` | A, M | — | `{provisioned, total}` — eksik SIP şifresi olan cihazları tamamlar, voicemail dosyasını DB'den baştan yazar |
| GET | `/api/panel-config?panel_code=X` | `X-Provisioning-Key` | — | `{panel_code, display_name, sip_username, sip_password, speed_dials[]}` — eski panel-provisioning modeli; panel_app kullanmıyor |

Panel silme/düzenleme ucu **yok**.

### 1.5 Yetkiler

| Metot | Yol | Kim | Gövde / Parametre | Cevap |
|---|---|---|---|---|
| GET | `/api/permissions?username=&panel_code=` | ∗ | — | `{username, panel_code, can_login, can_call, can_anons, can_config}` (satır yoksa hepsi false) |
| POST | `/api/permissions` | A, M | `{username, panel_code, can_login, can_call, can_anons, can_config}` | `{"status":"ok"}` (upsert) |

### 1.6 Çatal arama (ring group)

| Metot | Yol | Kim | Gövde | Cevap |
|---|---|---|---|---|
| GET | `/api/ring-groups` | ∗ | — | `[{id, group_code, display_name, strategy, timeout_sec, members:[{type, code, display_name}]}]` |
| POST | `/api/ring-groups` | A, M | `{group_code, display_name, strategy?, timeout_sec?, members:[{type: USER\|PANEL\|INTERKOM\|IP_HORN, code}]}` (≥2 üye) | 201 `{id}`; 409 üye bulunamadı / SIP hesabı yok (grup DB'de kalır!); dialplan yazılamazsa DB geri alınır |

`strategy` yalnızca DB'ye yazılır; dialplan her zaman `Dial(A&B&C, timeout)` (ringall). Silme/düzenleme yok.

### 1.7 Kısayollar ve Sayfam

| Metot | Yol | Kim | Gövde / Parametre | Cevap |
|---|---|---|---|---|
| GET | `/api/speed-dials?username=X` | ∗ | — | `[{id, username, label, target_type, target_value, position, color_hint}]` (X verilmezse kendi) |
| POST | `/api/speed-dials` | ∗ (başkası için A, M) | `{username?, label, target_type?, target_value, position?, color_hint?}` | 201 `{id}`; `target_type` ∈ USER, RING_GROUP, TRUNK_REMOTE, NUMBER, PANEL, INTERKOM, IP_HORN, CONFERENCE |
| PUT | `/api/speed-dials` | sahibi / A, M | `{username?, ids:[...]}` | `{"ok":true}` — sıralama |
| DELETE | `/api/speed-dials?id=N` | sahibi / A, M | — | `{"ok":true}` |
| GET | `/api/sayfam-tiles?username=X` | ∗ | — | `[{id, username, label, target_type, target_value, position, color_hint, icon_hint}]` |
| POST | `/api/sayfam-tiles` | ∗ (başkası için A, M) | `{username?, label, target_type?, target_value, position?, color_hint?, icon_hint?}` | 201 `{id}`; `target_type` ∈ USER, PANEL, NUMBER, INTERKOM |
| PATCH | `/api/sayfam-tiles?id=N` | sahibi / A, M | `{label?, target_type?, target_value?, color_hint?, icon_hint?}` | `{"ok":true}` |
| PUT | `/api/sayfam-tiles` | sahibi / A, M | `{username?, ids:[...]}` | `{"ok":true}` |
| DELETE | `/api/sayfam-tiles?id=N` | sahibi / A, M | — | `{"ok":true}` |
| GET | `/api/sayfam-searches?username=X` | ∗ | — | `[{query}]` (son 20) |
| POST | `/api/sayfam-searches` | ∗ (başkası için A, M) | `{username?, query}` | 201 `{"ok":true}` (upsert, `used_at` güncellenir) |

### 1.8 IAX2 trunk

| Metot | Yol | Kim | Gövde | Cevap |
|---|---|---|---|---|
| GET | `/api/iax-trunks` | ∗ | — | `[{id, trunk_name, remote_host, remote_port, prefix, enabled}]` (secret dönmez) |
| POST | `/api/iax-trunks` | A | `{trunk_name, remote_host, remote_port?, secret, prefix}` | 201 `{id}`; `iax_rnvcs_dynamic.conf`'a blok + `iax2 reload` |

`prefix` yalnızca DB'de; giden prefix dialplan'i ve gelen `[rnvcs-trunks]` context'i **yazılmıyor**.

### 1.9 Sesli mesaj

| Metot | Yol | Kim | Parametre | Cevap |
|---|---|---|---|---|
| GET | `/api/voicemail?panel_code=X` | ∗ (O: yalnız oturum paneli) | — | `[{id, caller_id, origtime, duration, folder}]` |
| DELETE | `/api/voicemail?panel_code=&id=&folder=` | aynı | — | `{removed: N}` |
| GET | `/api/voicemail/audio?panel_code=&id=&folder=&token=` | aynı (token query'de de kabul) | — | `audio/wav` veya `audio/x-gsm` akışı |

Spool kökü: `RNVCS_VOICEMAIL_DIR` ya da `/var/spool/asterisk/voicemail/rnvcs-vm`. `panel_code` ve `id`
alfanümerik kontrolünden geçer (path traversal koruması var); `folder` **kontrol edilmez** ama `filepath.Join` ile
birleştirildiği için `..` kullanımı spool kökü dışına çıkabilir (bkz. GUVENLIK_RAPORU).

### 1.10 Arama / olay kayıtları

| Metot | Yol | Kim | Parametre | Cevap |
|---|---|---|---|---|
| GET | `/api/call-records?limit=100` | ∗ (O: izinli panelleri) | limit ≤ 1000 | `[{start, answer_at, end, src_panel, src_panel_name, src_user, dst_panel, dst_panel_name, dst_user, duration_s, billsec_s, disposition, to_voicemail}]` |
| GET | `/api/events?limit=100` | ∗ (O: izinli panelleri + kendi) | limit ≤ 1000 | `[{id, ts, event_type, panel_code, username, peer, duration_s, detail}]` |

### 1.11 Konferans

| Metot | Yol | Kim | Gövde | Cevap |
|---|---|---|---|---|
| POST | `/api/conference` | ∗ | `{room_code?, display_name?, participants:[{type: USER\|INTERKOM\|IP_HORN, code}]}` ya da eski `{participant_usernames:[...]}` | `{room_code, invited:[...], failed?:[...]}`; 503 AMI yapılandırılmamış; 502 AMI bağlanamadı |
| POST | `/api/conference/kick` | ∗ | `{room_code, code?}` (`code` yok/`all` → herkes) | `{"ok":true}` |

---

## 2. panel-backend (`:8090`, **kimlik doğrulaması yok**, `0.0.0.0`'a bağlı)

| Metot | Yol | Gövde | Cevap |
|---|---|---|---|
| GET | `/healthz` | — | `ok` |
| GET | `/api/audio-devices` | — | `[{id, pipewire_node_id, display_name, kind: INPUT\|OUTPUT, transport: USB\|ANALOG\|HDMI\|BLUETOOTH, connected, detected_at}]` |
| GET | `/api/volume` | — | `{volume: 0-100, muted}` (wpctl `@DEFAULT_AUDIO_SINK@`) |
| POST | `/api/volume` | `{action: up\|down\|mute}` | aynı |
| POST | `/api/session` | `{yonetim_servisi_url, token}` | `RegStatus{active, sip_username, last_error, last_ok_at}` — CORE'dan `/api/my-sip-credentials` çekip REGISTER olur |
| POST | `/api/session/logout` | — | 204; Expires:0 ile unregister |
| GET | `/api/sip-status` | — | `RegStatus` |
| GET | `/api/call/status` | — | `{state: idle\|ringing\|dialing\|active, from, call_id, started_at, last_error}` |
| POST | `/api/call/dial` | `{target}` | 202 (asenkron; sonuç `/api/call/status`) |
| POST | `/api/call/answer` | — | 204 / 409 çalan çağrı yok |
| POST | `/api/call/reject` | — | 204 / 409 |
| POST | `/api/call/hangup` | — | 204 |

Ortam değişkenleri: `RNVCS_ASTERISK_ADDR` (vars. CORE host + `:5060`), `RNVCS_MIC_GAIN` (4.0), `RNVCS_SPEAKER_GAIN` (1.0).

---

## 3. Ortam değişkenleri (core)

| Değişken | Zorunlu | Açıklama |
|---|---|---|
| `RNVCS_DB_DSN` | ✔ | libpq URI, `psql`'e komut satırı argümanı olarak verilir |
| `RNVCS_PROVISIONING_KEY` | ✔ | `/api/panel-config` için paylaşımlı anahtar |
| `RNVCS_AMI_ADDR` | – | vars. `127.0.0.1:5038` |
| `RNVCS_AMI_USER`, `RNVCS_AMI_SECRET` | – | boşsa konferans 503, confwatch kapalı |
| `RNVCS_ENFORCE_PANEL_LOGIN` | – | `1/true/yes` → OPERATOR login'de `can_login` zorunlu |
| `RNVCS_VOICEMAIL_DIR` | – | voicemail spool kökü (test için) |
