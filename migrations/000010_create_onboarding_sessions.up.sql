-- Onboarding session management tables

CREATE TYPE onboarding_product_type AS ENUM (
    'TAHAPAN_BCA',
    'TAHAPAN_XPRESI',
    'TABUNGANKU'
);

CREATE TYPE onboarding_step AS ENUM (
    'TNC',
    'OCR',
    'PERSONAL_DATA',
    'OTP_VERIFY',
    'BIOMETRIC',
    'VIDEO_CALL',
    'CREDENTIALS',
    'REVIEW',
    'COMPLETED'
);

CREATE TABLE onboarding_sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id    VARCHAR(32) NOT NULL,
    device_id     VARCHAR(64) NOT NULL,
    product_type  onboarding_product_type NOT NULL,
    current_step  onboarding_step NOT NULL DEFAULT 'TNC',
    tnc_version   VARCHAR(20),
    steps_completed JSONB NOT NULL DEFAULT '{
        "tnc_accepted": false,
        "ocr_verified": false,
        "personal_data_saved": false,
        "otp_verified": false,
        "biometric_verified": false,
        "video_call_verified": false,
        "credentials_set": false,
        "submitted": false
    }'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    deleted_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_onboarding_sessions_session_id ON onboarding_sessions (session_id);
CREATE INDEX idx_onboarding_sessions_device_id ON onboarding_sessions (device_id);
CREATE INDEX idx_onboarding_sessions_expires_at ON onboarding_sessions (expires_at) WHERE deleted_at IS NULL;

-- Immutable audit trail for onboarding
CREATE TABLE onboarding_audit_logs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  VARCHAR(32) NOT NULL,
    event_type  VARCHAR(64) NOT NULL,
    actor       VARCHAR(128) NOT NULL DEFAULT 'system',
    details     JSONB,
    ip_address  VARCHAR(45),
    user_agent  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_onboarding_audit_session ON onboarding_audit_logs (session_id);
CREATE INDEX idx_onboarding_audit_created ON onboarding_audit_logs (created_at);

-- Prevent UPDATE/DELETE on audit logs
CREATE OR REPLACE FUNCTION prevent_audit_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'onboarding_audit_logs is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_onboarding_audit_immutable
    BEFORE UPDATE OR DELETE ON onboarding_audit_logs
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
