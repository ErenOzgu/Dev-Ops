// outbound.go — panelden GİDEN çağrı (Bölüm 10.20'nin ilk sürümünde sadece
// gelen çağrı destekleniyordu; bu, panel_app.html'deki dialpad'in ihtiyaç
// duyduğu "başka bir kullanıcıyı ara" özelliğini ekler).
//
// Çağrı, Asterisk'e (register olduğumuz aynı adrese) bir INVITE göndererek
// başlatılır — Asterisk bir B2BUA olduğu için (endpoint'lerde direct_media=no,
// bkz. internal/pjsip/writer.go) bizim gördüğümüz RTP hedefi her zaman
// Asterisk'in kendi medya relay adresidir, hedef kullanıcının kendisi değil;
// bu da gelen/giden çağrı ses köprüsü kodunun ortak kalabilmesini sağlıyor.
//
// DIGEST RETRY (2026-07-22): gerçek donanım testinde bazı endpoint'ler için
// Asterisk'in outbound INVITE'ı da 401/407 ile challenge ettiği görüldü
// (örn. "1011" — eski manuel pjsip.conf girdisi, kom/ast gibi otomatik
// AppendEndpoint akışından geçmemiş). REGISTER'daki digest hesaplama
// mantığının aynısı (method=INVITE, uri=hedef URI) burada da kullanılarak
// tek seferlik bir 401/407 retry'ı yapılır — bkz. aşağıdaki "DIGEST RETRY"
// bloğu. Retry de reddedilirse (ör. gerçekten yanlış kimlik bilgisi) net
// bir hata döndürülür.
package voip

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Dial, bu panelin register olduğu SIP kimliğiyle targetExt'e (başka bir
// kullanıcının sip_username'i ya da tanımlı bir dialplan extension'ı)
// giden bir çağrı başlatır.
func (e *Engine) Dial(targetExt string) error {
	targetExt = strings.TrimSpace(targetExt)
	if targetExt == "" {
		return fmt.Errorf("aranacak numara/kullanıcı adı boş olamaz")
	}

	e.mu.Lock()
	if e.state != "idle" {
		e.mu.Unlock()
		return fmt.Errorf("zaten bir çağrı sürüyor, önce onu kapatın")
	}
	if e.conn == nil {
		e.lastError = "SIP soketi açık değil (henüz register olunmadı)"
		e.mu.Unlock()
		return fmt.Errorf("SIP soketi açık değil (henüz register olunmadı)")
	}
	conn := e.conn
	cfg := e.cfg
	localIP := e.localIP
	e.state = "dialing"
	e.fromDisplay = targetExt
	e.lastError = ""
	e.mu.Unlock()

	remoteAddr, err := net.ResolveUDPAddr("udp", cfg.AsteriskAddr)
	if err != nil {
		e.resetCallStateErr("asterisk adresi çözümlenemedi: " + err.Error())
		return fmt.Errorf("asterisk adresi çözümlenemedi: %w", err)
	}

	rtpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		e.resetCallStateErr("RTP soketi açılamadı: " + err.Error())
		return fmt.Errorf("RTP soketi açılamadı: %w", err)
	}

	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	localHostPort := fmt.Sprintf("%s:%d", localIP, localPort)
	host := strings.Split(cfg.AsteriskAddr, ":")[0]
	fromURI := fmt.Sprintf("sip:%s@%s", cfg.SipUsername, host)
	toURI := fmt.Sprintf("sip:%s@%s", targetExt, host)
	contact := fmt.Sprintf("sip:%s@%s", cfg.SipUsername, localHostPort)
	callID := randHex(8) + "@rnvcs-panel-backend"
	fromTag := randHex(6)
	localRTPPort := rtpConn.LocalAddr().(*net.UDPAddr).Port

	sdpOffer := fmt.Sprintf(
		"v=0\r\no=rnvcs 0 0 IN IP4 %s\r\ns=RNVCS Panel\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n",
		localIP, localIP, localRTPPort,
	)

	branch := "z9hG4bK" + randHex(8)
	viaLine := fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", localHostPort, branch)

	e.mu.Lock()
	e.callID = callID
	e.outBranch = branch
	e.outVia = viaLine
	e.outToURI = toURI
	e.outFromURI = fromURI
	e.mu.Unlock()

	cseq := 1
	msg := buildInviteReqWithVia(viaLine, toURI, fromURI, contact, callID, fromTag, cseq, "", sdpOffer)
	finalResp, err := e.inviteTransaction(conn, remoteAddr, msg)

	// YARIŞ KORUMASI: bu inviteTransaction() 30 saniyeye kadar sürebilir.
	// Bu sırada operatör CANCEL ile vazgeçip HEMEN başka bir çağrı
	// başlatmış olabilir (yeni bir callID ile) — bu durumda gecikmiş
	// yanıtımız engine state'ini artık bize ait olmayan/yeni bir çağrının
	// üzerine yazmamalı. callID hâlâ bizimkiyle eşleşmiyorsa sessizce çık.
	if !e.stillOurDial(callID) {
		rtpConn.Close()
		return fmt.Errorf("çağrı bu sırada iptal edildi ya da değişti")
	}
	if err != nil {
		rtpConn.Close()
		e.resetCallStateErr(err.Error())
		return err
	}

	code, reason := statusCode(finalResp)
	// Her final yanıt (2xx da dahil, RFC 3261 diyalog kurallarına göre 2xx'in
	// ACK'i AYRI bir uçtan uca işlemdir) için ACK göndermek gerekir; niyeti
	// basitleştirmek için burada TEK bir ACK inşasını iki durumda da (2xx/
	// non-2xx) kullanıyoruz — aradaki fark sadece Route/Contact ayrıntıları
	// ki bu basit doğrudan-Asterisk topolojisinde önemli değil.
	toTag := extractToTag(finalResp)
	ackMsg := buildAckReq(toURI, fromURI, contact, localHostPort, callID, fromTag, toTag, cseq)
	_, _ = conn.WriteToUDP([]byte(ackMsg), remoteAddr)

	// DIGEST RETRY (2026-07-22 eklendi): gerçek donanımda "1011" gibi
	// REGISTER'daki gibi Asterisk'in bazı endpoint'ler için outbound INVITE'ı
	// da 401/407 ile challenge ettiği görüldü (ör. auth bölümü INVITE'ları
	// da kapsayan endpoint'ler). REGISTER'daki digest hesaplama mantığının
	// aynısı burada da (method=INVITE, uri=toURI) kullanılarak YENİ bir
	// branch/CSeq ile tek seferlik retry yapılır.
	if code == 401 || code == 407 {
		challenge := extractAuthChallenge(finalResp)
		if challenge == "" {
			rtpConn.Close()
			errMsg := "Asterisk yetkilendirme (401/407) istedi ama WWW-Authenticate/Proxy-Authenticate bulunamadı"
			e.resetCallStateErr(errMsg)
			return fmt.Errorf(errMsg)
		}
		params := parseAuthHeader(challenge)
		realm := params["realm"]
		nonce := params["nonce"]
		qop := params["qop"]
		cnonce := randHex(8)
		nc := "00000001"

		ha1 := md5hex(cfg.SipUsername + ":" + realm + ":" + cfg.SipPassword)
		ha2 := md5hex("INVITE:" + toURI)

		var authHeader string
		if qop != "" {
			response := md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
			authHeader = fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5, qop=auth, nc=%s, cnonce="%s"`,
				cfg.SipUsername, realm, nonce, toURI, response, nc, cnonce)
		} else {
			response := md5hex(ha1 + ":" + nonce + ":" + ha2)
			authHeader = fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5`,
				cfg.SipUsername, realm, nonce, toURI, response)
		}

		cseq++
		branch = "z9hG4bK" + randHex(8)
		viaLine = fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", localHostPort, branch)
		e.mu.Lock()
		e.outBranch = branch
		e.outVia = viaLine
		e.mu.Unlock()

		retryMsg := buildInviteReqWithVia(viaLine, toURI, fromURI, contact, callID, fromTag, cseq, authHeader, sdpOffer)
		retryResp, err := e.inviteTransaction(conn, remoteAddr, retryMsg)
		if !e.stillOurDial(callID) {
			rtpConn.Close()
			return fmt.Errorf("çağrı bu sırada iptal edildi ya da değişti")
		}
		if err != nil {
			rtpConn.Close()
			e.resetCallStateErr(err.Error())
			return err
		}
		finalResp = retryResp
		code, reason = statusCode(finalResp)
		toTag = extractToTag(finalResp)
		ackMsg = buildAckReq(toURI, fromURI, contact, localHostPort, callID, fromTag, toTag, cseq)
		_, _ = conn.WriteToUDP([]byte(ackMsg), remoteAddr)

		if code == 401 || code == 407 {
			rtpConn.Close()
			errMsg := "Asterisk yetkilendirmeyi (401/407) tekrar denemeden sonra da reddetti — kullanıcı adı/parola hatalı olabilir"
			e.resetCallStateErr(errMsg)
			return fmt.Errorf(errMsg)
		}
	}
	if code != 200 {
		rtpConn.Close()
		errMsg := fmt.Sprintf("çağrı bağlanamadı: %d %s", code, reason)
		e.resetCallStateErr(errMsg)
		return fmt.Errorf(errMsg)
	}

	body := sdpBody(finalResp)
	connMatch := sdpConnRe.FindStringSubmatch(body)
	mediaMatch := sdpMediaRe.FindStringSubmatch(body)
	if connMatch == nil || mediaMatch == nil {
		rtpConn.Close()
		e.resetCallStateErr("200 OK içinde geçerli SDP (ses adresi) bulunamadı")
		return fmt.Errorf("200 OK içinde geçerli SDP (ses adresi) bulunamadı")
	}
	remoteIP := connMatch[1]
	remotePort, _ := strconv.Atoi(mediaMatch[1])
	remoteRTP := &net.UDPAddr{IP: net.ParseIP(remoteIP), Port: remotePort}

	if !e.stillOurDial(callID) {
		rtpConn.Close()
		// 200 OK geldi ama operatör bu sırada vazgeçmiş/başka çağrı başlatmış —
		// nezaketen az önce kurulan bu çağrıyı da BYE ile kapatalım ki Asterisk
		// tarafında asılı kalmasın.
		toTag := extractToTag(finalResp)
		bye := buildByeForAbandonedDial(toURI, fromURI, contact, localHostPort, callID, fromTag, toTag)
		_, _ = conn.WriteToUDP([]byte(bye), remoteAddr)
		return fmt.Errorf("çağrı bağlandı ama bu sırada operatör başka bir işlem yaptı, kapatıldı")
	}

	e.mu.Lock()
	e.state = "active"
	e.weInitiated = true
	e.dialogRemoteURI = toURI
	e.remoteAddr = remoteAddr
	e.remoteRTP = remoteRTP
	e.rtpConn = rtpConn
	e.startedAt = time.Now()
	e.viaHeader = "" // giden çağrıda BYE'ı biz başlatırız, Via'ya gerek yok
	e.toHeader = fmt.Sprintf("<%s>;tag=%s", toURI, toTag)
	e.fromHeader = fmt.Sprintf("<%s>;tag=%s", fromURI, fromTag)
	e.audioStop = make(chan struct{})
	stopCh := e.audioStop
	e.mu.Unlock()

	go e.rtpSendLoop(rtpConn, remoteRTP, stopCh)
	go e.rtpRecvLoop(rtpConn, stopCh)

	return nil
}

