# Database Schema — PostgreSQL

> Desain database banking-grade dengan audit trail, encryption at rest, dan optimasi index

> **Status:** Dokumen ini sudah dikoreksi dan konsisten dengan SKILL.md §14. Lihat juga [06-LEDGER-AND-DEVICE-BINDING.md](./06-LEDGER-AND-DEVICE-BINDING.md) untuk detail migration 000009.

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

    -- Note: global UNIQUE(user_id, device_id) replaced by partial index below
);

CREATE INDEX idx_devices_user_id ON devices (user_id) WHERE revoked_at IS NULL;
-- Partial unique: one active device_id at a time (migration 000009)
CREATE UNIQUE INDEX idx_devices_device_id_active ON devices (device_id) WHERE revoked_at IS NULL;

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
    user_id         UUID REFERENCES users(id),              -- NULL for internal/settlement accounts
    owner_type      VARCHAR(10) NOT NULL DEFAULT 'CUSTOMER'
                    CHECK (owner_type IN ('CUSTOMER', 'INTERNAL')),
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
    usage_date      DATE NOT NULL,                          -- WIB date, passed by application (no default)
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
    idempotency_key     VARCHAR(255),               -- Scoped per user (see constraint below)
    user_id             UUID NOT NULL REFERENCES users(id),
    source_account_id   UUID REFERENCES accounts(id),
    destination_account_id UUID REFERENCES accounts(id),    -- FK for internal transfers (migration 000009)
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

