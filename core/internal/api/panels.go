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

// validDeviceType, panels.device_type için izin verilen değerleri kontrol
// eder (Anons Sistemi FKT — Interkom/IP Horn provizyonu, madde 2).
func validDeviceType(t string) bool {
	switch t {
	case "PANEL", "INTERKOM", "IP_HORN":
		return true
	}
	return false
}

type createPanelRequest struct {
	PanelCode   string `json:"panel_code"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location"`
	DeviceType  string `json:"device_type"` // PANEL (varsayılan) | INTERKOM | IP_HORN
}

type panelListItem struct {
	ID          int    `json:"id"`
	PanelCode   string `json:"panel_code"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location"`
	Enabled     bool   `json:"enabled"`
	DeviceType  string `json:"device_type"`
}

// genSipPassword, bir panel/cihaz için rastgele SIP şifresi üretir (PJSIP
// auth bloğunda kullanılır). 16 hex karakter yeterli entropiyi sağlar.
func genSipPassword() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Panels — GET: panel/cihaz listesi (?device_type= ile filtrelenebilir).
// POST: yeni panel/cihaz tanımlama (ADMIN/MAINTAINER).
//
// SIP kimliği modeli KULLANICIYA ait (bkz. internal/pjsip/writer.go) —
// bu yüzden device_type='PANEL' olan satırlar SIP register OLMUYOR, sadece
// bir yetki/tanım etiketi + sesli mesaj kutusu. device_type='INTERKOM'|
// 'IP_HORN' olan satırlar ise GERÇEK donanım olduğundan (kendi SIP
// client'ları/firmware'leri var, bir kullanıcı login olmasını beklemiyor)
// kendi PJSIP endpoint'leriyle register olur (Anons Sistemi FKT madde 2).
func (h *Handler) Panels(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		q := "SELECT id, panel_code, display_name, COALESCE(location,''), enabled, device_type FROM panels"
		if dt := r.URL.Query().Get("device_type"); dt != "" && validDeviceType(dt) {
			q += " WHERE device_type = " + pg.EscapeLiteral(dt)
		}
		q += " ORDER BY panel_code"
		rows, err := h.db.Query(q)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "paneller okunamadı: "+err.Error())
			return
		}
		var list []panelListItem
		for _, rr := range rows {
			if len(rr) < 6 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			list = append(list, panelListItem{
				ID: id, PanelCode: rr[1], DisplayName: rr[2], Location: rr[3], Enabled: pgBool(rr[4]), DeviceType: rr[5],
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
			writeErr(w, http.StatusForbidden, "panel/cihaz tanımlamak için ADMIN veya MAINTAINER yetkisi gerekir (Bölüm 10.5)")
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
		if req.DeviceType == "" {
			req.DeviceType = "PANEL" // geriye dönük uyumlu varsayılan
		}
		if !validDeviceType(req.DeviceType) {
			writeErr(w, http.StatusBadRequest, "device_type PANEL|INTERKOM|IP_HORN olmalı")
			return
		}
		sipPassword := genSipPassword()
		row, err := h.db.QueryRow(
			"INSERT INTO panels (panel_code, display_name, location, sip_password, device_type) VALUES (" +
				pg.EscapeLiteral(req.PanelCode) + "," + pg.EscapeLiteral(req.DisplayName) + "," +
				pg.EscapeLiteral(req.Location) + "," + pg.EscapeLiteral(sipPassword) + "," +
				pg.EscapeLiteral(req.DeviceType) + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusConflict, "panel/cihaz oluşturulamadı (panel_code zaten var olabilir): "+err.Error())
			return
		}
		newID, _ := strconv.Atoi(row[0])

		provisionErr := h.provisionPanelAsterisk(req.PanelCode, sipPassword, req.DisplayName, req.DeviceType)

		detail := `{"action":"create_panel","panel_code":"` + req.PanelCode + `","device_type":"` + req.DeviceType + `"}`
		if provisionErr != nil {
			detail = `{"action":"create_panel_asterisk_failed","panel_code":"` + req.PanelCode + `","error":"` + provisionErr.Error() + `"}`
		}
		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(detail) + ")")

		resp := map[string]interface{}{"id": newID}
		if provisionErr != nil {
			resp["warning"] = "kayıt oluşturuldu ama Asterisk config yazılamadı: " + provisionErr.Error() + " (/api/panels/sync ile onarılabilir)"
		}
		writeJSON(w, http.StatusCreated, resp)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET veya POST kullanın")
	}
}

