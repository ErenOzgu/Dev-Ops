// Package auth — parola hashleme (PBKDF2-HMAC-SHA256, stdlib-only) ve
// basit in-memory oturum (session) yönetimi.
//
// NEDEN argon2 DEĞİL: golang.org/x/crypto harici bir modül ve bu proje
// SIFIR harici Go bağımlılığı ilkesini benimsedi (bkz. internal/pg
// paketindeki açıklama). PBKDF2-HMAC-SHA256, 210.000 iterasyonla
// (OWASP 2023 önerisi) stdlib'in crypto/hmac + crypto/sha256
// paketleriyle uygulanabiliyor.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	pbkdf2Iterations = 210000
	pbkdf2KeyLen     = 32
	saltLen          = 16
)

func pbkdf2(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	var buf [4]byte
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf[:4])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// HashPassword, düz metin şifreyi "$pbkdf2-sha256$i=<iter>$salt$hash"
// formatında kodlar (users.password_hash sütununa yazılır).
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := pbkdf2([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("$pbkdf2-sha256$i=%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword, düz metin şifreyi kodlanmış hash ile karşılaştırır.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 {
		return false, errors.New("geçersiz hash formatı")
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[2], "i=%d", &iterations); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	got := pbkdf2([]byte(password), salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
