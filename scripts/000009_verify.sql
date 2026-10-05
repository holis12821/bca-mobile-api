-- scripts/000009_verify.sql — behavioural checks for migration 000009.
-- Every SELECT below must print PASS. Run with -v ON_ERROR_STOP=1.
--
-- The script is rerunnable. It used not to be: the fixtures below carry fixed
-- UUIDs and nothing removed them, so a second run died on
--
--     ERROR: duplicate key value violates unique constraint "users_pkey"
--
-- and `make verify-009` was dead on every database it had ever succeeded on
-- once. The leftovers also moved two INTERNAL account balances (see L1), which
-- is the kind of residue `make ledger-check` is supposed to catch.
--
-- verify_009_cleanup() below owns the whole fixture footprint and runs twice:
-- once up front, so a database an older version of this script polluted heals
-- itself, and once at the end. The one row it cannot remove is the audit_logs
-- row from X1 — audit_logs rejects DELETE by trigger, which is the property X1
-- exists to prove. One append-only row per run is the intended cost.
--
-- The fixture accounts also moved off 1234567890 / 0987654321. Those are numbers
-- scripts/seed/main.go creates, and while the fixtures were being left behind the
-- seeder's ON CONFLICT (account_number) adopted one of them — the row kept the
-- fixture UUID and became a real customer account. Fixture numbers now live in a
-- 915* range nothing else writes.
--
-- Wrapping everything in a single transaction and rolling back would be
-- shorter, and it does not work: set_updated_at uses NOW(), which is frozen for
-- the whole transaction, so F1 would compare a timestamp against itself and
-- report a failure that is an artifact of the harness.
\set QUIET on
\pset footer off

CREATE OR REPLACE FUNCTION verify_009_cleanup() RETURNS void AS $fn$
DECLARE
    v_users UUID[] := ARRAY['11111111-1111-1111-1111-111111111111',
                            '22222222-2222-2222-2222-222222222222']::UUID[];
    -- Both the current fixture accounts and the pair older versions of this
    -- script used, so a database polluted by one of those runs heals too.
    v_accts UUID[] := ARRAY['ffff0000-0000-4000-8000-000000000001',
                            'ffff0000-0000-4000-8000-000000000002',
                            'aaaaaaaa-0000-0000-0000-000000000001',
                            'bbbbbbbb-0000-0000-0000-000000000002']::UUID[];
    v_kept  INT;
BEGIN
    -- Rows only — this function never touches a balance. Reversing a posting is
    -- the job of the block that made it (L8), because only that block knows the
    -- posting is still in the balance it is about to undo. Up here the residue
    -- belongs to an EARLIER run, and that balance may since have been changed by
    -- anything at all: a re-seed, a tester, a real transfer. Corrective
    -- arithmetic against a number like that does not restore money, it invents
    -- it.
    DELETE FROM account_mutations
     WHERE transaction_id IN (SELECT id FROM transactions WHERE user_id = ANY(v_users));

    DELETE FROM transactions       WHERE user_id = ANY(v_users);
    DELETE FROM transaction_limits WHERE user_id = ANY(v_users);
    DELETE FROM devices            WHERE user_id = ANY(v_users);

    -- An account is removed only while a fixture user still owns it.
    --
    -- This guard is not hypothetical. An older version of this script left its
    -- fixtures behind, and because the fixture account reused a seeded account
    -- NUMBER, the seeder's ON CONFLICT (account_number) adopted the row: it kept
    -- the fixture UUID but became a real customer's account, thirty mutations
    -- and all. Deleting by id alone would have taken that with it. The fixtures
    -- now use numbers no seeder claims, so adoption cannot recur — but the
    -- databases it already happened to still exist.
    SELECT COUNT(*) INTO v_kept
      FROM accounts
     WHERE id = ANY(v_accts)
       AND (user_id IS NULL OR NOT (user_id = ANY(v_users)));
    IF v_kept > 0 THEN
        RAISE NOTICE 'NOTE  % account(s) sit on a verify-009 fixture UUID but belong to someone else; left untouched', v_kept;
    END IF;

    DELETE FROM accounts WHERE id = ANY(v_accts) AND user_id = ANY(v_users);
    DELETE FROM users    WHERE id = ANY(v_users);
END $fn$ LANGUAGE plpgsql;

SELECT verify_009_cleanup();

