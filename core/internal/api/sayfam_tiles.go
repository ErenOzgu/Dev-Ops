package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"rnvcs-yonetim-servisi/internal/pg"
)

// ============================================================
// Sayfam sekmesi — panel_app_3.html'in çağırdığı ama CORE'da hiç
// karşılığı olmayan iki endpoint (2026-08-25 geliştirici notu, madde 3):
//   /api/sayfam-tiles    (GET/POST/PUT/PATCH/DELETE)
//   /api/sayfam-searches (GET/POST)
// Şema: core_migration_003_sayfam_tiles.sql (sayfam_tiles, sayfam_searches).
// Kayıtlar operatöre (users.id) bağlıdır, cihaza değil — Bölüm 10.19
// modeliyle tutarlı: bir operatör hangi panelde login olursa olsun
// kendi kutularını/arama geçmişini orada da görür.
// ============================================================

type createSayfamTileRequest struct {
	Username    string `json:"username"` // boşsa: oturum sahibi
	Label       string `json:"label"`
	TargetType  string `json:"target_type"` // USER | PANEL | NUMBER | INTERKOM
	TargetValue string `json:"target_value"`
	Position    int    `json:"position"`
	ColorHint   string `json:"color_hint"`
	IconHint    string `json:"icon_hint"` // 'video' | 'interkom'
}

type updateSayfamTileRequest struct {
	Label       *string `json:"label"`
	TargetType  *string `json:"target_type"`
	TargetValue *string `json:"target_value"`
	ColorHint   *string `json:"color_hint"`
	IconHint    *string `json:"icon_hint"`
}

type reorderSayfamTileRequest struct {
	Username string `json:"username"`
	IDs      []int  `json:"ids"`
}

type sayfamTileItem struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Label       string `json:"label"`
	TargetType  string `json:"target_type"`
	TargetValue string `json:"target_value"`
	Position    int    `json:"position"`
	ColorHint   string `json:"color_hint"`
	IconHint    string `json:"icon_hint"`
}

func validSayfamTileTarget(t string) bool {
	switch t {
	case "USER", "PANEL", "NUMBER", "INTERKOM":
		return true
	}
	return false
}

