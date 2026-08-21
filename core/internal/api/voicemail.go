package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Bölüm 10.22 — Panel sesli mesajları (voicemail).
//
// Kimse login değilken bir panel numarası arandığında (dialplan VoiceMail()
// fallback'i) mesaj şuraya düşer:
//   <spoolRoot>/<panel_code>/INBOX/msgNNNN.txt   (metadata)
//   <spoolRoot>/<panel_code>/INBOX/msgNNNN.wav   (ses — tercih edilen)
// spoolRoot varsayılanı /var/spool/asterisk/voicemail/rnvcs-vm; sandbox/test
// için RNVCS_VOICEMAIL_DIR ortam değişkeniyle değiştirilebilir.
//
// Yetki: bir operatör YALNIZCA oturum açtığı panelin mesajlarına erişebilir
// (sess.PanelCode eşleşmeli); ADMIN/MAINTAINER her panele erişir.

func voicemailRoot() string {
	if v := os.Getenv("RNVCS_VOICEMAIL_DIR"); v != "" {
		return v
	}
	return "/var/spool/asterisk/voicemail/rnvcs-vm"
}

type voicemailMessage struct {
	ID       string `json:"id"`        // "0000" (msg0000)
	CallerID string `json:"caller_id"` // arayan panel/no
	Origtime int64  `json:"origtime"`  // unix saniye
	Duration int    `json:"duration"`  // saniye
	Folder   string `json:"folder"`    // INBOX | Old
}

// panelAccessAllowed, bir rol+oturum-paneli çiftinin istenen panel_code'a
// erişimini denetler: ADMIN/MAINTAINER her panele, OPERATOR yalnızca oturum
// açtığı panele.
func panelAccessAllowed(role, sessionPanelCode, panelCode string) bool {
	if role == "ADMIN" || role == "MAINTAINER" {
		return true
	}
	return sessionPanelCode == panelCode && panelCode != ""
}

// Voicemail — GET (liste) ve DELETE (sil). Ses için ayrı: VoicemailAudio.
func (h *Handler) Voicemail(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		panelCode := r.URL.Query().Get("panel_code")
		if panelCode == "" {
			panelCode = sess.PanelCode // varsayılan: oturumun kendi paneli
		}
		if !panelAccessAllowed(sess.Role, sess.PanelCode, panelCode) {
			writeErr(w, http.StatusForbidden, "bu panelin mesajlarına erişim yetkiniz yok")
			return
		}
		msgs := listVoicemail(panelCode)
		writeJSON(w, http.StatusOK, msgs)

	case http.MethodDelete:
		panelCode := r.URL.Query().Get("panel_code")
		if panelCode == "" {
			panelCode = sess.PanelCode
		}
		msgID := r.URL.Query().Get("id")
		folder := r.URL.Query().Get("folder")
		if folder == "" {
			folder = "INBOX"
		}
		if msgID == "" {
			writeErr(w, http.StatusBadRequest, "id parametresi gerekli")
			return
		}
		if !panelAccessAllowed(sess.Role, sess.PanelCode, panelCode) {
			writeErr(w, http.StatusForbidden, "bu panelin mesajlarına erişim yetkiniz yok")
			return
		}
		if !safeMsgID(msgID) || !safePanelCode(panelCode) {
			writeErr(w, http.StatusBadRequest, "geçersiz id/panel_code")
			return
		}
		dir := filepath.Join(voicemailRoot(), panelCode, folder)
		removed := 0
		// msgNNNN.* (txt + wav/WAV/gsm) hepsini sil.
		matches, _ := filepath.Glob(filepath.Join(dir, "msg"+msgID+".*"))
		for _, m := range matches {
			if err := os.Remove(m); err == nil {
				removed++
			}
		}
		if removed == 0 {
			writeErr(w, http.StatusNotFound, "mesaj bulunamadı")
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"removed": removed})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET veya DELETE kullanın")
	}
}