-- ---------- fixtures ----------
INSERT INTO users (id, full_name, display_name, phone_encrypted, phone_hash, pin_hash, pin_salt)
VALUES ('11111111-1111-1111-1111-111111111111','NURHOLIS MAJID','NURHOLIS','\x00','hashA','h','s'),
       ('22222222-2222-2222-2222-222222222222','JOHN DOE','JOHN','\x00','hashB','h','s');

INSERT INTO accounts (id, user_id, account_number, account_type, account_label, balance, is_primary)
VALUES ('ffff0000-0000-4000-8000-000000000001','11111111-1111-1111-1111-111111111111','9150000001','TAHAPAN','Tahapan BCA',15750000,TRUE),
       ('ffff0000-0000-4000-8000-000000000002','22222222-2222-2222-2222-222222222222','9150000002','TAHAPAN','Tahapan BCA', 5000000,TRUE);

-- ---------- G. default limits seeded by trigger ----------
SELECT CASE WHEN COUNT(*) = 5 THEN 'PASS' ELSE 'FAIL' END AS "G1 trigger seeds 5 limit rows"
FROM transaction_limits WHERE user_id = '11111111-1111-1111-1111-111111111111';

-- ---------- G. ceiling constraint rejects an over-limit raise ----------
DO $$
BEGIN
    UPDATE transaction_limits SET daily_limit = 999000000
    WHERE user_id = '11111111-1111-1111-1111-111111111111' AND limit_type = 'EWALLET';
    RAISE EXCEPTION 'FAIL: ceiling constraint did not fire';
EXCEPTION WHEN check_violation THEN
    RAISE NOTICE 'PASS  G2 ceiling rejects e-wallet daily_limit above Rp 20jt';
END $$;

-- ---------- F. updated_at actually advances ----------
DO $$
DECLARE before_ts TIMESTAMPTZ; after_ts TIMESTAMPTZ;
BEGIN
    SELECT updated_at INTO before_ts FROM users WHERE phone_hash = 'hashA';
    PERFORM pg_sleep(0.05);
    UPDATE users SET display_name = 'NURHOLIS M' WHERE phone_hash = 'hashA';
    SELECT updated_at INTO after_ts FROM users WHERE phone_hash = 'hashA';
    IF after_ts > before_ts THEN RAISE NOTICE 'PASS  F1 updated_at advances on UPDATE';
    ELSE RAISE EXCEPTION 'FAIL: updated_at did not advance'; END IF;
END $$;

-- ---------- A. one active device_id, two users ----------
INSERT INTO devices (user_id, device_id) VALUES ('11111111-1111-1111-1111-111111111111','fp-shared');
DO $$
BEGIN
    INSERT INTO devices (user_id, device_id) VALUES ('22222222-2222-2222-2222-222222222222','fp-shared');
    RAISE EXCEPTION 'FAIL: second active row accepted for the same device_id';
EXCEPTION WHEN unique_violation THEN
    RAISE NOTICE 'PASS  A1 device_id unique across users while active';
END $$;

-- ---------- A. revoking releases the fingerprint (resold handset) ----------
UPDATE devices SET revoked_at = NOW() WHERE device_id = 'fp-shared';
INSERT INTO devices (user_id, device_id) VALUES ('22222222-2222-2222-2222-222222222222','fp-shared');
SELECT CASE WHEN COUNT(*) = 1 THEN 'PASS' ELSE 'FAIL' END AS "A2 revoked device_id is reusable"
FROM devices WHERE device_id = 'fp-shared' AND revoked_at IS NULL;

-- ---------- B. settlement shards seeded ----------
SELECT CASE WHEN COUNT(*) = 64 THEN 'PASS' ELSE 'FAIL' END AS "B1 64 internal accounts (4 rails x 16)"
FROM accounts WHERE owner_type = 'INTERNAL';

SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "B2 no internal account carries a user_id"
FROM accounts WHERE owner_type = 'INTERNAL' AND user_id IS NOT NULL;

-- ---------- B. owner-shape constraint rejects a malformed internal account ----------
DO $$
BEGIN
    INSERT INTO accounts (user_id, account_number, account_type, account_label, owner_type)
    VALUES (NULL, '9999999999', 'SETTLEMENT', 'bad', 'INTERNAL');
    RAISE EXCEPTION 'FAIL: internal account without a rail was accepted';
