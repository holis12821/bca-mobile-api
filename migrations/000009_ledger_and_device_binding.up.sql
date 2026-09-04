-- migrations/000009_ledger_and_device_binding.up.sql
--
-- Closes the two open decisions from the spec review:
--   A. devices.device_id becomes the global identity used by the APIs (1 device = 1 active user)
--   B. settlement + fee-income accounts, so outbound rails balance under double-entry
--
-- Plus the constraint fixes that block correct implementation of §5-§8 of the
-- bca-mobile-backend conventions:
--   C. idempotency scoped per user
--   D. transactions.destination_account_id for internal transfers
--   E. collision-free, non-sequential reference numbers
--   F. updated_at actually maintained
--   G. default transaction limits guaranteed to exist, with server-side ceilings
--   H. WIB dates must be supplied by the application, never by the server clock
--
-- Run inside a single transaction. golang-migrate wraps this file automatically.

-- ============================================================
-- 0. PRE-CHECK — abort before touching anything if data violates decision A
-- ============================================================
-- A device fingerprint that is currently active for two different users cannot
-- satisfy the new unique index. Fail loudly with a count rather than letting
-- CREATE UNIQUE INDEX emit an opaque error on one arbitrary row.

DO $$
DECLARE
    conflicting_devices INT;
BEGIN
    SELECT COUNT(*) INTO conflicting_devices
    FROM (
        SELECT device_id
        FROM devices
        WHERE revoked_at IS NULL
        GROUP BY device_id
        HAVING COUNT(DISTINCT user_id) > 1
    ) AS d;

    IF conflicting_devices > 0 THEN
        RAISE EXCEPTION
            'Migration 000009 aborted: % device_id value(s) are active for more than one user. '
            'Run scripts/000009_precheck_devices.sql and revoke the stale rows first.',
            conflicting_devices;
    END IF;
END $$;

-- ============================================================
-- 1. SHARED updated_at TRIGGER  (fix F)
-- ============================================================
-- The 000001-000008 DDL declares updated_at with DEFAULT NOW() but nothing ever
-- advances it, so every updated_at in the database is really a created_at.

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at              BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_accounts_updated_at           BEFORE UPDATE ON accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_transaction_limits_updated_at BEFORE UPDATE ON transaction_limits
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_daily_usage_updated_at        BEFORE UPDATE ON daily_usage
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_favorite_transfers_updated_at BEFORE UPDATE ON favorite_transfers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_ewallet_providers_updated_at  BEFORE UPDATE ON ewallet_providers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_promotions_updated_at         BEFORE UPDATE ON promotions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_registrations_updated_at      BEFORE UPDATE ON registrations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- NOTE: no trigger on transactions or account_mutations — those tables are
-- append-only and have no updated_at. audit_logs keeps its immutability trigger.

-- ============================================================
-- 2. DEVICE BINDING  (decision A)
-- ============================================================
-- device_id (the client fingerprint) is now the identity the APIs key on:
-- X-Device-ID, POST /auth/login/pin, /auth/login/biometric, /auth/token/refresh,
-- /auth/logout all resolve the user from it alone.
--
-- Uniqueness is PARTIAL on revoked_at IS NULL. A global unique constraint would
-- permanently burn a fingerprint: a resold or re-provisioned handset could never
-- be registered to its new owner. Revoking the old row releases the fingerprint.

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_user_id_device_id_key;

CREATE UNIQUE INDEX idx_devices_device_id_active
    ON devices (device_id)
    WHERE revoked_at IS NULL;

-- Retain a non-unique lookup for revoked history (audit, fraud review).
DROP INDEX IF EXISTS idx_devices_device_id;
CREATE INDEX idx_devices_device_id_history ON devices (device_id, revoked_at DESC);

COMMENT ON COLUMN devices.device_id IS
    'Client device fingerprint. Unique across ALL users while revoked_at IS NULL '
    '(1 active device = 1 user). Resolve the user from this value on login.';

-- ============================================================
-- 3. SETTLEMENT & FEE-INCOME ACCOUNTS  (decision B)
-- ============================================================
-- Only BCA-to-BCA transfers have both legs inside this database. External
-- transfers, e-wallet top-ups and QRIS payments move money OUT, and every one of
-- them also collects an admin fee. Without a counter-account the ledger is
-- single-sided and SUM(mutations) will never reconcile to SUM(balances).

