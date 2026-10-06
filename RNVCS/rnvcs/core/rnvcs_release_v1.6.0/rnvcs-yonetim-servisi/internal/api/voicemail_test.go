package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListVoicemail, sahte bir voicemail spool klasörü kurup listeleme +
// metadata çözümlemenin doğru çalıştığını (ve yeni mesajın önce geldiğini)
// doğrular. Bölüm 10.22.
func TestListVoicemail(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RNVCS_VOICEMAIL_DIR", root)

	inbox := filepath.Join(root, "1005", "INBOX")
	if err := os.MkdirAll(inbox, 0755); err != nil {
		t.Fatal(err)
	}

	// İki mesaj: msg0000 (eski) ve msg0001 (yeni). Liste yeni->eski olmalı.
	write := func(id, callerid, origtime, dur string) {
		meta := "[message]\ncallerid=\"" + callerid + "\" <" + callerid + ">\norigtime=" + origtime + "\nduration=" + dur + "\n"
		if err := os.WriteFile(filepath.Join(inbox, "msg"+id+".txt"), []byte(meta), 0644); err != nil {
			t.Fatal(err)
		}
		// ses dosyası da olsun (varlığı yeterli)
		_ = os.WriteFile(filepath.Join(inbox, "msg"+id+".wav"), []byte("RIFF"), 0644)
	}
	write("0000", "1001", "1753000000", "7")
	write("0001", "1002", "1753009999", "3")

	msgs := listVoicemail("1005")
	if len(msgs) != 2 {
		t.Fatalf("2 mesaj bekleniyordu, %d bulundu", len(msgs))
	}
	if msgs[0].ID != "0001" {
		t.Errorf("en yeni mesaj (0001) ilk sırada olmalı, gelen: %s", msgs[0].ID)
	}
	if msgs[0].CallerID != "1002" {
		t.Errorf("callerid çözümlenemedi: %q", msgs[0].CallerID)
	}
	if msgs[1].Duration != 7 {
		t.Errorf("duration çözümlenemedi: %d", msgs[1].Duration)
	}

	// Olmayan panel -> boş liste (hata değil).
	if got := listVoicemail("9999"); len(got) != 0 {
		t.Errorf("olmayan panel için boş liste bekleniyordu, %d geldi", len(got))
	}
}

// TestPanelAccessAllowed, yetki denetimini doğrular.
func TestPanelAccessAllowed(t *testing.T) {
	cases := []struct {
		role, sessPanel, want, desc string
		allowed                     bool
	}{
		{"ADMIN", "", "1005", "admin her panele", true},
		{"MAINTAINER", "1001", "1005", "maintainer her panele", true},
		{"OPERATOR", "1005", "1005", "operatör kendi paneline", true},
		{"OPERATOR", "1005", "1001", "operatör başka panele YASAK", false},
		{"OPERATOR", "", "1005", "panele bağlı olmayan operatör YASAK", false},
	}
	for _, c := range cases {
		if got := panelAccessAllowed(c.role, c.sessPanel, c.want); got != c.allowed {
			t.Errorf("%s: beklenen %v, gelen %v", c.desc, c.allowed, got)
		}
	}
}

// TestSafeGuards, path traversal korumasını doğrular.
func TestSafeGuards(t *testing.T) {
	if safeMsgID("../etc") || safeMsgID("0000; rm") {
		t.Error("safeMsgID path traversal'a izin verdi")
	}
	if !safeMsgID("0001") {
		t.Error("safeMsgID geçerli id'yi reddetti")
	}
	if safePanelCode("../../etc/passwd") || safePanelCode("1005/..") {
		t.Error("safePanelCode path traversal'a izin verdi")
	}
	if !safePanelCode("1005") {
		t.Error("safePanelCode geçerli kodu reddetti")
	}
}
