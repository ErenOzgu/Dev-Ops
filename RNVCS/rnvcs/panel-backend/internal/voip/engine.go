// engine.go — RNVCS panelinin GERÇEK SIP User Agent'ı: hem Asterisk'e
// register olur (Bölüm 10.19) hem de gelen bir çağrıyı (INVITE) karşılar,
// operatör "Kabul Et" dediğinde sesi RTP/G.711 üzerinden gerçek donanıma
// (arecord/aplay -> ALSA) taşır.
//
// Önceki sürüm (internal/sipclient) her REGISTER denemesinde YENİ bir UDP
// soketi açıp hemen kapatıyordu — bu, register olmak için yeterliydi ama
// Asterisk'in Contact'ta ilan edilen adrese geri INVITE gönderebilmesi
// için o soketin KALICI ve DİNLEMEDE kalması gerekir. Bu paket bu yüzden
// tek bir kalıcı UDP soketi üzerinden hem REGISTER'ı yürütür hem de gelen
// tüm SIP isteklerini (INVITE/BYE/CANCEL/OPTIONS) aynı soketten dinler.
package voip

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	AsteriskAddr string
	SipUsername  string
	SipPassword  string
	ExpiresSec   int
}

// CallStatus, panel_app.html'nin GET /api/call/status ile periyodik olarak
// sorgulayacağı anlık çağrı durumu.
type CallStatus struct {
	State     string `json:"state"` // "idle" | "ringing" | "dialing" | "active"
	From      string `json:"from,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	LastError string `json:"last_error,omitempty"` // son giden çağrı başarısız olduysa (örn. "486 Busy Here")
}

type RegStatus struct {
	Active      bool   `json:"active"`
	SipUsername string `json:"sip_username,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastOKAt    string `json:"last_ok_at,omitempty"`
}

type Engine struct {
	mu sync.Mutex

	conn        *net.UDPConn // kalıcı SIP sinyalleşme soketi
	localIP     string
	respCh      chan string
	respWaiters map[string]chan string

	cfg           Config
	registered    bool
	regLastError  string
	regLastOK     time.Time
	refreshCancel chan struct{}

	// aktif/çalan çağrının dialog bilgisi
	state           string // idle|ringing|dialing|active
	callID          string
	fromHeader      string
	toHeader        string // to-tag eklenmiş hali
	viaHeader       string
	cseqLine        string // "<n> INVITE"
	remoteAddr      *net.UDPAddr
	remoteRTP       *net.UDPAddr
	fromDisplay     string
	startedAt       time.Time
	lastError       string
	weInitiated     bool   // true: bu çağrıyı BİZ başlattık (Dial), false: bize geldi (INVITE aldık)
	dialogRemoteURI string // BYE'ın Request-URI'si — karşı tarafın adresi (biz cevapladıysak arayanın AOR'u, biz aradıysak hedef extension)

	// giden (bu panelin başlattığı) bir çağrı henüz cevaplanmadan (state=
	// "dialing") operatör vazgeçerse CANCEL gönderebilmek için orijinal
	// INVITE'ın Via/branch'i (RFC 3261: CANCEL AYNI branch'i kullanmalıdır).
	outBranch  string
	outVia     string
	outToURI   string
	outFromURI string

	// medya (RTP + ses köprüsü)
	rtpConn   *net.UDPConn
	audioStop chan struct{}
	recordCmd *exec.Cmd
	playCmd   *exec.Cmd
}

func New() *Engine {
	return &Engine{state: "idle"}
}

// ---- Register (Bölüm 10.19) ----

func (e *Engine) RegStatus() RegStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := RegStatus{Active: e.registered, LastError: e.regLastError}
	if e.registered {
		st.SipUsername = e.cfg.SipUsername
	}
	if !e.regLastOK.IsZero() {
		st.LastOKAt = e.regLastOK.Format(time.RFC3339)
	}
	return st
}

func (e *Engine) CallStatusNow() CallStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs := CallStatus{State: e.state, From: e.fromDisplay, CallID: e.callID, LastError: e.lastError}
	if !e.startedAt.IsZero() {
		cs.StartedAt = e.startedAt.Format(time.RFC3339)
	}
	return cs
}