-- Idempotency scoped per user (migration 000009)
CREATE UNIQUE INDEX uq_txn_user_idempotency ON transactions (user_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

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
    transaction_date    DATE NOT NULL,                      -- WIB date, passed by application (no default — prevents timezone bugs)
    transaction_time    TIME NOT NULL,                      -- WIB time, passed by application (no default)
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
    -- Daftar dilebarkan migrasi 000024 menjadi delapan nilai. Ia HARUS sama
    -- dengan transaction.ValidPurposes di Go: dua salinan yang melenceng
    -- membuat POST /auth/pin/verify menjawab 500, dan itu persis yang terjadi
    -- pada CHANGE_PROFILE sampai migrasi itu ada.
    purpose         VARCHAR(30) NOT NULL
                    CHECK (purpose IN ('TRANSFER', 'EWALLET_TOPUP', 'QRIS_PAYMENT',
                           'CHANGE_LIMIT', 'CHANGE_PIN', 'CHANGE_PROFILE',
                           'BLOCK_CARD', 'REPLACE_CARD')),
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

### Triggers — `set_updated_at()` (migration 000009)

```sql
-- Shared trigger for all tables with updated_at column
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Applied to: users, accounts, transaction_limits, daily_usage,
-- ewallet_providers, favorite_transfers, promotions, registrations
```

### Seed Transaction Limits (migration 000009)

```sql
-- Auto-seed 5 limit rows per new user via AFTER INSERT trigger
-- Limit types: TRANSFER_INTERNAL, TRANSFER_EXTERNAL, EWALLET, QRIS, PAYMENT
-- Server-side ceilings enforced by CHECK constraint ck_transaction_limit_ceiling
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
| `onboarding_card_selection_log` | **10 tahun** | Belum ada job pembersihan — lihat catatan di bawah |

`onboarding_card_selection_log` menyimpan biaya bulanan yang **dilihat nasabah**
saat memilih kartu, jadi retensinya sejajar audit log. Job pembersihannya
**sengaja belum dipasang**: penghapusan data audit harus berjalan terjadwal dan
bisa diaudit sendiri, bukan disisipkan sebagai efek samping migrasi. Keputusan
retensinya tercatat di `docs/08-PILIH-KARTU-API-SPEC.md` §17 butir 9.

---

## Katalog Kartu Paspor — dari mana angkanya

`card_products` (migrasi `000019`) menyimpan biaya dan keempat limit per kartu;
isinya datang dari migrasi `000022_card_catalog_rates`, bukan dari seeder.

Dulu satu-satunya yang mengisi tabel itu adalah `scripts/seed/main.go`, dengan
angka yang sengaja palsu dan digerbangi `APP_ENV=development`. Akibatnya staging
tidak pernah punya katalog sama sekali. Katalog adalah data referensi, jadi
sekarang dibawa migrasi — satu sumber angka, ikut ke setiap environment.

**Angkanya adalah data portofolio, bukan tarif resmi BCA.** Perubahan tarif
berikutnya dilakukan lewat admin API katalog (`PUT /internal/v1/cards/{card_type}`),
yang menaikkan `card_catalog_version` dan menulis nilai lama + baru ke
`card_catalog_audit_log` — bukan lewat migrasi baru: tarif adalah operasi, bukan
skema.

Migrasi `down`-nya tidak menghapus baris `card_products` tanpa syarat.
`account_cards.card_type` dan `onboarding_sessions.card_type` keduanya punya
foreign key ke tabel itu, jadi baris yang masih dirujuk hanya dinolkan dan
dinonaktifkan; yang tidak dirujuk siapa pun dihapus.

---

## Konten Statis (migrasi `000023`)

Dua tabel yang melayani `GET /v1/content/help-center` dan
`GET /v1/content/contact-cs`.

```sql
CREATE TABLE content_help_center (
    id             BIGSERIAL PRIMARY KEY,
    category_key   TEXT NOT NULL,
    category_title TEXT NOT NULL,
    category_order INT  NOT NULL DEFAULT 1,
    question       TEXT NOT NULL,
    answer         TEXT NOT NULL,
    item_order     INT  NOT NULL DEFAULT 1,
    is_active      BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT content_help_unique_question UNIQUE (category_key, question)
);

CREATE TABLE content_contact_cs (
    id         SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),  -- satu baris saja
    phone      TEXT NOT NULL,
    phone_free TEXT NOT NULL,
    whatsapp   TEXT NOT NULL,
    email      TEXT NOT NULL,
    chat_url   TEXT NOT NULL,
    hours      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Tiga keputusan yang perlu diketahui sebelum mengubahnya:

- **Tabel FAQ datar, bukan kategori + item di dua tabel.** Isinya dibaca sekali
  seluruhnya lalu dikelompokkan di aplikasi; join untuk data sebesar ini hanya
  menambah bagian yang bisa rusak.
- **`CHECK (id = 1)` memaksa kontak CS hanya punya satu baris.** Pola yang sama
  dipakai `card_catalog_version`. Tanpa itu, "nomor CS mana yang benar" menjadi
  pertanyaan yang harus dijawab kode.
- **Datanya dibawa migrasi, bukan seeder.** Seeder menolak jalan di luar
  `APP_ENV=development`, jadi konten yang ditanam di sana tidak akan pernah ada
  di staging — kekeliruan yang sama pernah terjadi pada katalog kartu.

Menarik satu pertanyaan dari peredaran dilakukan dengan `is_active = FALSE`,
bukan `DELETE`: pertanyaan yang ditarik sementara sering kembali, dan
menghapusnya menghilangkan jawabannya juga.

---

## Syarat & Ketentuan Buka Rekening (migrasi `000025`)

Dua tabel yang melayani `GET /v1/onboarding/tnc` dan memvalidasi
`accepted_tnc_version` pada `POST /v1/onboarding/sessions`.

```sql
CREATE TABLE onboarding_tnc_documents (
    id      BIGSERIAL PRIMARY KEY,
    version TEXT NOT NULL UNIQUE CHECK (length(version) BETWEEN 1 AND 20),
    heading        TEXT NOT NULL,
    subtitle       TEXT NOT NULL,
    trust_title    TEXT NOT NULL,
    trust_subtitle TEXT NOT NULL,
    notice_label   TEXT NOT NULL,
    notice_body    TEXT NOT NULL,
    consent_prefix TEXT NOT NULL,
    consent_link   TEXT NOT NULL,
    consent_suffix TEXT NOT NULL DEFAULT '.',
    agree_cta      TEXT NOT NULL,
    is_active      BOOLEAN NOT NULL DEFAULT FALSE,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Paling banyak SATU versi aktif, ditegakkan database.
CREATE UNIQUE INDEX idx_onboarding_tnc_single_active
    ON onboarding_tnc_documents ((TRUE)) WHERE is_active;

CREATE TABLE onboarding_tnc_sections (
    id            BIGSERIAL PRIMARY KEY,
    document_id   BIGINT NOT NULL
        REFERENCES onboarding_tnc_documents (id) ON DELETE CASCADE,
    section_order INT  NOT NULL,
    icon_key      TEXT NOT NULL,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    CONSTRAINT onboarding_tnc_sections_order_unique UNIQUE (document_id, section_order)
);
```

Empat keputusan yang perlu diketahui sebelum mengubahnya:

- **Baris lama TIDAK PERNAH dihapus.** `onboarding_sessions.tnc_version` (migrasi
  `000010`) menunjuk ke `version` di sini — bukan lewat foreign key, karena
  kolomnya `VARCHAR(20)` dan sudah berisi nilai sejak sebelum tabel ini ada.
  Menghapus versi lama berarti menghapus bukti persetujuan nasabah yang
  memakainya. Mencabut versi dilakukan dengan `is_active = FALSE`.
- **`CHECK (length(version) ≤ 20)` menjaga kedua sisi tetap sejalan.** Kolom di
  sisi sesi `VARCHAR(20)`. Tanpa CHECK ini, versi ke-21 karakter lolos di sini
  lalu **menggagalkan setiap pembuatan sesi** yang menyebutnya — kegagalan yang
  muncul jauh dari penyebabnya.
- **Indeks unik parsial pada `((TRUE))` memaksa satu versi aktif.** Tanpa itu,
  "versi S&K yang benar hari ini" menjadi pertanyaan yang dijawab `ORDER BY`, dan
  jawaban yang bergantung pada urutan baris bisa berubah sendiri. Konsekuensinya:
  mengaktifkan versi baru **harus** didahului `UPDATE … SET is_active = FALSE`
  dalam transaksi yang sama, atau `INSERT`-nya ditolak.
- **Pasal di tabel terpisah, bukan JSONB atau lima kolom `section_N_*`.** Jumlah
  pasalnya berubah tiap revisi teks hukum, dan menambah pasal keenam tidak boleh
  berarti menambah kolom.

Teks awalnya disalin apa adanya dari `strings.xml` aplikasi Android (`buka_rekening_sk_*`)
supaya nasabah tidak melihat perubahan kata satu pun saat layarnya pindah ke API, dan
nomor versinya sengaja sama dengan konstanta `TNC_VERSION` yang sudah beredar — nomor baru
akan membuat setiap pembukaan rekening dari APK lama ditolak `TNC_VERSION_OUTDATED` pada
hari migrasi ini jalan.

---

## Petugas CS Video Call (migrasi `000026`)

```sql
CREATE TABLE cs_agents (
    employee_id  VARCHAR(32) PRIMARY KEY,
    name         VARCHAR(128) NOT NULL,
    api_key_hash TEXT        NOT NULL,
    is_active    BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- migrasi 000027
    scopes       TEXT[]      NOT NULL DEFAULT ARRAY['VIDEO_CALL']::TEXT[],
    CONSTRAINT cs_agents_scopes_valid CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY['VIDEO_CALL', 'CARD_ADMIN', 'CUSTOMER_PII', 'TICKET']::TEXT[]
    )
);

CREATE INDEX idx_cs_agents_active ON cs_agents (employee_id) WHERE is_active;
```

Siapa yang berwenang melayani verifikasi video call e-KYC. Sebelum tabel ini,
`onboarding_video_calls.agent_employee_id` dan `agent_name` diisi dari **body** permintaan
dan dipercaya apa adanya, dengan satu `INTERNAL_API_KEY` yang sama untuk seluruh integrasi
CS — jadi siapa pun yang memegang key itu bisa mengaku sebagai pegawai mana pun, dan string
itulah yang masuk `onboarding_audit_logs.actor` serta tampil ke layar nasabah lewat
`agent_assigned`. Untuk verifikasi identitas yang hasilnya membuka pembukaan rekening,
jejaknya harus bisa dipertanggungjawabkan ke orang.

| Kolom | Catatan |
|---|---|
| `employee_id` | yang dikirim pemanggil di `X-Agent-Employee-ID`; sama lebarnya dengan `onboarding_video_calls.agent_employee_id` |
| `api_key_hash` | PHC Argon2id, format yang sama dengan `users.pin_hash` |
| `is_active` | pencabutan hak tanpa menghapus baris — panggilan lama tetap punya rujukan nama petugasnya |
| `scopes` | cakupan kewenangan (migrasi `000027`). Dibaca bersama baris yang mengautentikasi, bukan ditanya terpisah: kewenangan yang dibaca dari baris berbeda membuka celah waktu antara keduanya |

Hash Argon2 ber-salt **tidak bisa dicari balik**, jadi pemanggil menyebut dirinya lebih
dulu lewat `X-Agent-Employee-ID` dan membuktikannya dengan `X-Agent-API-Key`: barisnya
dicari dengan `employee_id`, hash-nya diverifikasi Argon2. Pola yang sama dipakai login
nasabah.

**Barisnya bukan data referensi dan tidak ikut di migrasi.** Isinya kredensial, jadi
migrasi ini hanya membuat tabelnya kosong. Di development `make seed` menanam satu petugas
(`CS-1042`, kunci `dev-agent-key`) dan **digerbangi `APP_ENV=development`** — seeder ini
tidak punya gerbang environment sendiri, jadi tanpa gerbang di sana satu kali `make seed`
yang salah arah akan membuat kunci yang diketahui umum bisa dipakai menandatangani hasil
verifikasi. Di luar development, barisnya dibuat yang mengoperasikan integrasi CS dengan
kunci acak, lewat jalur yang sama dengan pendistribusian `INTERNAL_API_KEY`.

### Cakupan kewenangan (migrasi `000027`)

Sebelum kolom `scopes`, satu `INTERNAL_API_KEY` membuka **seluruh** `/internal/v1`:
petugas yang tugasnya melayani video call e-KYC juga bisa mengubah biaya dan limit kartu
Paspor untuk seluruh nasabah, dan menaikkan `catalog_version` yang memaksa setiap aplikasi
memuat ulang katalognya. Kewenangan itu tidak pernah diberikan kepadanya — hanya kebetulan
tidak dipisahkan.

| Cakupan | Membuka |
|---|---|
| `VIDEO_CALL` | ambil panggilan, submit hasil, antrean, daftar sesi onboarding |
| `CUSTOMER_PII` | detail sesi berisi data pribadi, pencarian & profil nasabah |
| `CARD_ADMIN` | `PUT /internal/v1/cards/*` |
| `TICKET` | `/internal/v1/tickets/*` |

`CUSTOMER_PII` **dipisah** dari `VIDEO_CALL` meski aplikasi desktop yang sama memakai
keduanya: melayani panggilan menampilkan nasabah yang *sedang* bicara, sementara membuka
data pribadi menjangkau nasabah mana pun yang pernah mendaftar. Dua kewenangan yang berbeda
ukurannya, dan menggabungkannya berarti setiap petugas panggilan diam-diam memegang yang
kedua.

Default `ARRAY['VIDEO_CALL']`, bukan keduanya: setiap baris yang sudah ada dibuat untuk
melayani video call, dan migrasi yang diam-diam memberi kewenangan katalog kepada mereka
akan melakukan persis hal yang kolom ini ada untuk mencegahnya.

> **Rollback migrasi 000027 MENGHAPUS data kewenangan.** `down` lalu `up` lagi membuat
> setiap baris kembali ke default `VIDEO_CALL` — petugas ber-scope `CARD_ADMIN` kehilangan
> kewenangannya dan setiap petugas mendadak memegang `VIDEO_CALL`. Ini bukan teori: terjadi
> saat migrasi diuji, dan gejalanya adalah `403` yang tampak seperti bug kode. Sebelum
> rollback di lingkungan yang dipakai:
> `COPY (SELECT employee_id, scopes FROM cs_agents) TO '/tmp/cs_scopes.csv' CSV;`

---

## Jejak Akses Petugas ke Data Nasabah (migrasi `000029`)

```sql
CREATE TABLE cs_access_logs (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_employee_id VARCHAR(32) NOT NULL,
    action            VARCHAR(48) NOT NULL,   -- CUSTOMER_SEARCH | CUSTOMER_VIEWED
    subject_user_id   UUID        REFERENCES users (id) ON DELETE SET NULL,
    query_kind        VARCHAR(24),            -- ACCOUNT_NUMBER | PHONE
    result_count      INTEGER     NOT NULL DEFAULT 0,
    ip_address        VARCHAR(45),
    user_agent        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_cs_access_logs_agent   ON cs_access_logs (agent_employee_id, created_at DESC);
CREATE INDEX idx_cs_access_logs_subject ON cs_access_logs (subject_user_id, created_at DESC)
    WHERE subject_user_id IS NOT NULL;

CREATE TRIGGER trg_cs_access_logs_immutable
    BEFORE UPDATE OR DELETE ON cs_access_logs
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
```

Siapa membuka data siapa, kapan, dan dari mana. Terpisah dari `onboarding_audit_logs`, dan
bukan karena rapi-rapi: jejak itu ber-kunci `session_id` onboarding, sementara nasabah yang
sudah punya rekening tidak punya sesi onboarding lagi. Pencarian nasabah juga tidak punya
subjek sampai hasilnya ditemukan.

| Kolom | Catatan |
|---|---|
| `subject_user_id` | NULL untuk pencarian yang tidak menemukan apa pun — tidak ada data siapa pun yang terbuka, dan baris dengan subjek palsu akan membuat jejak satu nasabah memuat pencarian yang bukan tentang dia. Diisi hanya kalau hasilnya tepat **satu** orang |
| `query_kind` | **JENIS** kunci pencarian, BUKAN nilainya |
| `result_count` | pencarian yang gagal ikut dicatat; pola pencarian yang gagal justru yang paling perlu terlihat saat memeriksa penyalahgunaan |

**`query_kind` tidak menyimpan kata kuncinya, dan itu keputusan yang paling mudah salah di
tabel seperti ini.** Menyimpannya akan menumpuk nomor rekening dan nomor HP nasabah di tabel
log yang tidak terenkripsi dan jarang ditinjau — memindahkan kebocoran, bukan mencatatnya.

Migrasi ini juga **mengganti pesan** `prevent_audit_mutation()` menjadi
`'% is append-only', TG_TABLE_NAME`. Sebelumnya pesannya menyebut `onboarding_audit_logs`
secara harfiah, jadi penolakan di `cs_access_logs` akan berbunyi dengan nama tabel yang
salah dan orang yang menelusurinya mencari di tempat yang salah.

---

## Indeks Daftar Sesi CS (migrasi `000028`)

```sql
CREATE INDEX idx_onboarding_sessions_cs_list
    ON onboarding_sessions (created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
```

`GET /internal/v1/onboarding/sessions` mengurut `created_at DESC, id DESC` dan diambil
lewat keyset. Tanpa indeks ini rencananya **Seq Scan + Sort atas seluruh
`onboarding_sessions`** — tabel yang tidak pernah menyusut, karena setiap percobaan
pendaftaran meninggalkan satu baris selamanya. Dan daftar itu di-polling tiap lima detik
oleh setiap petugas yang sedang bertugas.

Partial `WHERE deleted_at IS NULL` karena setiap query daftar menyertakan syarat itu.

---

## Tiket Layanan (migrasi `000030`)

```sql
CREATE TYPE ticket_status   AS ENUM ('OPEN', 'IN_PROGRESS', 'RESOLVED', 'CLOSED');
CREATE TYPE ticket_priority AS ENUM ('LOW', 'NORMAL', 'HIGH', 'URGENT');
CREATE TYPE ticket_category AS ENUM ('KARTU','TRANSAKSI','AKUN','BUKA_REKENING','APLIKASI','LAINNYA');

CREATE SEQUENCE service_ticket_number_seq;

CREATE TABLE service_tickets (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_number VARCHAR(32) NOT NULL UNIQUE,
    user_id       UUID        REFERENCES users (id) ON DELETE SET NULL,
    session_id    VARCHAR(32),
    category      ticket_category NOT NULL DEFAULT 'LAINNYA',
    priority      ticket_priority NOT NULL DEFAULT 'NORMAL',
    status        ticket_status   NOT NULL DEFAULT 'OPEN',
    subject       VARCHAR(200) NOT NULL,
    description   TEXT        NOT NULL DEFAULT '',
    created_by_agent  VARCHAR(32) NOT NULL,
    assigned_to_agent VARCHAR(32),
    resolved_at   TIMESTAMPTZ,
    closed_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT service_tickets_subject_not_blank CHECK (btrim(subject) <> ''),
    CONSTRAINT service_tickets_resolved_consistent CHECK (
        (status IN ('RESOLVED', 'CLOSED')) = (resolved_at IS NOT NULL)),
    CONSTRAINT service_tickets_closed_consistent CHECK (
        (status = 'CLOSED') = (closed_at IS NOT NULL))
);

CREATE TABLE service_ticket_notes (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id  UUID        NOT NULL REFERENCES service_tickets (id) ON DELETE CASCADE,
    author     VARCHAR(32) NOT NULL,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT service_ticket_notes_body_not_blank CHECK (btrim(body) <> '')
);
```

**`ticket_number` dirakit di dalam `INSERT`** dari `service_ticket_number_seq`, bukan
dihitung lebih dulu lalu dikirim: `COUNT(*)+1` akan memberi nomor yang sama kepada dua
petugas yang membuat tiket bersamaan, dan angka acak menghasilkan nomor yang tidak bisa
disebutkan lewat telepon. Sequence aman terhadap keduanya dan tidak mundur saat transaksi
dibatalkan — celah nomor jauh lebih murah daripada tabrakan nomor. Formatnya
`TKT-YYYYMMDD-000123`, dan **itulah** yang dipakai sebagai kunci di URL, bukan UUID-nya.

**Stempel waktu diikat ke status oleh CHECK.** Tanpa itu, tiket bisa berstatus `RESOLVED`
tanpa `resolved_at` dan setiap laporan waktu penyelesaian akan diam-diam melewatkannya.
Konsekuensinya: perubahan status dan stempelnya harus satu `UPDATE`, bukan dua.

**Catatan terpisah dari `description`** supaya riwayat penanganan tidak saling menimpa:
satu petugas yang menyunting deskripsi akan menghapus apa yang ditulis petugas sebelumnya,
dan di tiket keluhan itu justru bagian yang paling perlu utuh.

`user_id` dan `session_id` keduanya nullable dan **boleh terisi sekaligus**: penelepon yang
belum punya rekening hanya punya `session_id`, nasabah lama hanya punya `user_id`, dan orang
yang gagal di tengah pembukaan rekening lalu menelepon punya dua-duanya. Menuntut salah
satunya akan menolak tiket yang paling perlu dicatat.

---

## Jadwal Video Call (migrasi `000031`)

```sql
CREATE TYPE video_call_schedule_status AS ENUM ('SCHEDULED', 'CANCELLED', 'FULFILLED');

CREATE TABLE onboarding_video_call_schedules (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    schedule_id  VARCHAR(40) NOT NULL UNIQUE,
    session_id   VARCHAR(32) NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    status       video_call_schedule_status NOT NULL DEFAULT 'SCHEDULED',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_vc_schedules_one_active
    ON onboarding_video_call_schedules (session_id) WHERE status = 'SCHEDULED';

CREATE INDEX idx_vc_schedules_upcoming
    ON onboarding_video_call_schedules (scheduled_at) WHERE status = 'SCHEDULED';
```

Melayani tombol "Jadwalkan Panggilan Nanti" di aplikasi Android, yang selama ini dimatikan
karena tidak ada endpoint yang menerimanya.

**Tabel tersendiri, bukan kolom di `onboarding_video_calls`:** jadwal ada *sebelum*
panggilan ada, dan baris `onboarding_video_calls` baru lahir saat nasabah benar-benar masuk
antrean. Menyimpannya di sana akan menuntut baris panggilan yang statusnya bukan panggilan.

**Partial unique index, bukan `UNIQUE` biasa:** nasabah yang membatalkan lalu menjadwalkan
ulang harus bisa, dan riwayat pembatalannya tetap ada. Tanpa index itu, menekan tombol dua
kali menghasilkan dua jadwal dan nasabah tidak tahu mana yang berlaku. Jadwal ganda
dijawab dari **pelanggaran index** (SQLSTATE 23505), bukan dari `SELECT` lebih dulu: dua
permintaan bersamaan akan sama-sama melihat "belum ada jadwal".

`scheduled_at` disimpan UTC; validasi jam operasional 06:00–22:00 dilakukan di aplikasi
dalam zona `Asia/Jakarta` — seperti batas harian transaksi, dan dengan alasan yang sama:
jam database bukan jam Jakarta.

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