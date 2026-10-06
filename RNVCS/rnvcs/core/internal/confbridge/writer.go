// Package confbridge, dinamik konferans odalarını Asterisk'in dialplan'ine
// yansıtır (Anons Sistemi FKT madde 4 — konferans görüşmesi).
//
// Model: her konferans odası, dialplan'de basit bir extension olarak var
// olur — "exten => <roomCode>,1,ConfBridge(<roomCode>)". res_confbridge.so
// varsayılan bridge_profile/user_profile'ı kullanır (confbridge.conf'ta
// özelleştirme MVP için şart değil). Katılımcılar CORE'daki
// internal/api/conference.go tarafından AMI Originate ile bu odaya
// bağlanır — bu paket sadece odanın dialplan tanımını yazar.
package confbridge

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const configPath = "/etc/asterisk/extensions_rnvcs_dynamic.conf"

func marker(roomCode string) string {
	return "; RNVCS-CONF:" + roomCode
}

// AppendConferenceRoom, roomCode için [rnvcs-panels] context'inde bir
// ConfBridge() extension'ı yazar (idempotent — pjsip.AppendEndpoint'teki
// işaretli-blok deseniyle aynı: aynı roomCode için tekrar çağrılırsa önce
// eski blok silinir).
func AppendConferenceRoom(roomCode string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	m := marker(roomCode)
	var kept []string
	skipping := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "; RNVCS-CONF:") {
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
	remaining := strings.Join(kept, "\n")

	block := fmt.Sprintf(`
%s
[rnvcs-panels]
exten => %s,1,NoOp(RNVCS konferans: %s)
 same => n,ConfBridge(%s)
 same => n,Hangup()
`, m, roomCode, roomCode, roomCode)

	newContent := strings.TrimRight(remaining, "\n") + block
	if remaining == "" {
		newContent = strings.TrimPrefix(block, "\n")
	}
	if err := os.WriteFile(configPath, []byte(newContent), 0640); err != nil {
		return err
	}
	return Reload()
}

// Reload, Asterisk'e dialplan'i yeniden yüklemesini söyler.
func Reload() error {
	return exec.Command("asterisk", "-rx", "dialplan reload").Run()
}
