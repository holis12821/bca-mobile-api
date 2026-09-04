-- 000004_create_ewallet.up.sql

-- ============================================================
-- E-WALLET PROVIDERS (master data)
-- ============================================================
CREATE TABLE ewallet_providers (
    id              VARCHAR(50) PRIMARY KEY,
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