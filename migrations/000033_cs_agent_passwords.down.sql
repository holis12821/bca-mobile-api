ALTER TABLE cs_agents DROP CONSTRAINT IF EXISTS cs_agents_password_consistent;
ALTER TABLE cs_agents
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS failed_login_attempts,
    DROP COLUMN IF EXISTS password_set_at,
    DROP COLUMN IF EXISTS password_hash;
