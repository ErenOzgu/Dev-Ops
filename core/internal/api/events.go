package api

import (
	"net/http"
	"strconv"
)

type eventItem struct {
	ID        int    `json:"id"`
	Ts        string `json:"ts"`
	EventType string `json:"event_type"`
	PanelCode string `json:"panel_code"`
	Username  string `json:"username"`
	Peer      string `json:"peer"`
	DurationS int    `json:"duration_s"`
	Detail    string `json:"detail"`
}

// Events — GET: son olay kayıtlarını (event_log) listeler; Bölüm 10.5 yetki
// matrisine göre ADMIN/MAINTAINER hepsini görür, OPERATOR sadece kendi
// panelleriyle ilgili olayları görür. ?limit= ile sınırlanabilir (varsayılan 100).
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "sadece GET")
		return
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	q := "SELECT e.id, e.ts::text, e.event_type, COALESCE(p.panel_code,''), COALESCE(u.username,''), " +
		"COALESCE(e.peer,''), COALESCE(e.duration_s,0), COALESCE(e.detail::text,'') " +
		"FROM event_log e LEFT JOIN panels p ON p.id = e.panel_id LEFT JOIN users u ON u.id = e.user_id"

	// OPERATOR: sadece kendi izinli olduğu panellerin olaylarını görür.
	if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
		q += " WHERE e.panel_id IN (SELECT panel_id FROM user_panel_permissions WHERE user_id=" + strconv.Itoa(sess.UserID) + ")" +
			" OR e.user_id=" + strconv.Itoa(sess.UserID)
	}
	q += " ORDER BY e.ts DESC LIMIT " + strconv.Itoa(limit)

	rows, err := h.db.Query(q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "olay kayıtları okunamadı: "+err.Error())
		return
	}
	var list []eventItem
	for _, rr := range rows {
		if len(rr) < 8 {
			continue
		}
		id, _ := strconv.Atoi(rr[0])
		dur, _ := strconv.Atoi(rr[6])
		list = append(list, eventItem{
			ID: id, Ts: rr[1], EventType: rr[2], PanelCode: rr[3], Username: rr[4], Peer: rr[5], DurationS: dur, Detail: rr[7],
		})
	}
	writeJSON(w, http.StatusOK, list)
}
