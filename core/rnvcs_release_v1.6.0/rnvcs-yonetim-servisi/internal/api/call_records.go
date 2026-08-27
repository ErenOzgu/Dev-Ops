package api

import (
	"net/http"
	"strconv"
)

// Bölüm 10.22 — Arama kayıtları (kim kimi ne zaman aradı).
//
// Asterisk CDR (asterisk_cdr) yalnızca numara→numara (src→dst = panel
// kodları) bilgisini tutar; insan ismini bilmez. İsim, panel_session_history
// tablosuyla ZAMAN BAZLI join'den gelir: "arama başladığı anda src/dst
// panelinde login olan kullanıcı kimdi". Böylece 1005'i Ahmet'in vardiyasında
// arayan → Ahmet, vardiya devrinden sonra arayan → Mehmet olarak listelenir.
//
// Bilinen sınırlama: CORE ile panellerin saatleri ciddi biçimde kayarsa
// (NTP yoksa) join yanlış kişiye denk gelebilir — ama hem CDR hem oturum
// geçmişi CORE saatiyle yazıldığından pratikte risk düşük.

type callRecord struct {
	Start       string `json:"start"`
	AnswerAt    string `json:"answer_at"`
	End         string `json:"end"`
	SrcPanel    string `json:"src_panel"`
	SrcName     string `json:"src_panel_name"`
	SrcUser     string `json:"src_user"`
	DstPanel    string `json:"dst_panel"`
	DstName     string `json:"dst_panel_name"`
	DstUser     string `json:"dst_user"`
	DurationS   int    `json:"duration_s"`
	BillsecS    int    `json:"billsec_s"`
	Disposition string `json:"disposition"`
	ToVoicemail bool   `json:"to_voicemail"`
}

// CallRecords — GET /api/call-records?limit= : CDR ⋈ oturum geçmişi.
// ADMIN/MAINTAINER hepsini görür; OPERATOR sadece kendi izinli panellerini
// içeren aramaları görür.
func (h *Handler) CallRecords(w http.ResponseWriter, r *http.Request) {
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

	// Zaman bazlı alt-sorgular: arama başlangıcında (c.start) src/dst
	// panelinde açık olan (login_at <= start < logout_at, veya hâlâ açık)
	// oturumun kullanıcısı.
	const userSubquery = `(SELECT h.username FROM panel_session_history h
		WHERE h.panel_code = %s AND h.login_at <= c.start
		  AND (h.logout_at IS NULL OR h.logout_at >= c.start)
		ORDER BY h.login_at DESC LIMIT 1)`

	srcUserSub := sprintfReplace(userSubquery, "c.src")
	dstUserSub := sprintfReplace(userSubquery, "c.dst")

	// NOT (Bölüm 10.22 / v1.4.1): CDR bitiş zamanı kolonu 'enddate' — 'end'
	// PostgreSQL'de rezerve kelime olduğundan cdr_adaptive_odbc INSERT'ü
	// tırnaksız 'end' ile patlıyordu. Kolon 'enddate' olarak standartlaştı
	// (cdr_adaptive_odbc.conf'ta 'alias end => enddate').
	q := "SELECT c.start::text, COALESCE(c.answer::text,''), COALESCE(c.enddate::text,''), " +
		"COALESCE(c.src,''), COALESCE(ps.display_name,''), COALESCE(" + srcUserSub + ",''), " +
		"COALESCE(c.dst,''), COALESCE(pd.display_name,''), COALESCE(" + dstUserSub + ",''), " +
		"COALESCE(c.duration,0), COALESCE(c.billsec,0), COALESCE(c.disposition,''), " +
		"CASE WHEN c.lastapp ILIKE 'VoiceMail%' THEN true ELSE false END " +
		"FROM asterisk_cdr c " +
		"LEFT JOIN panels ps ON ps.panel_code = c.src " +
		"LEFT JOIN panels pd ON pd.panel_code = c.dst"

	// OPERATOR: sadece kendi izinli panellerinden birinin (src ya da dst)
	// yer aldığı aramalar.
	if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
		permPanels := "(SELECT p.panel_code FROM user_panel_permissions upp JOIN panels p ON p.id=upp.panel_id WHERE upp.user_id=" + strconv.Itoa(sess.UserID) + ")"
		q += " WHERE c.src IN " + permPanels + " OR c.dst IN " + permPanels
	}
	q += " ORDER BY c.start DESC LIMIT " + strconv.Itoa(limit)

	rows, err := h.db.Query(q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "arama kayıtları okunamadı: "+err.Error())
		return
	}

	list := []callRecord{}
	for _, rr := range rows {
		if len(rr) < 13 {
			continue
		}
		dur, _ := strconv.Atoi(rr[9])
		bill, _ := strconv.Atoi(rr[10])
		list = append(list, callRecord{
			Start: rr[0], AnswerAt: rr[1], End: rr[2],
			SrcPanel: rr[3], SrcName: rr[4], SrcUser: rr[5],
			DstPanel: rr[6], DstName: rr[7], DstUser: rr[8],
			DurationS: dur, BillsecS: bill, Disposition: rr[11],
			ToVoicemail: pgBool(rr[12]),
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// sprintfReplace, tek "%s" içeren şablona güvenli bir sütun/ifade adı
// (kod sabiti, kullanıcı girdisi DEĞİL) yerleştirir. fmt import etmeden
// tek yer değiştirme.
func sprintfReplace(tmpl, val string) string {
	out := ""
	i := 0
	for i < len(tmpl) {
		if i+1 < len(tmpl) && tmpl[i] == '%' && tmpl[i+1] == 's' {
			out += val
			i += 2
			continue
		}
		out += string(tmpl[i])
		i++
	}
	return out
}
