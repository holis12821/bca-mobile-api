-- 000001_create_users.up.sql

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    full_name       VARCHAR(255) NOT NULL,
    display_name    VARCHAR(100) NOT NULL,
    nik_encrypted   BYTEA,
    phone_encrypted BYTEA NOT NULL,
    phone_hash      VARCHAR(64) NOT NULL UNIQUE,
    email_encrypted BYTEA,
    email_hash      VARCHAR(64) UNIQUE,
    pin_hash        VARCHAR(255) NOT NULL,
    pin_salt        VARCHAR(64) NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'ACTIVE'
                    CHECK (status IN ('ACTIVE', 'LOCKED', 'SUSPENDED', 'CLOSED')),
    locked_until    TIMESTAMPTZ,
    failed_pin_attempts INT NOT NULL DEFAULT 0,
    max_pin_attempts    INT NOT NULL DEFAULT 5,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_users_phone_hash ON users (phone_hash) WHERE deleted_at IS NULL;
CREATE INDEX idx_users_email_hash ON users (email_hash) WHERE deleted_at IS NULL;
CREATE INDEX idx_users_status ON users (status) WHERE deleted_at IS NULL;

-- ============================================================
-- DEVICES
-- ============================================================
CREATE TABLE devices (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    device_id       VARCHAR(255) NOT NULL,
    device_name     VARCHAR(255),
    device_model    VARCHAR(255),
    os_version      VARCHAR(50),
    app_version     VARCHAR(20),
    push_token      TEXT,
    is_trusted      BOOLEAN NOT NULL DEFAULT FALSE,
    last_active_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ,

    UNIQUE (user_id, device_id)
);

CREATE INDEX idx_devices_user_id ON devices (user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_devices_device_id ON devices (device_id);

-- ============================================================
-- BIOMETRIC KEYS
-- ============================================================
CREATE TABLE biometric_keys (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    device_id       UUID NOT NULL REFERENCES devices(id),
    key_id          VARCHAR(255) NOT NULL UNIQUE,
    public_key      TEXT NOT NULL,
    biometric_type  VARCHAR(20) NOT NULL
                    CHECK (biometric_type IN ('FINGERPRINT', 'FACE_ID')),
    attestation     TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX idx_biometric_user ON biometric_keys (user_id) WHERE is_active = TRUE;
CREATE INDEX idx_biometric_key_id ON biometric_keys (key_id) WHERE is_active = TRUE;

-- ============================================================
-- SESSIONS
-- ============================================================
CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    device_id       UUID NOT NULL REFERENCES devices(id),
    refresh_token_hash VARCHAR(64) NOT NULL UNIQUE,
    ip_address      INET,
    user_agent      TEXT,
    auth_method     VARCHAR(20) NOT NULL
                    CHECK (auth_method IN ('PIN', 'FINGERPRINT', 'FACE_ID')),
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX idx_sessions_user ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_refresh ON sessions (refresh_token_hash) WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_expires ON sessions (expires_at) WHERE revoked_at IS NULL;