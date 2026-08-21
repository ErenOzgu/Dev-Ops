package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"rnvcs-yonetim-servisi/internal/auth"
	"rnvcs-yonetim-servisi/internal/dialplan"
	"rnvcs-yonetim-servisi/internal/pg"
	"rnvcs-yonetim-servisi/internal/pjsip"
)

type createUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	FullName    string `json:"full_name"`
	Role        string `json:"role"`         // ADMIN | MAINTAINER | OPERATOR
	SipUsername string `json:"sip_username"` // opsiyonel — Bölüm 10.19: SIP kimliği artık kullanıcıya ait
	SipPassword string `json:"sip_password"` // opsiyonel
}

type setUserSipRequest struct {
	Username    string `json:"username"`
	SipUsername string `json:"sip_username"`
	SipPassword string `json:"sip_password"`
}

// updateUserRequest — PUT /api/users?id=<id> gövdesi. Tüm alanlar
// opsiyoneldir: sadece gönderilenler güncellenir (boş string/nil = "dokunma").
// Password boş bırakılırsa şifre değişmez. Enabled bir pointer çünkü
// "false gönderildi" ile "hiç gönderilmedi" ayrımı gerekiyor.
type updateUserRequest struct {
	FullName    *string `json:"full_name"`
	Role        *string `json:"role"`
	Enabled     *bool   `json:"enabled"`
	Password    *string `json:"password"`
	SipUsername *string `json:"sip_username"`
	SipPassword *string `json:"sip_password"`
}

type userSummary struct {
	ID           int    `json:"id"`
	Username     string `json:"username"`
	FullName     string `json:"full_name"`
	Role         string `json:"role"`
	Enabled      bool   `json:"enabled"`
	SipUsername  string `json:"sip_username"`   // boşsa SIP hesabı henüz tanımlanmamış
	HasSipAccout bool   `json:"has_sip_account"` // SipPassword ASLA listede dönmez (güvenlik)
}

