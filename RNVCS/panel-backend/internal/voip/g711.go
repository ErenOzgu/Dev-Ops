// Package voip, RNVCS panelinin Asterisk'e user-bazlı SIP kimliğiyle
// register olmasının (Bölüm 10.19) YANINDA, gerçek bir çağrıyı (INVITE)
// karşılayıp sesi donanıma taşıyan kısmını da içerir (Bölüm 5/10.19'un
// buluştuğu nokta: "panelde kullanıcının araması gelsin, kabul etsin,
// sesli haberleşme sağlansın").
//
// g711.go, G.711 μ-law (PCMU, RTP payload type 0) kodlayıcı/çözücüsünü
// stdlib-only (matematiksel, harici codec kütüphanesi YOK) olarak
// uygular — ITU-T G.711 referans algoritmasının standart Go karşılığı.
package voip

const (
	ulawBias = 0x84
	ulawClip = 32635
)

// encodeUlawSample, 16-bit lineer PCM örneğini 8-bit G.711 μ-law'a çevirir.
func encodeUlawSample(pcm int16) byte {
	sign := 0
	s := int(pcm)
	if s < 0 {
		sign = 0x80
		s = -s
	}
	if s > ulawClip {
		s = ulawClip
	}
	s += ulawBias

	exponent := 7
	for mask := 0x4000; (s&mask) == 0 && exponent > 0; mask >>= 1 {
		exponent--
	}
	mantissa := (s >> uint(exponent+3)) & 0x0F
	ulawByte := byte(sign | (exponent << 4) | mantissa)
	return ^ulawByte
}

// decodeUlawSample, 8-bit G.711 μ-law örneğini 16-bit lineer PCM'e çevirir.
func decodeUlawSample(u byte) int16 {
	u = ^u
	sign := u & 0x80
	exponent := (u >> 4) & 0x07
	mantissa := u & 0x0F
	sample := (int(mantissa) << 3) + 0x84
	sample <<= uint(exponent)
	sample -= 0x84
	if sign != 0 {
		sample = -sample
	}
	return int16(sample)
}

// encodeUlaw, ardışık 16-bit lineer PCM örneklerini μ-law byte dizisine çevirir.
func encodeUlaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = encodeUlawSample(s)
	}
	return out
}

// decodeUlaw, μ-law byte dizisini ardışık 16-bit lineer PCM örneklerine çevirir.
func decodeUlaw(data []byte) []int16 {
	out := make([]int16, len(data))
	for i, u := range data {
		out[i] = decodeUlawSample(u)
	}
	return out
}