// Start, kalıcı bir UDP soketi açar, Asterisk'e REGISTER olur ve gelen
// istekleri (INVITE dahil) dinlemeye başlar. Önceden açık bir soket varsa
// önce onu kapatır (Stop ile).
func (e *Engine) Start(cfg Config) error {
	if cfg.ExpiresSec <= 0 {
		cfg.ExpiresSec = 300
	}
	e.Stop()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("yerel UDP soketi açılamadı: %w", err)
	}

	// Bu soketten Asterisk'e ulaşırken hangi yerel IP kullanılacağını
	// öğrenmek için kısa bir "dial" denemesi (paket gönderilmez, sadece
	// işletim sisteminin route seçimini öğreniriz).
	probe, err := net.Dial("udp", cfg.AsteriskAddr)
	if err != nil {
		conn.Close()
		return fmt.Errorf("asterisk adresine ulaşılamadı: %w", err)
	}
	localIP := probe.LocalAddr().(*net.UDPAddr).IP.String()
	probe.Close()

	e.mu.Lock()
	e.conn = conn
	e.localIP = localIP
	e.cfg = cfg
	e.respCh = make(chan string, 4)
	e.respWaiters = make(map[string]chan string)
	e.mu.Unlock()

	go e.readLoop(conn)

	if err := e.register(cfg.ExpiresSec); err != nil {
		e.mu.Lock()
		e.regLastError = err.Error()
		e.registered = false
		e.mu.Unlock()
		return err
	}

	e.mu.Lock()
	e.registered = true
	e.regLastError = ""
	e.regLastOK = time.Now()
	stopCh := make(chan struct{})
	e.refreshCancel = stopCh
	e.mu.Unlock()

	go e.refreshLoop(cfg, stopCh)
	return nil
}

func (e *Engine) refreshLoop(cfg Config, stopCh chan struct{}) {
	interval := time.Duration(float64(cfg.ExpiresSec)*0.7) * time.Second
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			err := e.register(cfg.ExpiresSec)
			e.mu.Lock()
			if err != nil {
				e.regLastError = err.Error()
				e.registered = false
			} else {
				e.regLastError = ""
				e.registered = true
				e.regLastOK = time.Now()
			}
			e.mu.Unlock()
			if err != nil {
				log.Printf("!! REGISTER yenileme basarisiz (bir sonraki turda tekrar denenecek): %v", err)
				// DUZELTME (2026-08-25): oncesinde burada "return" ile dongu
				// KALICI olarak duruyordu - respWaiters duzeltmesinden onceki
				// tek seferlik bir catisma bile kaydi sonsuza kadar dusuruyordu.
				// Artik hata olsa da bir sonraki ticker turunda tekrar
				// denenmeye devam ediliyor.
			}
		}
	}
}

