CREATE TABLE IF NOT EXISTS onboarding_credentials (
    id              UUID PRIMARY KEY,
    credential_id   VARCHAR(20) NOT NULL UNIQUE,
    session_id      VARCHAR(30) NOT NULL REFERENCES onboarding_sessions(session_id),
    access_code_hash TEXT NOT NULL,
    pin_hash         TEXT NOT NULL,
    encryption_key_id VARCHAR(64) NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_onboarding_credentials_session ON onboarding_credentials(session_id);