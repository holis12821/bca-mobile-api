-- migrations/000009_ledger_and_device_binding.down.sql
--
-- Exact reverse of 000009, in reverse dependency order.
--
-- SAFETY: this rollback DELETES the settlement and fee-income accounts. If any
-- account_mutations rows already reference them, the FK will refuse the delete and
-- the whole migration aborts — which is the correct outcome. Once real money has
-- been posted against the settlement ledger, rolling back this migration would
-- destroy the counter-legs of live transactions. Do not force it; fix forward.

-- ============================================================
-- 9. Reconciliation views
-- ============================================================
DROP VIEW IF EXISTS v_unbalanced_transactions;
DROP VIEW IF EXISTS v_ledger_reconciliation;

-- ============================================================
-- 8. Restore server-clock defaults for dates
-- ============================================================
ALTER TABLE account_mutations ALTER COLUMN transaction_date SET DEFAULT CURRENT_DATE;
ALTER TABLE account_mutations ALTER COLUMN transaction_time SET DEFAULT CURRENT_TIME;
ALTER TABLE daily_usage      ALTER COLUMN usage_date       SET DEFAULT CURRENT_DATE;

COMMENT ON COLUMN account_mutations.transaction_date IS NULL;
COMMENT ON COLUMN daily_usage.usage_date IS NULL;

-- ============================================================
-- 7. Transaction limits
-- ============================================================
ALTER TABLE transaction_limits DROP CONSTRAINT IF EXISTS ck_transaction_limit_positive;
ALTER TABLE transaction_limits DROP CONSTRAINT IF EXISTS ck_transaction_limit_ceiling;

DROP TRIGGER IF EXISTS trg_users_seed_limits ON users;
DROP FUNCTION IF EXISTS trg_seed_limits_for_new_user();
DROP FUNCTION IF EXISTS seed_default_transaction_limits(UUID);

-- Seeded limit rows are intentionally LEFT IN PLACE. They are user data by the
-- time anyone rolls back, and deleting them would silently remove limits that the
-- customer may since have adjusted.

-- ============================================================
-- 6. Reference numbers
-- ============================================================
DROP FUNCTION IF EXISTS next_reference_number(DATE);
DROP SEQUENCE IF EXISTS seq_reference_number;

-- ============================================================
-- 5. Internal transfer destination
-- ============================================================
DROP INDEX IF EXISTS idx_txn_destination_account;
ALTER TABLE transactions DROP COLUMN IF EXISTS destination_account_id;

-- ============================================================
-- 4. Idempotency scope
-- ============================================================
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS uq_txn_user_idempotency;

-- Restoring the global unique constraint fails if two users have since used the
-- same key — which is exactly the situation the forward migration made legal.
DO $$
DECLARE dupes INT;
BEGIN
    SELECT COUNT(*) INTO dupes
    FROM (
        SELECT idempotency_key
        FROM transactions
        WHERE idempotency_key IS NOT NULL
        GROUP BY idempotency_key
        HAVING COUNT(*) > 1
    ) d;

    IF dupes > 0 THEN
        RAISE EXCEPTION
            'Rollback of 000009 aborted: % idempotency_key value(s) are now shared '
            'across users and cannot satisfy the old global UNIQUE constraint.', dupes;
    END IF;
END $$;

ALTER TABLE transactions
    ADD CONSTRAINT transactions_idempotency_key_key UNIQUE (idempotency_key);
CREATE INDEX idx_txn_idempotency ON transactions (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- ============================================================
-- 3. Settlement & fee-income accounts
-- ============================================================
DROP FUNCTION IF EXISTS settlement_account_id(VARCHAR, UUID);
DROP INDEX IF EXISTS idx_accounts_settlement_shard;

DELETE FROM accounts WHERE owner_type = 'INTERNAL';

ALTER TABLE accounts DROP CONSTRAINT accounts_account_type_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_account_type_check CHECK (
    account_type IN ('TAHAPAN', 'TAHAPAN_GOLD', 'TAPRES', 'TAHAPAN_XPRESI',
                     'GIRO', 'DEPOSITO')
);

ALTER TABLE accounts DROP CONSTRAINT IF EXISTS ck_accounts_owner_shape;
ALTER TABLE accounts ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE accounts DROP COLUMN IF EXISTS shard_index;
ALTER TABLE accounts DROP COLUMN IF EXISTS settlement_rail;
ALTER TABLE accounts DROP COLUMN IF EXISTS owner_type;

-- ============================================================
-- 2. Device binding
-- ============================================================
COMMENT ON COLUMN devices.device_id IS NULL;

DROP INDEX IF EXISTS idx_devices_device_id_history;
DROP INDEX IF EXISTS idx_devices_device_id_active;

CREATE INDEX idx_devices_device_id ON devices (device_id);
ALTER TABLE devices ADD CONSTRAINT devices_user_id_device_id_key UNIQUE (user_id, device_id);

-- ============================================================
-- 1. updated_at triggers
-- ============================================================
DROP TRIGGER IF EXISTS trg_registrations_updated_at      ON registrations;
DROP TRIGGER IF EXISTS trg_promotions_updated_at         ON promotions;
DROP TRIGGER IF EXISTS trg_ewallet_providers_updated_at  ON ewallet_providers;
DROP TRIGGER IF EXISTS trg_favorite_transfers_updated_at ON favorite_transfers;
DROP TRIGGER IF EXISTS trg_daily_usage_updated_at        ON daily_usage;
DROP TRIGGER IF EXISTS trg_transaction_limits_updated_at ON transaction_limits;
DROP TRIGGER IF EXISTS trg_accounts_updated_at           ON accounts;
DROP TRIGGER IF EXISTS trg_users_updated_at              ON users;

DROP FUNCTION IF EXISTS set_updated_at();
