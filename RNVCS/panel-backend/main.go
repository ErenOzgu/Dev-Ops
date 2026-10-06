// RNVCS Panel — Go Backend İskeleti (Faz 1: Hot-Plug Ses Kartı Testi)
//
// Bu program, panelde gerçek donanım test edilirken (USB kulaklık, taktik
// mikrofon, HDMI vb. takıp çıkarırken) Bölüm 9'daki audiowatch paketini
// çalıştırır ve iki şekilde sonuç verir:
//  1. Terminale canlı [OK]/[DEĞİŞTİ] logu basar (şu an ne bağlı, ne oldu).
//  2. http://<panel-ip>:8090/api/audio-devices üzerinden JSON olarak
//     son bilinen cihaz listesini yayınlar (ileride panel_app.html'nin
//     WebSocket/gRPC yerine ilk testte basitçe fetch ile okuyabilmesi için).
//
// Çalıştırma (panelde, pipewire/wireplumber kurulu olduktan sonra):
//
//	cd rnvcs-panel-backend
//	go run .
//
// NOT: Bu, Bölüm 6'daki tam gRPC/Olric entegrasyonunun yerini tutmaz —
// donanım keşfinin gerçekten çalıştığını doğrulamak için en kısa yoldur.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"rnvcs-panel-backend/audiowatch"
	"rnvcs-panel-backend/internal/voip"
)

type deviceStore struct {
	mu      sync.RWMutex
	devices []audiowatch.AudioDevice
}

func (s *deviceStore) Set(devices []audiowatch.AudioDevice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = devices
}

func (s *deviceStore) Get() []audiowatch.AudioDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]audiowatch.AudioDevice, len(s.devices))
	copy(out, s.devices)
	return out
}

// deriveAsteriskAddr, panel_app.html'den gelen YONETIM_SERVISI_URL'nin
// host'unu kullanarak Asterisk'in SIP/UDP adresini tahmin eder (CORE
// sunucusu ile Asterisk aynı makinede çalışıyor — Bölüm 10.1 topolojisi).
// RNVCS_ASTERISK_ADDR ortam değişkeni tanımlıysa o değer öncelikli olur
// (Asterisk ayrı bir makinede/portta çalışıyorsa kullanılır).
// safePrefix, log satırlarında bearer token'ın tamamını basmamak için ilk
// n karakterini döner (teşhis amaçlı yeterli, sızıntı riski taşımaz).
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func deriveAsteriskAddr(yonetimServisiURL string) string {
	if override := os.Getenv("RNVCS_ASTERISK_ADDR"); override != "" {
		return override
	}
	host := yonetimServisiURL
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i] // portu at (8091 vb.), sadece host adı/IP kalsın
	}
	return host + ":5060"
}

type sessionRequest struct {
	YonetimServisiURL string `json:"yonetim_servisi_url"`
	Token             string `json:"token"`
}

type sipCredsResponse struct {
	SipUsername string `json:"sip_username"`
	SipPassword string `json:"sip_password"`
}

