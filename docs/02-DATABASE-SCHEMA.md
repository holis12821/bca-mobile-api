# Database Schema — PostgreSQL

> Desain database banking-grade dengan audit trail, encryption at rest, dan optimasi index

---

## Prinsip Desain Database

1. **Immutable transaction records** — Transaksi TIDAK PERNAH di-update/delete, hanya INSERT
2. **Soft delete** — User/account tidak pernah hard-delete, hanya `deleted_at`
3. **Audit trail** — Setiap perubahan state tercatat di `audit_logs`
4. **Sensitive data encrypted** — PIN hash, PII menggunakan application-level encryption
5. **UUID primary keys** — Tidak mengekspos auto-increment IDs
6. **Timezone UTC** — Semua timestamp dalam UTC, konversi ke WIB di application layer

---

## Migration Files

### 000001 — Users & Authentication

```sql
-- migrations/000001_create_users.up.sql

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    full_name       VARCHAR(255) NOT NULL,
    display_name    VARCHAR(100) NOT NULL,
    nik_encrypted   BYTEA,                          -- NIK dienkripsi (AES-256-GCM)
    phone_encrypted BYTEA NOT NULL,                 -- Phone dienkripsi
    phone_hash      VARCHAR(64) NOT NULL UNIQUE,    -- Hash untuk lookup
    email_encrypted BYTEA,
    email_hash      VARCHAR(64) UNIQUE,
    pin_hash        VARCHAR(255) NOT NULL,           -- Argon2id hash
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
-- DEVICES (multi-device support)
-- ============================================================
CREATE TABLE devices (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    device_id       VARCHAR(255) NOT NULL,          -- Device fingerprint dari client
    device_name     VARCHAR(255),
    device_model    VARCHAR(255),
    os_version      VARCHAR(50),
    app_version     VARCHAR(20),
    push_token      TEXT,                           -- FCM token
    is_trusted      BOOLEAN NOT NULL DEFAULT FALSE,
    last_active_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ,

    UNIQUE (user_id, device_id)
);

CREATE INDEX idx_devices_user_id ON devices (user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_devices_device_id ON devices (device_id);

-- ============================================================
-- BIOMETRIC KEYS (FIDO2/WebAuthn style)
-- ============================================================
CREATE TABLE biometric_keys (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    device_id       UUID NOT NULL REFERENCES devices(id),
    key_id          VARCHAR(255) NOT NULL UNIQUE,   -- Key identifier dari Android Keystore
    public_key      TEXT NOT NULL,                   -- Public key (PEM format)
    biometric_type  VARCHAR(20) NOT NULL
                    CHECK (biometric_type IN ('FINGERPRINT', 'FACE_ID')),
    attestation     TEXT,                            -- Key attestation data
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX idx_biometric_user ON biometric_keys (user_id) WHERE is_active = TRUE;
CREATE INDEX idx_biometric_key_id ON biometric_keys (key_id) WHERE is_active = TRUE;

-- ============================================================
-- SESSIONS (server-side session tracking)
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
```

### 000002 — Accounts & Balances

```sql
-- migrations/000002_create_accounts.up.sql

-- ============================================================
-- ACCOUNTS
-- ============================================================
CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    account_number  VARCHAR(20) NOT NULL UNIQUE,
    account_type    VARCHAR(30) NOT NULL
                    CHECK (account_type IN ('TAHAPAN', 'TAHAPAN_GOLD', 'TAPRES',
                           'TAHAPAN_XPRESI', 'GIRO', 'DEPOSITO')),
    account_label   VARCHAR(100) NOT NULL,
    currency        VARCHAR(3) NOT NULL DEFAULT 'IDR',
    balance         DECIMAL(18,2) NOT NULL DEFAULT 0,
    hold_amount     DECIMAL(18,2) NOT NULL DEFAULT 0,
    available_balance DECIMAL(18,2) GENERATED ALWAYS AS (balance - hold_amount) STORED,
    is_primary      BOOLEAN NOT NULL DEFAULT FALSE,
    status          VARCHAR(20) NOT NULL DEFAULT 'ACTIVE'
                    CHECK (status IN ('ACTIVE', 'DORMANT', 'FROZEN', 'CLOSED')),
    opened_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_accounts_user ON accounts (user_id) WHERE status = 'ACTIVE';
CREATE UNIQUE INDEX idx_accounts_primary ON accounts (user_id) WHERE is_primary = TRUE AND status = 'ACTIVE';
CREATE INDEX idx_accounts_number ON accounts (account_number);

-- ============================================================
-- TRANSACTION LIMITS
-- ============================================================
CREATE TABLE transaction_limits (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    limit_type      VARCHAR(30) NOT NULL
                    CHECK (limit_type IN ('TRANSFER_INTERNAL', 'TRANSFER_EXTERNAL',
                           'EWALLET', 'QRIS', 'PAYMENT')),
    daily_limit     DECIMAL(18,2) NOT NULL,
    monthly_limit   DECIMAL(18,2),
    per_transaction_limit DECIMAL(18,2),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (user_id, limit_type)
);

-- ============================================================
-- DAILY USAGE TRACKING (untuk enforce limit harian)
-- ============================================================
CREATE TABLE daily_usage (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    usage_date      DATE NOT NULL DEFAULT CURRENT_DATE,
    limit_type      VARCHAR(30) NOT NULL,
    total_amount    DECIMAL(18,2) NOT NULL DEFAULT 0,
    transaction_count INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (user_id, usage_date, limit_type)
);

CREATE INDEX idx_daily_usage_lookup ON daily_usage (user_id, usage_date, limit_type);
```