EXCEPTION WHEN check_violation THEN
    RAISE NOTICE 'PASS  B3 internal account requires settlement_rail + shard_index';
END $$;

-- ---------- B. shard resolution is deterministic and in range ----------
SELECT CASE WHEN COUNT(DISTINCT sid) = 1 THEN 'PASS' ELSE 'FAIL' END AS "B4 shard lookup is deterministic"
FROM (SELECT settlement_account_id('EWALLET','33333333-3333-3333-3333-333333333333') AS sid
      FROM generate_series(1,5)) t;

SELECT CASE WHEN COUNT(*) = 200 THEN 'PASS' ELSE 'FAIL' END AS "B5 every random key resolves to a shard"
FROM (SELECT settlement_account_id('QRIS', gen_random_uuid()) AS sid FROM generate_series(1,200)) t
WHERE sid IS NOT NULL;

-- ---------- C. idempotency is per user, not global ----------
INSERT INTO transactions (idempotency_key,user_id,source_account_id,type,status,amount,total_amount,reference_number)
VALUES ('idk-same','11111111-1111-1111-1111-111111111111','ffff0000-0000-4000-8000-000000000001','EWALLET_TOPUP','SUCCESS',100000,101000, next_reference_number(DATE '2026-09-02'));

INSERT INTO transactions (idempotency_key,user_id,source_account_id,type,status,amount,total_amount,reference_number)
VALUES ('idk-same','22222222-2222-2222-2222-222222222222','ffff0000-0000-4000-8000-000000000002','EWALLET_TOPUP','SUCCESS',100000,101000, next_reference_number(DATE '2026-09-02'));

SELECT CASE WHEN COUNT(*) = 2 THEN 'PASS' ELSE 'FAIL' END AS "C1 same key accepted for two different users"
FROM transactions WHERE idempotency_key = 'idk-same';

DO $$
BEGIN
    INSERT INTO transactions (idempotency_key,user_id,source_account_id,type,status,amount,total_amount,reference_number)
    VALUES ('idk-same','11111111-1111-1111-1111-111111111111','ffff0000-0000-4000-8000-000000000001','EWALLET_TOPUP','SUCCESS',100000,101000, next_reference_number(DATE '2026-09-02'));
    RAISE EXCEPTION 'FAIL: replay for the SAME user created a second transaction';
EXCEPTION WHEN unique_violation THEN
    RAISE NOTICE 'PASS  C2 replay by the same user is rejected';
END $$;

-- ---------- E. reference numbers unique, formatted, non-sequential ----------
SELECT CASE WHEN COUNT(*) = COUNT(DISTINCT r) AND COUNT(*) = 20000 THEN 'PASS' ELSE 'FAIL' END
         AS "E1 20k reference numbers, zero collisions"
FROM (SELECT next_reference_number(DATE '2026-09-02') AS r FROM generate_series(1,20000)) t;

SELECT CASE WHEN r ~ '^REF20260902\d{8}$' THEN 'PASS' ELSE 'FAIL' END AS "E2 reference number format"
FROM (SELECT next_reference_number(DATE '2026-09-02') AS r) t;

SELECT CASE WHEN abs(b - a) > 1 THEN 'PASS' ELSE 'FAIL' END AS "E3 consecutive refs are not consecutive numbers"
FROM (SELECT right(next_reference_number(DATE '2026-09-02'),8)::BIGINT AS a,
             right(next_reference_number(DATE '2026-09-02'),8)::BIGINT AS b) t;

-- ---------- H. WIB dates must be supplied ----------
DO $$
BEGIN
    INSERT INTO account_mutations (account_id,mutation_type,amount,balance_before,balance_after,description)
    VALUES ('ffff0000-0000-4000-8000-000000000001','DEBIT',1,0,0,'no date');
    RAISE EXCEPTION 'FAIL: mutation inserted without an explicit transaction_date';
EXCEPTION WHEN not_null_violation THEN
    RAISE NOTICE 'PASS  H1 transaction_date has no server-clock default';
END $$;

-- ---------- Ledger: three-legged e-wallet top-up balances ----------
DO $$
DECLARE
    v_txn UUID := gen_random_uuid();
    v_set UUID; v_fee UUID;
    v_cust_before DECIMAL(18,2); v_set_before DECIMAL(18,2); v_fee_before DECIMAL(18,2);
    v_amount DECIMAL(18,2) := 100000; v_admin DECIMAL(18,2) := 1000;
