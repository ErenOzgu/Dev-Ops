-- =========================================================
-- RNVCS-CORE — Migration 004: Anons Sistemi FKT — Interkom/IP Horn +
-- Konferans oturumları
-- Uygulama: core_migrate.sh 002'yi otomatik arıyor ama 003/004'ü aramıyor
--           (bkz. core_migrate.sh) — bu dosya da 003 gibi ELLE uygulanmalı:
--   cp core_migration_004_annon_devices.sql /tmp/ && chmod 644 /tmp/core_migration_004_annon_devices.sql
--   sudo -u postgres psql -d rnvcs -v ON_ERROR_STOP=1 -f /tmp/core_migration_004_annon_devices.sql
--   rm -f /tmp/core_migration_004_annon_devices.sql
-- İdempotent yazılmıştır — tekrar çalıştırmak güvenlidir.
-- =========================================================

BEGIN;

-- ---------------------------------------------------------
-- 1) panels: Interkom / IP Horn de bu tabloda bir satır olarak
--    tanımlanıyor (ayrı tablo yerine — ring_group_members.panel_id zaten
--    panels'e FK, ek şema değişikliği gerekmesin diye).
-- ---------------------------------------------------------
ALTER TABLE panels ADD COLUMN IF NOT EXISTS device_type TEXT NOT NULL DEFAULT 'PANEL';
ALTER TABLE panels DROP CONSTRAINT IF EXISTS chk_panels_device_type;
ALTER TABLE panels ADD CONSTRAINT chk_panels_device_type CHECK (device_type IN ('PANEL','INTERKOM','IP_HORN'));

-- ---------------------------------------------------------
-- 2) Konferans oturumları (madde 4 — izleme/audit amaçlı)
-- ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS conference_sessions (
  id            BIGSERIAL PRIMARY KEY,
  room_code     TEXT NOT NULL,
  created_by    INT REFERENCES users(id) ON DELETE SET NULL,
  participants  JSONB NOT NULL DEFAULT '[]'::jsonb,  -- ["kom","astsubay",...] sip_username listesi
  started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  ended_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_conference_sessions_started ON conference_sessions (started_at DESC);

GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO rnvcs;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO rnvcs;

COMMIT;
