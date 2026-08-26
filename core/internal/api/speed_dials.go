package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"rnvcs-yonetim-servisi/internal/pg"
)

type createSpeedDialRequest struct {
	Username    string `json:"username"` // boşsa: oturum sahibinin kendisi
	Label       string `json:"label"`
	TargetType  string `json:"target_type"`  // USER | RING_GROUP | TRUNK_REMOTE | NUMBER | PANEL
	TargetValue string `json:"target_value"` // aranacak numara/hedef
	Position    int    `json:"position"`
	ColorHint   string `json:"color_hint"`
}

type reorderSpeedDialRequest struct {
	Username string `json:"username"` // boşsa: oturum sahibi
	IDs      []int  `json:"ids"`      // yeni sıra (0..n)
}

type speedDialItem struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Label       string `json:"label"`
	TargetType  string `json:"target_type"`
	TargetValue string `json:"target_value"`
	Position    int    `json:"position"`
	ColorHint   string `json:"color_hint"`
}

func validSpeedTarget(t string) bool {
	switch t {
	case "USER", "RING_GROUP", "TRUNK_REMOTE", "NUMBER", "PANEL", "INTERKOM", "IP_HORN", "CONFERENCE":
		return true
	}
	return false
}

// SpeedDials — kısayol yönetimi (Bölüm 10.19: kısayollar KULLANICIYA bağlı).
//
//	GET    ?username=X        : listele (verilmezse oturum sahibinin kendisi)
//	POST   {username?,label,target_type,target_value,position,color_hint}
//	       : kendi kısayolunu herkes ekleyebilir; başkası için ADMIN/MAINTAINER
//	DELETE ?id=N              : kısayol sil (sahibi ya da ADMIN/MAINTAINER)
//	PUT    {username?,ids:[]} : sıralama (position=indeks) — sahibi ya da ADMIN
func (h *Handler) SpeedDials(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	isAdmin := sess.Role == "ADMIN" || sess.Role == "MAINTAINER"

	switch r.Method {
	case http.MethodGet:
		username := r.URL.Query().Get("username")
		if username == "" {
			username = sess.Username // parametre yoksa kendi kısayolların
		}
		q := "SELECT sd.id, u.username, sd.label, sd.target_type, sd.target_value, sd.position, COALESCE(sd.color_hint,'') " +
			"FROM speed_dials sd JOIN users u ON u.id = sd.user_id WHERE u.username = " + pg.EscapeLiteral(username) +
			" ORDER BY sd.position, sd.id"
		rows, err := h.db.Query(q)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "kısayollar okunamadı: "+err.Error())
			return
		}
		list := []speedDialItem{}
		for _, rr := range rows {
			if len(rr) < 7 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			pos, _ := strconv.Atoi(rr[5])
			list = append(list, speedDialItem{
				ID: id, Username: rr[1], Label: rr[2], TargetType: rr[3], TargetValue: rr[4], Position: pos, ColorHint: rr[6],
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var req createSpeedDialRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Username == "" {
			req.Username = sess.Username // kendi kısayolun
		}
		// Kendi kısayolunu herkes ekleyebilir; başka kullanıcı için ADMIN/MAINTAINER şart.
		if req.Username != sess.Username && !isAdmin {
			writeErr(w, http.StatusForbidden, "başka kullanıcı için kısayol tanımlamak ADMIN/MAINTAINER gerektirir")
			return
		}
		if req.Label == "" || req.TargetValue == "" {
			writeErr(w, http.StatusBadRequest, "label ve target_value zorunlu")
			return
		}
		if req.TargetType == "" {
			req.TargetType = "NUMBER"
		}
		if !validSpeedTarget(req.TargetType) {
			writeErr(w, http.StatusBadRequest, "target_type USER|RING_GROUP|TRUNK_REMOTE|NUMBER|PANEL|INTERKOM|IP_HORN|CONFERENCE olmalı")
			return
		}
		userRow, err := h.db.QueryRow("SELECT id FROM users WHERE username=" + pg.EscapeLiteral(req.Username))
		if err != nil || len(userRow) == 0 {
			writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
			return
		}
		// position verilmediyse (0) listenin sonuna ekle
		pos := req.Position
		if pos == 0 {
			if pr, e := h.db.QueryRow("SELECT COALESCE(MAX(position)+1,0) FROM speed_dials WHERE user_id=" + userRow[0]); e == nil && len(pr) > 0 {
				pos, _ = strconv.Atoi(pr[0])
			}
		}
		row, err := h.db.QueryRow(
			"INSERT INTO speed_dials (user_id, label, target_type, target_value, position, color_hint) VALUES (" +
				userRow[0] + "," + pg.EscapeLiteral(req.Label) + "," + pg.EscapeLiteral(req.TargetType) + "," +
				pg.EscapeLiteral(req.TargetValue) + "," + strconv.Itoa(pos) + "," + pg.EscapeLiteral(req.ColorHint) + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "kısayol oluşturulamadı: "+err.Error())
			return
		}
		newID, _ := strconv.Atoi(row[0])
		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"create_speed_dial","username":"`+req.Username+`"}`) + ")")
		writeJSON(w, http.StatusCreated, map[string]int{"id": newID})

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "geçerli bir id gerekli")
			return
		}
		// sahiplik kontrolü
		own, err := h.db.QueryRow("SELECT user_id FROM speed_dials WHERE id=" + strconv.Itoa(id))
		if err != nil || len(own) == 0 {
			writeErr(w, http.StatusNotFound, "kısayol bulunamadı")
			return
		}
		ownerID, _ := strconv.Atoi(own[0])
		if ownerID != sess.UserID && !isAdmin {
			writeErr(w, http.StatusForbidden, "bu kısayolu silme yetkiniz yok")
			return
		}
		if err := h.db.Exec("DELETE FROM speed_dials WHERE id=" + strconv.Itoa(id)); err != nil {
			writeErr(w, http.StatusInternalServerError, "kısayol silinemedi: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case http.MethodPut:
		var req reorderSpeedDialRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Username == "" {
			req.Username = sess.Username
		}
		if req.Username != sess.Username && !isAdmin {
			writeErr(w, http.StatusForbidden, "başka kullanıcının kısayollarını sıralama yetkiniz yok")
			return
		}
		urow, err := h.db.QueryRow("SELECT id FROM users WHERE username=" + pg.EscapeLiteral(req.Username))
		if err != nil || len(urow) == 0 {
			writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
			return
		}
		uid := urow[0]
		for i, id := range req.IDs {
			// yalnızca bu kullanıcıya ait satırların pozisyonunu güncelle
			_ = h.db.Exec("UPDATE speed_dials SET position=" + strconv.Itoa(i) +
				" WHERE id=" + strconv.Itoa(id) + " AND user_id=" + uid)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET, POST, PUT veya DELETE kullanın")
	}
}