### 000003 — Transactions

```sql
-- migrations/000003_create_transactions.up.sql

-- ============================================================
-- TRANSACTIONS (immutable ledger)
-- ============================================================
CREATE TABLE transactions (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    idempotency_key     VARCHAR(255) UNIQUE,        -- Mencegah duplikasi
    user_id             UUID NOT NULL REFERENCES users(id),
    source_account_id   UUID REFERENCES accounts(id),
    type                VARCHAR(30) NOT NULL
                        CHECK (type IN ('TRANSFER_INTERNAL', 'TRANSFER_EXTERNAL',
                               'EWALLET_TOPUP', 'QRIS_PAYMENT', 'PULSA',
                               'PAYMENT', 'DEPOSIT', 'WITHDRAWAL')),
    status              VARCHAR(20) NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING', 'PROCESSING', 'SUCCESS',
                               'FAILED', 'REVERSED', 'EXPIRED')),
    amount              DECIMAL(18,2) NOT NULL,
    admin_fee           DECIMAL(18,2) NOT NULL DEFAULT 0,
    total_amount        DECIMAL(18,2) NOT NULL,      -- amount + admin_fee
    currency            VARCHAR(3) NOT NULL DEFAULT 'IDR',
    reference_number    VARCHAR(50) NOT NULL UNIQUE,
    description         TEXT,
    notes               VARCHAR(500),

    -- Destination info (denormalized untuk immutability)
    destination_account VARCHAR(30),
    destination_name    VARCHAR(255),
    destination_bank    VARCHAR(50),
    destination_bank_code VARCHAR(10),

    -- Provider info (untuk e-wallet, pulsa, dll)
    provider_id         VARCHAR(50),
    provider_name       VARCHAR(100),
    provider_ref        VARCHAR(100),                -- Reference dari provider

    -- Timing
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at        TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    expired_at          TIMESTAMPTZ
);

CREATE INDEX idx_txn_user ON transactions (user_id, created_at DESC);
CREATE INDEX idx_txn_user_type ON transactions (user_id, type, created_at DESC);
CREATE INDEX idx_txn_status ON transactions (status) WHERE status IN ('PENDING', 'PROCESSING');
CREATE INDEX idx_txn_idempotency ON transactions (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_txn_reference ON transactions (reference_number);
CREATE INDEX idx_txn_created ON transactions (created_at DESC);

-- ============================================================
-- ACCOUNT MUTATIONS (bank statement entries — immutable)
-- ============================================================
CREATE TABLE account_mutations (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id          UUID NOT NULL REFERENCES accounts(id),
    transaction_id      UUID REFERENCES transactions(id),
    mutation_type       VARCHAR(10) NOT NULL
                        CHECK (mutation_type IN ('DEBIT', 'CREDIT')),
    amount              DECIMAL(18,2) NOT NULL,
    balance_before      DECIMAL(18,2) NOT NULL,
    balance_after       DECIMAL(18,2) NOT NULL,
    description         VARCHAR(500) NOT NULL,
    detail              TEXT,
    category            VARCHAR(30),
    reference_number    VARCHAR(50),
    transaction_date    DATE NOT NULL DEFAULT CURRENT_DATE,
    transaction_time    TIME NOT NULL DEFAULT CURRENT_TIME,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index utama: query mutasi per akun dengan range tanggal
CREATE INDEX idx_mutations_account_date ON account_mutations (account_id, transaction_date DESC, created_at DESC);
CREATE INDEX idx_mutations_txn ON account_mutations (transaction_id);

-- Partitioning by month untuk performa (opsional, aktifkan saat data besar)
-- CREATE TABLE account_mutations (...) PARTITION BY RANGE (transaction_date);

-- ============================================================
-- TRANSFER INQUIRIES (temporary, expires)
-- ============================================================
CREATE TABLE transfer_inquiries (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id             UUID NOT NULL REFERENCES users(id),
    inquiry_type        VARCHAR(20) NOT NULL
                        CHECK (inquiry_type IN ('TRANSFER', 'EWALLET')),
    destination_account VARCHAR(30) NOT NULL,
    destination_name    VARCHAR(255),
    destination_bank    VARCHAR(50),
    bank_code           VARCHAR(10),
    provider_id         VARCHAR(50),
    amount              DECIMAL(18,2),
    admin_fee           DECIMAL(18,2),
    metadata            JSONB,                       -- Additional provider-specific data
    expires_at          TIMESTAMPTZ NOT NULL,
    used_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_inquiry_expires ON transfer_inquiries (expires_at) WHERE used_at IS NULL;

-- ============================================================
-- VERIFICATION TOKENS (PIN verification for transactions)
-- ============================================================
CREATE TABLE verification_tokens (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    token_hash      VARCHAR(64) NOT NULL UNIQUE,
    purpose         VARCHAR(30) NOT NULL
                    CHECK (purpose IN ('TRANSFER', 'EWALLET_TOPUP', 'QRIS_PAYMENT',
                           'CHANGE_LIMIT', 'CHANGE_PIN')),
    transaction_id  UUID,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_vtoken_hash ON verification_tokens (token_hash) WHERE used_at IS NULL;
CREATE INDEX idx_vtoken_expires ON verification_tokens (expires_at) WHERE used_at IS NULL;

-- ============================================================
-- FAVORITE TRANSFERS (daftar transfer sering)
-- ============================================================
CREATE TABLE favorite_transfers (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id             UUID NOT NULL REFERENCES users(id),
    destination_account VARCHAR(30) NOT NULL,
    destination_name    VARCHAR(255) NOT NULL,
    destination_bank    VARCHAR(50) NOT NULL DEFAULT 'BCA',
    bank_code           VARCHAR(10) NOT NULL DEFAULT '014',
    transfer_type       VARCHAR(20) NOT NULL,
    transfer_count      INT NOT NULL DEFAULT 0,
    last_transfer_at    TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (user_id, destination_account, bank_code)
);

CREATE INDEX idx_favorites_user ON favorite_transfers (user_id, last_transfer_at DESC);
```

