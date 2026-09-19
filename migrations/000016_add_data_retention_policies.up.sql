-- Data retention policies for POJK compliance
-- Audit logs: 7 years retention (no auto-delete, archived separately)
-- Session data: 30 days after completion
-- KTP photos: 30 days (auto_delete_at set at creation in ocr_service)
-- Biometric photos: 7 days (auto_delete_at set at creation in biometric_service)
-- Video recordings: 5 years
-- Credentials: until account closed (no auto-delete)

-- Index for efficient cleanup queries on completed sessions
CREATE INDEX IF NOT EXISTS idx_onboarding_sessions_completed_cleanup
    ON onboarding_sessions (current_step, updated_at)
    WHERE current_step = 'COMPLETED' AND deleted_at IS NULL;

-- Index for OCR auto-delete scheduling
CREATE INDEX IF NOT EXISTS idx_onboarding_ocr_auto_delete
    ON onboarding_ocr_results (auto_delete_at)
    WHERE auto_delete_at IS NOT NULL;

-- Index for biometric auto-delete scheduling
CREATE INDEX IF NOT EXISTS idx_onboarding_biometrics_auto_delete
    ON onboarding_biometrics (auto_delete_at)
    WHERE auto_delete_at IS NOT NULL;

-- Partition-ready index on audit logs by created_at for 7-year retention archival
CREATE INDEX IF NOT EXISTS idx_onboarding_audit_retention
    ON onboarding_audit_logs (created_at);

-- Comment documenting retention schedule
COMMENT ON TABLE onboarding_audit_logs IS 'Immutable audit trail. Retain 7 years per POJK regulation. No UPDATE/DELETE allowed.';
COMMENT ON TABLE onboarding_sessions IS 'Session data. Soft-delete 30 days after COMPLETED step.';
COMMENT ON TABLE onboarding_ocr_results IS 'OCR results. auto_delete_at = created_at + 30 days.';
COMMENT ON TABLE onboarding_biometrics IS 'Biometric results. auto_delete_at = created_at + 7 days (UU PDP).';
COMMENT ON TABLE onboarding_credentials IS 'Credential hashes. Retained until account closure.';