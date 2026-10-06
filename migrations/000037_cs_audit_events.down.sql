DROP TRIGGER IF EXISTS trg_cs_audit_events_immutable ON cs_audit_events;
DROP INDEX IF EXISTS idx_cs_audit_events_type;
DROP INDEX IF EXISTS idx_cs_audit_events_terminal;
DROP INDEX IF EXISTS idx_cs_audit_events_actor;
DROP TABLE IF EXISTS cs_audit_events;
