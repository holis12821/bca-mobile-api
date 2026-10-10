-- Audit trail for every active-liveness attempt.
--
-- Deliberately holds NO frames. There is no retention policy for raw biometric
-- frames in this project, and the Phase 2 decisions say not to invent one, so what
-- is kept is the minimum needed to investigate an attempt afterwards: which
-- challenge it answered, what the server decided, and why.
--
-- `reason` is an internal label (challenge_unknown_or_spent, signature_invalid,
-- step_pose_mismatch, ...). It is never returned to the client: naming the check
-- that refused tells whoever is probing which one to work around next.
CREATE TABLE liveness_attempts (
    id               UUID PRIMARY KEY,
    session_id       VARCHAR(64)  NOT NULL,
    challenge_id     VARCHAR(64)  NOT NULL,
    device_id        VARCHAR(128) NOT NULL,
    outcome          VARCHAR(16)  NOT NULL,
    reason           VARCHAR(64)  NOT NULL DEFAULT '',
    liveness_score   NUMERIC(5,2) NOT NULL DEFAULT 0,
    face_match_score NUMERIC(5,2) NOT NULL DEFAULT 0,
    frame_count      INT          NOT NULL DEFAULT 0,
    integrity_ok     BOOLEAN      NOT NULL DEFAULT FALSE,

    -- Client-reported risk signals, kept as an input to review and never as a
    -- decision: anything that can defeat an on-device root check can also lie here.
    risk_signals     JSONB        NOT NULL DEFAULT '{}'::jsonb,

    ip_address       VARCHAR(64)  NOT NULL DEFAULT '',
    user_agent       TEXT         NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT liveness_attempts_outcome_check
        CHECK (outcome IN ('PASSED', 'FAILED', 'BLOCKED', 'ESCALATED'))
);

-- The 24-hour failure window is counted in Redis, but Redis can be flushed. This
-- index backs the Postgres fallback count, which is why it is ordered by time.
CREATE INDEX idx_liveness_attempts_session_created
    ON liveness_attempts (session_id, created_at DESC);

-- Per-device review: one device driving many sessions is the pattern worth seeing.
CREATE INDEX idx_liveness_attempts_device_created
    ON liveness_attempts (device_id, created_at DESC);
