DROP INDEX IF EXISTS idx_vc_escalations_claimed;

ALTER TABLE onboarding_video_call_escalations
    DROP CONSTRAINT IF EXISTS vc_escalations_resolved_has_notes,
    DROP CONSTRAINT IF EXISTS vc_escalations_rejection_has_reason,
    DROP CONSTRAINT IF EXISTS vc_escalations_resolution_reason_valid,
    DROP CONSTRAINT IF EXISTS vc_escalations_in_review_has_agent,
    DROP CONSTRAINT IF EXISTS vc_escalations_claimed_consistent;

ALTER TABLE onboarding_video_call_escalations
    DROP COLUMN IF EXISTS resolution_notes,
    DROP COLUMN IF EXISTS resolution_reason,
    DROP COLUMN IF EXISTS claimed_at,
    DROP COLUMN IF EXISTS claimed_by_agent;
