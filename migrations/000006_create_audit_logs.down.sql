-- 000006_create_audit_logs.down.sql

DROP TRIGGER IF EXISTS trg_audit_immutable ON audit_logs;
DROP FUNCTION IF EXISTS prevent_audit_modification();
DROP TABLE IF EXISTS audit_logs;