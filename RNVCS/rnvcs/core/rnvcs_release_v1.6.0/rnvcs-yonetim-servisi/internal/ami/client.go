// Package ami, Asterisk Manager Interface (AMI) ile ham TCP protokolü
// üzerinden konuşur (Anons Sistemi FKT madde 4 — konferans: katılımcıları
// "Originate" action'ıyla arayıp ConfBridge extension'ına bağlamak için).
//
// SIFIR harici Go bağımlılığı ilkesiyle tutarlı (bkz. internal/pg) — AMI
// protokolü zaten basit bir metin protokolü (CRLF ile ayrılmış
// "Anahtar: Değer" satırları, boş satırla biten paketler), üçüncü parti
// kütüphaneye gerek yok.
//
// GÜVENLİK: AMI varsayılan olarak SADECE localhost'a bind edilir
// (/etc/asterisk/manager.conf, bindaddr=127.0.0.1) — CORE'un kendi Go
// process'i dışında hiçbir yerden erişilemez, dışarı açılmaz.
package ami

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

type Client struct {
	conn net.Conn
	r    *bufio.Reader
}

// Dial, AMI sunucusuna bağlanır ve login olur.
func Dial(addr, username, secret string) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("AMI bağlantısı kurulamadı (%s): %w", addr, err)
	}
	c := &Client{conn: conn, r: bufio.NewReader(conn)}

	// Banner satırını oku (örn. "Asterisk Call Manager/9.0.0").
	if _, err := c.r.ReadString('\n'); err != nil {
		conn.Close()
		return nil, fmt.Errorf("AMI banner okunamadı: %w", err)
	}

	resp, err := c.action(map[string]string{
		"Action":   "Login",
		"Username": username,
		"Secret":   secret,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp["Response"] != "Success" {
		conn.Close()
		return nil, fmt.Errorf("AMI login başarısız: %s", resp["Message"])
	}
	return c, nil
}

func (c *Client) Close() {
	_, _ = c.action(map[string]string{"Action": "Logoff"})
	_ = c.conn.Close()
}

// action, bir AMI action'ı gönderir ve TEK bir yanıt paketini (Response:
// satırıyla başlayan ilk paket) ayrıştırıp döner. Originate gibi bazı
// action'lar ayrıca asenkron event'ler de üretir ama bu MVP'de onları
// dinlemiyoruz — senkron "Response" yeterli (Async: false olmayan
// Originate, çağrının BAŞLATILDIĞINI, sonucunu değil bildirir).
func (c *Client) action(fields map[string]string) (map[string]string, error) {
	var b strings.Builder
	for k, v := range fields {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	if _, err := c.conn.Write([]byte(b.String())); err != nil {
		return nil, fmt.Errorf("AMI action gönderilemedi: %w", err)
	}

	resp := map[string]string{}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("AMI yanıtı okunamadı: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			resp[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return resp, nil
}

// Originate, bir SIP uç noktasını (sipUsername) arayıp, cevap verirse
// verilen context/exten/priority'ye bağlar (burada: konferans odası
// extension'ı). Timeout milisaniye cinsindendir.
func (c *Client) Originate(sipUsername, context, exten string, priority int, callerID string, timeoutMs int) error {
	resp, err := c.action(map[string]string{
		"Action":   "Originate",
		"Channel":  "PJSIP/" + sipUsername,
		"Context":  context,
		"Exten":    exten,
		"Priority": fmt.Sprintf("%d", priority),
		"CallerID": callerID,
		"Timeout":  fmt.Sprintf("%d", timeoutMs),
		"Async":    "true",
	})
	if err != nil {
		return err
	}
	if resp["Response"] != "Success" {
		return fmt.Errorf("Originate başarısız (%s): %s", sipUsername, resp["Message"])
	}
	return nil
}