-- 3a. Internal accounts have no owning customer.
ALTER TABLE accounts ADD COLUMN owner_type VARCHAR(20) NOT NULL DEFAULT 'CUSTOMER'
    CHECK (owner_type IN ('CUSTOMER', 'INTERNAL'));

ALTER TABLE accounts ADD COLUMN settlement_rail VARCHAR(30)
    CHECK (settlement_rail IN ('TRANSFER_EXTERNAL', 'EWALLET', 'QRIS', 'FEE_INCOME'));

ALTER TABLE accounts ADD COLUMN shard_index INT;

ALTER TABLE accounts ALTER COLUMN user_id DROP NOT NULL;

ALTER TABLE accounts ADD CONSTRAINT ck_accounts_owner_shape CHECK (
    (owner_type = 'CUSTOMER' AND user_id IS NOT NULL
                             AND settlement_rail IS NULL
                             AND shard_index IS NULL)
 OR (owner_type = 'INTERNAL' AND user_id IS NULL
                             AND settlement_rail IS NOT NULL
                             AND shard_index IS NOT NULL)
);

-- 3b. Widen account_type for the internal accounts.
ALTER TABLE accounts DROP CONSTRAINT accounts_account_type_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_account_type_check CHECK (
    account_type IN ('TAHAPAN', 'TAHAPAN_GOLD', 'TAPRES', 'TAHAPAN_XPRESI',
                     'GIRO', 'DEPOSITO', 'SETTLEMENT', 'FEE_INCOME')
);

CREATE UNIQUE INDEX idx_accounts_settlement_shard
    ON accounts (settlement_rail, shard_index)
    WHERE owner_type = 'INTERNAL';

-- 3c. Seed 16 shards per rail.
--
-- WHY SHARDS: every outbound transaction locks its settlement row with
-- SELECT ... FOR UPDATE. A single row per rail would serialise the entire
-- e-wallet throughput of the bank behind one lock. 16 shards, picked by
-- hashtext(transaction_id) mod 16, spread that contention. Shards participate in
-- the ORDER BY id lock ordering like any other account, so no new deadlock risk.
-- The rail balance is SUM(balance) over its shards.
--
-- Account numbers: 99 + rail(2) + shard(4) + check(2), outside the customer range.

INSERT INTO accounts (user_id, account_number, account_type, account_label,
                      currency, balance, hold_amount, is_primary, status,
                      owner_type, settlement_rail, shard_index)
SELECT
    NULL,
    '99' || rail.code || lpad(s.i::text, 4, '0') || '00',
    CASE WHEN rail.name = 'FEE_INCOME' THEN 'FEE_INCOME' ELSE 'SETTLEMENT' END,
    rail.label || ' Shard ' || lpad(s.i::text, 2, '0'),
    'IDR', 0, 0, FALSE, 'ACTIVE',
    'INTERNAL', rail.name, s.i
FROM (VALUES
        ('01', 'TRANSFER_EXTERNAL', 'Settlement Transfer Antar Bank'),
        ('02', 'EWALLET',           'Settlement E-Wallet'),
        ('03', 'QRIS',              'Settlement QRIS'),
        ('09', 'FEE_INCOME',        'Pendapatan Biaya Admin')
     ) AS rail(code, name, label)
CROSS JOIN generate_series(0, 15) AS s(i);

-- 3d. Helper: resolve the shard for a transaction, deterministically.
CREATE OR REPLACE FUNCTION settlement_account_id(p_rail VARCHAR, p_key UUID)
RETURNS UUID AS $$
    SELECT id
    FROM accounts
    WHERE owner_type = 'INTERNAL'
      AND settlement_rail = p_rail
      AND shard_index = (abs(hashtext(p_key::text)) % 16);
$$ LANGUAGE sql STABLE;

COMMENT ON FUNCTION settlement_account_id IS
    'Deterministic settlement/fee shard for a transaction id. Change the modulus '
    'here and the shard seed in 000009 together, never separately.';

