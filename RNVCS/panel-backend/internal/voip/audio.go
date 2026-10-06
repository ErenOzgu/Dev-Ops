package voip

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// micGain / speakerGain: ALSA'nın varsayılan yakalama seviyesi genelde çok
// düşük geliyor ("ses çok kısık" şikayeti) — donanım mikser seviyesini
// (amixer/alsamixer) yükseltmek asıl çözüm, ama panelde bunu her seferinde
// ayarlamak yerine burada da bir yazılımsal kazanç (gain) uyguluyoruz.
// RNVCS_MIC_GAIN / RNVCS_SPEAKER_GAIN ortam değişkenleriyle ayarlanabilir
// (varsayılan: mikrofon 4x, hoparlör 1x — gelen ses genelde zaten Asterisk/
// karşı uçtan normal seviyede gelir, kısıksa RNVCS_SPEAKER_GAIN=2 gibi
// artırılabilir).
func envGain(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}

var micGain = envGain("RNVCS_MIC_GAIN", 4.0)
var speakerGain = envGain("RNVCS_SPEAKER_GAIN", 1.0)

func applyGain(pcm []int16, gain float64) {
	if gain == 1.0 {
		return
	}
	for i, s := range pcm {
		v := float64(s) * gain
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		pcm[i] = int16(v)
	}
}

// rtpSendLoop: mikrofonu ALSA "arecord" ile 16-bit lineer PCM (8kHz, mono)
// olarak yakalar, her 20ms'lik (160 örnek) parçayı G.711 μ-law'a çevirip
// RTP paketi olarak karşı tarafın ilan ettiği RTP adresine gönderir.
//
// NOT: "arecord" (alsa-utils) sistemde yoksa ya da ses kartı erişilemezse
// bu goroutine hatayı loglar ve çıkar — SIP sinyalleşmesi (çağrı kabul
// edilmiş görünür) buna rağmen etkilenmez, sadece giden ses akmaz. Bu,
// sandbox/test ortamlarında donanım olmadan sinyalleşmeyi doğrulayabilmek
// için kasıtlı bir tasarım tercihidir.
func (e *Engine) rtpSendLoop(rtpConn *net.UDPConn, remote *net.UDPAddr, stop chan struct{}) {
	cmd := exec.Command("arecord", "-D", "default", "-f", "S16_LE", "-r", "8000", "-c", "1", "-t", "raw")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("!! arecord pipe kurulamadı: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("!! arecord başlatılamadı (giden ses akmayacak — alsa-utils kurulu mu?): %v", err)
		return
	}
	e.mu.Lock()
	e.recordCmd = cmd
	e.mu.Unlock()

	seq := uint16(randHex16())
	ssrc := uint32(randHex32())
	var ts uint32

	frame := make([]byte, 320) // 160 örnek * 2 byte (S16_LE)
	for {
		select {
		case <-stop:
			return
		default:
		}
		if _, err := io.ReadFull(stdout, frame); err != nil {
			return // arecord kapandı / hata
		}
		pcm := make([]int16, 160)
		for i := 0; i < 160; i++ {
			pcm[i] = int16(binary.LittleEndian.Uint16(frame[i*2:]))
		}
		applyGain(pcm, micGain)
		payload := encodeUlaw(pcm)
		packet := append(buildRTPHeader(seq, ts, ssrc, 0), payload...)
		if _, err := rtpConn.WriteToUDP(packet, remote); err != nil {
			return
		}
		seq++
		ts += 160
	}
}

// rtpRecvLoop: RTP soketinden gelen paketleri çözüp (μ-law -> lineer PCM)
// "aplay" ile hoparlöre basar.
func (e *Engine) rtpRecvLoop(rtpConn *net.UDPConn, stop chan struct{}) {
	cmd := exec.Command("aplay", "-D", "default", "-f", "S16_LE", "-r", "8000", "-c", "1", "-t", "raw")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("!! aplay pipe kurulamadı: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("!! aplay başlatılamadı (gelen ses duyulmayacak — alsa-utils kurulu mu?): %v", err)
		return
	}
	e.mu.Lock()
	e.playCmd = cmd
	e.mu.Unlock()
	defer stdin.Close()

	buf := make([]byte, 2048)
	for {
		select {
		case <-stop:
			return
		default:
		}
		_ = rtpConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := rtpConn.ReadFromUDP(buf)
		if err != nil {
			continue // timeout ya da geçici hata — stop kontrolüne dön
		}
		payload, ok := rtpPayload(buf, n)
		if !ok {
			continue
		}
		pcm := decodeUlaw(payload)
		applyGain(pcm, speakerGain)
		out := make([]byte, len(pcm)*2)
		for i, s := range pcm {
			binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
		}
		if _, err := stdin.Write(out); err != nil {
			return
		}
	}
}

func randHex16() uint16 {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return binary.BigEndian.Uint16(b)
}

func randHex32() uint32 {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return binary.BigEndian.Uint32(b)
}
