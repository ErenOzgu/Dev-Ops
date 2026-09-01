// Package pjsip, yeni oluşturulan SIP hesaplarını Asterisk'in PJSIP
// konfigürasyonuna yansıtır (Bölüm 10.1: "tek gerçek kaynak PostgreSQL").
//
// Bölüm 10.19 kararı (2026-07-21, 10.22'nin geri alınmasıyla yeniden
// yürürlükte — 2026-08-25): SIP kimliği PANELE değil KULLANICIYA ait —
// AppendEndpoint kullanıcının sip_username/sip_password'üyle çağrılıyor
// (internal/api/users.go, UserSip). Fonksiyonun kendisi jenerik kaldı
// (herhangi bir <isim>+<şifre> çifti için endpoint/auth/aor üçlüsü yazar).
//
// İDEMPOTENCY DÜZELTMESİ (2026-08-25): AppendEndpoint artık append-only
// DEĞİL. Her blok, kaldırılabilir olması için bir "; RNVCS-USER:<isim>"
// işaret satırıyla başlıyor; aynı isim için tekrar çağrıldığında (SIP
// şifresi güncellendiğinde) önce eski blok bu işaretten bir sonraki
// işarete kadar dosyadan siliniyor, sonra yeni blok ekleniyor — dosyada
// asla aynı isim için birden fazla blok kalmıyor. RemoveEndpoint de aynı
// mekanizmayla bir kullanıcının SIP hesabını tamamen kaldırabiliyor
// (kullanıcı silindiğinde internal/api/users.go tarafından çağrılabilir).
//
// NOT: Bu dosyanın 10.22 öncesi (append-only) sürümüyle yazılmış eski
// bloklarda işaret satırı YOK — onlar bu fonksiyonla otomatik temizlenmez,
// elle silinmesi gerekir. Yeni yazılan/güncellenen her blok işaretli
// olduğundan bu sorun ileriye dönük olarak oluşmaz.
package pjsip

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const configPath = "/etc/asterisk/pjsip_rnvcs_dynamic.conf"

func marker(name string) string {
	return "; RNVCS-USER:" + name
}

// removeBlock, configPath içeriğinden (bellekte) verilen isme ait işaretli
// bloğu çıkarır ve kalan içeriği döner. Dosya yoksa boş içerikle başlar.
func removeBlock(name string) (string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	lines := strings.Split(string(raw), "\n")
	m := marker(name)
	var kept []string
	skipping := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "; RNVCS-USER:") {
			skipping = ln == m
			if skipping {
				continue
			}
		}
		if skipping {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n"), nil
}

// AppendEndpoint, verilen sipUsername + sipPassword için PJSIP
// endpoint/auth/aor bloklarını pjsip_rnvcs_dynamic.conf dosyasına yazar
// (idempotent — aynı sipUsername için tekrar çağrılırsa önce eski blok
// silinir, sonra yenisi eklenir) ve Asterisk'e "pjsip reload" gönderir.
// AOR bölüm adı endpoint ile aynı tutuluyor (Bölüm 10.13'teki register
// bug'ından ders — Asterisk REGISTER işleminde AOR'u literal kullanıcı
// adıyla eşleştiriyor).
func AppendEndpoint(sipUsername, sipPassword string) error {
	name := sipUsername
	remaining, err := removeBlock(name)
	if err != nil {
		return err
	}

	block := fmt.Sprintf(`
%s
[%s]
type=endpoint
context=rnvcs-panels
disallow=all
allow=ulaw,alaw,opus
auth=%s-auth
aors=%s
direct_media=no
rtp_symmetric=yes
force_rport=yes
rewrite_contact=yes
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
`, marker(name), name, name, name, name, name, name, name, sipPassword, name)

	newContent := strings.TrimRight(remaining, "\n") + block
	if remaining == "" {
		newContent = strings.TrimPrefix(block, "\n")
	}
	if err := os.WriteFile(configPath, []byte(newContent), 0640); err != nil {
		return err
	}
	return Reload()
}

// RemoveEndpoint, verilen sipUsername'e ait PJSIP bloğunu
// pjsip_rnvcs_dynamic.conf dosyasından tamamen kaldırır ve Asterisk'e
// "pjsip reload" gönderir. Kullanıcı silindiğinde ya da SIP hesabı elle
// kaldırıldığında çağrılmalı — çağrılmazsa eski (artık kullanılmayan)
// blok dosyada zararsız biçimde kalmaya devam eder.
func RemoveEndpoint(sipUsername string) error {
	remaining, err := removeBlock(sipUsername)
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, []byte(remaining), 0640); err != nil {
		return err
	}
	return Reload()
}

// Reload, Asterisk'e PJSIP config'i yeniden yüklemesini söyler.
func Reload() error {
	cmd := exec.Command("asterisk", "-rx", "pjsip reload")
	return cmd.Run()
}
