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

// sendAction, bir AMI action paketini yazar (yanıt okumaz).
func (c *Client) sendAction(fields map[string]string) error {
	var b strings.Builder
	for k, v := range fields {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	if _, err := c.conn.Write([]byte(b.String())); err != nil {
		return fmt.Errorf("AMI action gönderilemedi: %w", err)
	}
	return nil
}

// readPacket, AMI'den TEK bir paketi (boş satırla biten "Anahtar: Değer"
// bloğu) okur — bu bir "Response:" paketi de olabilir, bir "Event:" paketi
// de (ConfbridgeList gibi action'lar birden çok Event paketi + en sonda
// tamamlanma event'i döner).
func (c *Client) readPacket() (map[string]string, error) {
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

// action, bir AMI action'ı gönderir ve TEK bir yanıt paketini (Response:
// satırıyla başlayan ilk paket) ayrıştırıp döner. Originate gibi bazı
// action'lar ayrıca asenkron event'ler de üretir ama bu MVP'de onları
// dinlemiyoruz — senkron "Response" yeterli (Async: false olmayan
// Originate, çağrının BAŞLATILDIĞINI, sonucunu değil bildirir).
func (c *Client) action(fields map[string]string) (map[string]string, error) {
	if err := c.sendAction(fields); err != nil {
		return nil, err
	}
	return c.readPacket()
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

// ConfbridgeMember, bir konferans odasındaki tek bir katılımcının AMI'den
// dönen kanal bilgisi. Channel her zaman "PJSIP/<sipHedefi>-<uniqueid>"
// biçimindedir (Originate'in Channel parametresiyle birebir eşleşir) —
// yani hangi katılımcının hangisi olduğu Channel'ın "PJSIP/" ile "-"
// arasındaki kısmından çıkarılabilir (bkz. MemberCode()).
type ConfbridgeMember struct {
	Channel     string
	CallerIDNum string
}

// MemberCode, Channel alanından sipHedefi'ni (users.sip_username ya da
// panels.panel_code) çıkarır — "PJSIP/konfhorn01-00000123" -> "konfhorn01".
func (m ConfbridgeMember) MemberCode() string {
	s := strings.TrimPrefix(m.Channel, "PJSIP/")
	if i := strings.LastIndex(s, "-"); i > 0 {
		return s[:i]
	}
	return s
}

// ConfbridgeList, roomCode odasındaki güncel katılımcıları AMI'den okur.
// Asterisk bu action için önce bir "Response: Success" paketi, sonra her
// katılımcı için bir "Event: ConfbridgeList" paketi, en sonda da
// "Event: ConfbridgeListComplete" paketi döner.
func (c *Client) ConfbridgeList(conference string) ([]ConfbridgeMember, error) {
	if err := c.sendAction(map[string]string{
		"Action":     "ConfbridgeList",
		"Conference": conference,
	}); err != nil {
		return nil, err
	}
	first, err := c.readPacket()
	if err != nil {
		return nil, err
	}
	if first["Response"] != "Success" {
		// Oda boş/yok ise Asterisk burada Error döner — boş liste say.
		return nil, nil
	}
	var members []ConfbridgeMember
	for {
		pkt, err := c.readPacket()
		if err != nil {
			return nil, err
		}
		if pkt["Event"] == "ConfbridgeListComplete" {
			break
		}
		if pkt["Event"] == "ConfbridgeList" && pkt["Channel"] != "" {
			members = append(members, ConfbridgeMember{Channel: pkt["Channel"], CallerIDNum: pkt["CallerIDNum"]})
		}
	}
	return members, nil
}

// ConfbridgeKick, roomCode odasından tek bir kanalı (ya da channel="all"
// verilirse odadaki HERKESİ) çıkarır. IP Horn/Interkom gibi kendi
// başına "kapat" butonu olmayan cihazlar konferansta asılı kaldığında
// kullanılır (Konferans — Kapatma Ekranı ve Kısayol Eksiklikleri notu,
// 2026-08-27 devam).
func (c *Client) ConfbridgeKick(conference, channel string) error {
	resp, err := c.action(map[string]string{
		"Action":     "ConfbridgeKick",
		"Conference": conference,
		"Channel":    channel,
	})
	if err != nil {
		return err
	}
	if resp["Response"] != "Success" {
		return fmt.Errorf("ConfbridgeKick başarısız (%s): %s", channel, resp["Message"])
	}
	return nil
}

// ConfbridgePlayFile, roomCode odasındaki tek bir kanala (ör. odada tek
// başına kalan son katılımcı) bir ses dosyası çalar — "tek kişi kaldınız,
// oda kapatılıyor" bildirimi için kullanılır (bkz. confwatch paketi).
// Bazı eski Asterisk sürümlerinde bu action olmayabilir; çağıran taraf
// hata durumunda sese takılmadan devam etmeli (kick yine de yapılmalı).
func (c *Client) ConfbridgePlayFile(conference, channel, file string) error {
	resp, err := c.action(map[string]string{
		"Action":     "ConfbridgePlayFile",
		"Conference": conference,
		"Channel":    channel,
		"File":       file,
	})
	if err != nil {
		return err
	}
	if resp["Response"] != "Success" {
		return fmt.Errorf("ConfbridgePlayFile başarısız (%s): %s", channel, resp["Message"])
	}
	return nil
}

// EnableEvents, bu bağlantı üzerinde tüm AMI event'lerinin akmasını
// garantiye alır (AMI login varsayılan olarak zaten event gönderir ama
// bazı manager.conf yapılandırmalarında kapalı olabilir — açıkça isteriz).
func (c *Client) EnableEvents() error {
	resp, err := c.action(map[string]string{
		"Action":    "Events",
		"EventMask": "on",
	})
	if err != nil {
		return err
	}
	if resp["Response"] != "Success" {
		return fmt.Errorf("Events action başarısız: %s", resp["Message"])
	}
	return nil
}

// ListenEvents, bağlantı kapanana/hata verene kadar gelen HER paketi okur
// ve "Event:" alanı dolu olanları handler'a iletir (Response paketlerini
// yok sayar). Bloklayıcıdır — kendi goroutine'inde çağrılmalı. confwatch
// paketi bunu, ConfbridgeLeave event'lerini izleyip odada tek kişi
// kaldığında odayı kapatmak için kullanır.
func (c *Client) ListenEvents(handler func(map[string]string)) error {
	for {
		pkt, err := c.readPacket()
		if err != nil {
			return err
		}
		if pkt["Event"] != "" {
			handler(pkt)
		}
	}
}
