package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"rnvcs-yonetim-servisi/internal/dialplan"
	"rnvcs-yonetim-servisi/internal/pg"
)

type createRingGroupRequest struct {
	GroupCode   string   `json:"group_code"`
	DisplayName string   `json:"display_name"`
	Strategy    string   `json:"strategy"`
	TimeoutSec  int      `json:"timeout_sec"`
	Usernames   []string `json:"usernames"` // Bölüm 10.19: çatal arama artık KULLANICILARI çaldırır, paneli değil
}

type ringGroupItem struct {
	ID          int      `json:"id"`
	GroupCode   string   `json:"group_code"`
	DisplayName string   `json:"display_name"`
	Strategy    string   `json:"strategy"`
	TimeoutSec  int      `json:"timeout_sec"`
	Members     []string `json:"members"` // üye kullanıcı adları
}

// RingGroups — GET: çatal arama (ring group) listesi, üye kullanıcılarla
// birlikte. POST: yeni ring group tanımlama — ADMIN veya MAINTAINER
// (Bölüm 10.5). Bölüm 10.19 kararı: üyeler artık kullanıcı — Asterisk
// zaten o kullanıcı hangi panelden register olduysa oraya çevirir, bu
// yüzden dialplan üyelerin SIP hesabı (users.sip_username) ile yazılır.
func (h *Handler) RingGroups(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query("SELECT id, group_code, display_name, strategy, timeout_sec FROM ring_groups ORDER BY group_code")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "ring group'lar okunamadı: "+err.Error())
			return
		}
		var list []ringGroupItem
		for _, rr := range rows {
			if len(rr) < 5 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			timeout, _ := strconv.Atoi(rr[4])
			memberRows, _ := h.db.Query(
				"SELECT u.username FROM ring_group_members m JOIN users u ON u.id=m.user_id " +
					"WHERE m.group_id=" + rr[0] + " ORDER BY m.priority")
			var members []string
			for _, mr := range memberRows {
				if len(mr) > 0 {
					members = append(members, mr[0])
				}
			}
			list = append(list, ringGroupItem{
				ID: id, GroupCode: rr[1], DisplayName: rr[2], Strategy: rr[3], TimeoutSec: timeout, Members: members,
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
			writeErr(w, http.StatusForbidden, "çatal arama tanımlamak için ADMIN veya MAINTAINER yetkisi gerekir (Bölüm 10.5)")
			return
		}
		var req createRingGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.GroupCode == "" || len(req.Usernames) < 2 {
			writeErr(w, http.StatusBadRequest, "group_code ve en az 2 kullanıcı adı gerekli (çatal arama tanımı gereği)")
			return
		}
		if req.Strategy == "" {
			req.Strategy = "ringall"
		}
		if req.TimeoutSec <= 0 {
			req.TimeoutSec = 30
		}

		row, err := h.db.QueryRow(
			"INSERT INTO ring_groups (group_code, display_name, strategy, timeout_sec) VALUES (" +
				pg.EscapeLiteral(req.GroupCode) + "," + pg.EscapeLiteral(req.DisplayName) + "," +
				pg.EscapeLiteral(req.Strategy) + "," + strconv.Itoa(req.TimeoutSec) + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusConflict, "ring group oluşturulamadı (group_code zaten var olabilir): "+err.Error())
			return
		}
		groupID := row[0]

		var missing []string
		var noSip []string
		var sipUsernames []string
		for i, uname := range req.Usernames {
			userRow, err := h.db.QueryRow("SELECT id, COALESCE(sip_username,'') FROM users WHERE username=" + pg.EscapeLiteral(uname))
			if err != nil {
				missing = append(missing, uname)
				continue
			}
			if userRow[1] == "" {
				noSip = append(noSip, uname)
				continue
			}
			_ = h.db.Exec("INSERT INTO ring_group_members (group_id, user_id, priority) VALUES (" +
				groupID + "," + userRow[0] + "," + strconv.Itoa(i) + ")")
			sipUsernames = append(sipUsernames, userRow[1])
		}
		if len(missing) > 0 {
			writeErr(w, http.StatusConflict, "ring group oluşturuldu ama şu kullanıcılar bulunamadı: "+strings.Join(missing, ", "))
			return
		}
		if len(noSip) > 0 {
			writeErr(w, http.StatusConflict, "ring group oluşturuldu ama şu kullanıcıların SIP hesabı tanımlı değil (Bakım Terminali > Kullanıcılar'dan atanmalı): "+strings.Join(noSip, ", "))
			return
		}

		if err := dialplan.AppendRingGroup(req.GroupCode, sipUsernames, req.TimeoutSec); err != nil {
			_ = h.db.Exec("DELETE FROM ring_group_members WHERE group_id=" + groupID)
			_ = h.db.Exec("DELETE FROM ring_groups WHERE id=" + groupID)
			writeErr(w, http.StatusInternalServerError, "ring group oluşturulamadı: dialplan yazılamadı ("+err.Error()+"), DB kaydı geri alındı")
			return
		}

		newID, _ := strconv.Atoi(groupID)
		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"create_ring_group","group_code":"`+req.GroupCode+`"}`) + ")")
		writeJSON(w, http.StatusCreated, map[string]int{"id": newID})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET veya POST kullanın")
	}
}
