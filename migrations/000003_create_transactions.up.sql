-- 000003_create_transactions.up.sql

-- ============================================================
-- TRANSACTIONS (immutable ledger)
-- ============================================================
CREATE TABLE transactions (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    idempotency_key     VARCHAR(255) UNIQUE,
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
    total_amount        DECIMAL(18,2) NOT NULL,
    currency            VARCHAR(3) NOT NULL DEFAULT 'IDR',
    reference_number    VARCHAR(50) NOT NULL UNIQUE,
    description         TEXT,
    notes               VARCHAR(500),

    -- Destination info (denormalized for immutability)
    destination_account VARCHAR(30),
    destination_name    VARCHAR(255),
    destination_bank    VARCHAR(50),
    destination_bank_code VARCHAR(10),

    -- Provider info
    provider_id         VARCHAR(50),
    provider_name       VARCHAR(100),
    provider_ref        VARCHAR(100),

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

CREATE INDEX idx_mutations_account_date ON account_mutations (account_id, transaction_date DESC, created_at DESC);
CREATE INDEX idx_mutations_txn ON account_mutations (transaction_id);

-- ============================================================
-- TRANSFER INQUIRIES
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
    metadata            JSONB,
    expires_at          TIMESTAMPTZ NOT NULL,
    used_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_inquiry_expires ON transfer_inquiries (expires_at) WHERE used_at IS NULL;

-- ============================================================
-- VERIFICATION TOKENS
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
-- FAVORITE TRANSFERS
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