// stillOurDial, bu Dial() çağrısının başlattığı çağrının hâlâ "güncel" olup
// olmadığını kontrol eder. inviteTransaction() ringTimeout kadar (30sn)
// sürebildiğinden, bu süre içinde operatör CANCEL/Hangup ile vazgeçip HEMEN
// başka bir çağrı başlatmış olabilir — bu durumda gecikmiş yanıtımız artık
// bize ait olmayan (yeni) bir çağrının state'ini ezmemeli.
func (e *Engine) stillOurDial(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callID == callID
}

// buildByeForAbandonedDial, 200 OK'in tam operatör vazgeçtiği anda gelmesi
// gibi nadir bir yarış durumunda, az önce kurulmuş ama artık kimsenin
// istemediği çağrıyı nazikçe kapatmak için kullanılır.
func buildByeForAbandonedDial(toURI, fromURI, contact, localHostPort, callID, fromTag, toTag string) string {
	branch := "z9hG4bK" + randHex(8)
	var b strings.Builder
	fmt.Fprintf(&b, "BYE %s SIP/2.0\r\n", toURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s\r\n", localHostPort, branch)
	fmt.Fprintf(&b, "Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: <%s>;tag=%s\r\n", fromURI, fromTag)
	fmt.Fprintf(&b, "To: <%s>;tag=%s\r\n", toURI, toTag)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: 2 BYE\r\n")
	fmt.Fprintf(&b, "Content-Length: 0\r\n\r\n")
	_ = contact
	return b.String()
}

// inviteTransaction, bir INVITE isteği gönderir ve provizyonel (1xx)
// yanıtları görmezden gelip FİNAL yanıtı (>=200) bekler. Çağrı çalarken
// (180 Ringing) uzun sürebileceği için her provizyonel yanıtta zaman aşımı
// süresi sıfırlanır.
func (e *Engine) inviteTransaction(conn *net.UDPConn, remote *net.UDPAddr, msg string) (string, error) {
	if _, err := conn.WriteToUDP([]byte(msg), remote); err != nil {
		return "", fmt.Errorf("INVITE gönderilemedi: %w", err)
	}
	const ringTimeout = 30 * time.Second
	deadline := time.Now().Add(ringTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("çağrı zaman aşımına uğradı (yanıt yok)")
		}
		select {
		case resp := <-e.respCh:
			code, _ := statusCode(resp)
			if code >= 200 {
				return resp, nil
			}
			// 1xx (Trying/Ringing) — beklemeye devam, süreyi yenile
			deadline = time.Now().Add(ringTimeout)
		case <-time.After(remaining):
			return "", fmt.Errorf("çağrı zaman aşımına uğradı (yanıt yok)")
		}
	}
}

