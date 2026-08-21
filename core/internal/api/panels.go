package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"rnvcs-yonetim-servisi/internal/dialplan"
	"rnvcs-yonetim-servisi/internal/pg"
	"rnvcs-yonetim-servisi/internal/pjsip"
	"rnvcs-yonetim-servisi/internal/voicemail"
)

type createPanelRequest struct {
	PanelCode   string `json:"panel_code"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location"`
}

type panelListItem struct {
	ID          int    `json:"id"`
	PanelCode   string `json:"panel_code"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location"`
	Enabled     bool   `json:"enabled"`
}

// genSipPassword, bir panel için rastgele SIP şifresi üretir (PJSIP auth
// bloğunda kullanılır). 16 hex karakter yeterli entropiyi sağlar.
func genSipPassword() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Panels — GET: panel listesi. POST: yeni panel tanımlama (ADMIN/MAINTAINER).
//
// Bölüm 10.22 (panel-SIP modeli): SIP kimliği yeniden PANELE ait. Bu yüzden
// panel oluşturmak artık Asterisk'e panelin kendi PJSIP endpoint'ini,
// doğrudan-arama extension'ını (sesli mesaj fallback'li) ve voicemail
// mailbox'ını yazar. Numara (panel_code) arandığında panel çaldırılır;
// kimse login değilse arayan mesaj bırakır. (Bölüm 10.19'da bu üretim
// KULLANICIYA taşınmıştı — 10.22 ile geri alındı; kullanıcı-SIP yolu
// internal/api/users.go'da geriye dönük uyumluluk için duruyor.)
func (h *Handler) Panels(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query("SELECT id, panel_code, display_name, COALESCE(location,''), enabled FROM panels ORDER BY panel_code")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "paneller okunamadı: "+err.Error())
			return
		}
		var list []panelListItem
		for _, rr := range rows {
			if len(rr) < 5 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			list = append(list, panelListItem{
				ID: id, PanelCode: rr[1], DisplayName: rr[2], Location: rr[3], Enabled: pgBool(rr[4]),
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
			writeErr(w, http.StatusForbidden, "panel tanımlamak için ADMIN veya MAINTAINER yetkisi gerekir (Bölüm 10.5)")
			return
		}
		var req createPanelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.PanelCode == "" {
			writeErr(w, http.StatusBadRequest, "panel_code zorunlu")
			return
		}
		sipPassword := genSipPassword()
		row, err := h.db.QueryRow(
			"INSERT INTO panels (panel_code, display_name, location, sip_password) VALUES (" +
				pg.EscapeLiteral(req.PanelCode) + "," + pg.EscapeLiteral(req.DisplayName) + "," +
				pg.EscapeLiteral(req.Location) + "," + pg.EscapeLiteral(sipPassword) + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusConflict, "panel oluşturulamadı (panel_code zaten var olabilir): "+err.Error())
			return
		}
		newID, _ := strconv.Atoi(row[0])

		// Asterisk tarafını hazırla: PJSIP endpoint + doğrudan-arama extension
		// (sesli mesaj fallback'li) + voicemail mailbox. Herhangi biri
		// başarısız olursa panel DB'de kalır ama event_log'a not düşülür;
		// sonradan /api/panels/sync ile onarılabilir.
		provisionErr := h.provisionPanelAsterisk(req.PanelCode, sipPassword, req.DisplayName)

		detail := `{"action":"create_panel","panel_code":"` + req.PanelCode + `"}`
		if provisionErr != nil {
			detail = `{"action":"create_panel_asterisk_failed","panel_code":"` + req.PanelCode + `","error":"` + provisionErr.Error() + `"}`
		}
		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(detail) + ")")

		resp := map[string]interface{}{"id": newID}
		if provisionErr != nil {
			resp["warning"] = "panel oluşturuldu ama Asterisk config yazılamadı: " + provisionErr.Error() + " (/api/panels/sync ile onarılabilir)"
		}
		writeJSON(w, http.StatusCreated, resp)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET veya POST kullanın")
	}
}

