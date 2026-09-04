-- scripts/000009_precheck_devices.sql
--
-- Run BEFORE migration 000009 on any environment that already has device rows.
-- Decision A makes devices.device_id unique across all users while the row is
-- active, so a fingerprint currently active for two users must be resolved first.

-- 1. Which fingerprints conflict, and who holds them?
SELECT
    d.device_id,
    COUNT(DISTINCT d.user_id)                      AS active_users,
    array_agg(d.user_id  ORDER BY d.last_active_at DESC NULLS LAST) AS user_ids,
    array_agg(d.id       ORDER BY d.last_active_at DESC NULLS LAST) AS device_row_ids,
    array_agg(d.last_active_at ORDER BY d.last_active_at DESC NULLS LAST) AS last_active
FROM devices d
WHERE d.revoked_at IS NULL
GROUP BY d.device_id
HAVING COUNT(DISTINCT d.user_id) > 1
ORDER BY active_users DESC;

-- 2. Resolution — keep the most recently active row per fingerprint, revoke the rest.
--    Review the output of query 1 before running this. Revoking a device also
--    invalidates its sessions and biometric keys, so the affected customers must
--    log in again with their Kode Akses.
--
-- BEGIN;
--
-- WITH ranked AS (
--     SELECT id,
--            ROW_NUMBER() OVER (
--                PARTITION BY device_id
--                ORDER BY last_active_at DESC NULLS LAST, created_at DESC
--            ) AS rn
--     FROM devices
--     WHERE revoked_at IS NULL
--       AND device_id IN (
--           SELECT device_id FROM devices WHERE revoked_at IS NULL
--           GROUP BY device_id HAVING COUNT(DISTINCT user_id) > 1
--       )
-- )
-- UPDATE devices d
-- SET revoked_at = NOW()
-- FROM ranked r
-- WHERE d.id = r.id AND r.rn > 1;
--
-- -- Revoke the biometric keys bound to those device rows.
-- UPDATE biometric_keys bk
-- SET is_active = FALSE, revoked_at = NOW()
-- FROM devices d
-- WHERE bk.device_id = d.id AND d.revoked_at IS NOT NULL AND bk.is_active;
--
-- -- Revoke their sessions.
-- UPDATE sessions s
-- SET revoked_at = NOW()
-- FROM devices d
-- WHERE s.device_id = d.id AND d.revoked_at IS NOT NULL AND s.revoked_at IS NULL;
--
-- COMMIT;

-- 3. Verify the conflict is gone — must return zero rows.
SELECT device_id
FROM devices
WHERE revoked_at IS NULL
GROUP BY device_id
HAVING COUNT(DISTINCT user_id) > 1;