// provisionPanelAsterisk, tek bir panel/cihaz için Asterisk config'ini
// hazırlar. deviceType'a göre iki farklı yol izlenir:
//
//   - PANEL: SIP register OLMUYOR (SIP kimliği kullanıcıya ait — bkz.
//     internal/pjsip/writer.go). Sadece sesli mesaj kutusu yazılır.
//   - INTERKOM / IP_HORN: gerçek donanım, kendi SIP client'ıyla register
//     olması gerekiyor — PJSIP endpoint + voicemail fallback'siz sade
//     Dial()+Hangup() extension'ı yazılır (Anons Sistemi FKT madde 2).
func (h *Handler) provisionPanelAsterisk(panelCode, sipPassword, displayName, deviceType string) error {
	switch deviceType {
	case "INTERKOM", "IP_HORN":
		if err := pjsip.AppendEndpoint(panelCode, sipPassword); err != nil {
			return err
		}
		return dialplan.AppendDeviceExtension(panelCode, 30)
	default: // PANEL
		return voicemail.AppendMailbox(panelCode, displayName)
	}
}

// SyncPanelsAsterisk — POST /api/panels/sync (ADMIN/MAINTAINER): mevcut
// panel/cihaz kayıtlarını Asterisk'e senkronlar. Sadece INTERKOM/IP_HORN
// tipindeki kayıtlar için SIP şifresi eksikse üretip PJSIP endpoint +
// cihaz extension'ı eklenir (PANEL tipi SIP register olmuyor, bkz. yukarı).
// Voicemail mailbox'ları (yalnızca PANEL tipi için anlamlı) her seferinde
// idempotent olarak baştan yazılır.
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

	rows, err := h.db.Query("SELECT id, panel_code, display_name, COALESCE(sip_password,''), enabled, device_type FROM panels WHERE enabled = true ORDER BY panel_code")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "paneller okunamadı: "+err.Error())
		return
	}

	provisioned := 0
	total := 0
	mailboxes := map[string]string{}
	for _, rr := range rows {
		if len(rr) < 6 {
			continue
		}
		panelID, _ := strconv.Atoi(rr[0])
		panelCode := rr[1]
		displayName := rr[2]
		sipPassword := rr[3]
		deviceType := rr[5]
		total++

		if deviceType == "PANEL" {
			mailboxes[panelCode] = displayName
			continue
		}

		// INTERKOM / IP_HORN — SIP şifresi eksikse tamamla.
		if sipPassword == "" {
			sipPassword = genSipPassword()
			if err := h.db.Exec("UPDATE panels SET sip_password = " + pg.EscapeLiteral(sipPassword) +
				" WHERE id = " + strconv.Itoa(panelID)); err != nil {
				writeErr(w, http.StatusInternalServerError, "cihaz "+panelCode+" şifresi yazılamadı: "+err.Error())
				return
			}
			if err := pjsip.AppendEndpoint(panelCode, sipPassword); err != nil {
				writeErr(w, http.StatusInternalServerError, "cihaz "+panelCode+" PJSIP endpoint yazılamadı: "+err.Error())
				return
			}
			if err := dialplan.AppendDeviceExtension(panelCode, 30); err != nil {
				writeErr(w, http.StatusInternalServerError, "cihaz "+panelCode+" extension yazılamadı: "+err.Error())
				return
			}
			provisioned++
		}
	}

	if err := voicemail.RewriteAll(mailboxes); err != nil {
		writeErr(w, http.StatusInternalServerError, "voicemail mailbox'ları yazılamadı: "+err.Error())
		return
	}

	_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
		strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"sync_panels_asterisk","provisioned":`+strconv.Itoa(provisioned)+`,"total":`+strconv.Itoa(total)+`}`) + ")")

	writeJSON(w, http.StatusOK, map[string]int{"provisioned": provisioned, "total": total})
}
