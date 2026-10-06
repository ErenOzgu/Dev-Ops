package audiowatch

import (
	"bufio"
	"context"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AudioDevice — 9.2'deki JSON şemasının Go karşılığı.
type AudioDevice struct {
	ID           string    `json:"id"`
	PipewireNode int       `json:"pipewire_node_id"`
	DisplayName  string    `json:"display_name"`
	Kind         string    `json:"kind"`      // INPUT | OUTPUT
	Transport    string    `json:"transport"` // USB | ANALOG | HDMI | BLUETOOTH
	Connected    bool      `json:"connected"`
	DetectedAt   time.Time `json:"detected_at"`
}

// Watcher, udev "sound" event'lerini dinler ve her değişiklikte
// PipeWire durumunu yeniden okuyup Olric cache'i + gRPC push'u tetikler.
type Watcher struct {
	OnChange func(devices []AudioDevice) // Olric yazma + gRPC push burada tetiklenir
	known    map[string]AudioDevice
}

func NewWatcher(onChange func([]AudioDevice)) *Watcher {
	return &Watcher{OnChange: onChange, known: map[string]AudioDevice{}}
}

func (w *Watcher) Run(ctx context.Context) error {
	if err := w.resync(ctx); err != nil {
		log.Printf("audiowatch: ilk resync hatası: %v", err)
	}

	cmd := exec.CommandContext(ctx, "udevadm", "monitor", "--udev", "--subsystem-match=sound")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "add") || strings.Contains(line, "remove") {
			time.Sleep(300 * time.Millisecond)
			if err := w.resync(ctx); err != nil {
				log.Printf("audiowatch: resync hatası: %v", err)
			}
		}
	}
	return cmd.Wait()
}

func (w *Watcher) resync(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "wpctl", "status").Output()
	if err != nil {
		return err
	}

	current := parseWpctlStatus(string(out))

	changed := len(current) != len(w.known)
	for _, d := range current {
		if prev, ok := w.known[d.ID]; !ok || prev.Connected != d.Connected {
			changed = true
		}
	}

	if changed {
		w.known = map[string]AudioDevice{}
		for _, d := range current {
			w.known[d.ID] = d
		}
		if w.OnChange != nil {
			w.OnChange(current)
		}
	}
	return nil
}

// nodeLineRe, "48. USB Composite Device Mono   [vol: 0.40]" gibi bir satırdan
// node id'sini (48) ve temiz ismi ("USB Composite Device Mono") ayıklar.
// Girdi, ağaç çizim karakterleri (│ ├ └ ─) ve "*" (varsayılan aygıt işareti)
// zaten strings.TrimLeft ile temizlendikten sonra bu regex'e verilir.
var nodeLineRe = regexp.MustCompile(`^(\d+)\.\s+(.+?)\s*(?:\[[^\]]*\])?\s*$`)

// parseWpctlStatus, `wpctl status` çıktısını satır satır bir durum makinesiyle
// gezer: önce "Audio" / "Video" / "Settings" üst bölümünü, sonra "Sinks:" /
// "Sources:" / "Devices:" / "Filters:" / "Streams:" alt bölümünü takip eder.
// Yalnızca Audio > Sinks (OUTPUT) ve Audio > Sources (INPUT) altındaki node
// satırları cihaz olarak kaydedilir; Video bölümündeki aynı isimli alt
// başlıklarla (Sinks/Sources) karışmaz.
func parseWpctlStatus(raw string) []AudioDevice {
	var devices []AudioDevice

	topSection := ""
	subSection := ""

	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Üst bölüm başlıkları (girintisiz, tek kelime)
		switch trimmed {
		case "Audio":
			topSection, subSection = "Audio", ""
			continue
		case "Video":
			topSection, subSection = "Video", ""
			continue
		case "Settings":
			topSection, subSection = "Settings", ""
			continue
		}

		// Alt bölüm başlıkları ağaç çizim karakterleriyle gelir: "├─ Sinks:" gibi
		header := strings.TrimLeft(trimmed, "│├└─ \t")
		switch header {
		case "Devices:":
			subSection = "Devices"
			continue
		case "Sinks:":
			subSection = "Sinks"
			continue
		case "Sources:":
			subSection = "Sources"
			continue
		case "Filters:":
			subSection = "Filters"
			continue
		case "Streams:":
			subSection = "Streams"
			continue
		}

		if topSection != "Audio" {
			continue
		}
		if subSection != "Sinks" && subSection != "Sources" {
			continue
		}

		candidate := strings.TrimLeft(trimmed, "│├└─*\t ")
		m := nodeLineRe.FindStringSubmatch(candidate)
		if m == nil {
			continue
		}

		nodeID, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		name := strings.TrimSpace(m[2])

		kind := "OUTPUT"
		if subSection == "Sources" {
			kind = "INPUT"
		}

		transport := "ANALOG"
		upper := strings.ToUpper(name)
		switch {
		case strings.Contains(upper, "HDMI"):
			transport = "HDMI"
		case strings.Contains(upper, "USB"):
			transport = "USB"
		case strings.Contains(upper, "BLUETOOTH") || strings.Contains(upper, "A2DP"):
			transport = "BLUETOOTH"
		}

		devices = append(devices, AudioDevice{
			ID:           strconv.Itoa(nodeID) + ":" + kind,
			PipewireNode: nodeID,
			DisplayName:  name,
			Kind:         kind,
			Transport:    transport,
			Connected:    true,
			DetectedAt:   time.Now(),
		})
	}
	return devices
}
