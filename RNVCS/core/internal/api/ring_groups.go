package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"rnvcs-yonetim-servisi/internal/dialplan"
	"rnvcs-yonetim-servisi/internal/pg"
)

// ringGroupMember — bir çatal arama grubunun tek üyesi. Type: USER
// (users.sip_username üzerinden) ya da PANEL/INTERKOM/IP_HORN
// (panels.panel_code üzerinden — Anons Sistemi FKT madde 3: ring group
// üyeliği cihaz tipinden bağımsız olmalı).
type ringGroupMember struct {
	Type string `json:"type"` // USER | PANEL | INTERKOM | IP_HORN
	Code string `json:"code"` // USER için username, diğerleri için panel_code
}

type createRingGroupRequest struct {
	GroupCode   string            `json:"group_code"`
	DisplayName string            `json:"display_name"`
	Strategy    string            `json:"strategy"`
	TimeoutSec  int               `json:"timeout_sec"`
	Members     []ringGroupMember `json:"members"`
}

type ringGroupMemberOut struct {
	Type        string `json:"type"`
	Code        string `json:"code"`
	DisplayName string `json:"display_name"`
}

type ringGroupItem struct {
	ID          int                  `json:"id"`
	GroupCode   string               `json:"group_code"`
	DisplayName string               `json:"display_name"`
	Strategy    string               `json:"strategy"`
	TimeoutSec  int                  `json:"timeout_sec"`
	Members     []ringGroupMemberOut `json:"members"`
}

func validMemberType(t string) bool {
	switch t {
	case "USER", "PANEL", "INTERKOM", "IP_HORN":
		return true
	}
	return false
}

// RingGroups — GET: çatal arama (ring group) listesi, üyelerle birlikte
// (kullanıcı VEYA panel/interkom/ip horn olabilir). POST: yeni ring group
// tanımlama — ADMIN veya MAINTAINER (Bölüm 10.5).
//
// Anons Sistemi FKT madde 3: üyelik artık cihaz tipinden bağımsız — bir
// gruba kullanıcıların yanı sıra Interkom/IP Horn da eklenebilir. Asterisk
// tarafında hiçbir şey değişmiyor (dialplan.AppendRingGroup zaten sadece
// "PJSIP/<kod>" string'i üretiyordu, kaynağı users ya da panels olması
// fark etmez).
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

			var members []ringGroupMemberOut
			userRows, _ := h.db.Query(
				"SELECT u.username, COALESCE(u.full_name,'') FROM ring_group_members m JOIN users u ON u.id=m.user_id " +
					"WHERE m.group_id=" + rr[0] + " ORDER BY m.priority")
			for _, mr := range userRows {
				if len(mr) >= 2 {
					members = append(members, ringGroupMemberOut{Type: "USER", Code: mr[0], DisplayName: mr[1]})
				}
			}
			panelRows, _ := h.db.Query(
				"SELECT p.panel_code, p.display_name, p.device_type FROM ring_group_members m JOIN panels p ON p.id=m.panel_id " +
					"WHERE m.group_id=" + rr[0] + " ORDER BY m.priority")
			for _, mr := range panelRows {
				if len(mr) >= 3 {
					members = append(members, ringGroupMemberOut{Type: mr[2], Code: mr[0], DisplayName: mr[1]})
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
		if req.GroupCode == "" || len(req.Members) < 2 {
			writeErr(w, http.StatusBadRequest, "group_code ve en az 2 üye gerekli (çatal arama tanımı gereği)")
			return
		}
		for _, m := range req.Members {
			if !validMemberType(m.Type) || m.Code == "" {
				writeErr(w, http.StatusBadRequest, "her üyenin type'ı USER|PANEL|INTERKOM|IP_HORN ve code'u dolu olmalı")
				return
			}
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
		for i, m := range req.Members {
			var idVal, sipTarget string
			var err error
			if m.Type == "USER" {
				// Dial hedefi kullanıcının sip_username'i (login adı DEĞİL —
				// bir kullanıcı istediği sip_username ile register olabilir).
				userRow, e := h.db.QueryRow("SELECT id, COALESCE(sip_username,'') FROM users WHERE username=" + pg.EscapeLiteral(m.Code))
				err = e
				if e == nil {
					idVal, sipTarget = userRow[0], userRow[1]
				}
			} else {
				// Panel/Interkom/IP Horn'da panel_code = SIP username (bkz.
				// internal/pjsip/writer.go); presence kontrolü sip_password
				// dolu mu diye bakar (boşsa hiç register olamaz).
				panelRow, e := h.db.QueryRow("SELECT id, COALESCE(sip_password,'') FROM panels WHERE panel_code=" +
					pg.EscapeLiteral(m.Code) + " AND device_type=" + pg.EscapeLiteral(m.Type))
				err = e
				if e == nil {
					idVal = panelRow[0]
					if panelRow[1] != "" {
						sipTarget = m.Code
					}
				}
			}
			if err != nil {
				missing = append(missing, m.Type+":"+m.Code)
				continue
			}
			if sipTarget == "" {
				noSip = append(noSip, m.Type+":"+m.Code)
				continue
			}
			if m.Type == "USER" {
				_ = h.db.Exec("INSERT INTO ring_group_members (group_id, user_id, priority) VALUES (" +
					groupID + "," + idVal + "," + strconv.Itoa(i) + ")")
			} else {
				_ = h.db.Exec("INSERT INTO ring_group_members (group_id, panel_id, priority) VALUES (" +
					groupID + "," + idVal + "," + strconv.Itoa(i) + ")")
			}
			sipUsernames = append(sipUsernames, sipTarget)
		}
		if len(missing) > 0 {
			writeErr(w, http.StatusConflict, "ring group oluşturuldu ama şu üyeler bulunamadı: "+strings.Join(missing, ", "))
			return
		}
		if len(noSip) > 0 {
			writeErr(w, http.StatusConflict, "ring group oluşturuldu ama şu üyelerin SIP hesabı tanımlı değil: "+strings.Join(noSip, ", "))
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