-- ============================================================
-- 4. IDEMPOTENCY SCOPED PER USER  (fix C)
-- ============================================================
-- A globally unique idempotency_key lets one customer''s key collide with
-- another''s, and the 409 that results leaks the existence of a foreign
-- transaction. Scope it, exactly like the Redis key idem:{user_id}:{key}.

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_idempotency_key_key;
DROP INDEX IF EXISTS idx_txn_idempotency;

ALTER TABLE transactions
    ADD CONSTRAINT uq_txn_user_idempotency UNIQUE (user_id, idempotency_key);

-- The replay path (Redis TTL expired, request retried) reads by this pair and
-- returns the ORIGINAL transaction rather than creating a second one.

-- ============================================================
-- 5. INTERNAL TRANSFER DESTINATION  (fix D)
-- ============================================================
-- destination_account is denormalised text kept for immutability of the receipt.
-- The CREDIT leg needs a real account id, resolved inside the transaction.

ALTER TABLE transactions
    ADD COLUMN destination_account_id UUID REFERENCES accounts(id);

CREATE INDEX idx_txn_destination_account
    ON transactions (destination_account_id, created_at DESC)
    WHERE destination_account_id IS NOT NULL;

COMMENT ON COLUMN transactions.destination_account_id IS
    'Set for TRANSFER_INTERNAL only. NULL for outbound rails, whose counter-leg '
    'is a settlement shard recorded in account_mutations.';

-- ============================================================
-- 6. REFERENCE NUMBERS  (fix E)
-- ============================================================
-- REF + YYYYMMDD(WIB) + 8 digits. The digits come from a sequence passed through
-- a multiply-modulo bijection, so they are collision-free within a 10^8 cycle but
-- do not expose a global transaction counter to customers.
-- 1327144003 is odd and not divisible by 5, therefore coprime with 10^8, therefore
-- the map is a bijection over Z/10^8.

CREATE SEQUENCE seq_reference_number AS BIGINT
    START WITH 1 MINVALUE 1 MAXVALUE 99999999 CYCLE;

CREATE OR REPLACE FUNCTION next_reference_number(p_wib_date DATE)
RETURNS VARCHAR AS $$
DECLARE
    raw        BIGINT;
    scrambled  BIGINT;
BEGIN
    raw := nextval('seq_reference_number');
    scrambled := (raw * 1327144003) % 100000000;
    RETURN 'REF' || to_char(p_wib_date, 'YYYYMMDD') || lpad(scrambled::text, 8, '0');
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION next_reference_number IS
    'Caller MUST pass the WIB (Asia/Jakarta) calendar date. Never CURRENT_DATE.';

-- ============================================================
-- 7. DEFAULT TRANSACTION LIMITS + SERVER-SIDE CEILINGS  (fix G)
-- ============================================================
-- transaction_limits had no seed data. A user with no row has no limit row to
-- compare against, and the natural coding mistake is to read that as unlimited.
-- Guarantee the rows exist, and cap what the customer may raise them to.

