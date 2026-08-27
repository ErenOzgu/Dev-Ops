// Package iaxconf, IAX2 trunk tanımlarını Asterisk'in iax.conf'una
// yansıtır (Bölüm 10.4 "Trunk" akışı — sunucular arası çağrı köprüsü).
package iaxconf

import (
	"fmt"
	"os"
	"os/exec"
)

const configPath = "/etc/asterisk/iax_rnvcs_dynamic.conf"

// AppendTrunk, bir uzak RNVCS-CORE sunucusuyla simetrik IAX2 trunk
// tanımını iax_rnvcs_dynamic.conf'a ekler ve Asterisk'e "iax2 reload"
// gönderir.
//
// NOT (MVP sınırlaması): pjsip.AppendEndpoint gibi idempotent DEĞİLDİR —
// aynı trunk_name için tekrar çağrılırsa yinelenen blok oluşur.
func AppendTrunk(trunkName, remoteHost string, remotePort int, secret string) error {
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	block := fmt.Sprintf(`
[%s]
type=friend
host=%s
port=%d
secret=%s
context=rnvcs-trunks
qualify=yes
`, trunkName, remoteHost, remotePort, secret)

	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return Reload()
}

// Reload, Asterisk'e IAX2 config'i yeniden yüklemesini söyler.
func Reload() error {
	cmd := exec.Command("asterisk", "-rx", "iax2 reload")
	return cmd.Run()
}
