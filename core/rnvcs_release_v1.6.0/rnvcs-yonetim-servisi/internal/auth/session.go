package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Session, login sonrası verilen bearer token'ın karşılığı.
//
// NOT: Bu MVP sürümde oturumlar bellek-içi (in-memory) tutuluyor —
// servis restart olursa herkes tekrar login olmalı. Bölüm 10.1'deki
// Redis (presence/online cache) ileride bu store'un yerini alabilir
// (birden fazla Yönetim Servisi instance'ı çalıştırılacaksa ZORUNLU
// olur — şimdilik tek instance için yeterli).
type Session struct {
	UserID    int
	Username  string
	Role      string
	PanelCode string // bu oturumu başlatan panelin panel_code'u (Bölüm 10.19 tek-aktif-oturum modeli)
	Expires   time.Time
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]Session)}
}

// Create yeni bir oturum başlatır VE aynı kullanıcının başka bir panelde
// (veya aynı panelde) açık kalmış ÖNCEKİ oturumunu geçersiz kılar — Bölüm
// 10.19 kararı: bir kullanıcı aynı anda yalnızca TEK panelde aktif olabilir.
// Panel A'daki eski oturum, bir sonraki authenticate() çağrısında 401 alır
// ve panel_app.html otomatik login ekranına döner.
func (s *SessionStore) Create(userID int, username, role, panelCode string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, tok)
		}
	}
	s.sessions[token] = Session{
		UserID:    userID,
		Username:  username,
		Role:      role,
		PanelCode: panelCode,
		Expires:   time.Now().Add(12 * time.Hour),
	}
	return token
}

// Delete, bir oturumu (explicit logout ile) sonlandırır.
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// DeleteByUserID, belirli bir kullanıcının AÇIK olan (varsa) tüm
// oturumlarını hemen geçersiz kılar. Bir kullanıcı silindiğinde/devre
// dışı bırakıldığında/rolü değiştirildiğinde çağrılır — aksi halde o
// kullanıcının elindeki bearer token, 12 saatlik doğal süresi dolana
// kadar (silinmiş/deaktif olsa bile) çalışmaya devam ederdi.
func (s *SessionStore) DeleteByUserID(userID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, tok)
		}
	}
}

func (s *SessionStore) Get(token string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok || time.Now().After(sess.Expires) {
		return Session{}, false
	}
	return sess, true
}