BEGIN
    v_set := settlement_account_id('EWALLET', v_txn);
    v_fee := settlement_account_id('FEE_INCOME', v_txn);

    -- lock every affected account in id order (deadlock-free ordering)
    PERFORM id FROM accounts
     WHERE id IN ('ffff0000-0000-4000-8000-000000000001', v_set, v_fee)
     ORDER BY id FOR UPDATE;

    SELECT balance INTO v_cust_before FROM accounts WHERE id = 'ffff0000-0000-4000-8000-000000000001';
    SELECT balance INTO v_set_before  FROM accounts WHERE id = v_set;
    SELECT balance INTO v_fee_before  FROM accounts WHERE id = v_fee;

    INSERT INTO transactions (id,idempotency_key,user_id,source_account_id,type,status,
                              amount,admin_fee,total_amount,reference_number,provider_id)
    VALUES (v_txn,'idk-ledger','11111111-1111-1111-1111-111111111111',
            'ffff0000-0000-4000-8000-000000000001','EWALLET_TOPUP','SUCCESS',
            v_amount,v_admin,v_amount+v_admin,next_reference_number(DATE '2026-09-02'),'gopay');

    UPDATE accounts SET balance = balance - (v_amount + v_admin) WHERE id = 'ffff0000-0000-4000-8000-000000000001';
    UPDATE accounts SET balance = balance + v_amount             WHERE id = v_set;
    UPDATE accounts SET balance = balance + v_admin              WHERE id = v_fee;

    INSERT INTO account_mutations (account_id,transaction_id,mutation_type,amount,
                                   balance_before,balance_after,description,transaction_date,transaction_time)
    VALUES
      ('ffff0000-0000-4000-8000-000000000001',v_txn,'DEBIT', v_amount+v_admin,
        v_cust_before, v_cust_before-(v_amount+v_admin),'TOP UP GOPAY', DATE '2026-09-02', TIME '10:30:00'),
      (v_set,v_txn,'CREDIT', v_amount, v_set_before, v_set_before+v_amount,
        'SETTLEMENT EWALLET', DATE '2026-09-02', TIME '10:30:00'),
      (v_fee,v_txn,'CREDIT', v_admin,  v_fee_before, v_fee_before+v_admin,
        'BIAYA ADMIN', DATE '2026-09-02', TIME '10:30:00');

    RAISE NOTICE 'PASS  L1 three-legged e-wallet posting committed';
END $$;

SELECT CASE WHEN SUM(CASE WHEN mutation_type='DEBIT' THEN amount ELSE -amount END) = 0
            THEN 'PASS' ELSE 'FAIL' END AS "L2 debits equal credits for the top-up"
FROM account_mutations WHERE transaction_id = (SELECT id FROM transactions WHERE idempotency_key='idk-ledger');

SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "L3 reconciliation view is empty"
FROM v_ledger_reconciliation;

SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "L5 no unbalanced transactions"
FROM v_unbalanced_transactions;

SELECT CASE WHEN balance = 15649000.00 THEN 'PASS' ELSE 'FAIL' END AS "L4 customer debited amount + fee"
FROM accounts WHERE id = 'ffff0000-0000-4000-8000-000000000001';

-- L6: a deliberately single-sided posting MUST be caught by v_unbalanced_transactions
--
-- The fixture is created, asserted, and REMOVED inside one block. It used to be
-- left behind, and that had a consequence beyond untidiness: `make ledger-check`
-- reads the very same two views, so any database this script had ever run
-- against reported a permanent ledger violation from then on. The repo's money
-- invariant check became a row nobody could explain and everybody learned to
-- ignore — which is worse than not having the check at all.
DO $$
DECLARE
    v_txn     UUID := gen_random_uuid();
    v_account UUID := 'ffff0000-0000-4000-8000-000000000001';
    v_before  NUMERIC;
    v_caught  INT;