### 000004 — E-Wallet & Providers

```sql
-- migrations/000004_create_ewallet.up.sql

-- ============================================================
-- E-WALLET PROVIDERS (master data)
-- ============================================================
CREATE TABLE ewallet_providers (
    id              VARCHAR(50) PRIMARY KEY,         -- 'gopay', 'ovo', 'dana', etc.
    name            VARCHAR(100) NOT NULL,
    icon_url        TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    min_amount      DECIMAL(18,2) NOT NULL DEFAULT 10000,
    max_amount      DECIMAL(18,2) NOT NULL DEFAULT 2000000,
    admin_fee       DECIMAL(18,2) NOT NULL DEFAULT 1000,
    preset_amounts  JSONB NOT NULL DEFAULT '[50000,100000,200000,500000,1000000]',
    sort_order      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed data
INSERT INTO ewallet_providers (id, name, is_active, admin_fee, sort_order) VALUES
    ('gopay',      'GoPay',      TRUE, 1000, 1),
    ('ovo',        'OVO',        TRUE, 1000, 2),
    ('dana',       'DANA',       TRUE, 1000, 3),
    ('shopeepay',  'ShopeePay',  TRUE, 1000, 4),
    ('linkaja',    'LinkAja',    TRUE, 1000, 5);
```

### 000005 — Notifications

```sql
-- migrations/000005_create_notifications.up.sql

-- ============================================================
-- NOTIFICATIONS
-- ============================================================
CREATE TABLE notifications (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),
    type            VARCHAR(30) NOT NULL
                    CHECK (type IN ('TRANSACTION', 'PROMO', 'SECURITY', 'SYSTEM', 'INFO')),
    title           VARCHAR(255) NOT NULL,
    body            TEXT NOT NULL,
    deep_link       VARCHAR(500),
    is_read         BOOLEAN NOT NULL DEFAULT FALSE,
    read_at         TIMESTAMPTZ,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_notif_user_unread ON notifications (user_id, created_at DESC) WHERE is_read = FALSE;
CREATE INDEX idx_notif_user ON notifications (user_id, created_at DESC);
```

