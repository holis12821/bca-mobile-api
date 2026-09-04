-- 000008_create_rate_limiting.up.sql

-- ============================================================
-- OTP (One-Time Password)
-- ============================================================
CREATE TABLE otp_codes (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID REFERENCES users(id),
    phone_hash      VARCHAR(64),
    purpose         VARCHAR(30) NOT NULL
                    CHECK (purpose IN ('REGISTRATION', 'LOGIN', 'CHANGE_PHONE',
                           'CHANGE_EMAIL', 'RESET_PIN')),
    code_hash       VARCHAR(64) NOT NULL,
    attempts        INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 3,
    expires_at      TIMESTAMPTZ NOT NULL,
    verified_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_otp_lookup ON otp_codes (phone_hash, purpose, created_at DESC)
    WHERE verified_at IS NULL;

-- ============================================================
-- REGISTRATIONS
-- ============================================================
CREATE TABLE registrations (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    full_name           VARCHAR(255) NOT NULL,
    nik_encrypted       BYTEA,
    phone_encrypted     BYTEA NOT NULL,
    phone_hash          VARCHAR(64) NOT NULL,
    email_encrypted     BYTEA,
    status              VARCHAR(20) NOT NULL DEFAULT 'OTP_PENDING'
                        CHECK (status IN ('OTP_PENDING', 'OTP_VERIFIED',
                               'DOCUMENT_PENDING', 'KYC_REVIEW',
                               'APPROVED', 'REJECTED', 'EXPIRED')),
    otp_verified_at     TIMESTAMPTZ,
    documents           JSONB DEFAULT '{}',
    rejection_reason    TEXT,
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_reg_phone ON registrations (phone_hash, status);
CREATE INDEX idx_reg_expires ON registrations (expires_at) WHERE status NOT IN ('APPROVED', 'REJECTED', 'EXPIRED');