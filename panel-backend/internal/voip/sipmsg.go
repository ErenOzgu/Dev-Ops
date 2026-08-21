package voip

import (
	"fmt"
	"strings"
)

// headerMap, bir SIP mesajının header'larını (küçük harfli anahtar ->
// ham değer) çözer. Aynı isimden birden fazla header (örn. çoklu Via)
// bu MVP'de desteklenmiyor — RNVCS panelinin doğrudan Asterisk'e/Asterisk'ten
// register/çağrı aldığı basit senaryoda (aradaki proxy yok) yeterli.
func headerMap(lines []string) map[string]string {
	m := map[string]string{}
	for _, l := range lines {
		if l == "" {
			break
		}
		idx := strings.Index(l, ":")
		if idx < 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(l[:idx]))
		v := strings.TrimSpace(l[idx+1:])
		m[k] = v
	}
	return m
}

func sdpBody(msg string) string {
	idx := strings.Index(msg, "\r\n\r\n")
	if idx < 0 {
		return ""
	}
	return msg[idx+4:]
}

func startLine(msg string) string {
	idx := strings.Index(msg, "\r\n")
	if idx < 0 {
		return msg
	}
	return msg[:idx]
}

// ensureTag, bir To/From header değerine (henüz ;tag= içermiyorsa) verilen
// tag'i ekler; zaten varsa değeri olduğu gibi döner (dialog tutarlılığı için
// — aynı çağrı için üretilen tüm yanıtlarda AYNI to-tag kullanılmalı).
func ensureTag(headerVal, tag string) string {
	if strings.Contains(headerVal, "tag=") {
		return headerVal
	}
	return headerVal + ";tag=" + tag
}

// buildResponse, verilen dialog header'larını (Via/From/To/Call-ID/CSeq)
// aynen geri yansıtarak bir SIP yanıtı inşa eder — RFC 3261'in temel
// UAS davranışı budur (proxy yok, tek atlamalı doğrudan UDP).
func buildResponse(statusCode int, reason, via, from, to, callID, cseq string, extraHeaders []string, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", statusCode, reason)
	fmt.Fprintf(&b, "Via: %s\r\n", via)
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: %s\r\n", cseq)
	for _, h := range extraHeaders {
		fmt.Fprintf(&b, "%s\r\n", h)
	}
	if body != "" {
		fmt.Fprintf(&b, "Content-Type: application/sdp\r\n")
		fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	} else {
		fmt.Fprintf(&b, "Content-Length: 0\r\n\r\n")
	}
	return b.String()
}
