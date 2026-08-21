// RNVCS Yönetim Servisi (Bölüm 10.1) — Go REST API.
//
// Görevi: users/panels/user_panel_permissions tablolarını (Bölüm 10.3)
// yönetmek, operatör login'ini doğrulamak (Bölüm 10.5 yetki matrisine
// göre), ve panellerin kendi SIP kimlik bilgilerini + kısayollarını
// (speed_dials) çekebileceği bir provisioning endpoint'i sunmak.
//
// Yeni bir panel API üzerinden oluşturulduğunda (/api/panels POST),
// bu servis /etc/asterisk/pjsip_rnvcs_dynamic.conf dosyasına yeni bir
// PJSIP endpoint/auth/aor bloğu ekler ve Asterisk'e "pjsip reload"
// gönderir — böylece DB'deki panel tanımı otomatik olarak çalışan bir
// SIP hesabına dönüşür (Bölüm 10.1'deki "tek gerçek kaynak PostgreSQL"
// ilkesine uygun).
//
// SIFIR harici Go bağımlılığı: PostgreSQL'e `psql` CLI üzerinden
// (internal/pg), parola hashleme stdlib PBKDF2-HMAC-SHA256 ile
// (internal/auth) yapılır — bkz. o paketlerdeki açıklamalar.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rnvcs-yonetim-servisi/internal/api"
	"rnvcs-yonetim-servisi/internal/auth"
	"rnvcs-yonetim-servisi/internal/pg"
)

// Bakım/Kontrol Terminali (Web UI) — ADMIN/MAINTAINER'ın panel/kullanıcı/
// yetki yönetimini curl yerine bir tarayıcı üzerinden yapabilmesi için tek
// dosyalık, bağımlılıksız bir HTML/JS arayüzü. go:embed ile binary'nin
// içine gömülür — CORE sunucusunda ayrıca bir web sunucusu (nginx vb.)
// kurmaya gerek kalmaz, Yönetim Servisi'nin kendisi bu sayfayı da sunar.
//
//go:embed web/bakim_terminali.html
var bakimTerminaliHTML []byte

// version, her release'te VERSION dosyasına yazılan semantik sürüm
// numarasıdır (bkz. RELEASE_NOTES.md). go:embed ile binary'ye gömülür ki
// hangi zip'in gerçekten deploy edildiği "GET /api/version" ile ya da
// Bakım Terminali başlığındaki rozetle her zaman tek bakışta anlaşılsın —
// dosya elle kopyalanırken eski sürümün unutulup unutulmadığı sorusu bir
// daha "acaba hangi zip çalışıyor" belirsizliğine yol açmasın.
//
//go:embed VERSION
var versionRaw string

var version = strings.TrimSpace(versionRaw)

