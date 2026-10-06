-- =========================================================
-- RNVCS-CORE — Migration 001: İlk Şema (Bölüm 10.3)
-- Hedef DB: rnvcs (core_kurulum.sh ile zaten oluşturulmuş olmalı)
-- Uygulama: psql -U rnvcs -d rnvcs -f core_migration_001_init.sql
--           (ya da core_migrate.sh script'iyle otomatik)
-- Bu migration idempotent yazılmıştır (IF NOT EXISTS / CREATE OR
-- REPLACE) — tekrar çalıştırmak güvenlidir.
-- =========================================================

BEGIN;

-- ---------------------------------------------------------
-- 1) Kullanıcılar (Bakım/Kontrol Terminali giriş yapan kişiler)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
  id            SERIAL PRIMARY KEY,
  username      TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,             -- argon2id (uygulama tarafında hashlenir)
  full_name     TEXT,
  role          TEXT NOT NULL DEFAULT 'OPERATOR',   -- ADMIN | MAINTAINER | OPERATOR
  enabled       BOOLEAN NOT NULL DEFAULT true,
  -- sip_username / sip_password: panel-SIP modelinde (Bölüm 10.22) kullanıcının
  -- kendi SIP kimliği YOKTUR; ancak Yönetim Servisi kodu bu kolonları
  -- (COALESCE ile) okuduğundan var olmaları gerekir. Boş/NULL kalabilirler.
  sip_username  TEXT,
  sip_password  TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_users_role CHECK (role IN ('ADMIN','MAINTAINER','OPERATOR'))
);
-- Mevcut (daha önce oluşturulmuş) users tablosuna kolonları idempotent ekle:
ALTER TABLE users ADD COLUMN IF NOT EXISTS sip_username TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS sip_password TEXT;

-- ---------------------------------------------------------
-- 2) Paneller (SIP/PJSIP register olan uç cihazlar)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS panels (
  id             SERIAL PRIMARY KEY,
  panel_code     TEXT UNIQUE NOT NULL,     -- '1011' — SIP endpoint/AOR adı
  display_name   TEXT NOT NULL,
  sip_password   TEXT NOT NULL,
  location       TEXT,
  enabled        BOOLEAN NOT NULL DEFAULT true,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------
-- 3) Kullanıcı ↔ Panel yetki eşlemesi (Bölüm 10.5 yetki matrisi)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_panel_permissions (
  user_id    INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  panel_id   INT NOT NULL REFERENCES panels(id) ON DELETE CASCADE,
  can_login  BOOLEAN NOT NULL DEFAULT true,
  can_call   BOOLEAN NOT NULL DEFAULT true,
  can_anons  BOOLEAN NOT NULL DEFAULT false,
  can_config BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY (user_id, panel_id)
);

-- ---------------------------------------------------------
-- 3b) Aktif panel oturumları (o AN açık oturumlar; kullanıcı başına tek satır)
--     Yönetim Servisi login/logout'ta buraya yazar (ON CONFLICT user_id upsert).
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS active_panel_sessions (
  user_id       INT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  panel_code    TEXT NOT NULL,
  session_token TEXT NOT NULL,
  started_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------
-- 4) Çatal Arama (Ring Group) tanımları
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS ring_groups (
  id           SERIAL PRIMARY KEY,
  group_code   TEXT UNIQUE NOT NULL,
  display_name TEXT NOT NULL,
  strategy     TEXT NOT NULL DEFAULT 'ringall',  -- hepsi çalar, ilk açan alır
  timeout_sec  INT NOT NULL DEFAULT 30,
  enabled      BOOLEAN NOT NULL DEFAULT true,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ring_group_members (
  group_id  INT NOT NULL REFERENCES ring_groups(id) ON DELETE CASCADE,
  panel_id  INT NOT NULL REFERENCES panels(id) ON DELETE CASCADE,
  priority  INT NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, panel_id)
);

