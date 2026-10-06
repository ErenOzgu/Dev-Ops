package api

import (
	"net/http"
	"strconv"

	"rnvcs-yonetim-servisi/internal/pg"
)

type speedDial struct {
	Label       string `json:"label"`
	TargetType  string `json:"target_type"`
	TargetValue string `json:"target_value"`
	Position    int    `json:"position"`
	ColorHint   string `json:"color_hint"`
}

type panelConfigResponse struct {
	PanelCode   string      `json:"panel_code"`
	DisplayName string      `json:"display_name"`
	SIPUsername string      `json:"sip_username"`
	SIPPassword string      `json:"sip_password"`
	SpeedDials  []speedDial `json:"speed_dials"`
}

// PanelConfig — panelin kendi kimlik bilgilerini (SIP username/password)
// ve kısayollarını (speed_dials) çekmesi için kullanılır. İnsan login'i
// DEĞİLDİR; panel donanımı kendi X-Provisioning-Key'ini kullanarak
// (kurulumda bir kere verilen paylaşımlı anahtar) bunu çağırır.
//
// GÜVENLİK NOTU (MVP sınırlaması): tek bir paylaşımlı anahtar tüm
// panellerin ortak sırrı — Bölüm 10.1'de bahsedilen mTLS/panel-başına
// sertifika modeline geçiş Faz 1.1 için planlanmalı.
func (h *Handler) PanelConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "sadece GET")
		return
	}
	if r.Header.Get("X-Provisioning-Key") != h.provisioningKey {
		writeErr(w, http.StatusUnauthorized, "geçersiz provisioning anahtarı")
		return
	}
	panelCode := r.URL.Query().Get("panel_code")
	if panelCode == "" {
		writeErr(w, http.StatusBadRequest, "panel_code parametresi gerekli")
		return
	}

	row, err := h.db.QueryRow(
		"SELECT id, display_name, sip_password, enabled FROM panels WHERE panel_code = " + pg.EscapeLiteral(panelCode))
	if err != nil {
		writeErr(w, http.StatusNotFound, "panel bulunamadı")
		return
	}
	panelID, _ := strconv.Atoi(row[0])
	displayName := row[1]
	sipPassword := row[2]
	enabled := pgBool(row[3])
	if !enabled {
		writeErr(w, http.StatusForbidden, "panel devre dışı")
		return
	}

	rows, err := h.db.Query(
		"SELECT label, target_type, target_value, position, COALESCE(color_hint,'') FROM speed_dials WHERE panel_id = " +
			strconv.Itoa(panelID) + " ORDER BY position")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "kısayollar okunamadı: "+err.Error())
		return
	}

	var dials []speedDial
	for _, rr := range rows {
		if len(rr) < 5 {
			continue
		}
		pos, _ := strconv.Atoi(rr[3])
		dials = append(dials, speedDial{
			Label: rr[0], TargetType: rr[1], TargetValue: rr[2], Position: pos, ColorHint: rr[4],
		})
	}

	writeJSON(w, http.StatusOK, panelConfigResponse{
		PanelCode:   panelCode,
		DisplayName: displayName,
		SIPUsername: panelCode,
		SIPPassword: sipPassword,
		SpeedDials:  dials,
	})
}
