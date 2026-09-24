-- 000018_account_settings_and_access_code.down.sql

ALTER TABLE devices DROP COLUMN IF EXISTS updated_at;

ALTER TABLE users
    DROP COLUMN IF EXISTS access_code_hash,
    DROP COLUMN IF EXISTS email_statement_enabled,
    DROP COLUMN IF EXISTS push_notification_enabled,
    DROP COLUMN IF EXISTS biometric_enabled;