// provisionPanelAsterisk, tek bir panel için Asterisk config üçlüsünü yazar.
func (h *Handler) provisionPanelAsterisk(panelCode, sipPassword, displayName string) error {
	if err := pjsip.AppendEndpoint(panelCode, sipPassword); err != nil {
		return err
	}
	if err := dialplan.AppendPanelExtension(panelCode, 30); err != nil {
		return err
	}
	return voicemail.AppendMailbox(panelCode, displayName)
}

// SyncPanelsAsterisk — POST /api/panels/sync (ADMIN/MAINTAINER): mevcut
// panelleri Asterisk'e senkronlar. Bölüm 10.22 öncesi oluşturulmuş paneller
// (SIP şifresi NULL, PJSIP endpoint'i yok) bu uçla geriye dönük olarak
// hazır hale getirilir — 10.22 upgrade'inden SONRA bir kez çalıştırılmalı.
//
// SIP şifresi olmayan her panele şifre üretilip PJSIP endpoint + arama
// extension'ı EKLENİR (append; ikinci çalıştırmada bu paneller artık
// şifreli olduğundan tekrar eklenmez — bloat olmaz). Voicemail mailbox'ları
// ise her seferinde idempotent olarak baştan yazılır (RewriteAll).
func (h *Handler) SyncPanelsAsterisk(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "sadece POST")
		return
	}
	if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
		writeErr(w, http.StatusForbidden, "ADMIN veya MAINTAINER yetkisi gerekir")
		return
	}

	rows, err := h.db.Query("SELECT id, panel_code, display_name, COALESCE(sip_password,''), enabled FROM panels WHERE enabled = true ORDER BY panel_code")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "paneller okunamadı: "+err.Error())
		return
	}

	provisioned := 0
	total := 0
	mailboxes := map[string]string{}
	for _, rr := range rows {
		if len(rr) < 5 {
			continue
		}
		panelID, _ := strconv.Atoi(rr[0])
		panelCode := rr[1]
		displayName := rr[2]
		sipPassword := rr[3]
		total++
		mailboxes[panelCode] = displayName

		if sipPassword == "" {
			// Bu panel 10.22 öncesinden — şifre üret, DB'ye yaz, Asterisk'e ekle.
			sipPassword = genSipPassword()
			if err := h.db.Exec("UPDATE panels SET sip_password = " + pg.EscapeLiteral(sipPassword) +
				" WHERE id = " + strconv.Itoa(panelID)); err != nil {
				writeErr(w, http.StatusInternalServerError, "panel "+panelCode+" şifresi yazılamadı: "+err.Error())
				return
			}
			if err := pjsip.AppendEndpoint(panelCode, sipPassword); err != nil {
				writeErr(w, http.StatusInternalServerError, "panel "+panelCode+" PJSIP endpoint yazılamadı: "+err.Error())
				return
			}
			if err := dialplan.AppendPanelExtension(panelCode, 30); err != nil {
				writeErr(w, http.StatusInternalServerError, "panel "+panelCode+" extension yazılamadı: "+err.Error())
				return
			}
			provisioned++
		}
	}

	// Voicemail mailbox'ları idempotent olarak baştan yaz (ayrı dosya,
	// güvenle tümüyle yeniden yazılabilir — dialplan/pjsip gibi paylaşımlı değil).
	if err := voicemail.RewriteAll(mailboxes); err != nil {
		writeErr(w, http.StatusInternalServerError, "voicemail mailbox'ları yazılamadı: "+err.Error())
		return
	}

	_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
		strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"sync_panels_asterisk","provisioned":`+strconv.Itoa(provisioned)+`,"total":`+strconv.Itoa(total)+`}`) + ")")

	writeJSON(w, http.StatusOK, map[string]int{"provisioned": provisioned, "total": total})
}