-- ---------------------------------------------------------
-- 5) Kısayollar (panel üzerindeki hızlı arama butonları)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS speed_dials (
  id           SERIAL PRIMARY KEY,
  panel_id     INT REFERENCES panels(id) ON DELETE CASCADE,
  user_id      INT REFERENCES users(id) ON DELETE CASCADE,
  label        TEXT NOT NULL,
  target_type  TEXT NOT NULL,              -- USER | PANEL | RING_GROUP | TRUNK_REMOTE | NUMBER
  target_value TEXT NOT NULL,
  position     INT NOT NULL DEFAULT 0,
  color_hint   TEXT
);
ALTER TABLE speed_dials ADD COLUMN IF NOT EXISTS user_id INT REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE speed_dials ALTER COLUMN panel_id DROP NOT NULL;
ALTER TABLE speed_dials DROP CONSTRAINT IF EXISTS chk_speed_dials_target_type;

-- ---------------------------------------------------------
-- 6) IAX2 Trunk tanımları (sunucular arası bağlantı)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS iax_trunks (
  id           SERIAL PRIMARY KEY,
  trunk_name   TEXT UNIQUE NOT NULL,
  remote_host  TEXT NOT NULL,
  remote_port  INT NOT NULL DEFAULT 4569,
  secret       TEXT NOT NULL,
  prefix       TEXT NOT NULL,              -- '9': 9XXX bu trunk'a gider
  enabled      BOOLEAN NOT NULL DEFAULT true,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------
-- 7) Olay Kayıtları (uygulama seviyesi event log)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS event_log (
  id          BIGSERIAL PRIMARY KEY,
  ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
  event_type  TEXT NOT NULL,    -- PANEL_LOGIN | PANEL_LOGOUT | CALL_START | CALL_ANSWER |
                                -- CALL_BUSY | CALL_END | ANONS | CONFIG_CHANGE | REGISTER
  panel_id    INT REFERENCES panels(id),
  user_id     INT REFERENCES users(id),
  peer        TEXT,
  duration_s  INT,
  detail      JSONB
);
CREATE INDEX IF NOT EXISTS idx_event_log_ts ON event_log (ts DESC);
CREATE INDEX IF NOT EXISTS idx_event_log_panel ON event_log (panel_id, ts DESC);

-- ---------------------------------------------------------
-- 8) Asterisk CDR tablosu (cdr_adaptive_odbc bu tabloya yazar —
--    core_kurulum.sh'nin yazdığı /etc/asterisk/cdr_adaptive_odbc.conf
--    içindeki table=asterisk_cdr ile eşleşmeli)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS asterisk_cdr (
  id           BIGSERIAL PRIMARY KEY,
  accountcode  VARCHAR(20),
  src          VARCHAR(80),
  dst          VARCHAR(80),
  dcontext     VARCHAR(80),
  clid         VARCHAR(80),
  channel      VARCHAR(80),
  dstchannel   VARCHAR(80),
  lastapp      VARCHAR(80),
  lastdata     VARCHAR(80),
  start        TIMESTAMP,
  answer       TIMESTAMP,
  enddate      TIMESTAMP,   -- Bölüm 10.22/v1.4.1: 'end' PostgreSQL'de rezerve kelime,
                            -- cdr_adaptive_odbc INSERT'ü patlatıyordu; 'enddate' + alias kullanılıyor
  duration     INT,
  billsec      INT,
  disposition  VARCHAR(45),   -- ANSWERED | NO ANSWER | BUSY | FAILED
  amaflags     INT,
  uniqueid     VARCHAR(150),
  userfield    VARCHAR(255)
);
CREATE INDEX IF NOT EXISTS idx_asterisk_cdr_start ON asterisk_cdr (start DESC);
CREATE INDEX IF NOT EXISTS idx_asterisk_cdr_uniqueid ON asterisk_cdr (uniqueid);

-- ---------------------------------------------------------
-- 9) rnvcs kullanıcısına yetkiler (odbc/res_odbc bu rol ile bağlanıyor)
-- ---------------------------------------------------------
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO rnvcs;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO rnvcs;

COMMIT;