// corsMiddleware, panel_app.html (kiosk/QtWebEngine tarafında ayrı bir
// origin'de yüklü) ile Bakım Terminali'nin başka makinelerden bu API'yi
// çağırabilmesi için CORS header'larını ekler. GÜVENLİK NOTU: Access-
// Control-Allow-Origin: * kullanılıyor çünkü panellerin/istemcilerin IP'si
// önceden bilinmiyor; asıl erişim kontrolü zaten bearer token (Bölüm 10.14)
// ve provisioning key (panel-config) ile sağlanıyor — CORS burada sadece
// tarayıcı kaynaklı isteklere izin vermek için var, tek başına güvenlik
// sınırı DEĞİLDİR.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Provisioning-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	dsn := os.Getenv("RNVCS_DB_DSN")
	if dsn == "" {
		log.Fatal("RNVCS_DB_DSN ortam değişkeni tanımlı değil (örn: postgresql://rnvcs:sifre@localhost:5432/rnvcs?sslmode=disable)")
	}
	provisioningKey := os.Getenv("RNVCS_PROVISIONING_KEY")
	if provisioningKey == "" {
		log.Fatal("RNVCS_PROVISIONING_KEY ortam değişkeni tanımlı değil (panel <-> servis paylaşımlı provisioning anahtarı)")
	}

	dbConn := pg.Open(dsn)
	if err := dbConn.Ping(); err != nil {
		log.Fatalf("db bağlantısı kurulamadı: %v", err)
	}

	// Bölüm 10.22: panel oturum geçmişi tablosu. active_panel_sessions
	// yalnızca GÜNCEL oturumu tutar (kullanıcı başına tek satır, upsert) —
	// geçmişe dönük "T anında 1005 panelinde kim login'di" sorusunu
	// yanıtlayamaz. Arama kayıtlarını (CDR ⋈ oturum) doğru kişilerle
	// eşleştirmek için login/logout aralıklarını burada tutuyoruz.
	// Migration dosyası (003) da var ama Jenkins yalnızca yonetim-servisi
	// zip'ini deploy ettiğinden, tablonun kesin oluşması için burada
	// idempotent (IF NOT EXISTS) yaratılıyor.
	if err := dbConn.Exec(`CREATE TABLE IF NOT EXISTS panel_session_history (
		id BIGSERIAL PRIMARY KEY,
		user_id INT REFERENCES users(id) ON DELETE SET NULL,
		username TEXT NOT NULL,
		panel_code TEXT NOT NULL,
		login_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		logout_at TIMESTAMPTZ
	)`); err != nil {
		log.Printf("!! panel_session_history tablosu oluşturulamadı (arama kayıtlarında kullanıcı adı boş görünebilir): %v", err)
	}
	_ = dbConn.Exec(`CREATE INDEX IF NOT EXISTS idx_psh_panel_time ON panel_session_history (panel_code, login_at DESC)`)

	// Alt komut: ilk ADMIN kullanıcısını oluşturmak için.
	// Kullanım: rnvcs-yonetim-servisi create-admin <kullanici_adi> <sifre>
	if len(os.Args) >= 4 && os.Args[1] == "create-admin" {
		username := os.Args[2]
		password := os.Args[3]
		hash, err := auth.HashPassword(password)
		if err != nil {
			log.Fatalf("şifre hashlenemedi: %v", err)
		}
		row, err := dbConn.QueryRow(
			"INSERT INTO users (username, password_hash, full_name, role) VALUES (" +
				pg.EscapeLiteral(username) + "," + pg.EscapeLiteral(hash) + "," +
				pg.EscapeLiteral("İlk Yönetici") + ",'ADMIN') RETURNING id")
		if err != nil {
			log.Fatalf("admin kullanıcısı oluşturulamadı: %v", err)
		}
		fmt.Printf("ADMIN kullanıcısı oluşturuldu: id=%s username=%s\n", row[0], username)
		return
	}

	h := api.NewHandler(dbConn, provisioningKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.Healthz)
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"version":"%s"}`, version)
	})
	mux.HandleFunc("/api/login", h.Login)
	mux.HandleFunc("/api/logout", h.Logout)
	mux.HandleFunc("/api/my-sip-credentials", h.MySipCredentials)
	mux.HandleFunc("/api/my-panel-sip-credentials", h.MyPanelSipCredentials)
	mux.HandleFunc("/api/panel-config", h.PanelConfig)
	mux.HandleFunc("/api/users", h.Users)
	mux.HandleFunc("/api/users/sip", h.UserSip)
	mux.HandleFunc("/api/panels", h.Panels)
	mux.HandleFunc("/api/panels/sync", h.SyncPanelsAsterisk)
	mux.HandleFunc("/api/voicemail", h.Voicemail)
	mux.HandleFunc("/api/voicemail/audio", h.VoicemailAudio)
	mux.HandleFunc("/api/call-records", h.CallRecords)
	mux.HandleFunc("/api/permissions", h.Permissions)
	mux.HandleFunc("/api/ring-groups", h.RingGroups)
	mux.HandleFunc("/api/speed-dials", h.SpeedDials)
	mux.HandleFunc("/api/iax-trunks", h.IAXTrunks)
	mux.HandleFunc("/api/events", h.Events)
	// Bakım/Kontrol Terminali (Web UI) — kök yolda sunulur.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(bakimTerminaliHTML)
	})

	addr := ":8091"
	srv := &http.Server{Addr: addr, Handler: corsMiddleware(mux)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("RNVCS Yönetim Servisi v%s, %s üzerinde dinliyor\n", version, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("sunucu hatası: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("kapatılıyor...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