CREATE OR REPLACE FUNCTION seed_default_transaction_limits(p_user_id UUID)
RETURNS VOID AS $$
BEGIN
    INSERT INTO transaction_limits (user_id, limit_type, daily_limit, monthly_limit, per_transaction_limit)
    VALUES
        (p_user_id, 'TRANSFER_INTERNAL',  50000000,  500000000,  50000000),
        (p_user_id, 'TRANSFER_EXTERNAL',  25000000,  250000000,  25000000),
        (p_user_id, 'EWALLET',            10000000,  100000000,   2000000),
        (p_user_id, 'QRIS',               10000000,  100000000,   5000000),
        (p_user_id, 'PAYMENT',            25000000,  250000000,  25000000)
    ON CONFLICT (user_id, limit_type) DO NOTHING;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION trg_seed_limits_for_new_user()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM seed_default_transaction_limits(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_seed_limits
    AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION trg_seed_limits_for_new_user();

-- Backfill existing users.
DO $$
DECLARE u RECORD;
BEGIN
    FOR u IN SELECT id FROM users WHERE deleted_at IS NULL LOOP
        PERFORM seed_default_transaction_limits(u.id);
    END LOOP;
END $$;

-- Hard ceilings. The API lets the customer raise their own limits; these are the
-- values they can never exceed, enforced below the service layer.
ALTER TABLE transaction_limits ADD CONSTRAINT ck_transaction_limit_ceiling CHECK (
    CASE limit_type
        WHEN 'TRANSFER_INTERNAL' THEN daily_limit <= 100000000
        WHEN 'TRANSFER_EXTERNAL' THEN daily_limit <= 100000000
        WHEN 'EWALLET'           THEN daily_limit <=  20000000
        WHEN 'QRIS'              THEN daily_limit <=  20000000
                                      AND COALESCE(per_transaction_limit, 0) <= 5000000
        WHEN 'PAYMENT'           THEN daily_limit <= 100000000
        ELSE TRUE
    END
);

ALTER TABLE transaction_limits ADD CONSTRAINT ck_transaction_limit_positive
    CHECK (daily_limit >= 0
       AND COALESCE(monthly_limit, 0) >= 0
       AND COALESCE(per_transaction_limit, 0) >= 0);

-- ============================================================
-- 8. WIB DATES MUST COME FROM THE APPLICATION  (fix H)
-- ============================================================
-- CURRENT_DATE / CURRENT_TIME follow the SERVER timezone. A transfer at 06:00 WIB
-- is 23:00 UTC the previous day, so a UTC server files it on the wrong statement
-- day and against the wrong daily limit bucket. Removing the defaults turns that
-- silent off-by-one-day into a NOT NULL violation at development time.

ALTER TABLE account_mutations ALTER COLUMN transaction_date DROP DEFAULT;
ALTER TABLE account_mutations ALTER COLUMN transaction_time DROP DEFAULT;
ALTER TABLE daily_usage      ALTER COLUMN usage_date       DROP DEFAULT;

COMMENT ON COLUMN account_mutations.transaction_date IS
    'WIB (Asia/Jakarta) calendar date, supplied by the application. No default: '
    'the server clock must never decide the statement day.';
COMMENT ON COLUMN daily_usage.usage_date IS
    'WIB calendar date. Daily limits reset at WIB midnight, not UTC midnight.';

-- ============================================================
-- 9. LEDGER RECONCILIATION VIEW
-- ============================================================
-- Every account that HAS ledger history must have balance = its latest
-- balance_after. Any row returned by this view is a ledger defect.
--
-- Accounts with no mutations at all are excluded deliberately: a freshly opened
-- account, or one seeded with an opening balance before the ledger existed, is
-- not drift. Comparing them against 0 would make the view permanently noisy and
-- therefore permanently ignored.

CREATE VIEW v_ledger_reconciliation AS
SELECT
    a.id               AS account_id,
    a.account_number,
    a.owner_type,
    a.settlement_rail,
    a.balance          AS account_balance,
    m.balance_after    AS last_mutation_balance,
    a.balance - m.balance_after AS drift
FROM accounts a
JOIN LATERAL (
    SELECT balance_after
    FROM account_mutations
    WHERE account_id = a.id
    ORDER BY transaction_date DESC, created_at DESC, id DESC
    LIMIT 1
) m ON TRUE
WHERE a.balance <> m.balance_after;

COMMENT ON VIEW v_ledger_reconciliation IS
    'Accounts whose balance disagrees with their latest mutation. Expected: empty. '
    'Accounts with no mutation history are out of scope by design.';

-- Companion check: within one transaction, debits must equal credits.
CREATE VIEW v_unbalanced_transactions AS
SELECT
    t.id AS transaction_id,
    t.reference_number,
    t.type,
    t.total_amount,
    SUM(CASE WHEN m.mutation_type = 'DEBIT' THEN m.amount ELSE -m.amount END) AS imbalance
FROM transactions t
JOIN account_mutations m ON m.transaction_id = t.id
WHERE t.status = 'SUCCESS'
GROUP BY t.id, t.reference_number, t.type, t.total_amount
HAVING SUM(CASE WHEN m.mutation_type = 'DEBIT' THEN m.amount ELSE -m.amount END) <> 0;

COMMENT ON VIEW v_unbalanced_transactions IS
    'Successful transactions whose mutation legs do not sum to zero — a single-sided '
    'posting, i.e. a missing settlement or fee leg. Expected: empty.';
