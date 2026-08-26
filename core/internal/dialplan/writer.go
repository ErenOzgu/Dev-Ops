// Package dialplan, çatal arama (ring group) tanımlarını Asterisk'in
// extensions.conf'una yansıtır (Bölüm 10.4 "Çatal arama" akışı).
package dialplan

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const configPath = "/etc/asterisk/extensions_rnvcs_dynamic.conf"

// AppendRingGroup, group_code'u panellerin arayabileceği bir extension
// olarak [rnvcs-panels] context'ine ekler: aranınca tüm panelleri aynı
// anda çaldırır (Dial'ın "ilk açan alır" doğal semantiği — bkz. Anayasa 10.4).
//
// NOT (MVP sınırlaması): pjsip.AppendEndpoint gibi bu da idempotent
// DEĞİLDİR — aynı group_code için tekrar çağrılırsa dosyada yinelenen
// bir extension bloğu oluşur. Ring group güncelleme/silme akışı henüz yok.
func AppendRingGroup(groupCode string, panelCodes []string, timeoutSec int) error {
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	dialTargets := make([]string, len(panelCodes))
	for i, code := range panelCodes {
		dialTargets[i] = "PJSIP/" + code
	}

	block := fmt.Sprintf(`
[rnvcs-panels]
exten => %s,1,NoOp(RNVCS catal arama: %s)
 same => n,Dial(%s,%d)
 same => n,Hangup()
`, groupCode, groupCode, strings.Join(dialTargets, "&"), timeoutSec)

	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return Reload()
}

// AppendDirectExtension, bir kullanıcının SIP kimliği (sip_username) atandığı/
// güncellendiği anda o kullanıcıyı DOĞRUDAN aranabilir hale getirir: extension
// adı SIP kullanıcı adının aynısı olur (örn. "kom" -> Dial(PJSIP/kom,30)).
//
// Bölüm 10.20'de (gerçek çağrı kabul/ret + ses köprüsü) bu adımın manuel
// yapıldığı fark edildi — bir kullanıcıya SIP hesabı atamak, onu otomatik
// olarak aranabilir yapmıyordu (sadece çatal arama grubu üyeliği extension
// üretiyordu). Bu fonksiyon internal/api/users.go'daki her iki SIP atama
// noktasından da (kullanıcı oluşturma + POST /api/users/sip) çağrılarak bu
// boşluğu kapatır — "tek gerçek kaynak PostgreSQL" ilkesiyle tutarlı: SIP
// hesabı olan HER kullanıcı otomatik olarak aranabilir olmalı.
//
// NOT (MVP sınırlaması): AppendRingGroup gibi bu da idempotent DEĞİLDİR —
// aynı sip_username için tekrar çağrılırsa dosyada yinelenen bir extension
// bloğu oluşur (Asterisk pratikte bir çakışmada SONUNCU tanımı kullanır,
// ama dosya zamanla şişer — üretimde idempotent bir writer'a geçilmeli).
func AppendDirectExtension(sipUsername string, timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	block := fmt.Sprintf(`
[rnvcs-panels]
exten => %s,1,NoOp(RNVCS: %s araniyor)
 same => n,Dial(PJSIP/%s,%d)
 same => n,Hangup()
`, sipUsername, sipUsername, sipUsername, timeoutSec)

	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return Reload()
}

// AppendPanelExtension, bir PANELİ (panel_code) doğrudan aranabilir yapar
// (Bölüm 10.22 panel-SIP modeli): numara arandığında panel çaldırılır;
// panel SIP'te kayıtlı değilse (kimse login değil) ya da timeout içinde
// kimse açmazsa VoiceMail() fallback'i devreye girer — arayan mesaj bırakır.
//
// Dial() sonrası ${DIALSTATUS}: CHANUNAVAIL/CONGESTION (panel kayıtsız,
// yani kimse login değil), NOANSWER (çaldı ama açılmadı), BUSY (meşgul —
// MVP tek çağrı). Hepsinde voicemail'e düşülür; ANSWER'da Dial zaten
// çağrıyı yürüttüğünden bu satırlara hiç gelinmez.
//
// NOT (MVP): idempotent DEĞİL (AppendRingGroup/AppendDirectExtension gibi).
// Panel senkronizasyon/onarım akışı tüm dosyayı baştan yazmalı.
func AppendPanelExtension(panelCode string, timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	block := fmt.Sprintf(`
[rnvcs-panels]
exten => %s,1,NoOp(RNVCS panel %s araniyor)
 same => n,Dial(PJSIP/%s,%d)
 same => n,NoOp(Panelde kimse yok / acilmadi — DIALSTATUS=${DIALSTATUS}, sesli mesaja yonlendiriliyor)
 same => n,VoiceMail(%s@rnvcs-vm,u)
 same => n,Hangup()
`, panelCode, panelCode, panelCode, timeoutSec, panelCode)

	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return Reload()
}

// AppendDeviceExtension, Interkom/IP Horn gibi bir "cihaz"ı (panels
// tablosunda device_type='INTERKOM'|'IP_HORN' olan satır) doğrudan
// aranabilir yapar. AppendPanelExtension'dan farkı: voicemail fallback'i
// YOK — sade Dial()+Hangup() (Anons Sistemi FKT madde 2: "Interkom
// extension'ı panel gibi voicemail fallback'ine düşmemeli"). Otomatik
// cevaplama davranışı Asterisk tarafında değil, cihazın kendi SIP
// client'ında/firmware'inde ayarlanmalı.
//
// NOT (MVP): AppendPanelExtension/AppendDirectExtension gibi idempotent
// DEĞİL — aynı deviceCode için tekrar çağrılırsa dosyada yinelenen blok
// oluşur.
func AppendDeviceExtension(deviceCode string, timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	block := fmt.Sprintf(`
[rnvcs-panels]
exten => %s,1,NoOp(RNVCS cihaz %s araniyor)
 same => n,Dial(PJSIP/%s,%d)
 same => n,Hangup()
`, deviceCode, deviceCode, deviceCode, timeoutSec)

	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return Reload()
}

// Reload, Asterisk'e dialplan'i yeniden yüklemesini söyler.
func Reload() error {
	cmd := exec.Command("asterisk", "-rx", "dialplan reload")
	return cmd.Run()
}
