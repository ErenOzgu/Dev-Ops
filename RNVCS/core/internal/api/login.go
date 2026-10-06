package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"rnvcs-yonetim-servisi/internal/auth"
	"rnvcs-yonetim-servisi/internal/pg"
)

// enforcePanelLogin, panele giriş için kullanıcının o panelde can_login
// yetkisinin ZORUNLU olup olmadığını belirler.
//   - Varsayılan (env verilmemiş): AÇIK giriş — geçerli kullanıcı/şifresi olan
//     HERKES her panele girebilir (yetki matrisi login'i kısıtlamaz).
//   - RNVCS_ENFORCE_PANEL_LOGIN=1 (veya true): eski davranış — kullanıcının o
//     panelde can_login yetkisi olmalı.
func enforcePanelLogin() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("RNVCS_ENFORCE_PANEL_LOGIN")))
	return v == "1" || v == "true" || v == "yes"
}

type loginRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	PanelCode string `json:"panel_code"` // opsiyonel: bu login'i başlatan fiziksel panel (Bölüm 10.19)
}

type panelSummary struct {
	PanelCode   string `json:"panel_code"`
	DisplayName string `json:"display_name"`
	CanCall     bool   `json:"can_call"`
	CanAnons    bool   `json:"can_anons"`
	CanConfig   bool   `json:"can_config"`
}

type loginResponse struct {
	Token    string         `json:"token"`
	Username string         `json:"username"`
	Role     string         `json:"role"`
	Panels   []panelSummary `json:"panels"`
}

func pgBool(s string) bool {
	return s == "t" || s == "true"
}

// Login — Bölüm 10.4 "Panel register" akışındaki operatör login adımı.
// Kullanıcı adı/şifreyi doğrular, yetkili olduğu panelleri (Bölüm 10.5
// yetki matrisi: user_panel_permissions.can_login=true) döner, ve
// event_log'a LOGIN kaydı düşer.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "sadece POST")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
		return
	}

	row, err := h.db.QueryRow(
		"SELECT id, password_hash, role, enabled FROM users WHERE username = " + pg.EscapeLiteral(req.Username))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "kullanıcı adı veya şifre hatalı")
		return
	}
	userID, _ := strconv.Atoi(row[0])
	passwordHash := row[1]
	role := row[2]
	enabled := pgBool(row[3])

	if !enabled {
		writeErr(w, http.StatusForbidden, "kullanıcı devre dışı")
		return
	}
	ok, err := auth.VerifyPassword(req.Password, passwordHash)
	if err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "kullanıcı adı veya şifre hatalı")
		return
	}

	rows, err := h.db.Query(`
		SELECT p.panel_code, p.display_name, upp.can_call, upp.can_anons, upp.can_config
		FROM user_panel_permissions upp
		JOIN panels p ON p.id = upp.panel_id
		WHERE upp.user_id = ` + strconv.Itoa(userID) + ` AND upp.can_login = true AND p.enabled = true
		ORDER BY p.panel_code`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "yetkiler okunamadı: "+err.Error())
		return
	}

	var panels []panelSummary
	for _, rr := range rows {
		if len(rr) < 5 {
			continue
		}
		panels = append(panels, panelSummary{
			PanelCode:   rr[0],
			DisplayName: rr[1],
			CanCall:     pgBool(rr[2]),
			CanAnons:    pgBool(rr[3]),
			CanConfig:   pgBool(rr[4]),
		})
	}

	// Bölüm 10.19: bu login belirli bir panelden geliyorsa (panel_app.html
	// panel_code'unu gönderdiyse), ADMIN/MAINTAINER olmayan bir kullanıcının
	// o panelde gerçekten can_login yetkisi olduğunu doğrula.
	full := role == "ADMIN" || role == "MAINTAINER"
	if req.PanelCode != "" && !full && enforcePanelLogin() {
		found := false
		for _, p := range panels {
			if p.PanelCode == req.PanelCode {
				found = true
				break
			}
		}
		if !found {
			writeErr(w, http.StatusForbidden, "bu kullanıcının bu panelde ("+req.PanelCode+") login yetkisi yok")
			return
		}
	}

	token := h.sessions.Create(userID, req.Username, role, req.PanelCode)

	_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('LOGIN', " +
		strconv.Itoa(userID) + ", " + pg.EscapeLiteral(`{"source":"yonetim-servisi","panel_code":"`+req.PanelCode+`"}`) + ")")

	// active_panel_sessions: kalıcı iz (bkz. Anayasa 10.19) — bir sonraki
	// login aynı kullanıcı için eski satırı upsert ile üzerine yazar.
	if req.PanelCode != "" {
		_ = h.db.Exec("INSERT INTO active_panel_sessions (user_id, panel_code, session_token) VALUES (" +
			strconv.Itoa(userID) + "," + pg.EscapeLiteral(req.PanelCode) + "," + pg.EscapeLiteral(token) + ") " +
			"ON CONFLICT (user_id) DO UPDATE SET panel_code=EXCLUDED.panel_code, session_token=EXCLUDED.session_token, started_at=now()")

		// Bölüm 10.22: oturum geçmişi. Önce bu kullanıcının VE bu panelin
		// açık kalmış (logout_at IS NULL) satırlarını kapat — vardiya
		// devrinde (Ahmet çıkmadan Mehmet girerse) ya da kullanıcı başka
		// panele geçerse geçmiş tutarlı kalsın. Sonra yeni açık satır aç.
		_ = h.db.Exec("UPDATE panel_session_history SET logout_at = now() WHERE logout_at IS NULL AND (user_id = " +
			strconv.Itoa(userID) + " OR panel_code = " + pg.EscapeLiteral(req.PanelCode) + ")")
		_ = h.db.Exec("INSERT INTO panel_session_history (user_id, username, panel_code) VALUES (" +
			strconv.Itoa(userID) + "," + pg.EscapeLiteral(req.Username) + "," + pg.EscapeLiteral(req.PanelCode) + ")")
	}

	writeJSON(w, http.StatusOK, loginResponse{
		Token: token, Username: req.Username, Role: role, Panels: panels,
	})
}