// corsMiddleware, panel_app.html'in (file:// veya ayrı bir origin'den yüklü)
// bu yerel API'ye Content-Type: application/json gibi "basit olmayan"
// header'larla istek atabilmesi için gerekli. Tarayıcı böyle isteklerden
// önce bir OPTIONS preflight isteği gönderir — bu middleware olmadan her
// handler OPTIONS'ı "yanlış method" diye 405 ile reddediyordu ve tarayıcı
// asıl POST isteğini HİÇ göndermiyordu (2026-07-22'de teşhis edildi: aynı
// CORS/preflight hatası Yönetim Servisi'nde de vardı, bkz. main.go orada).
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	store := &deviceStore{}
	sip := voip.New()

	watcher := audiowatch.NewWatcher(func(devices []audiowatch.AudioDevice) {
		store.Set(devices)
		logDeviceChange(devices)
		// TODO (Bölüm 6/9 tam entegrasyon): olricCache.Put("audio_devices", devices)
		// TODO (Bölüm 6/9 tam entegrasyon): grpcServer.PushEvent("audio_devices_changed", devices)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Println("==> audiowatch başlatılıyor (udevadm + wpctl gerekli)...")
		if err := watcher.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("!! audiowatch durdu: %v", err)
			log.Println("   Kontrol et: 'udevadm' ve 'wpctl' PATH üzerinde mi? (wireplumber paketi kurulu mu?)")
		}
	}()

	http.HandleFunc("/api/audio-devices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*") // yerel test kolaylığı; üretimde kaldırılacak
		json.NewEncoder(w).Encode(store.Get())
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// ---- Bölüm 10.23: hoparlör ses kontrolü (panel arayüzündeki aç/kıs) ----
	// Karşıdan gelen çağrı sesi aplay ile varsayılan PipeWire çıkışına
	// basıldığından, o çıkışın (default sink) sesini wpctl ile yönetmek
	// "gelen sesi kıs/aç" ihtiyacını doğrudan karşılar.
	//   GET  /api/volume                   -> {"volume":65,"muted":false}
	//   POST /api/volume {"action":"up"}   -> %5 artır (üst sınır %100)
	//   POST /api/volume {"action":"down"} -> %5 azalt
	//   POST /api/volume {"action":"mute"} -> sesi aç/kapat (toggle)
	const sinkArg = "@DEFAULT_AUDIO_SINK@"
	readVolume := func() (int, bool, error) {
		out, err := exec.Command("wpctl", "get-volume", sinkArg).CombinedOutput()
		if err != nil {
			return 0, false, fmt.Errorf("wpctl get-volume: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		// Çıktı: "Volume: 0.65" ya da "Volume: 0.65 [MUTED]"
		s := strings.TrimSpace(string(out))
		muted := strings.Contains(s, "[MUTED]")
		vol := 0
		if i := strings.Index(s, "Volume:"); i >= 0 {
			fields := strings.Fields(s[i+len("Volume:"):])
			if len(fields) > 0 {
				if f, perr := strconv.ParseFloat(fields[0], 64); perr == nil {
					vol = int(f*100 + 0.5)
				}
			}
		}
		return vol, muted, nil
	}
	writeVolumeJSON := func(w http.ResponseWriter) {
		vol, muted, err := readVolume()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"volume": vol, "muted": muted})
	}
	http.HandleFunc("/api/volume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		switch r.Method {
		case http.MethodGet:
			writeVolumeJSON(w)
		case http.MethodPost:
			var req struct {
				Action string `json:"action"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			var cmd *exec.Cmd
			switch req.Action {
			case "up":
				// üst sınır %100 (-l 1.0): patlamayı önler
				cmd = exec.Command("wpctl", "set-volume", "-l", "1.0", sinkArg, "5%+")
			case "down":
				cmd = exec.Command("wpctl", "set-volume", sinkArg, "5%-")
			case "mute":
				cmd = exec.Command("wpctl", "set-mute", sinkArg, "toggle")
			default:
				http.Error(w, "geçersiz action (up|down|mute)", http.StatusBadRequest)
				return
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("!! /api/volume %s: %v (%s)", req.Action, err, strings.TrimSpace(string(out)))
				http.Error(w, "ses ayarlanamadı (wpctl/PipeWire erişilebilir mi?)", http.StatusServiceUnavailable)
				return
			}
			writeVolumeJSON(w)
		default:
			http.Error(w, "GET veya POST kullanın", http.StatusMethodNotAllowed)
		}
	})

	// ---- Bölüm 10.19: gerçek SIP (PJSIP karşılığı) register/unregister ----
	// panel_app.html, operatör login/logout olduğunda bu uç noktaları çağırır.
	http.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		log.Printf(">> /api/session isteği alındı (%s)", r.RemoteAddr)
		if r.Method != http.MethodPost {
			log.Printf("!! /api/session: yanlış method: %s", r.Method)
			http.Error(w, "POST kullanın", http.StatusMethodNotAllowed)
			return
		}
		var req sessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" || req.YonetimServisiURL == "" {
			log.Printf("!! /api/session: geçersiz istek gövdesi (decode err=%v)", err)
			http.Error(w, "geçersiz istek gövdesi (yonetim_servisi_url ve token gerekli)", http.StatusBadRequest)
			return
		}
		log.Printf(">> /api/session: yonetim_servisi_url=%s token=%s...", req.YonetimServisiURL, safePrefix(req.Token, 8))

		// 1) Yönetim Servisi'nden bu oturumun BAĞLI OLDUĞU PANELİN SIP
		// kimlik bilgilerini çek (Bölüm 10.22: panel-SIP modeli — numara
		// artık kullanıcının değil PANELİN; 1001/1005 gibi. Kim login
		// olursa olsun panel kendi numarasıyla REGISTER olur, arama o an
		// panelde oturum açmış kişinin önüne düşer. Önceki model — Bölüm
		// 10.19 kullanıcı-SIP kimliği — /api/my-sip-credentials üzerinden
		// hâlâ mevcut ama panel akışında artık KULLANILMIYOR).
		credReq, _ := http.NewRequest(http.MethodGet, req.YonetimServisiURL+"/api/my-sip-credentials", nil)
		credReq.Header.Set("Authorization", "Bearer "+req.Token)
		credResp, err := http.DefaultClient.Do(credReq)
		if err != nil {
			log.Printf("!! /api/session: Yönetim Servisi'ne ulaşılamadı: %v", err)
			http.Error(w, "Yönetim Servisi'ne ulaşılamadı: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer credResp.Body.Close()
		body, _ := io.ReadAll(credResp.Body)
		if credResp.StatusCode == http.StatusNotFound {
			log.Printf("!! /api/session: kullanıcının SIP hesabı tanımsız (404 my-sip-credentials): %s", string(body))
			http.Error(w, "kullanıcının SIP hesabı tanımlı değil (BKT > Kullanıcılar sekmesinden atanmalı)", http.StatusNotFound)
			return
		}
		if credResp.StatusCode != http.StatusOK {
			log.Printf("!! /api/session: my-sip-credentials http %d: %s", credResp.StatusCode, string(body))
			http.Error(w, fmt.Sprintf("panel SIP kimlik bilgileri alınamadı (http %d): %s", credResp.StatusCode, string(body)), http.StatusBadGateway)
			return
		}
		var creds sipCredsResponse
		if err := json.Unmarshal(body, &creds); err != nil || creds.SipUsername == "" {
			log.Printf("!! /api/session: panel SIP kimlik bilgileri çözümlenemedi: %v body=%s", err, string(body))
			http.Error(w, "panel SIP kimlik bilgileri çözümlenemedi", http.StatusBadGateway)
			return
		}
		log.Printf(">> /api/session: kullanıcı SIP kimliği alındı: sip_username=%s", creds.SipUsername)

		// 2) Bu kimlikle Asterisk'e gerçek bir SIP REGISTER gönder (aynı kalıcı
		// soket, register sonrası gelen INVITE'ları da dinlemeye devam eder —
		// Bölüm 10.20: gerçek çağrı kabul/ret/ses köprüsü).
		asteriskAddr := deriveAsteriskAddr(req.YonetimServisiURL)
		if err := sip.Start(voip.Config{
			AsteriskAddr: asteriskAddr,
			SipUsername:  creds.SipUsername,
			SipPassword:  creds.SipPassword,
			ExpiresSec:   300,
		}); err != nil {
			log.Printf("!! SIP REGISTER başarısız (%s @ %s): %v", creds.SipUsername, asteriskAddr, err)
			http.Error(w, "SIP register başarısız: "+err.Error(), http.StatusBadGateway)
			return
		}

		log.Printf("==> SIP REGISTER başarılı: %s @ %s", creds.SipUsername, asteriskAddr)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(sip.RegStatus())
	})

	http.HandleFunc("/api/session/logout", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			http.Error(w, "POST kullanın", http.StatusMethodNotAllowed)
			return
		}
		wasActive := sip.RegStatus().Active
		sip.Stop()
		if wasActive {
			log.Println("==> SIP unregister tamamlandı (operatör logout)")
		} else {
			log.Println(">> /api/session/logout çağrıldı ama zaten aktif bir SIP kaydı yoktu (register hiç başarılı olmamış olabilir)")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/api/sip-status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(sip.RegStatus())
	})

	// ---- Bölüm 10.20: gerçek çağrı kabul/ret + ses köprüsü ----
	// panel_app.html, bunu periyodik olarak (örn. 1sn'de bir) yoklayıp
	// "state" alanı "ringing" olduğunda operatöre gelen çağrı ekranını gösterir.
	http.HandleFunc("/api/call/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(sip.CallStatusNow())
	})

	// POST /api/call/dial — panel_app.html'deki dialpad'in kullandığı uç
	// nokta. {"target":"ast"} gibi bir gövde bekler (aranacak kullanıcının
	// sip_username'i ya da tanımlı bir dialplan extension'ı). Çağrı çalma
	// süresi (30sn'e kadar) beklenmeden HEMEN döner — operatör durumu
	// GET /api/call/status ile (dialing -> active/idle) izler.
	http.HandleFunc("/api/call/dial", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			http.Error(w, "POST kullanın", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Target string `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
			http.Error(w, "geçersiz istek gövdesi (target gerekli)", http.StatusBadRequest)
			return
		}
		go func() {
			if err := sip.Dial(req.Target); err != nil {
				log.Printf("!! /api/call/dial (%s): %v", req.Target, err)
			} else {
				log.Printf("==> Giden çağrı bağlandı: %s", req.Target)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
	})

	http.HandleFunc("/api/call/answer", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			http.Error(w, "POST kullanın", http.StatusMethodNotAllowed)
			return
		}
		if err := sip.Answer(); err != nil {
			log.Printf("!! /api/call/answer: %v", err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/api/call/reject", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			http.Error(w, "POST kullanın", http.StatusMethodNotAllowed)
			return
		}
		if err := sip.Reject(); err != nil {
			log.Printf("!! /api/call/reject: %v", err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/api/call/hangup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			http.Error(w, "POST kullanın", http.StatusMethodNotAllowed)
			return
		}
		sip.HangupActive()
		w.WriteHeader(http.StatusNoContent)
	})

	addr := ":8090"
	log.Printf("==> HTTP test endpoint dinleniyor: http://0.0.0.0%s/api/audio-devices", addr)
	srv := &http.Server{Addr: addr, Handler: corsMiddleware(http.DefaultServeMux)}

	go func() {
		<-ctx.Done()
		log.Println("==> Kapatılıyor... (aktifse SIP unregister gönderiliyor)")
		sip.Stop() // panel kapanırken kayıtlı kalan bir SIP hesabı bırakmamak için best-effort unregister

		// HATA DÜZELTMESİ (2026-07-22): burada eskiden srv.Shutdown() hiç
		// çağrılmıyordu — sip.Stop() çalışsa da HTTP sunucusu asla
		// kapanmıyordu, süreç ListenAndServe() içinde sonsuza kadar takılı
		// kalıyordu. Sonuç: "systemctl --user restart" SIGTERM gönderiyor,
		// süreç ölmüyor, systemd varsayılan durdurma zaman aşımı (~90sn)
		// dolana kadar bekleyip SIGKILL atana kadar komut asılı kalıyordu.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("!! http sunucusu düzgün kapanmadı: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http sunucu hatası: %v", err)
	}
}

func logDeviceChange(devices []audiowatch.AudioDevice) {
	log.Printf("==================== SES AYGITI DURUMU (%d aygıt) ====================", len(devices))
	if len(devices) == 0 {
		log.Println("  (henüz aygıt algılanmadı — wpctl status boş döndü)")
	}
	for _, d := range devices {
		mark := "[OK]"
		log.Printf("  %s %-8s %-6s %s", mark, d.Kind, d.Transport, d.DisplayName)
	}
	log.Println("========================================================================")
}
