-- 000002_create_accounts.up.sql

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
-- DAILY USAGE TRACKING
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