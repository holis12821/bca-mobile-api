DROP TRIGGER IF EXISTS trg_cs_access_logs_immutable ON cs_access_logs;
DROP INDEX IF EXISTS idx_cs_access_logs_subject;
DROP INDEX IF EXISTS idx_cs_access_logs_agent;
DROP TABLE IF EXISTS cs_access_logs;

-- Kembalikan pesan harfiah milik migrasi 000010. Fungsinya dipakai bersama
-- onboarding_audit_logs, jadi rollback yang meninggalkan versi baru akan membuat
-- 000010 tidak lagi menggambarkan keadaan sebenarnya.
CREATE OR REPLACE FUNCTION prevent_audit_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'onboarding_audit_logs is append-only';
END;
$$ LANGUAGE plpgsql;
