-- Membatalkan kunci meja. Urutannya terbalik dari up, dan dua hal harus dibereskan
-- LEBIH DULU karena CHECK yang dipersempit diperiksa terhadap baris yang sudah ada.

-- 1. Baris cs_audit_events ber-event_type DESK_*.
--
-- Tabelnya append-only lewat trg_cs_audit_events_immutable, jadi DELETE-nya ditolak
-- trigger — bukan kebetulan, itu memang tujuannya. Trigger dilepas, baris dibuang,
-- trigger dipasang lagi di transaksi migrasi yang sama. Ini SATU-SATUNYA jalan sah
-- membuang baris audit, dan pola yang sama sudah dipakai 000041; jangan
-- melonggarkan trigger-nya sebagai gantinya.
DROP TRIGGER trg_cs_audit_events_immutable ON cs_audit_events;

DELETE FROM cs_audit_events
WHERE event_type IN ('DESK_PIN_SET', 'DESK_LOCKED', 'DESK_UNLOCKED', 'DESK_UNLOCK_FAILED');

ALTER TABLE cs_audit_events
    DROP CONSTRAINT cs_audit_events_type_valid;

ALTER TABLE cs_audit_events
    ADD CONSTRAINT cs_audit_events_type_valid CHECK (
        event_type IN (
            'AGENT_REGISTERED',
            'AGENT_UPDATED',
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

-- 2. Terminal yang sedang LOCKED.
--
-- Dipulangkan ke ONLINE, bukan OFFLINE: petugasnya masih memegang sesi yang hidup
-- dan active_agent_id-nya masih terisi, jadi OFFLINE akan menutup loket orang yang
-- sedang bertugas hanya karena sebuah rollback skema. Constraint
-- cs_terminals_locked_consistent masih terpasang di titik ini, jadi locked_at harus
-- ikut dikosongkan dalam UPDATE yang sama.
UPDATE cs_terminals
SET status = 'ONLINE', locked_at = NULL, locked_reason = NULL, updated_at = now()
WHERE status = 'LOCKED';

ALTER TABLE cs_terminals DROP CONSTRAINT cs_terminals_locked_reason_valid;
ALTER TABLE cs_terminals DROP CONSTRAINT cs_terminals_locked_consistent;

ALTER TABLE cs_terminals
    DROP COLUMN locked_reason,
    DROP COLUMN locked_at;

-- Indeks dikembalikan ke bentuk 000032. Dilakukan SETELAH baris LOCKED dipindahkan,
-- supaya tidak ada petugas yang punya dua baris memenuhi predikat lamanya.
DROP INDEX idx_cs_terminals_one_online_per_agent;

CREATE UNIQUE INDEX idx_cs_terminals_one_online_per_agent
    ON cs_terminals (active_agent_id)
    WHERE status = 'ONLINE' AND active_agent_id IS NOT NULL;

ALTER TABLE cs_terminals DROP CONSTRAINT cs_terminals_status_valid;

ALTER TABLE cs_terminals
    ADD CONSTRAINT cs_terminals_status_valid CHECK (
        status IN ('REGISTERED', 'READY', 'ONLINE', 'OFFLINE')
    );

-- 3. PIN meja. Constraint dilepas sebelum kolomnya, supaya penghapusan kolom tidak
--    menyeret constraint yang namanya masih dirujuk migrasi lain.
ALTER TABLE cs_agents DROP CONSTRAINT cs_agents_desk_pin_consistent;

ALTER TABLE cs_agents
    DROP COLUMN failed_unlock_attempts,
    DROP COLUMN desk_pin_set_at,
    DROP COLUMN desk_pin_hash;
