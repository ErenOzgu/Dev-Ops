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

// conferenceMember — konferansa tek bir katılımcı. Type: USER (users.sip_username
// üzerinden) ya da INTERKOM/IP_HORN (panels.sip_password/panel_code üzerinden).
// Desen ring_groups.go'daki ringGroupMember ile birebir aynı (Konferansa
// Interkom/IP Horn Ekleme — Tamamlayıcı Geliştirici Notu, 2026-08-26).
//
// NOT: Type="PANEL" burada KASITLI OLARAK desteklenmiyor — device_type='PANEL'
// olan ham panel kaydı kendi başına SIP register olmuyor (SIP kimliği
// kullanıcıya ait, bkz. internal/pjsip/writer.go). O panelde login olan
// kullanıcı konferansa Type="USER" ile eklenmeli.
type conferenceMember struct {
	Type string `json:"type"` // USER | INTERKOM | IP_HORN
	Code string `json:"code"` // USER için username, diğerleri için panel_code
}

func validConferenceMemberType(t string) bool {
	switch t {
	case "USER", "INTERKOM", "IP_HORN":
		return true
	}
	return false
}

type createConferenceRequest struct {
	RoomCode    string `json:"room_code"` // boşsa otomatik üretilir
	DisplayName string `json:"display_name"`

	// Yeni model — en az 2 üye, USER/INTERKOM/IP_HORN karışık olabilir.
	Participants []conferenceMember `json:"participants"`

	// Geriye dönük uyumluluk: eski panel_app sürümleri hâlâ bunu gönderebilir.
	// Participants doluysa bu alan YOK SAYILIR. Boş participants + dolu
	// ParticipantUsernames görülürse otomatik olarak Type=USER üyelere çevrilir.
	ParticipantUsernames []string `json:"participant_usernames"`
}

type conferenceResponse struct {
	RoomCode string   `json:"room_code"`
	Invited  []string `json:"invited"`          // başarıyla Originate edilen sip hedefleri
	Failed   []string `json:"failed,omitempty"` // Originate başarısız olan katılımcılar (tip:kod: sebep)
}

// Conference — POST /api/conference: madde 4 (Anons Sistemi FKT) — listeden
// çoklu katılımcı seçip aynı konferans odasına dahil etme. Madde 14.3.5
// tamamlayıcısı (2026-08-26): katılımcılar artık sadece kullanıcı değil,
// Interkom/IP Horn da olabilir (ring group'takiyle aynı TİP:kod deseni).
//
// Akış: 1) roomCode için ConfBridge() dialplan extension'ı yazılır,
// 2) AMI'ye bağlanılıp her katılımcının sip hedefi Originate ile o
// extension'a bağlanır (katılımcı/cihaz çalar, açarsa konferansa düşer —
// MP'nin kendisi aramıyor, CORE arıyor).
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

	// Geriye dönük uyumluluk: eski istemci participant_usernames gönderdiyse
	// ve yeni participants alanı boşsa, USER tipine çevir.
	members := req.Participants
	if len(members) == 0 && len(req.ParticipantUsernames) > 0 {
		for _, uname := range req.ParticipantUsernames {
			members = append(members, conferenceMember{Type: "USER", Code: uname})
		}
	}
	if len(members) < 2 {
		writeErr(w, http.StatusBadRequest, "konferans için en az 2 katılımcı gerekli")
		return
	}
	for _, m := range members {
		if !validConferenceMemberType(m.Type) || m.Code == "" {
			writeErr(w, http.StatusBadRequest, "her katılımcının type'ı USER|INTERKOM|IP_HORN ve code'u dolu olmalı")
			return
		}
	}
	if req.RoomCode == "" {
		req.RoomCode = "konf" + strconv.FormatInt(int64(sess.UserID), 10) + strconv.Itoa(len(members))
	}

	// Katılımcıların SIP hedeflerini çöz (ring_groups.go POST akışıyla aynı
	// desen — USER: users.sip_username, INTERKOM/IP_HORN: panels.panel_code,
	// presence kontrolü sip_password dolu mu diye bakar).
	var sipTargets []string
	var noSip []string
	for _, m := range members {
		var sipTarget string
		if m.Type == "USER" {
			row, err := h.db.QueryRow("SELECT COALESCE(sip_username,'') FROM users WHERE username=" + pg.EscapeLiteral(m.Code))
			if err == nil {
				sipTarget = row[0]
			}
		} else {
			row, err := h.db.QueryRow("SELECT COALESCE(sip_password,'') FROM panels WHERE panel_code=" +
				pg.EscapeLiteral(m.Code) + " AND device_type=" + pg.EscapeLiteral(m.Type))
			if err == nil && row[0] != "" {
				sipTarget = m.Code
			}
		}
		if sipTarget == "" {
			noSip = append(noSip, m.Type+":"+m.Code)
			continue
		}
		sipTargets = append(sipTargets, sipTarget)
	}
	if len(sipTargets) < 2 {
		writeErr(w, http.StatusConflict, "en az 2 katılımcının SIP hesabı tanımlı/register olabilir olmalı — eksik: "+strings.Join(noSip, ", "))
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