// Logout — POST: mevcut oturumu sonlandırır (explicit logout). panel_app.html
// operatör çıkış yaptığında bunu çağırır ki oturum 12 saatlik expiry'yi
// beklemeden hemen geçersiz olsun ve active_panel_sessions izi temizlensin.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum zaten geçersiz")
		return
	}
	tok := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(tok, prefix) {
		h.sessions.Delete(strings.TrimPrefix(tok, prefix))
	}
	_ = h.db.Exec("DELETE FROM active_panel_sessions WHERE user_id=" + strconv.Itoa(sess.UserID))
	// Bölüm 10.22: oturum geçmişindeki açık satırı kapat (arama kayıtları
	// join'i için doğru login/logout aralığı). event_log'a da LOGOUT düş —
	// önceden hiç LOGOUT olayı yazılmıyordu.
	_ = h.db.Exec("UPDATE panel_session_history SET logout_at = now() WHERE user_id = " +
		strconv.Itoa(sess.UserID) + " AND logout_at IS NULL")
	_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('LOGOUT', " +
		strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"source":"yonetim-servisi","panel_code":"`+sess.PanelCode+`"}`) + ")")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MyPanelSipCredentials — GET: authenticate olan oturumun BAĞLI OLDUĞU
// PANELİN SIP hesap bilgilerini döner (Bölüm 10.22: panel-SIP modeli).
//
// Bölüm 10.19'daki "SIP kimliği kullanıcınındır" kararı 10.22 ile geri
// çevrildi: numara artık PANELİN (örn. komutan paneli 1001, vardiya
// astsubayı paneli 1005). Kim login olursa olsun panel kendi numarasıyla
// REGISTER olur; 1005'i arayan, o an panelde oturum açmış kişiye ulaşır.
// Kullanıcı sadece "o panele oturum açma yetkisi olan kişi"dir — arama
// kayıtlarında insan ismi, CDR ile active_panel_sessions'ın zaman bazlı
// join'inden çıkarılır (bkz. /api/call-records).
//
// Güvenlik: panelin ham SIP şifresi tarayıcıya gitmez — panel-yerel Go
// backend'i (rnvcs-panel-backend) operatörün bearer token'ı ile bunu
// sunucu-sunucu çağırır. Token'ın oturumu hangi panelden açıldıysa YALNIZCA
// o panelin kimlik bilgileri döner; oturum bir panele bağlı değilse (örn.
// BKT/tarayıcı login'i, panel_code gönderilmemiş) 404 döner.
func (h *Handler) MyPanelSipCredentials(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if sess.PanelCode == "" {
		writeErr(w, http.StatusNotFound, "bu oturum bir panele bağlı değil (login'de panel_code gönderilmemiş)")
		return
	}
	row, err := h.db.QueryRow(
		"SELECT panel_code, COALESCE(sip_password,''), enabled, display_name FROM panels WHERE panel_code = " +
			pg.EscapeLiteral(sess.PanelCode))
	if err != nil {
		writeErr(w, http.StatusNotFound, "oturumun bağlı olduğu panel ("+sess.PanelCode+") bulunamadı")
		return
	}
	if !pgBool(row[2]) {
		writeErr(w, http.StatusForbidden, "panel ("+sess.PanelCode+") devre dışı")
		return
	}
	if row[1] == "" {
		writeErr(w, http.StatusNotFound, "panel ("+sess.PanelCode+") için SIP şifresi tanımlı değil")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"sip_username": row[0], // panel_code = SIP username (pjsip writer'daki kuralla aynı)
		"sip_password": row[1],
		"display_name": row[3],
	})
}

// MySipCredentials — GET: authenticate olan kullanıcının KENDİ SIP hesap
// bilgilerini döner. Panel'in yerel Go backend'i (rnvcs-panel-backend),
// operatör login sonrası panel_app.html'den aldığı bearer token ile bunu
// çağırıp Asterisk'e gerçek SIP REGISTER atmak için kullanır — ham SIP
// şifresi tarayıcıya HİÇ gitmez, sadece panel-yerel backend<->Yönetim
// Servisi arasında (sunucu-sunucu) taşınır (Bölüm 10.19/10.20).
func (h *Handler) MySipCredentials(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	row, err := h.db.QueryRow("SELECT COALESCE(sip_username,''), COALESCE(sip_password,'') FROM users WHERE id=" + strconv.Itoa(sess.UserID))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sip bilgisi okunamadı: "+err.Error())
		return
	}
	if row[0] == "" || row[1] == "" {
		writeErr(w, http.StatusNotFound, "bu kullanıcı için henüz SIP hesabı tanımlanmamış (Bakım Terminali > Kullanıcılar'dan atanmalı)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sip_username": row[0], "sip_password": row[1]})
}