### 000006 — Audit Logs

```sql
-- migrations/000006_create_audit_logs.up.sql

-- ============================================================
-- AUDIT LOGS (immutable, append-only)
-- ============================================================
CREATE TABLE audit_logs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID,
    session_id      UUID,
    action          VARCHAR(100) NOT NULL,
    resource_type   VARCHAR(50) NOT NULL,
    resource_id     VARCHAR(255),
    ip_address      INET,
    user_agent      TEXT,
    request_id      VARCHAR(100),
    old_values      JSONB,
    new_values      JSONB,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- BRIN index lebih efisien untuk time-series data
CREATE INDEX idx_audit_created ON audit_logs USING BRIN (created_at);
CREATE INDEX idx_audit_user ON audit_logs (user_id, created_at DESC);
CREATE INDEX idx_audit_action ON audit_logs (action, created_at DESC);
CREATE INDEX idx_audit_resource ON audit_logs (resource_type, resource_id);

-- Protect from updates/deletes
CREATE OR REPLACE FUNCTION prevent_audit_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'Audit logs are immutable. UPDATE and DELETE are not allowed.';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_immutable
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_modification();
```

### 000007 — Promotions

```sql
-- migrations/000007_create_promotions.up.sql

-- ============================================================
-- PROMOTIONS (untuk dashboard Beranda)
-- ============================================================
CREATE TABLE promotions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    title           VARCHAR(255) NOT NULL,
    description     TEXT,
    image_url       TEXT NOT NULL,
    deep_link       VARCHAR(500),
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    priority        INT NOT NULL DEFAULT 0,
    valid_from      TIMESTAMPTZ NOT NULL,
    valid_until     TIMESTAMPTZ NOT NULL,
    target_segment  VARCHAR(50),                     -- ALL, PREMIUM, NEW_USER
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_promo_active ON promotions (valid_from, valid_until) WHERE is_active = TRUE;
```

### 000008 — Rate Limiting & OTP

```sql
-- migrations/000008_create_rate_limiting.up.sql

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
-- REGISTRATIONS (pending account opening)
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
```

---

## Index Strategy Summary

| Table | Index | Purpose | Type |
|-------|-------|---------|------|
| `users` | `phone_hash` | Login lookup | B-tree, partial |
| `accounts` | `user_id` + `is_primary` | Dashboard balance | Unique, partial |
| `account_mutations` | `account_id, transaction_date DESC` | Mutasi listing | B-tree composite |
| `transactions` | `user_id, created_at DESC` | History listing | B-tree composite |
| `transactions` | `idempotency_key` | Duplicate prevention | B-tree, partial |
| `audit_logs` | `created_at` | Time-series query | BRIN |
| `sessions` | `refresh_token_hash` | Token refresh | B-tree, partial |
| `notifications` | `user_id` WHERE `is_read = FALSE` | Unread count | Partial |

---

## Data Retention Policy

| Data | Retention | Action |
|------|-----------|--------|
| Transactions | **Indefinite** | Immutable, never delete |
| Account Mutations | **7 tahun** | Regulatory requirement |
| Audit Logs | **10 tahun** | Compliance |
| Sessions | **30 hari** setelah expire | Hard delete via cron |
| OTP Codes | **24 jam** setelah expire | Hard delete via cron |
| Transfer Inquiries | **1 jam** setelah expire | Hard delete via cron |
| Verification Tokens | **5 menit** setelah expire | Hard delete via cron |
| Notifications | **1 tahun** | Soft archive |

---

## Backup Strategy

```
┌──────────────────────────────────────────────────┐
│                 Backup Strategy                  │
├──────────────────────────────────────────────────┤
│                                                  │
│  Continuous:  WAL streaming ke replica            │
│  Hourly:     Point-in-time recovery snapshots    │
│  Daily:      Full logical backup (pg_dump)       │
│  Weekly:     Full physical backup + verification │
│                                                  │
│  Encryption: Backup dienkripsi dengan AES-256    │
│  Location:   Multi-region (min 2 data center)    │
│  Testing:    Restore test setiap minggu          │
└──────────────────────────────────────────────────┘
```