// SayfamTiles — panel_app "Sayfam" sekmesi kutuları.
//
//	GET    ?username=X        : listele (verilmezse oturum sahibi)
//	POST   {username?,label,target_type,target_value,position?,color_hint?,icon_hint?}
//	PATCH  ?id=N  {label?,target_type?,target_value?,color_hint?,icon_hint?} : kısmi güncelleme (ör. etiket değiştirme)
//	PUT    {username?,ids:[]} : sıralama (position=indeks)
//	DELETE ?id=N              : sil
func (h *Handler) SayfamTiles(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	isAdmin := sess.Role == "ADMIN" || sess.Role == "MAINTAINER"

	switch r.Method {
	case http.MethodGet:
		username := r.URL.Query().Get("username")
		if username == "" {
			username = sess.Username
		}
		q := "SELECT st.id, u.username, st.label, st.target_type, st.target_value, st.position, COALESCE(st.color_hint,''), COALESCE(st.icon_hint,'') " +
			"FROM sayfam_tiles st JOIN users u ON u.id = st.user_id WHERE u.username = " + pg.EscapeLiteral(username) +
			" ORDER BY st.position, st.id"
		rows, err := h.db.Query(q)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "sayfam kutuları okunamadı: "+err.Error())
			return
		}
		list := []sayfamTileItem{}
		for _, rr := range rows {
			if len(rr) < 8 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			pos, _ := strconv.Atoi(rr[5])
			list = append(list, sayfamTileItem{
				ID: id, Username: rr[1], Label: rr[2], TargetType: rr[3], TargetValue: rr[4],
				Position: pos, ColorHint: rr[6], IconHint: rr[7],
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var req createSayfamTileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Username == "" {
			req.Username = sess.Username
		}
		if req.Username != sess.Username && !isAdmin {
			writeErr(w, http.StatusForbidden, "başka kullanıcı için sayfam kutusu eklemek ADMIN/MAINTAINER gerektirir")
			return
		}
		if req.Label == "" || req.TargetValue == "" {
			writeErr(w, http.StatusBadRequest, "label ve target_value zorunlu")
			return
		}
		if req.TargetType == "" {
			req.TargetType = "USER"
		}
		if !validSayfamTileTarget(req.TargetType) {
			writeErr(w, http.StatusBadRequest, "target_type USER|PANEL|NUMBER|INTERKOM olmalı")
			return
		}
		userRow, err := h.db.QueryRow("SELECT id FROM users WHERE username=" + pg.EscapeLiteral(req.Username))
		if err != nil || len(userRow) == 0 {
			writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
			return
		}
		pos := req.Position
		if pos == 0 {
			if pr, e := h.db.QueryRow("SELECT COALESCE(MAX(position)+1,0) FROM sayfam_tiles WHERE user_id=" + userRow[0]); e == nil && len(pr) > 0 {
				pos, _ = strconv.Atoi(pr[0])
			}
		}
		row, err := h.db.QueryRow(
			"INSERT INTO sayfam_tiles (user_id, label, target_type, target_value, position, color_hint, icon_hint) VALUES (" +
				userRow[0] + "," + pg.EscapeLiteral(req.Label) + "," + pg.EscapeLiteral(req.TargetType) + "," +
				pg.EscapeLiteral(req.TargetValue) + "," + strconv.Itoa(pos) + "," +
				pg.EscapeLiteral(req.ColorHint) + "," + pg.EscapeLiteral(req.IconHint) + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "sayfam kutusu oluşturulamadı: "+err.Error())
			return
		}
		newID, _ := strconv.Atoi(row[0])
		writeJSON(w, http.StatusCreated, map[string]int{"id": newID})

	case http.MethodPatch:
		idStr := r.URL.Query().Get("id")
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "geçerli bir id gerekli")
			return
		}
		own, err := h.db.QueryRow("SELECT user_id FROM sayfam_tiles WHERE id=" + strconv.Itoa(id))
		if err != nil || len(own) == 0 {
			writeErr(w, http.StatusNotFound, "sayfam kutusu bulunamadı")
			return
		}
		ownerID, _ := strconv.Atoi(own[0])
		if ownerID != sess.UserID && !isAdmin {
			writeErr(w, http.StatusForbidden, "bu kutuyu güncelleme yetkiniz yok")
			return
		}
		var req updateSayfamTileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.TargetType != nil && !validSayfamTileTarget(*req.TargetType) {
			writeErr(w, http.StatusBadRequest, "target_type USER|PANEL|NUMBER|INTERKOM olmalı")
			return
		}
		sets := []string{}
		if req.Label != nil {
			sets = append(sets, "label="+pg.EscapeLiteral(*req.Label))
		}
		if req.TargetType != nil {
			sets = append(sets, "target_type="+pg.EscapeLiteral(*req.TargetType))
		}
		if req.TargetValue != nil {
			sets = append(sets, "target_value="+pg.EscapeLiteral(*req.TargetValue))
		}
		if req.ColorHint != nil {
			sets = append(sets, "color_hint="+pg.EscapeLiteral(*req.ColorHint))
		}
		if req.IconHint != nil {
			sets = append(sets, "icon_hint="+pg.EscapeLiteral(*req.IconHint))
		}
		if len(sets) == 0 {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		q := "UPDATE sayfam_tiles SET "
		for i, s := range sets {
			if i > 0 {
				q += ", "
			}
			q += s
		}
		q += " WHERE id=" + strconv.Itoa(id)
		if err := h.db.Exec(q); err != nil {
			writeErr(w, http.StatusInternalServerError, "sayfam kutusu güncellenemedi: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case http.MethodPut:
		var req reorderSayfamTileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Username == "" {
			req.Username = sess.Username
		}
		if req.Username != sess.Username && !isAdmin {
			writeErr(w, http.StatusForbidden, "başka kullanıcının sayfam kutularını sıralama yetkiniz yok")
			return
		}
		urow, err := h.db.QueryRow("SELECT id FROM users WHERE username=" + pg.EscapeLiteral(req.Username))
		if err != nil || len(urow) == 0 {
			writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
			return
		}
		uid := urow[0]
		for i, id := range req.IDs {
			_ = h.db.Exec("UPDATE sayfam_tiles SET position=" + strconv.Itoa(i) +
				" WHERE id=" + strconv.Itoa(id) + " AND user_id=" + uid)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "geçerli bir id gerekli")
			return
		}
		own, err := h.db.QueryRow("SELECT user_id FROM sayfam_tiles WHERE id=" + strconv.Itoa(id))
		if err != nil || len(own) == 0 {
			writeErr(w, http.StatusNotFound, "sayfam kutusu bulunamadı")
			return
		}
		ownerID, _ := strconv.Atoi(own[0])
		if ownerID != sess.UserID && !isAdmin {
			writeErr(w, http.StatusForbidden, "bu kutuyu silme yetkiniz yok")
			return
		}
		if err := h.db.Exec("DELETE FROM sayfam_tiles WHERE id=" + strconv.Itoa(id)); err != nil {
			writeErr(w, http.StatusInternalServerError, "sayfam kutusu silinemedi: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET, POST, PATCH, PUT veya DELETE kullanın")
	}
}

// ============================================================

type createSayfamSearchRequest struct {
	Username string `json:"username"` // boşsa: oturum sahibi
	Query    string `json:"query"`
}

type sayfamSearchItem struct {
	Query string `json:"query"`
}

// SayfamSearches — "Görüntülü Görüşme Seçimi" arama kutusunun son
// aramalar listesi.
//
//	GET  ?username=X       : son aramalar (en yeni önce), verilmezse oturum sahibi
//	POST {username?,query} : arama kaydı ekle/güncelle (upsert — aynı sorgu
//	                          tekrar kullanılırsa sadece used_at güncellenir)
func (h *Handler) SayfamSearches(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		username := r.URL.Query().Get("username")
		if username == "" {
			username = sess.Username
		}
		q := "SELECT ss.query FROM sayfam_searches ss JOIN users u ON u.id = ss.user_id " +
			"WHERE u.username = " + pg.EscapeLiteral(username) + " ORDER BY ss.used_at DESC LIMIT 20"
		rows, err := h.db.Query(q)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "arama geçmişi okunamadı: "+err.Error())
			return
		}
		list := []sayfamSearchItem{}
		for _, rr := range rows {
			if len(rr) < 1 {
				continue
			}
			list = append(list, sayfamSearchItem{Query: rr[0]})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		var req createSayfamSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Username == "" {
			req.Username = sess.Username
		}
		if req.Username != sess.Username && sess.Role != "ADMIN" && sess.Role != "MAINTAINER" {
			writeErr(w, http.StatusForbidden, "başka kullanıcı için arama kaydı eklemek ADMIN/MAINTAINER gerektirir")
			return
		}
		if req.Query == "" {
			writeErr(w, http.StatusBadRequest, "query zorunlu")
			return
		}
		userRow, err := h.db.QueryRow("SELECT id FROM users WHERE username=" + pg.EscapeLiteral(req.Username))
		if err != nil || len(userRow) == 0 {
			writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
			return
		}
		// uq_sayfam_searches (user_id, query) — aynı sorgu varsa used_at'i güncelle (upsert)
		err = h.db.Exec("INSERT INTO sayfam_searches (user_id, query) VALUES (" +
			userRow[0] + "," + pg.EscapeLiteral(req.Query) +
			") ON CONFLICT (user_id, query) DO UPDATE SET used_at = now()")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "arama kaydedilemedi: "+err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET veya POST kullanın")
	}
}
