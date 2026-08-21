package audiowatch

import (
	"fmt"
	"testing"
)

const sampleWpctl = `PipeWire 'pipewire-0' [1.6.2, onur@onur, cookie:65618659]
 └─ Clients:
        33. WirePlumber                         [1.6.2, onur@onur, pid:16728]
        39. pipewire                            [1.6.2, onur@onur, pid:16756]
        38. WirePlumber [export]                [1.6.2, onur@onur, pid:16728]
        48. wpctl                               [1.6.2, onur@onur, pid:16849]
Audio
 ├─ Devices:
 │      49. Built-in Audio                      [alsa]
 │      50. USB Composite Device                [alsa]
 │
 ├─ Sinks:
 │  *   48. USB Composite Device Mono           [vol: 0.40]
 │      54. Built-in Audio Analog Stereo        [vol: 0.40]
 │
 ├─ Sources:
 │  *   40. USB Composite Device Mono           [vol: 1.00]
 │      55. Built-in Audio Analog Stereo        [vol: 1.00]
 │
 ├─ Filters:
 │
 └─ Streams:
Video
 ├─ Devices:
 │
 ├─ Sinks:
 │
 ├─ Sources:
 │
 ├─ Filters:
 │
 └─ Streams:
Settings
 └─ Default Configured Devices:
`

func TestParseWpctlStatusManual(t *testing.T) {
	devices := parseWpctlStatus(sampleWpctl)
	for _, d := range devices {
		fmt.Printf("%+v\n", d)
	}
	if len(devices) != 4 {
		t.Fatalf("expected 4 devices, got %d", len(devices))
	}
	foundUSBOut := false
	for _, d := range devices {
		if d.PipewireNode == 48 && d.Kind == "OUTPUT" && d.Transport == "USB" && d.DisplayName == "USB Composite Device Mono" {
			foundUSBOut = true
		}
	}
	if !foundUSBOut {
		t.Fatalf("USB output device not parsed cleanly")
	}
}