// Users — GET: kullanıcı listesi (herhangi bir login'li kullanıcı görebilir).
// POST: yeni kullanıcı oluşturma — Bölüm 10.5 yetki matrisine göre SADECE ADMIN.
func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query("SELECT id, username, COALESCE(full_name,''), role, enabled, COALESCE(sip_username,'') FROM users ORDER BY username")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "kullanıcılar okunamadı: "+err.Error())
			return
		}
		var list []userSummary
		for _, rr := range rows {
			if len(rr) < 6 {
				continue
			}
			id, _ := strconv.Atoi(rr[0])
			list = append(list, userSummary{
				ID: id, Username: rr[1], FullName: rr[2], Role: rr[3], Enabled: pgBool(rr[4]),
				SipUsername: rr[5], HasSipAccout: rr[5] != "",
			})
		}
		writeJSON(w, http.StatusOK, list)

	case http.MethodPost:
		if sess.Role != "ADMIN" {
			writeErr(w, http.StatusForbidden, "sadece ADMIN kullanıcı oluşturabilir (Bölüm 10.5 yetki matrisi)")
			return
		}
		var req createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Role != "ADMIN" && req.Role != "MAINTAINER" && req.Role != "OPERATOR" {
			writeErr(w, http.StatusBadRequest, "role ADMIN|MAINTAINER|OPERATOR olmalı")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "şifre hashlenemedi")
			return
		}
		sipUserSQL := "NULL"
		sipPassSQL := "NULL"
		if req.SipUsername != "" && req.SipPassword != "" {
			sipUserSQL = pg.EscapeLiteral(req.SipUsername)
			sipPassSQL = pg.EscapeLiteral(req.SipPassword)
		}
		row, err := h.db.QueryRow(
			"INSERT INTO users (username, password_hash, full_name, role, sip_username, sip_password) VALUES (" +
				pg.EscapeLiteral(req.Username) + "," + pg.EscapeLiteral(hash) + "," +
				pg.EscapeLiteral(req.FullName) + "," + pg.EscapeLiteral(req.Role) + "," +
				sipUserSQL + "," + sipPassSQL + ") RETURNING id")
		if err != nil {
			writeErr(w, http.StatusConflict, "kullanıcı oluşturulamadı (kullanıcı adı zaten var olabilir): "+err.Error())
			return
		}
		newID, _ := strconv.Atoi(row[0])

		if req.SipUsername != "" && req.SipPassword != "" {
			if err := pjsip.AppendEndpoint(req.SipUsername, req.SipPassword); err != nil {
				_ = h.db.Exec("DELETE FROM users WHERE id=" + strconv.Itoa(newID))
				writeErr(w, http.StatusInternalServerError, "kullanıcı oluşturulamadı: SIP config yazılamadı ("+err.Error()+"), DB kaydı geri alındı")
				return
			}
			// Bölüm 10.20: SIP hesabı olan her kullanıcı otomatik olarak
			// doğrudan aranabilir olmalı (extension adı = sip_username).
			if err := dialplan.AppendDirectExtension(req.SipUsername, 30); err != nil {
				writeErr(w, http.StatusInternalServerError, "kullanıcı ve SIP hesabı oluşturuldu ama doğrudan arama extension'ı yazılamadı: "+err.Error())
				return
			}
		}

		// DÜZELTME (2026-07-22): daha önce yeni oluşturulan bir kullanıcının
		// HİÇBİR panelde login yetkisi olmuyordu — Bakım Terminali'nin
		// "Yetkiler" sekmesinden panel_code seçip ayrıca "Yetkiyi Kaydet"
		// yapmak gerekiyordu, aksi halde panelde "bu kullanıcının bu panelde
		// login yetkisi yok" hatası alınıyordu (gerçek donanımda test2
		// kullanıcısıyla doğrulandı). Artık yeni kullanıcı, o an ENABLED
		// olan TÜM panellere varsayılan olarak login+arama yetkisiyle
		// (can_login=true, can_call=true, can_anons=false, can_config=false
		// — Bakım Terminali'ndeki checkbox varsayılanlarıyla aynı) otomatik
		// ekleniyor. Yönetici isterse Yetkiler sekmesinden bunu daraltabilir/
		// genişletebilir; bu sadece güvenli bir varsayılan.
		if err := h.db.Exec("INSERT INTO user_panel_permissions (user_id, panel_id, can_login, can_call, can_anons, can_config) " +
			"SELECT " + strconv.Itoa(newID) + ", id, true, true, false, false FROM panels WHERE enabled = true " +
			"ON CONFLICT (user_id, panel_id) DO NOTHING"); err != nil {
			// Bu sadece bir kolaylık (varsayılan yetki) olduğundan kullanıcı
			// oluşturmayı BAŞARISIZ saymıyoruz — ama yönetici Yetkiler
			// sekmesinden elle atama yapması gerektiğini bilsin diye logluyoruz.
			_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
				strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"create_user_default_perms_failed","target_user_id":`+strconv.Itoa(newID)+`,"error":"`+err.Error()+`"}`) + ")")
		}

		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"create_user","target_user_id":`+strconv.Itoa(newID)+`}`) + ")")
		writeJSON(w, http.StatusCreated, map[string]int{"id": newID})

	case http.MethodPut, http.MethodPatch:
		if sess.Role != "ADMIN" {
			writeErr(w, http.StatusForbidden, "sadece ADMIN kullanıcı düzenleyebilir (Bölüm 10.5 yetki matrisi)")
			return
		}
		targetID, err := strconv.Atoi(r.URL.Query().Get("id"))
		if err != nil || targetID <= 0 {
			writeErr(w, http.StatusBadRequest, "?id=<kullanıcı_id> query parametresi zorunlu")
			return
		}
		var req updateUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
			return
		}
		if req.Role != nil && *req.Role != "ADMIN" && *req.Role != "MAINTAINER" && *req.Role != "OPERATOR" {
			writeErr(w, http.StatusBadRequest, "role ADMIN|MAINTAINER|OPERATOR olmalı")
			return
		}
		var sets []string
		if req.FullName != nil {
			sets = append(sets, "full_name="+pg.EscapeLiteral(*req.FullName))
		}
		if req.Role != nil {
			sets = append(sets, "role="+pg.EscapeLiteral(*req.Role))
		}
		if req.Enabled != nil {
			sets = append(sets, "enabled="+pg.Bool(*req.Enabled))
		}
		if req.Password != nil && *req.Password != "" {
			hash, err := auth.HashPassword(*req.Password)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "şifre hashlenemedi")
				return
			}
			sets = append(sets, "password_hash="+pg.EscapeLiteral(hash))
		}
		if req.SipUsername != nil && req.SipPassword != nil && *req.SipUsername != "" && *req.SipPassword != "" {
			sets = append(sets, "sip_username="+pg.EscapeLiteral(*req.SipUsername))
			sets = append(sets, "sip_password="+pg.EscapeLiteral(*req.SipPassword))
		}
		if len(sets) == 0 {
			writeErr(w, http.StatusBadRequest, "güncellenecek en az bir alan gönderilmeli")
			return
		}
		sqlStr := "UPDATE users SET "
		for i, set := range sets {
			if i > 0 {
				sqlStr += ", "
			}
			sqlStr += set
		}
		sqlStr += " WHERE id=" + strconv.Itoa(targetID)
		if err := h.db.Exec(sqlStr); err != nil {
			writeErr(w, http.StatusInternalServerError, "kullanıcı güncellenemedi: "+err.Error())
			return
		}
		// Yeni bir SIP hesabı atandıysa (var olanı değiştirdiyse), UserSip'teki
		// gibi Asterisk config'ini de güncelle + doğrudan aranabilir yap.
		if req.SipUsername != nil && req.SipPassword != nil && *req.SipUsername != "" && *req.SipPassword != "" {
			if err := pjsip.AppendEndpoint(*req.SipUsername, *req.SipPassword); err != nil {
				writeErr(w, http.StatusInternalServerError, "kullanıcı güncellendi ama Asterisk SIP config yazılamadı: "+err.Error())
				return
			}
			if err := dialplan.AppendDirectExtension(*req.SipUsername, 30); err != nil {
				writeErr(w, http.StatusInternalServerError, "kullanıcı güncellendi ama doğrudan arama extension'ı yazılamadı: "+err.Error())
				return
			}
		}
		// Kullanıcı devre dışı bırakıldıysa ya da rolü değiştiyse, elindeki
		// bearer token'ın 12 saatlik doğal süresi dolana kadar geçerli
		// kalmaması için AÇIK oturumunu hemen sonlandır.
		if (req.Enabled != nil && !*req.Enabled) || req.Role != nil {
			h.sessions.DeleteByUserID(targetID)
		}
		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"update_user","target_user_id":`+strconv.Itoa(targetID)+`}`) + ")")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case http.MethodDelete:
		if sess.Role != "ADMIN" {
			writeErr(w, http.StatusForbidden, "sadece ADMIN kullanıcı silebilir (Bölüm 10.5 yetki matrisi)")
			return
		}
		targetID, err := strconv.Atoi(r.URL.Query().Get("id"))
		if err != nil || targetID <= 0 {
			writeErr(w, http.StatusBadRequest, "?id=<kullanıcı_id> query parametresi zorunlu")
			return
		}
		if targetID == sess.UserID {
			writeErr(w, http.StatusBadRequest, "kendi hesabını silemezsin — başka bir ADMIN ile sil")
			return
		}
		row, err := h.db.QueryRow("SELECT username FROM users WHERE id=" + strconv.Itoa(targetID))
		if err != nil {
			writeErr(w, http.StatusNotFound, "kullanıcı bulunamadı")
			return
		}
		targetUsername := row[0]

		// event_log.user_id'de ON DELETE CASCADE YOK (bilinçli — audit kaydı
		// kullanıcı silinse de kalıcı olmalı), bu yüzden önce FK'yi NULL'a
		// çekiyoruz ki DELETE FK ihlaliyle başarısız olmasın. Diğer tablolar
		// (user_panel_permissions, active_panel_sessions, ring_group_members,
		// speed_dials) ON DELETE CASCADE ile otomatik temizlenir.
		if err := h.db.Exec("UPDATE event_log SET user_id = NULL WHERE user_id=" + strconv.Itoa(targetID)); err != nil {
			writeErr(w, http.StatusInternalServerError, "olay kayıtları ayrıştırılamadı: "+err.Error())
			return
		}
		if err := h.db.Exec("DELETE FROM users WHERE id=" + strconv.Itoa(targetID)); err != nil {
			writeErr(w, http.StatusInternalServerError, "kullanıcı silinemedi: "+err.Error())
			return
		}
		h.sessions.DeleteByUserID(targetID)

		_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
			strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"delete_user","target_username":"`+targetUsername+`"}`) + ")")
		// BİLİNEN SINIRLAMA: bu kullanıcının bir SIP hesabı vardıysa,
		// pjsip_rnvcs_dynamic.conf / extensions_rnvcs_dynamic.conf içindeki
		// PJSIP endpoint + doğrudan-arama extension blokları BURADA
		// silinmiyor (AppendEndpoint/AppendDirectExtension append-only,
		// idempotent bir "kaldır" karşılığı henüz yok — bkz. 10.14/10.16/10.20
		// MVP sınırlamaları). Kalan blok zararsızdır (kimse register olmadığı
		// sürece kullanılmaz) ama aynı sip_username'i başka bir kullanıcıya
		// tekrar atarsan dosyada eski + yeni blok yan yana kalır, Asterisk
		// pratikte sonuncuyu kullanır. Üretimde idempotent bir writer'a
		// (ya da Asterisk config'ini DB'den yeniden üreten bir "reconcile"
		// adımına) geçilmeli.
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET, POST, PUT veya DELETE kullanın")
	}
}