BEGIN
    SELECT balance INTO v_before FROM accounts WHERE id = v_account;

    INSERT INTO transactions (id,user_id,source_account_id,type,status,amount,admin_fee,total_amount,reference_number)
    VALUES (v_txn,'11111111-1111-1111-1111-111111111111',v_account,
            'TRANSFER_EXTERNAL','SUCCESS',50000,6500,56500,next_reference_number(DATE '2026-09-02'));
    INSERT INTO account_mutations (account_id,transaction_id,mutation_type,amount,
                                   balance_before,balance_after,description,transaction_date,transaction_time)
    VALUES (v_account,v_txn,'DEBIT',56500,15649000,15592500,
            'SINGLE SIDED', DATE '2026-09-02', TIME '11:00:00');
    UPDATE accounts SET balance = 15592500 WHERE id = v_account;

    SELECT COUNT(*) INTO v_caught
    FROM v_unbalanced_transactions WHERE transaction_id = v_txn;

    -- Dibersihkan SEBELUM hasilnya dilaporkan, supaya pembersihan tidak
    -- terlewat ketika assertion-nya gagal.
    DELETE FROM account_mutations WHERE transaction_id = v_txn;
    DELETE FROM transactions WHERE id = v_txn;
    UPDATE accounts SET balance = v_before WHERE id = v_account;

    IF v_caught = 1 THEN
        RAISE NOTICE 'PASS  L6 single-sided posting is detected';
    ELSE
        RAISE EXCEPTION 'FAIL: L6 single-sided posting was NOT detected';
    END IF;
END $$;

-- Dan kedua view harus kembali kosong: fixture di atas tidak boleh menyisakan
-- apa pun yang membuat `make ledger-check` melaporkan pelanggaran selamanya.
SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "L7 verify fixture leaves no ledger drift"
FROM v_unbalanced_transactions;

-- L8: undo L1's posting, now that L2-L7 have all read it.
--
-- L1 debits the fixture account and credits two INTERNAL accounts that belong to
-- migration 000009, not to this script. The fixture account is deleted in the
-- teardown, but the internal ones have to survive — and a credit left sitting on
-- a surviving account is exactly what v_ledger_reconciliation reports as drift.
-- So the posting is reversed here, in the same run that made it: this is the
-- last moment anything in this file is allowed to assume the posting is still in
-- the balance.
DO $$
DECLARE
    v_txn UUID;
BEGIN
    SELECT id INTO v_txn FROM transactions
     WHERE idempotency_key = 'idk-ledger'
       AND user_id = '11111111-1111-1111-1111-111111111111';
    IF v_txn IS NULL THEN
        RAISE EXCEPTION 'FAIL: L8 could not find the L1 transaction to reverse';
    END IF;

    UPDATE accounts a
       SET balance = a.balance - x.net
      FROM (SELECT account_id,
                   SUM(CASE WHEN mutation_type = 'CREDIT' THEN amount ELSE -amount END) AS net
              FROM account_mutations
             WHERE transaction_id = v_txn
             GROUP BY account_id) x
     WHERE a.id = x.account_id;

    DELETE FROM account_mutations WHERE transaction_id = v_txn;
    DELETE FROM transactions      WHERE id = v_txn;

    RAISE NOTICE 'PASS  L8 L1 posting reversed, internal accounts restored';
END $$;

-- ---------- audit immutability still intact ----------
INSERT INTO audit_logs (action, resource_type) VALUES ('TXN_EWALLET_TOPUP','transaction');
DO $$
BEGIN
    UPDATE audit_logs SET action = 'tampered';
    RAISE EXCEPTION 'FAIL: audit log was mutable';
EXCEPTION WHEN raise_exception THEN
    IF SQLERRM LIKE 'FAIL:%' THEN RAISE; END IF;
    RAISE NOTICE 'PASS  X1 audit_logs remain immutable';
END $$;

-- ---------- teardown ----------
SELECT verify_009_cleanup();

SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "Z1 teardown removed every fixture user"
FROM users WHERE id IN ('11111111-1111-1111-1111-111111111111',
                        '22222222-2222-2222-2222-222222222222');

SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "Z2 teardown removed every fixture account"
FROM accounts WHERE id IN ('ffff0000-0000-4000-8000-000000000001',
                           'ffff0000-0000-4000-8000-000000000002');

-- The reason the teardown exists at all: both money views must read exactly as
-- they did before this script ran.
SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "Z3 reconciliation view empty after teardown"
FROM v_ledger_reconciliation;

SELECT CASE WHEN COUNT(*) = 0 THEN 'PASS' ELSE 'FAIL' END AS "Z4 no unbalanced transactions after teardown"
FROM v_unbalanced_transactions;

DROP FUNCTION verify_009_cleanup();
