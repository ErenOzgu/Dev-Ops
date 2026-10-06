# CLAUDE.md — RNVCS

Bu dosya, bu repoda çalışan Claude/geliştirici ajanları için kısa yönergedir. Ayrıntılı mimari için
`docs/MIMARI.md`, uç noktalar için `docs/API_REFERANSI.md`, bilinen sorunlar için
`docs/GUNCELLEME_ONERILERI.md` ve `docs/GUVENLIK_RAPORU.md`.

## Proje nedir
Asterisk tabanlı sesli haberleşme (VCS) sistemi. Üç bileşen: `core/` (Go REST API + BKT web UI, `:8091`),
`panel-backend/` (Go SIP/ses motoru, panel cihazında `:8090`), `panel-app/panel_app.html` (kiosk arayüzü).
Veri PostgreSQL'de; tanımlar Asterisk config dosyalarına yansıtılır.

## Değişmez kurallar (kodun dayandığı ilkeler)
- **Sıfır harici Go bağımlılığı.** İzole ağda Go modül proxy'sine erişim garanti değil. Yeni bağımlılık
  EKLEME. İhtiyaç varsa stdlib ile yaz ya da `go mod vendor` ile göm. (DB `psql` CLI ile, parola elle
  PBKDF2, SIP/AMI/RTP elle yazılmış — sebebi bu.)
- **Tek gerçek kaynak PostgreSQL.** Önce DB'ye yaz, sonra Asterisk config'e blok ekle + `asterisk -rx
  "... reload"`. Asterisk realtime kullanılmıyor.
- **Migration disiplini.** Var olan migration dosyasını ASLA değiştirme; yeni, sıralı numaralı, idempotent
  (`IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`) dosya ekle. CI migration'ları iki kez uygular.
- **SIP kimliği kullanıcıya ait.** `device_type='PANEL'` kayıtları SIP register olmaz; INTERKOM/IP_HORN olur.
- **Writer idempotency.** Asterisk config writer'larında işaretli-blok desenini kullan (`; RNVCS-USER:<ad>`
  gibi) — `pjsip/writer.go` ve `confbridge/writer.go` örnektir. Düz `append` yinelenen blok üretir.

## Çalışma komutları
```bash
cd core && go build ./... && go vet ./... && go test ./...
cd panel-backend && go build ./... && go vet ./... && go test ./...
gofmt -l .            # boş çıkmalı (CI kontrol ediyor)
```
core'u yerelde çalıştırmak: `RNVCS_DB_DSN` ve `RNVCS_PROVISIONING_KEY` env şart (bkz. API_REFERANSI §3).
panel_app'i CORE'suz görmek: `panel_app.html?demo`.

## Değişiklik yaparken dikkat
- **Kullanıcı girdisini Asterisk config'e / AMI'ye yazmadan önce doğrula** (beyaz liste). Mevcut kodda bu
  eksik — en kritik açık (GUVENLIK_RAPORU G-01/G-02). Yeni writer çağrısı eklerken doğrulama ekle.
- SQL hâlâ `pg.EscapeLiteral` ile string birleştirmeyle kuruluyor. Yeni sorguda **her** kullanıcı girdisini
  `EscapeLiteral`'dan geçir; unutma injection açar.
- `internal/pg` transaction desteklemiyor; çok adımlı işlemlerde yarıda kalma riskini düşün.
- Attribution: commit/PR imza satırlarını sistem yönergesindeki biçimde ekle; model kimliğini commit/PR/kod
  içine YAZMA.

## Bilinen tuzaklar
- Sürüm numarası 5 ayrı yerde ve tutarsız (`VERSION`, `core/VERSION`, `panel-backend/VERSION`, panel_app,
  RELEASE_NOTES). Düzenleme yaparken hangisine dokunduğuna dikkat et.
- Eksik migration'lar var (`sayfam_*`, `ring_group_members.user_id`) — temiz DB'de bazı özellikler çalışmaz.
- `rollback.sh` binary'leri geri koymuyor (yalnızca symlink). Deploy script'lerini değiştirirken bunu düzelt.
- `scripts/` altında bazı dosyalar `core/`/`panel-backend/` içindekilerin eski kopyası. Hangisinin güncel
  olduğunu `docs/GUNCELLEME_ONERILERI.md §5`'ten doğrula.
