-- Mengembalikan kedua CHECK ke daftar lamanya. Dua hal harus dibereskan LEBIH DULU,
-- karena CHECK yang dipersempit diperiksa terhadap baris yang sudah ada.

-- 1. Baris cs_agents yang memegang cakupan baru.
--
-- Cakupannya dicabut, bukan barisnya dihapus: menghapus petugas akan memutus rujukan
-- namanya dari panggilan lama (lihat alasan `is_active = false` di 000026).
UPDATE cs_agents
SET scopes = ARRAY(
        SELECT s FROM unnest(scopes) AS s
        WHERE s NOT IN ('AUDIT_READ', 'ESCALATION_REVIEW')
    )
WHERE scopes && ARRAY['AUDIT_READ', 'ESCALATION_REVIEW']::TEXT[];

-- Petugas yang cakupannya HABIS setelah pencabutan di atas — Tier 2 yang hanya memegang
-- ESCALATION_REVIEW, misalnya — menabrak `cardinality(scopes) > 0`. Ia dinonaktifkan dan
-- diberi cakupan paling sempit, bukan diberi VIDEO_CALL yang hidup: baris nonaktif tidak
-- pernah lolos AuthenticateAgent, jadi tidak ada kewenangan yang diberikan diam-diam
-- oleh sebuah rollback. Yang mengembalikannya harus menyetel ulang dengan sengaja.
UPDATE cs_agents
SET scopes = ARRAY['VIDEO_CALL']::TEXT[], is_active = FALSE
WHERE cardinality(scopes) = 0;

ALTER TABLE cs_agents
    DROP CONSTRAINT cs_agents_scopes_valid;

ALTER TABLE cs_agents
    ADD CONSTRAINT cs_agents_scopes_valid CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY['VIDEO_CALL', 'CARD_ADMIN', 'CUSTOMER_PII', 'TICKET']::TEXT[]
    );

-- 2. Baris cs_audit_events ber-event_type AGENT_UPDATED.
--
-- Tabelnya append-only lewat trg_cs_audit_events_immutable, jadi DELETE-nya ditolak
-- trigger — bukan kebetulan, itu memang tujuannya. Trigger dilepas, baris dibuang, trigger
-- dipasang lagi di transaksi migrasi yang sama. Inilah SATU-SATUNYA jalan sah membuang
-- baris audit, dan ia hanya ada di sini karena CHECK tidak bisa dipersempit sementara
-- barisnya masih ada.
DROP TRIGGER trg_cs_audit_events_immutable ON cs_audit_events;

DELETE FROM cs_audit_events WHERE event_type = 'AGENT_UPDATED';

ALTER TABLE cs_audit_events
    DROP CONSTRAINT cs_audit_events_type_valid;

ALTER TABLE cs_audit_events
    ADD CONSTRAINT cs_audit_events_type_valid CHECK (
        event_type IN (
            'AGENT_REGISTERED',
            'AGENT_LOGIN',
            'AGENT_LOGIN_FAILED',
            'AGENT_LOGOUT',
            'AGENT_PASSWORD_SET',
            'SUPERVISOR_AUTHORIZED',
            'SUPERVISOR_AUTH_FAILED',
            'DEVICE_HEALTHCHECK',
            'PII_ACKNOWLEDGED',
            'TERMINAL_REGISTERED',
            'TERMINAL_ACTIVATED',
            'TERMINAL_DEACTIVATED'
        )
    );

CREATE TRIGGER trg_cs_audit_events_immutable
    BEFORE UPDATE OR DELETE ON cs_audit_events
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
