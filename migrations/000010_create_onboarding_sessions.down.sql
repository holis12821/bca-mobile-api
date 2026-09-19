DROP TRIGGER IF EXISTS trg_onboarding_audit_immutable ON onboarding_audit_logs;
DROP FUNCTION IF EXISTS prevent_audit_mutation();
DROP TABLE IF EXISTS onboarding_audit_logs;
DROP TABLE IF EXISTS onboarding_sessions;
DROP TYPE IF EXISTS onboarding_step;
DROP TYPE IF EXISTS onboarding_product_type;
