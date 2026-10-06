package api

import (
	"encoding/json"
	"net/http"

	"rnvcs-yonetim-servisi/internal/pg"
)

type assignPermissionRequest struct {
	Username  string `json:"username"`
	PanelCode string `json:"panel_code"`
	CanLogin  bool   `json:"can_login"`
	CanCall   bool   `json:"can_call"`
	CanAnons  bool   `json:"can_anons"`
	CanConfig bool   `json:"can_config"`
}

// Permissions — GET: bir kullanıcının bir panel için MEVCUT yetkisini okur
// (Bakım Terminali'ndeki checkbox'ların DB'deki gerçek durumu göstermesi
// için — DÜZELTME 2026-07-22: eskiden checkbox'lar her zaman statik
// "checked"/"unchecked" varsayılanla açılıyordu, gerçek yetkiyi YANSITMIYORDU;
// bu da yöneticinin "zaten işaretli görünüyor, kaydetmeye gerek yok" sanıp
// hiç kaydetmeden login denemesine ve "yetki yok" hatasına yol açıyordu.
// Satır yoksa tüm alanlar false döner (henüz hiç yetki atanmamış demektir).
// POST: bir kullanıcıya bir panel için yetki atar/günceller
// (user_panel_permissions upsert). Bölüm 10.5 yetki matrisine göre ADMIN
// veya MAINTAINER gerekir.
func (h *Handler) Permissions(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if r.Method == http.MethodGet {
		username := r.URL.Query().Get("username")
		panelCode := r.URL.Query().Get("panel_code")
		if username == "" || panelCode == "" {
			writeErr(w, http.StatusBadRequest, "username ve panel_code query parametreleri zorunlu")
			return
		}
		row, err := h.db.QueryRow(
			"SELECT upp.can_login, upp.can_call, upp.can_anons, upp.can_config " +
				"FROM user_panel_permissions upp " +
				"JOIN users u ON u.id = upp.user_id " +
				"JOIN panels p ON p.id = upp.panel_id " +
				"WHERE u.username = " + pg.EscapeLiteral(username) + " AND p.panel_code = " + pg.EscapeLiteral(panelCode))
		if err != nil {
			// Satır yok = henüz yetki atanmamış, hepsi false.
			writeJSON(w, http.StatusOK, assignPermissionRequest{Username: username, PanelCode: panelCode})
			return
		}
		writeJSON(w, http.StatusOK, assignPermissionRequest{
			Username: username, PanelCode: panelCode,
			CanLogin: pgBool(row[0]), CanCall: pgBool(row[1]), CanAnons: pgBool(row[2]), CanConfig: pgBool(row[3]),
		})
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "sadece GET veya POST")
		return
	}
	if sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
		writeErr(w, http.StatusForbidden, "yetki atamak için ADMIN veya MAINTAINER yetkisi gerekir (Bölüm 10.5)")
		return
	}

	var req assignPermissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
		return
	}

	userRow, err := h.db.QueryRow("SELECT id FROM users WHERE username=" + pg.EscapeLiteral(req.Username))
	if err != nil {
		writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
		return
	}
	panelRow, err := h.db.QueryRow("SELECT id FROM panels WHERE panel_code=" + pg.EscapeLiteral(req.PanelCode))
	if err != nil {
		writeErr(w, http.StatusNotFound, "panel bulunamadı")
		return
	}
	userID := userRow[0]
	panelID := panelRow[0]

	sql := "INSERT INTO user_panel_permissions (user_id, panel_id, can_login, can_call, can_anons, can_config) VALUES (" +
		userID + "," + panelID + "," + pg.Bool(req.CanLogin) + "," + pg.Bool(req.CanCall) + "," +
		pg.Bool(req.CanAnons) + "," + pg.Bool(req.CanConfig) + ") " +
		"ON CONFLICT (user_id, panel_id) DO UPDATE SET can_login=EXCLUDED.can_login, can_call=EXCLUDED.can_call, " +
		"can_anons=EXCLUDED.can_anons, can_config=EXCLUDED.can_config"

	if err := h.db.Exec(sql); err != nil {
		writeErr(w, http.StatusInternalServerError, "yetki kaydedilemedi: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
