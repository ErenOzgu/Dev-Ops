// Package api, RNVCS Yönetim Servisi'nin HTTP handler'larını içerir.
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"rnvcs-yonetim-servisi/internal/auth"
	"rnvcs-yonetim-servisi/internal/pg"
)

type Handler struct {
	db              *pg.DB
	sessions        *auth.SessionStore
	provisioningKey string
}

func NewHandler(db *pg.DB, provisioningKey string) *Handler {
	return &Handler{db: db, sessions: auth.NewSessionStore(), provisioningKey: provisioningKey}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// authenticate, "Authorization: Bearer <token>" başlığını doğrular.
func (h *Handler) authenticate(r *http.Request) (auth.Session, bool) {
	tok := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(tok, prefix) {
		return auth.Session{}, false
	}
	return h.sessions.Get(strings.TrimPrefix(tok, prefix))
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
