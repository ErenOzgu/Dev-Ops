package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"rnvcs-yonetim-servisi/internal/iaxconf"
	"rnvcs-yonetim-servisi/internal/pg"
)

type createTrunkRequest struct {
	TrunkName  string `json:"trunk_name"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	Secret     string `json:"secret"`
	Prefix     string `json:"prefix"`
}

type trunkItem struct {
	ID         int    `json:"id"`
	TrunkName  string `json:"trunk_name"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	Prefix     string `json:"prefix"`
	Enabled    bool   `json:"enabled"`
}

// IAXTrunks — GET: trunk listesi (secret DÖNMEZ). POST: yeni trunk tanımlama
// — SADECE ADMIN (Bölüm 10.5 yetki matrisi: "IAX trunk tanımlama" satırı).
func (h *Handler) IAXTrunks(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query("SELECT id, trunk_name, remote_host, remote_port, prefix, enabled FROM iax_trunks ORDER BY trunk_name")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "trunk'lar okunamadı: "+err.Error())
			return
		}
		var list []trunkItem
		for _, rr := range rows {
			if len(rr) < 6 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			port, _ := strconv.Atoi(rr[3])
			list = append(list, trunkItem{
				ID: id, TrunkName: rr[1], RemoteHost: rr[2], RemotePort: port, Prefix: rr[4], Enabled: pgBool(rr[5]),
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		if sess.Role != "ADMIN" {
			writeErr(w, http.StatusForbidden, "IAX trunk tanımlamak için SADECE ADMIN yetkisi vardır (Bölüm 10.5)")
			return
		}
		var req createTrunkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.TrunkName == "" || req.RemoteHost == "" || req.Secret == "" || req.Prefix == "" {
			writeErr(w, http.StatusBadRequest, "trunk_name, remote_host, secret ve prefix zorunlu")
			return
		}
		if req.RemotePort <= 0 {
			req.RemotePort = 4569
		}
		row, err := h.db.QueryRow(
			"INSERT INTO iax_trunks (trunk_name, remote_host, remote_port, secret, prefix) VALUES (" +
				pg.EscapeLiteral(req.TrunkName) + "," + pg.EscapeLiteral(req.RemoteHost) + "," +
				strconv.Itoa(req.RemotePort) + "," + pg.EscapeLiteral(req.Secret) + "," + pg.EscapeLiteral(req.Prefix) + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusConflict, "trunk oluşturulamadı (trunk_name zaten var olabilir): "+err.Error())
			return
		}
		trunkID := row[0]
		if err := iaxconf.AppendTrunk(req.TrunkName, req.RemoteHost, req.RemotePort, req.Secret); err != nil {
			_ = h.db.Exec("DELETE FROM iax_trunks WHERE id=" + trunkID)
			writeErr(w, http.StatusInternalServerError, "trunk oluşturulamadı: iax.conf yazılamadı ("+err.Error()+"), DB kaydı geri alındı")
			return
		}
		newID, _ := strconv.Atoi(row[0])
		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"create_iax_trunk","trunk_name":"`+req.TrunkName+`"}`) + ")")
		writeJSON(w, http.StatusCreated, map[string]int{"id": newID})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET veya POST kullanın")
	}
}
