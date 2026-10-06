package voip

import "encoding/binary"

// buildRTPHeader, RFC 3550'deki en basit 12 byte'lık sabit RTP header'ı
// inşa eder (padding/extension/CSRC yok — G.711 tek kanallı ses için
// bu MVP kapsamında yeterli).
func buildRTPHeader(seq uint16, timestamp uint32, ssrc uint32, payloadType byte) []byte {
	h := make([]byte, 12)
	h[0] = 0x80 // version=2, padding=0, extension=0, CSRC count=0
	h[1] = payloadType & 0x7F
	binary.BigEndian.PutUint16(h[2:4], seq)
	binary.BigEndian.PutUint32(h[4:8], timestamp)
	binary.BigEndian.PutUint32(h[8:12], ssrc)
	return h
}

// rtpPayload, alınan bir RTP paketinin 12 byte'lık header'ını atlayıp
// ses verisini (payload) döner. n < 12 ise (bozuk/çok kısa paket) false döner.
func rtpPayload(buf []byte, n int) ([]byte, bool) {
	if n < 12 {
		return nil, false
	}
	return buf[12:n], true
}
