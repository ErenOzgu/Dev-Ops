// Package pjsip, yeni oluşturulan SIP hesaplarını Asterisk'in PJSIP
// konfigürasyonuna yansıtır (Bölüm 10.1: "tek gerçek kaynak PostgreSQL").
//
// Bölüm 10.19 kararı (2026-07-21): SIP kimliği artık PANELE değil
// KULLANICIYA ait — bu yüzden AppendEndpoint artık panel_code değil,
// kullanıcının sip_username/sip_password'ü ile çağrılıyor
// (internal/api/users.go, UserSip). Fonksiyonun kendisi jenerik kaldı
// (herhangi bir <isim>+<şifre> çifti için endpoint/auth/aor üçlüsü yazar).
package pjsip

import (
	"fmt"
	"os"
	"os/exec"
)

const configPath = "/etc/asterisk/pjsip_rnvcs_dynamic.conf"

// AppendEndpoint, verilen sipUsername + sipPassword için PJSIP
// endpoint/auth/aor bloklarını pjsip_rnvcs_dynamic.conf dosyasına EKLER
// ve Asterisk'e "pjsip reload" gönderir. AOR bölüm adı endpoint ile aynı
// tutuluyor (Bölüm 10.13'teki register bug'ından ders — Asterisk REGISTER
// işleminde AOR'u literal kullanıcı adıyla eşleştiriyor).
//
// NOT (MVP sınırlaması): Bu fonksiyon idempotent DEĞİLDİR — aynı
// sipUsername için tekrar çağrılırsa dosyada YİNELENEN bir blok oluşur.
// Güncelleme/silme akışı henüz yazılmadı.
func AppendEndpoint(sipUsername, sipPassword string) error {
	panelCode := sipUsername
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	block := fmt.Sprintf(`
[%s]
type=endpoint
context=rnvcs-panels
disallow=all
allow=ulaw,alaw,opus
auth=%s-auth
aors=%s
direct_media=no
callerid=%s <%s>

[%s-auth]
type=auth
auth_type=userpass
username=%s
password=%s

[%s]
type=aor
max_contacts=1
remove_existing=yes
`, panelCode, panelCode, panelCode, panelCode, panelCode, panelCode, panelCode, sipPassword, panelCode)

	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return Reload()
}

// Reload, Asterisk'e PJSIP config'i yeniden yüklemesini söyler.
func Reload() error {
	cmd := exec.Command("asterisk", "-rx", "pjsip reload")
	return cmd.Run()
}
