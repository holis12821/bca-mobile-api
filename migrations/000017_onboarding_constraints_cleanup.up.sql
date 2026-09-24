-- Onboarding schema cleanup:
--   1. Drop indexes that duplicate an existing one (migration 000016 re-created
--      indexes that 000010/000011/000013 already provide). A duplicate index
--      costs write throughput and buys nothing.
--   2. Enforce one row per session where the code already assumes one.

-- 1. Duplicate indexes ------------------------------------------------------

-- idx_ocr_auto_delete (000011) already covers auto_delete_at.
DROP INDEX IF EXISTS idx_onboarding_ocr_auto_delete;

-- idx_bio_auto_delete (000013) already covers auto_delete_at.
DROP INDEX IF EXISTS idx_onboarding_biometrics_auto_delete;

-- idx_onboarding_audit_created (000010) already covers created_at.
DROP INDEX IF EXISTS idx_onboarding_audit_retention;

-- 2. One row per session ----------------------------------------------------
--
-- PersonalDataRepo.Update writes `WHERE session_id = $1` with no row limit and
-- the services read with `ORDER BY created_at DESC LIMIT 1`: both only make
-- sense if a session has exactly one row. Nothing enforced that, so a race on
-- Create could leave two, and the update would then rewrite both.
--
-- Older duplicates are removed first, newest row kept.

DELETE FROM onboarding_personal_data a
USING onboarding_personal_data b
WHERE a.session_id = b.session_id
  AND (a.created_at, a.id) < (b.created_at, b.id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_pd_session_unique
    ON onboarding_personal_data (session_id);

DELETE FROM onboarding_credentials a
USING onboarding_credentials b
WHERE a.session_id = b.session_id
  AND (a.created_at, a.id) < (b.created_at, b.id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_onboarding_credentials_session_unique
    ON onboarding_credentials (session_id);

-- A session may hold several finished video calls over time (a rejection is
-- followed by a retry), but never two live ones at once.
CREATE UNIQUE INDEX IF NOT EXISTS idx_vc_session_active_unique
    ON onboarding_video_calls (session_id)
    WHERE status IN ('QUEUED', 'ACTIVE');

-- The non-unique lookup indexes are now redundant with the unique ones above.
DROP INDEX IF EXISTS idx_pd_session_id;
DROP INDEX IF EXISTS idx_onboarding_credentials_session;

-- 3. Widen the id columns -----------------------------------------------------
--
-- The public ids carry 8 random bytes now instead of 6 (a session id is the
-- only bearer credential for the whole flow, so 48 bits was thin). That makes
-- "cred_" + 16 hex = 21 characters, one over the VARCHAR(20) this table was
-- created with. The other onboarding tables already use VARCHAR(32); this
-- brings the credentials table in line with them.
ALTER TABLE onboarding_credentials
    ALTER COLUMN credential_id TYPE VARCHAR(32),
    ALTER COLUMN session_id TYPE VARCHAR(32);
