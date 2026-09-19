-- Video call records for onboarding verification

CREATE TABLE onboarding_video_calls (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    queue_id              VARCHAR(32) NOT NULL,
    session_id            VARCHAR(32) NOT NULL,
    queue_number          VARCHAR(16) NOT NULL,
    status                VARCHAR(16) NOT NULL DEFAULT 'QUEUED',
    agent_employee_id     VARCHAR(32),
    agent_name            VARCHAR(128),
    result                VARCHAR(16),
    ktp_shown_live        BOOLEAN NOT NULL DEFAULT false,
    identity_confirmed    BOOLEAN NOT NULL DEFAULT false,
    notes                 TEXT,
    call_duration_seconds INTEGER NOT NULL DEFAULT 0,
    recording_id          VARCHAR(64),
    joined_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at            TIMESTAMPTZ,
    ended_at              TIMESTAMPTZ,

    CONSTRAINT fk_vc_session FOREIGN KEY (session_id)
        REFERENCES onboarding_sessions(session_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_vc_queue_id ON onboarding_video_calls (queue_id);
CREATE INDEX idx_vc_session_id ON onboarding_video_calls (session_id);
CREATE INDEX idx_vc_status ON onboarding_video_calls (status) WHERE status = 'QUEUED';