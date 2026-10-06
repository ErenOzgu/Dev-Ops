// Package voicemail, panel başına bir Asterisk voicemail kutusu (mailbox)
// tanımını voicemail.conf'a yansıtır (Bölüm 10.22: boş panele arama →
// mesaj bırakma akışı).
//
// Model: her panelin panel_code'u ile aynı adlı bir mailbox'ı vardır
// (bağlam/context = rnvcs-vm). Kimse o panele login değilken (panel SIP'te
// kayıtlı değilken) o numara arandığında dialplan Dial() başarısız olur ve
// VoiceMail(panel_code@rnvcs-vm) fallback'i devreye girer — arayan "mesaj
// bırakın" anonsunu duyar. Mesaj /var/spool/asterisk/voicemail/rnvcs-vm/
// <panel_code>/INBOX/ altına düşer; panele bir sonraki login olan operatör
// bunu web arayüzünden (Yönetim Servisi /api/voicemail) görür ve dinler.
//
// NOT (MVP): mailbox PIN'i sabit "0000" — operatörler telefon tuşuyla değil
// web arayüzünden eriştiği için PIN pratikte kullanılmıyor; yine de
// VoiceMail()/voicemail.conf bir mailbox tanımı ister.
package voicemail

import (
	"fmt"
	"os"
	"os/exec"
)

const configPath = "/etc/asterisk/voicemail_rnvcs_dynamic.conf"

// AppendMailbox, panel_code için rnvcs-vm context'inde bir mailbox tanımı
// EKLER ve "voicemail reload" gönderir. displayName, sesli mesaj listesinde
// ve bildirimde görünen ada karşılık gelir.
//
// NOT (MVP): idempotent DEĞİL — aynı panel_code için tekrar çağrılırsa
// dosyada yinelenen satır oluşur (Asterisk sonuncuyu kullanır). Panel
// silme/yeniden-üretme akışında tüm dosya baştan yazılmalı (bkz.
// RewriteAll — Panels senkronizasyon/onarım ucu bunu kullanır).
func AppendMailbox(panelCode, displayName string) error {
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(mailboxLine(panelCode, displayName)); err != nil {
		return err
	}
	return Reload()
}

// RewriteAll, tüm panel mailbox'larını sıfırdan (idempotent) yazar —
// mevcut panelleri Asterisk'e senkronlarken / bir panel silindiğinde
// çağrılır. context başlığı bir kez, ardından her panel için tek satır.
func RewriteAll(panels map[string]string) error {
	content := "; RNVCS — otomatik üretildi, elle düzenlemeyin (Bölüm 10.22)\n[rnvcs-vm]\n"
	for code, name := range panels {
		content += mailboxLine(code, name)
	}
	if err := os.WriteFile(configPath, []byte(content), 0640); err != nil {
		return err
	}
	return Reload()
}

func mailboxLine(panelCode, displayName string) string {
	if displayName == "" {
		displayName = "Panel " + panelCode
	}
	// mailbox => PIN, isim  (e-posta alanı boş bırakıldı — SMTP yok)
	return fmt.Sprintf("%s => 0000,%s\n", panelCode, displayName)
}

// Reload, Asterisk'e voicemail konfigürasyonunu yeniden yüklemesini söyler.
func Reload() error {
	return exec.Command("asterisk", "-rx", "voicemail reload").Run()
}
