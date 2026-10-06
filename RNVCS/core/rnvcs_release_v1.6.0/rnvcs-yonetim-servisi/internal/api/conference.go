package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"rnvcs-yonetim-servisi/internal/ami"
	"rnvcs-yonetim-servisi/internal/confbridge"
	"rnvcs-yonetim-servisi/internal/pg"
)

type createConferenceRequest struct {
	RoomCode             string   `json:"room_code"` // boşsa otomatik üretilir
	DisplayName          string   `json:"display_name"`
	ParticipantUsernames []string `json:"participant_usernames"` // en az 2 KULLANICI adı (login username)
}

type conferenceResponse struct {
	RoomCode string   `json:"room_code"`
	Invited  []string `json:"invited"`          // başarıyla Originate edilen sip_username'ler
	Failed   []string `json:"failed,omitempty"` // Originate başarısız olan katılımcılar (username:sebep)
}

// Conference — POST /api/conference: madde 4 (Anons Sistemi FKT) —
// listeden çoklu katılımcı seçip aynı konferans odasına dahil etme.
//
// Akış: 1) roomCode için ConfBridge() dialplan extension'ı yazılır,
// 2) AMI'ye bağlanılıp her katılımcı sip_username'i Originate ile o
// extension'a bağlanır (katılımcı telefonu çalar, açarsa konferansa
// düşer — MP'nin kendisi aramıyor, CORE arıyor).
//
// Yetki: ADMIN/MAINTAINER/OPERATOR hepsi başlatabilir (Bölüm 10.5'te
// konferans için ayrı bir kısıt tanımlı değil — panel_app'ten herhangi
// bir login'li operatör kullanabilmeli).
func (h *Handler) Conference(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "sadece POST")
		return
	}
	if h.amiAddr == "" || h.amiUser == "" {
		writeErr(w, http.StatusServiceUnavailable, "konferans özelliği bu sunucuda yapılandırılmamış (RNVCS_AMI_* ortam değişkenleri eksik)")
		return
	}

	var req createConferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
		return
	}
	if len(req.ParticipantUsernames) < 2 {
		writeErr(w, http.StatusBadRequest, "konferans için en az 2 katılımcı gerekli")
		return
	}
	if req.RoomCode == "" {
		req.RoomCode = "konf" + strconv.FormatInt(int64(sess.UserID), 10) + strconv.Itoa(len(req.ParticipantUsernames))
	}

	// Katılımcıların SIP kimliklerini çöz (sip_username boşsa o kişi atlanır).
	var sipTargets []string
	var noSip []string
	for _, uname := range req.ParticipantUsernames {
		row, err := h.db.QueryRow("SELECT COALESCE(sip_username,'') FROM users WHERE username=" + pg.EscapeLiteral(uname))
		if err != nil || row[0] == "" {
			noSip = append(noSip, uname)
			continue
		}
		sipTargets = append(sipTargets, row[0])
	}
	if len(sipTargets) < 2 {
		writeErr(w, http.StatusConflict, "en az 2 katılımcının SIP hesabı tanımlı olmalı — eksik: "+strings.Join(noSip, ", "))
		return
	}

	if err := confbridge.AppendConferenceRoom(req.RoomCode); err != nil {
		writeErr(w, http.StatusInternalServerError, "konferans odası dialplan'e yazılamadı: "+err.Error())
		return
	}

	amiClient, err := ami.Dial(h.amiAddr, h.amiUser, h.amiSecret)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Asterisk AMI'ye bağlanılamadı: "+err.Error())
		return
	}
	defer amiClient.Close()

	var invited []string
	var failed []string
	for _, sipUser := range sipTargets {
		if err := amiClient.Originate(sipUser, "rnvcs-panels", req.RoomCode, 1,
			"RNVCS Konferans <"+req.RoomCode+">", 30000); err != nil {
			failed = append(failed, sipUser+": "+err.Error())
			continue
		}
		invited = append(invited, sipUser)
	}

	participantsJSON, _ := json.Marshal(sipTargets)
	_ = h.db.Exec("INSERT INTO conference_sessions (room_code, created_by, participants) VALUES (" +
		pg.EscapeLiteral(req.RoomCode) + "," + strconv.Itoa(sess.UserID) + "," + pg.EscapeLiteral(string(participantsJSON)) + ")")
	_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
		strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"start_conference","room_code":"`+req.RoomCode+`"}`) + ")")

	writeJSON(w, http.StatusOK, conferenceResponse{RoomCode: req.RoomCode, Invited: invited, Failed: failed})
}