// UserSip — POST /api/users/sip: mevcut bir kullanıcının SIP hesabını
// tanımlar/günceller — SADECE ADMIN (Bölüm 10.19 kararı: "SIP şifresi
// Bakım Terminali'nde ayarlanabilsin"). internal/pjsip.AppendEndpoint
// append-only olduğundan, aynı sip_username için tekrar çağrılırsa
// Asterisk config'inde yinelenen blok oluşur (bilinen MVP sınırlaması,
// bkz. 10.14/10.16) — üretimde idempotent bir writer'a geçilmeli.
func (h *Handler) UserSip(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "oturum geçersiz, tekrar login olun")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "sadece POST")
		return
	}
	if sess.Role != "ADMIN" {
		writeErr(w, http.StatusForbidden, "SIP hesabı atamak için SADECE ADMIN yetkisi vardır")
		return
	}
	var req setUserSipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "geçersiz istek gövdesi")
		return
	}
	if req.Username == "" || req.SipUsername == "" || req.SipPassword == "" {
		writeErr(w, http.StatusBadRequest, "username, sip_username ve sip_password zorunlu")
		return
	}
	if err := h.db.Exec("UPDATE users SET sip_username=" + pg.EscapeLiteral(req.SipUsername) +
		", sip_password=" + pg.EscapeLiteral(req.SipPassword) + " WHERE username=" + pg.EscapeLiteral(req.Username)); err != nil {
		writeErr(w, http.StatusInternalServerError, "SIP hesabı kaydedilemedi: "+err.Error())
		return
	}
	if err := pjsip.AppendEndpoint(req.SipUsername, req.SipPassword); err != nil {
		writeErr(w, http.StatusInternalServerError, "DB güncellendi ama Asterisk config yazılamadı: "+err.Error())
		return
	}
	// Bölüm 10.20: SIP hesabı atanan/güncellenen her kullanıcı otomatik
	// olarak doğrudan aranabilir olmalı (extension adı = sip_username) —
	// önceden bu adım eksikti, kullanıcı SIP hesabıyla register olabiliyordu
	// ama kimse onu arayamıyordu (extension hiç yoktu).
	if err := dialplan.AppendDirectExtension(req.SipUsername, 30); err != nil {
		writeErr(w, http.StatusInternalServerError, "SIP hesabı kaydedildi ama doğrudan arama extension'ı yazılamadı: "+err.Error())
		return
	}
	_ = h.db.Exec("INSERT INTO event_log (event_type, user_id, detail) VALUES ('CONFIG_CHANGE', " +
		strconv.Itoa(sess.UserID) + ", " + pg.EscapeLiteral(`{"action":"set_user_sip","username":"`+req.Username+`"}`) + ")")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
