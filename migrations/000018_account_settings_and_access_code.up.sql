-- 000018_account_settings_and_access_code.up.sql
--
-- Two gaps the API had already been written against but no migration created:
--
--  1. PUT /account/settings updated users.biometric_enabled,
--     push_notification_enabled and email_statement_enabled. None of those
--     columns existed, so the endpoint answered 500 (SQLSTATE 42703) on every
--     call since it was written.
--
--  2. Onboarding collects a kode akses, hashes it, hands it to the provisioner
--     — which dropped it. The nasabah was then told "silakan login dengan kode
--     akses Anda" while login only ever checked pin_hash. access_code_hash
--     gives that credential somewhere to live; login prefers it when set and
--     falls back to pin_hash for users provisioned before this migration
--     (the seeded demo users, mainly).

ALTER TABLE users
    ADD COLUMN biometric_enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN push_notification_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN email_statement_enabled   BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN access_code_hash          VARCHAR(255);

-- devices gets an updated_at for the same reason every other table has one:
-- the registration executor was already writing to it.
ALTER TABLE devices
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