// Stop, aktif çağrıyı (varsa) kapatır, kaydı (Expires:0 ile) düşürmeye
// çalışır ve dinleme soketini kapatır.
func (e *Engine) Stop() {
	e.mu.Lock()
	conn := e.conn
	stopCh := e.refreshCancel
	cfg := e.cfg
	wasRegistered := e.registered
	e.conn = nil
	e.refreshCancel = nil
	e.registered = false
	e.mu.Unlock()

	e.HangupActive()

	if stopCh != nil {
		close(stopCh)
	}
	if wasRegistered && conn != nil && cfg.SipUsername != "" {
		_ = e.registerOn(conn, cfg, 0)
	}
	if conn != nil {
		conn.Close()
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

var authHeaderRe = regexp.MustCompile(`(\w+)="?([^",]+)"?`)

func parseAuthHeader(h string) map[string]string {
	out := map[string]string{}
	for _, m := range authHeaderRe.FindAllStringSubmatch(h, -1) {
		out[strings.ToLower(m[1])] = m[2]
	}
	return out
}

func (e *Engine) register(expiresSec int) error {
	e.mu.Lock()
	conn := e.conn
	cfg := e.cfg
	e.mu.Unlock()
	return e.registerOn(conn, cfg, expiresSec)
}

// registerOn, kalıcı soket üzerinden bir REGISTER denemesi yapar (401/407
// digest challenge'ına karşı ikinci bir istekle). Yanıtlar readLoop
// tarafından respCh'ye yönlendirilir, burada zaman aşımıyla beklenir.
func (e *Engine) registerOn(conn *net.UDPConn, cfg Config, expiresSec int) error {
	if conn == nil {
		return fmt.Errorf("SIP soketi açık değil")
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", cfg.AsteriskAddr)
	if err != nil {
		return fmt.Errorf("asterisk adresi çözümlenemedi: %w", err)
	}
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	localHostPort := fmt.Sprintf("%s:%d", e.localIP, localPort)
	contact := fmt.Sprintf("sip:%s@%s", cfg.SipUsername, localHostPort)

	callID := randHex(8) + "@rnvcs-panel-backend"
	fromTag := randHex(6)
	cseq := 1

	msg1 := buildRegisterReq(cfg, contact, localHostPort, callID, fromTag, cseq, expiresSec, "")
	resp1, err := e.sendAndAwait(conn, remoteAddr, msg1, callID)
	if err != nil {
		return err
	}
	code1, _ := statusCode(resp1)
	if code1 == 200 {
		return nil
	}
	if code1 != 401 && code1 != 407 {
		return fmt.Errorf("beklenmeyen REGISTER yanıtı: %s", startLine(resp1))
	}

	challenge := extractAuthChallenge(resp1)
	if challenge == "" {
		return fmt.Errorf("401/407 alındı ama WWW-Authenticate/Proxy-Authenticate bulunamadı")
	}
	params := parseAuthHeader(challenge)
	realm := params["realm"]
	nonce := params["nonce"]
	qop := params["qop"]
	cnonce := randHex(8)
	nc := "00000001"

	ha1 := md5hex(cfg.SipUsername + ":" + realm + ":" + cfg.SipPassword)
	uri := fmt.Sprintf("sip:%s", strings.Split(cfg.AsteriskAddr, ":")[0])
	ha2 := md5hex("REGISTER:" + uri)

	var authHeader string
	if qop != "" {
		response := md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
		authHeader = fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5, qop=auth, nc=%s, cnonce="%s"`,
			cfg.SipUsername, realm, nonce, uri, response, nc, cnonce)
	} else {
		response := md5hex(ha1 + ":" + nonce + ":" + ha2)
		authHeader = fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5`,
			cfg.SipUsername, realm, nonce, uri, response)
	}

	cseq++
	msg2 := buildRegisterReq(cfg, contact, localHostPort, callID, fromTag, cseq, expiresSec, authHeader)
	resp2, err := e.sendAndAwait(conn, remoteAddr, msg2, callID)
	if err != nil {
		return err
	}
	code2, reason2 := statusCode(resp2)
	if code2 != 200 {
		return fmt.Errorf("REGISTER reddedildi: %d %s", code2, reason2)
	}
	return nil
}

func (e *Engine) sendAndAwait(conn *net.UDPConn, remote *net.UDPAddr, msg, callID string) (string, error) {
	ch := e.registerWaiter(callID)
	defer e.unregisterWaiter(callID)
	if _, err := conn.WriteToUDP([]byte(msg), remote); err != nil {
		return "", fmt.Errorf("istek gonderilemedi: %w", err)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("yanit zaman asimi (Asterisk erisilebilir mi, SIP/UDP portu acik mi?)")
	}
}

func buildRegisterReq(cfg Config, contact, localHostPort, callID, fromTag string, cseq int, expiresSec int, authHeader string) string {
	host := strings.Split(cfg.AsteriskAddr, ":")[0]
	branch := "z9hG4bK" + randHex(8)
	uri := fmt.Sprintf("sip:%s", host)
	fromURI := fmt.Sprintf("sip:%s@%s", cfg.SipUsername, host)

	var b strings.Builder
	fmt.Fprintf(&b, "REGISTER %s SIP/2.0\r\n", uri)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s;rport\r\n", localHostPort, branch)
	fmt.Fprintf(&b, "Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: <%s>;tag=%s\r\n", fromURI, fromTag)
	fmt.Fprintf(&b, "To: <%s>\r\n", fromURI)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: %d REGISTER\r\n", cseq)
	fmt.Fprintf(&b, "Contact: <%s>\r\n", contact)
	fmt.Fprintf(&b, "Expires: %d\r\n", expiresSec)
	if authHeader != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", authHeader)
	}
	fmt.Fprintf(&b, "User-Agent: rnvcs-panel-backend/1.0 (Bölüm 10.19/10.20)\r\n")
	fmt.Fprintf(&b, "Content-Length: 0\r\n\r\n")
	return b.String()
}

func startLineParts(msg string) []string {
	return strings.SplitN(startLine(msg), " ", 3)
}

func statusCode(msg string) (int, string) {
	parts := startLineParts(msg)
	if len(parts) < 3 {
		return 0, ""
	}
	code, _ := strconv.Atoi(parts[1])
	return code, parts[2]
}

func extractAuthChallenge(msg string) string {
	for _, line := range strings.Split(msg, "\r\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "www-authenticate:") {
			return strings.TrimSpace(line[len("WWW-Authenticate:"):])
		}
		if strings.HasPrefix(lower, "proxy-authenticate:") {
			return strings.TrimSpace(line[len("Proxy-Authenticate:"):])
		}
	}
	return ""
}

