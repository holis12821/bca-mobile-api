-- Biometric verification results for onboarding

CREATE TABLE onboarding_biometrics (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    biometric_id        VARCHAR(32) NOT NULL,
    session_id          VARCHAR(32) NOT NULL,
    face_photo_path     TEXT NOT NULL,
    liveness_verified   BOOLEAN NOT NULL DEFAULT false,
    liveness_score      NUMERIC(5,2) NOT NULL DEFAULT 0,
    face_match_verified BOOLEAN NOT NULL DEFAULT false,
    face_match_score    NUMERIC(5,2) NOT NULL DEFAULT 0,
    iso_compliant       BOOLEAN NOT NULL DEFAULT false,
    spoof_detected      BOOLEAN NOT NULL DEFAULT false,
    frame_count         SMALLINT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    auto_delete_at      TIMESTAMPTZ NOT NULL,

    CONSTRAINT fk_bio_session FOREIGN KEY (session_id)
        REFERENCES onboarding_sessions(session_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_bio_biometric_id ON onboarding_biometrics (biometric_id);
CREATE INDEX idx_bio_session_id ON onboarding_biometrics (session_id);
CREATE INDEX idx_bio_auto_delete ON onboarding_biometrics (auto_delete_at);