-- Restore the plain lookup indexes and drop the uniqueness guarantees.
-- Rows deleted by the up migration are not restored: they were duplicates.

CREATE INDEX IF NOT EXISTS idx_pd_session_id ON onboarding_personal_data (session_id);
CREATE INDEX IF NOT EXISTS idx_onboarding_credentials_session ON onboarding_credentials (session_id);

DROP INDEX IF EXISTS idx_pd_session_unique;
DROP INDEX IF EXISTS idx_onboarding_credentials_session_unique;
DROP INDEX IF EXISTS idx_vc_session_active_unique;

-- Narrow the credential id columns back. This fails if any stored id is longer
-- than the old limit, which is the correct outcome: those rows were written by
-- the current code and truncating them would lose the reference.
ALTER TABLE onboarding_credentials
    ALTER COLUMN credential_id TYPE VARCHAR(20),
    ALTER COLUMN session_id TYPE VARCHAR(30);

-- Re-create the duplicate indexes from migration 000016.
CREATE INDEX IF NOT EXISTS idx_onboarding_ocr_auto_delete
    ON onboarding_ocr_results (auto_delete_at)
    WHERE auto_delete_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_onboarding_biometrics_auto_delete
    ON onboarding_biometrics (auto_delete_at)
    WHERE auto_delete_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_onboarding_audit_retention
    ON onboarding_audit_logs (created_at);