var toTagRe = regexp.MustCompile(`[Tt]o:.*?;tag=([\w.\-]+)`)

func extractToTag(msg string) string {
	m := toTagRe.FindStringSubmatch(msg)
	if m == nil {
		return randHex(6)
	}
	return m[1]
}

// buildInviteReqWithVia, çağıranın (Dial) önceden ürettiği Via satırını
// (branch dahil) kullanır — bu branch, çağrı henüz cevaplanmadan operatör
// vazgeçerse gönderilecek CANCEL'de AYNEN tekrar kullanılmak zorundadır
// (RFC 3261 §9: CANCEL, iptal ettiği isteğin branch'iyle eşleşmelidir).
func buildInviteReqWithVia(viaLine, toURI, fromURI, contact, callID, fromTag string, cseq int, authHeader, sdp string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "INVITE %s SIP/2.0\r\n", toURI)
	fmt.Fprintf(&b, "Via: %s\r\n", viaLine)
	fmt.Fprintf(&b, "Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: <%s>;tag=%s\r\n", fromURI, fromTag)
	fmt.Fprintf(&b, "To: <%s>\r\n", toURI)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: %d INVITE\r\n", cseq)
	fmt.Fprintf(&b, "Contact: <%s>\r\n", contact)
	if authHeader != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", authHeader)
	}
	fmt.Fprintf(&b, "User-Agent: rnvcs-panel-backend/1.0 (Bölüm 10.20)\r\n")
	fmt.Fprintf(&b, "Content-Type: application/sdp\r\n")
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(sdp), sdp)
	return b.String()
}

func buildAckReq(toURI, fromURI, contact, localHostPort, callID, fromTag, toTag string, cseq int) string {
	branch := "z9hG4bK" + randHex(8)
	var b strings.Builder
	fmt.Fprintf(&b, "ACK %s SIP/2.0\r\n", toURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s\r\n", localHostPort, branch)
	fmt.Fprintf(&b, "Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: <%s>;tag=%s\r\n", fromURI, fromTag)
	fmt.Fprintf(&b, "To: <%s>;tag=%s\r\n", toURI, toTag)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: %d ACK\r\n", cseq)
	fmt.Fprintf(&b, "Content-Length: 0\r\n\r\n")
	return b.String()
}