// ---- Gelen istekleri dinleme (INVITE/BYE/CANCEL/OPTIONS) ----

func (e *Engine) readLoop(conn *net.UDPConn) {
	buf := make([]byte, 8192)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // soket kapatildi
		}
		msg := string(buf[:n])
		if strings.HasPrefix(msg, "SIP/2.0") {
			// DUZELTME (2026-08-25): cevaplar artik paylasilan TEK bir kanala
			// degil, Call-ID'sine gore dogru bekleyen istege yonlendiriliyor.
			// Oncesinde REGISTER yenilemesi ile giden bir INVITE ayni kanaldan
			// okudugu icin birbirlerinin (or. eski nonce iceren) cevabini
			// calabiliyordu.
			cid := extractCallID(msg)
			e.mu.Lock()
			ch := e.respWaiters[cid]
			e.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg:
				default:
				}
			}
			continue
		}
		e.handleRequest(msg, from)
	}
}

var callIDRe = regexp.MustCompile(`(?im)^Call-ID:\s*(.+?)\s*$`)

func extractCallID(msg string) string {
	m := callIDRe.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func (e *Engine) registerWaiter(callID string) chan string {
	ch := make(chan string, 4)
	e.mu.Lock()
	if e.respWaiters == nil {
		e.respWaiters = make(map[string]chan string)
	}
	e.respWaiters[callID] = ch
	e.mu.Unlock()
	return ch
}

func (e *Engine) unregisterWaiter(callID string) {
	e.mu.Lock()
	delete(e.respWaiters, callID)
	e.mu.Unlock()
}

func (e *Engine) handleRequest(msg string, from *net.UDPAddr) {
	parts := startLineParts(msg)
	if len(parts) < 1 {
		return
	}
	method := parts[0]
	lines := strings.Split(msg, "\r\n")
	h := headerMap(lines)

	switch method {
	case "INVITE":
		e.handleInvite(msg, h, from)
	case "ACK":
		// dialog zaten 200 OK ile kurulmuş, ek işlem gerekmiyor
	case "BYE":
		e.handleBye(h, from)
	case "CANCEL":
		e.handleCancel(h, from)
	case "OPTIONS":
		e.replyTo(from, buildResponse(200, "OK", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
	default:
		log.Printf("!! voip: desteklenmeyen SIP metodu: %s", method)
		e.replyTo(from, buildResponse(501, "Not Implemented", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
	}
}

func (e *Engine) replyTo(addr *net.UDPAddr, msg string) {
	e.mu.Lock()
	conn := e.conn
	e.mu.Unlock()
	if conn == nil {
		return
	}
	_, _ = conn.WriteToUDP([]byte(msg), addr)
}

var sdpConnRe = regexp.MustCompile(`c=IN IP4 ([0-9.]+)`)
var sdpMediaRe = regexp.MustCompile(`m=audio (\d+) RTP/AVP`)

func (e *Engine) handleInvite(msg string, h map[string]string, from *net.UDPAddr) {
	e.mu.Lock()
	busy := e.state != "idle"
	e.mu.Unlock()
	if busy {
		e.replyTo(from, buildResponse(486, "Busy Here", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
		return
	}

	body := sdpBody(msg)
	connMatch := sdpConnRe.FindStringSubmatch(body)
	mediaMatch := sdpMediaRe.FindStringSubmatch(body)
	if connMatch == nil || mediaMatch == nil {
		e.replyTo(from, buildResponse(488, "Not Acceptable Here", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
		return
	}
	remoteIP := connMatch[1]
	remotePort, _ := strconv.Atoi(mediaMatch[1])
	remoteRTP := &net.UDPAddr{IP: net.ParseIP(remoteIP), Port: remotePort}

	toTag := randHex(6)
	toWithTag := ensureTag(h["to"], toTag)

	// 100 Trying — to-tag'sız (henüz dialog kurulmadı)
	e.replyTo(from, buildResponse(100, "Trying", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
	// 180 Ringing — dialog'u tanımlayan to-tag'i burada sabitliyoruz
	e.replyTo(from, buildResponse(180, "Ringing", h["via"], h["from"], toWithTag, h["call-id"], h["cseq"], nil, ""))

	e.mu.Lock()
	e.state = "ringing"
	e.callID = h["call-id"]
	e.fromHeader = h["from"]
	e.toHeader = toWithTag
	e.viaHeader = h["via"]
	e.cseqLine = h["cseq"]
	e.remoteAddr = from
	e.remoteRTP = remoteRTP
	e.fromDisplay = extractDisplayName(h["from"])
	e.dialogRemoteURI = extractSipURI(h["from"])
	e.lastError = ""
	e.weInitiated = false
	e.mu.Unlock()

	log.Printf("==> Gelen çağrı: %s (RTP %s:%d)", e.fromDisplay, remoteIP, remotePort)
}

func extractDisplayName(fromHeader string) string {
	// "\"Onur PC\" <sip:1041@10.1.60.6>;tag=..." ya da "<sip:1041@10.1.60.6>"
	if i := strings.Index(fromHeader, "<sip:"); i >= 0 {
		rest := fromHeader[i+5:]
		if j := strings.IndexAny(rest, "@>"); j >= 0 {
			return rest[:j]
		}
	}
	return fromHeader
}

// extractSipURI, bir From/To header'ından "sip:user@host" biçimindeki tam
// URI'yi (tag ve display name olmadan) çıkarır — sonraki bir isteğin
// (BYE gibi) Request-URI'sini doğru hedefe kurabilmek için kullanılır.
func extractSipURI(header string) string {
	if i := strings.Index(header, "<sip:"); i >= 0 {
		rest := header[i+1:]
		if j := strings.Index(rest, ">"); j >= 0 {
			return rest[:j]
		}
	}
	return header
}

func (e *Engine) handleBye(h map[string]string, from *net.UDPAddr) {
	e.mu.Lock()
	match := e.state != "idle" && e.callID == h["call-id"]
	e.mu.Unlock()
	e.replyTo(from, buildResponse(200, "OK", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
	if match {
		log.Println("==> Karşı taraf çağrıyı sonlandırdı (BYE)")
		e.resetCallState()
	}
}

func (e *Engine) handleCancel(h map[string]string, from *net.UDPAddr) {
	e.mu.Lock()
	match := e.state == "ringing" && e.callID == h["call-id"]
	e.mu.Unlock()
	e.replyTo(from, buildResponse(200, "OK", h["via"], h["from"], h["to"], h["call-id"], h["cseq"], nil, ""))
	if match {
		// orijinal INVITE'a da 487 dönmek gerekir (RFC 3261) — bekleyen INVITE'ın
		// kendi CSeq'i (örn. "1 INVITE") saklanmış haliyle kullanılıyor
		e.mu.Lock()
		inviteCseq := e.cseqLine
		via, fromH, toH, callID := e.viaHeader, e.fromHeader, e.toHeader, e.callID
		e.mu.Unlock()
		e.replyTo(from, buildResponse(487, "Request Terminated", via, fromH, toH, callID, inviteCseq, nil, ""))
		log.Println("==> Karşı taraf çağrıyı iptal etti (CANCEL) — telefon çalmadan kesildi")
		e.resetCallState()
	}
}

func (e *Engine) resetCallState() {
	e.resetCallStateErr("")
}

// resetCallStateErr, state'i idle'a döndürür; errMsg boş değilse son giden
// çağrının neden başarısız olduğunu (örn. "486 Busy Here") bir sonraki
// Dial()/gelen çağrıya kadar CallStatusNow() üzerinden görünür tutar —
// panel_app.html bunu operatöre kısaca gösterebilsin diye.
func (e *Engine) resetCallStateErr(errMsg string) {
	e.stopAudioBridge()
	e.mu.Lock()
	e.state = "idle"
	e.callID = ""
	e.fromHeader = ""
	e.toHeader = ""
	e.viaHeader = ""
	e.cseqLine = ""
	e.remoteAddr = nil
	e.remoteRTP = nil
	e.outBranch = ""
	e.outVia = ""
	e.outToURI = ""
	e.outFromURI = ""
	e.dialogRemoteURI = ""
	if errMsg != "" {
		e.lastError = errMsg
	} else {
		e.fromDisplay = ""
	}
	e.startedAt = time.Time{}
	e.mu.Unlock()
}

// ---- Operatör aksiyonları: Kabul Et / Reddet / Kapat ----

// Answer, çalmakta olan çağrıyı kabul eder: RTP soketini açar, 200 OK +
// SDP cevabı gönderir, ses köprüsünü (arecord/aplay) başlatır.
func (e *Engine) Answer() error {
	e.mu.Lock()
	if e.state != "ringing" {
		e.mu.Unlock()
		return fmt.Errorf("şu an çalan bir çağrı yok")
	}
	remoteAddr := e.remoteAddr
	remoteRTP := e.remoteRTP
	via, fromH, toH, callID, cseq := e.viaHeader, e.fromHeader, e.toHeader, e.callID, e.cseqLine
	localIP := e.localIP
	sipUser := e.cfg.SipUsername
	signalPort := e.localSignalPortLocked()
	e.mu.Unlock()

	rtpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("RTP soketi açılamadı: %w", err)
	}
	localRTPPort := rtpConn.LocalAddr().(*net.UDPAddr).Port

	sdp := fmt.Sprintf(
		"v=0\r\no=rnvcs 0 0 IN IP4 %s\r\ns=RNVCS Panel\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n",
		localIP, localIP, localRTPPort,
	)
	// HATA DÜZELTMESİ (2026-07-22): Contact burada yanlışlıkla RTP portunu
	// gösteriyordu (medya soketi), SIP sinyalleşme portunu değil. Karşı taraf
	// (Asterisk B2BUA) sonraki isteklerini (özellikle BYE) bu Contact'a
	// gönderir — yanlış port yüzünden BYE hiç bize ulaşmıyordu, state
	// "active"de takılı kalıyordu ve bir sonraki INVITE'a hep 486 Busy Here
	// dönüyorduk ("tekrar arayamıyorum" hatası buradan geliyordu).
	contact := fmt.Sprintf("Contact: <sip:%s@%s:%d>", sipUser, localIP, signalPort)
	resp := buildResponse(200, "OK", via, fromH, toH, callID, cseq, []string{contact}, sdp)
	e.replyTo(remoteAddr, resp)

	e.mu.Lock()
	e.state = "active"
	e.rtpConn = rtpConn
	e.startedAt = time.Now()
	e.audioStop = make(chan struct{})
	stopCh := e.audioStop
	e.mu.Unlock()

	go e.rtpSendLoop(rtpConn, remoteRTP, stopCh)
	go e.rtpRecvLoop(rtpConn, stopCh)

	log.Println("==> Çağrı kabul edildi, ses köprüsü başlatıldı")
	return nil
}

// Reject, çalan bir çağrıyı reddeder (486 Busy Here).
func (e *Engine) Reject() error {
	e.mu.Lock()
	if e.state != "ringing" {
		e.mu.Unlock()
		return fmt.Errorf("şu an çalan bir çağrı yok")
	}
	remoteAddr, via, fromH, toH, callID, cseq := e.remoteAddr, e.viaHeader, e.fromHeader, e.toHeader, e.callID, e.cseqLine
	e.mu.Unlock()
	e.replyTo(remoteAddr, buildResponse(486, "Busy Here", via, fromH, toH, callID, cseq, nil, ""))
	log.Println("==> Çağrı reddedildi (486 Busy Here)")
	e.resetCallState()
	return nil
}

// HangupActive, aktif (cevaplanmış) bir çağrı varsa BYE göndererek kapatır.
func (e *Engine) HangupActive() {
	e.mu.Lock()
	if e.state == "" || e.state == "idle" {
		e.mu.Unlock()
		return
	}
	wasActive := e.state == "active"
	wasDialing := e.state == "dialing"
	weInitiated := e.weInitiated
	remoteAddr := e.remoteAddr
	callID := e.callID
	fromH := e.fromHeader
	toH := e.toHeader
	byeURI := e.dialogRemoteURI
	asteriskAddr := e.cfg.AsteriskAddr
	outBranch, outVia, outToURI, outFromURI := e.outBranch, e.outVia, e.outToURI, e.outFromURI
	e.mu.Unlock()

	if wasDialing && remoteAddr == nil && asteriskAddr != "" {
		if ra, err := net.ResolveUDPAddr("udp", asteriskAddr); err == nil {
			remoteAddr = ra
		}
	}

	if wasDialing && outBranch != "" {
		// Çağrı henüz cevaplanmadan operatör vazgeçti — CANCEL, iptal ettiği
		// INVITE ile AYNI Via/branch'i kullanmak zorunda (RFC 3261 §9).
		var b strings.Builder
		fmt.Fprintf(&b, "CANCEL %s SIP/2.0\r\n", outToURI)
		fmt.Fprintf(&b, "Via: %s\r\n", outVia)
		fmt.Fprintf(&b, "Max-Forwards: 70\r\n")
		fmt.Fprintf(&b, "From: <%s>\r\n", outFromURI)
		fmt.Fprintf(&b, "To: <%s>\r\n", outToURI)
		fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
		fmt.Fprintf(&b, "CSeq: 1 CANCEL\r\n")
		fmt.Fprintf(&b, "Content-Length: 0\r\n\r\n")
		if remoteAddr != nil {
			e.replyTo(remoteAddr, b.String())
			log.Println("==> CANCEL gönderildi (operatör çalarken vazgeçti)")
		}
		_ = outBranch // branch zaten outVia içinde taşınıyor
	} else if wasActive && remoteAddr != nil {
		branch := "z9hG4bK" + randHex(8)
		via := fmt.Sprintf("SIP/2.0/UDP %s:%d;branch=%s", e.localIP, e.localSignalPort(), branch)
		// HATA DÜZELTMESİ (2026-07-22): burada eskiden Request-URI yanlışlıkla
		// KENDİ kimliğimizi (sip:<bizim_sip_user>@host) hedefliyordu — BYE'ın
		// Request-URI'si karşı tarafı göstermeli (biz cevapladıysak arayanın
		// AOR'u, biz aradıysak hedef extension) — bkz. e.dialogRemoteURI.
		uri := byeURI
		if uri == "" {
			uri = outToURI // giden çağrıda dialogRemoteURI set edilmemiş olabilir, yedek
		}
		var b strings.Builder
		fmt.Fprintf(&b, "BYE %s SIP/2.0\r\n", uri)
		fmt.Fprintf(&b, "Via: %s\r\n", via)
		if weInitiated {
			// Diyaloğu biz başlattık (Dial) — kendi kimliğimiz From'da kaldı.
			fmt.Fprintf(&b, "From: %s\r\n", fromH)
			fmt.Fprintf(&b, "To: %s\r\n", toH)
		} else {
			// Diyaloğu karşı taraf başlattı, biz cevapladık — orijinal INVITE'ta
			// biz "To" idik; şimdi BİZ istek başlattığımız için kendi kimliğimiz
			// From'a geçer (RFC 3261 dialog kuralı).
			fmt.Fprintf(&b, "From: %s\r\n", toH)
			fmt.Fprintf(&b, "To: %s\r\n", fromH)
		}
		fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
		fmt.Fprintf(&b, "CSeq: 1 BYE\r\n")
		fmt.Fprintf(&b, "Content-Length: 0\r\n\r\n")
		e.replyTo(remoteAddr, b.String())
		log.Println("==> BYE gönderildi (operatör kapattı)")
	}
	e.resetCallState()
}

// LocalSignalPort, kalıcı SIP sinyalleşme soketinin dinlediği yerel portu
// döner (0: soket açık değil) — teşhis/loglama amaçlı.
func (e *Engine) LocalSignalPort() int {
	return e.localSignalPort()
}

func (e *Engine) localSignalPort() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.localSignalPortLocked()
}

// localSignalPortLocked, e.mu ZATEN tutulu durumdayken çağrılmalı (kilit
// almaz) — Answer()/Dial() gibi zaten kilit altında olan yerlerden kullanılır.
func (e *Engine) localSignalPortLocked() int {
	if e.conn == nil {
		return 0
	}
	return e.conn.LocalAddr().(*net.UDPAddr).Port
}

func (e *Engine) stopAudioBridge() {
	e.mu.Lock()
	stopCh := e.audioStop
	rtpConn := e.rtpConn
	recordCmd := e.recordCmd
	playCmd := e.playCmd
	e.audioStop = nil
	e.rtpConn = nil
	e.recordCmd = nil
	e.playCmd = nil
	e.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if recordCmd != nil && recordCmd.Process != nil {
		_ = recordCmd.Process.Kill()
	}
	if playCmd != nil && playCmd.Process != nil {
		_ = playCmd.Process.Kill()
	}
	if rtpConn != nil {
		rtpConn.Close()
	}
}