// VoicemailAudio — GET /api/voicemail/audio?panel_code=&id=&folder= :
// mesajın ses dosyasını (tercihen linear PCM .wav) stream eder.
func (h *Handler) VoicemailAudio(w http.ResponseWriter, r *http.Request) {
	// <audio src> öğesi Authorization header taşıyamadığından, bu TEK uçta
	// token query param olarak da kabul edilir (ses dosyası, panele oturum
	// açmış operatörün token'ıyla korunur — panel-yerel bir istek).
	sess, ok := h.authenticate(r)
	if !ok {
		if tok := r.URL.Query().Get("token"); tok != "" {
			sess, ok = h.sessions.Get(tok)
		}
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "sadece GET")
		return
	}
	panelCode := r.URL.Query().Get("panel_code")
	if panelCode == "" {
		panelCode = sess.PanelCode
	}
	msgID := r.URL.Query().Get("id")
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	if !panelAccessAllowed(sess.Role, sess.PanelCode, panelCode) {
		writeErr(w, http.StatusForbidden, "bu panelin mesajlarına erişim yetkiniz yok")
		return
	}
	if msgID == "" || !safeMsgID(msgID) || !safePanelCode(panelCode) {
		writeErr(w, http.StatusBadRequest, "geçersiz id/panel_code")
		return
	}
	dir := filepath.Join(voicemailRoot(), panelCode, folder)
	// Tarayıcıda çalabilmesi için sırayla dene: linear PCM .wav > .WAV (wav49) > .gsm
	for _, ext := range []string{".wav", ".WAV", ".gsm"} {
		p := filepath.Join(dir, "msg"+msgID+ext)
		if f, err := os.Open(p); err == nil {
			defer f.Close()
			ct := "audio/wav"
			if ext == ".gsm" {
				ct = "audio/x-gsm"
			}
			w.Header().Set("Content-Type", ct)
			st, _ := f.Stat()
			if st != nil {
				w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
			}
			_, _ = io.Copy(w, f)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "ses dosyası bulunamadı")
}

// listVoicemail, bir panelin INBOX'ındaki mesajları metadata .txt
// dosyalarından okuyup çözümler (yeni mesaj önce).
func listVoicemail(panelCode string) []voicemailMessage {
	out := []voicemailMessage{}
	if !safePanelCode(panelCode) {
		return out
	}
	dir := filepath.Join(voicemailRoot(), panelCode, "INBOX")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out // klasör yoksa hiç mesaj yok — boş liste
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "msg") || !strings.HasSuffix(name, ".txt") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "msg"), ".txt")
		m := parseVoicemailMeta(filepath.Join(dir, name))
		m.ID = id
		m.Folder = "INBOX"
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Origtime > out[j].Origtime })
	return out
}

func parseVoicemailMeta(path string) voicemailMessage {
	var m voicemailMessage
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		switch key {
		case "callerid":
			m.CallerID = cleanCallerID(val)
		case "origtime":
			m.Origtime, _ = strconv.ParseInt(val, 10, 64)
		case "duration":
			m.Duration, _ = strconv.Atoi(val)
		}
	}
	return m
}

// cleanCallerID, Asterisk callerid alanını insan-okunur hale getirir.
// Girdi biçimleri: `"1002" <1002>`, `"Ahmet" <1005>`, `<1001>`, `1001`.
// Öncelik: köşeli parantez içindeki numara; yoksa tırnak içindeki ad;
// yoksa ham değer.
func cleanCallerID(val string) string {
	if lt := strings.IndexByte(val, '<'); lt >= 0 {
		if gt := strings.IndexByte(val[lt:], '>'); gt > 0 {
			num := strings.TrimSpace(val[lt+1 : lt+gt])
			if num != "" {
				return num
			}
		}
	}
	return strings.TrimSpace(strings.Trim(val, `"`))
}

// safeMsgID / safePanelCode — path traversal koruması: sadece rakam/harf.
func safeMsgID(s string) bool {
	if s == "" || len(s) > 12 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func safePanelCode(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
