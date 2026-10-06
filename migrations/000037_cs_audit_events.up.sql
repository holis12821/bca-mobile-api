-- Dua belas tindakan Rule 7 yang wajib menghasilkan peristiwa audit.
--
-- ENAM di antaranya SUDAH tercatat di tempat lain, dan tabel ini TIDAK menduplikasinya:
--   Queue Reservation, Call Started, Call Ended, Verification Decision
--      → onboarding_audit_logs (ber-kunci session_id nasabah)
--   akses PII petugas
--      → cs_access_logs (migrasi 000029)
--
-- Yang ditampung di sini adalah enam sisanya, yang tidak punya session_id nasabah sama
-- sekali: registrasi, login, otorisasi supervisor, healthcheck, pakta PII, aktivasi
-- terminal, dan logout. Mereka tentang PETUGAS dan TERMINAL, bukan tentang nasabah.
CREATE TABLE cs_audit_events (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type  VARCHAR(40) NOT NULL,

    -- Pelaku. employee_id petugas, atau supervisor_id pada otorisasi dual-control.
    actor       VARCHAR(40) NOT NULL,

    terminal_id VARCHAR(40),
    session_id  UUID,

    -- JSONB bebas per jenis peristiwa, seperti onboarding_audit_logs.details. Bidangnya
    -- berubah tanpa rilis; yang membacanya menampilkan apa adanya.
    details     JSONB,

    ip_address  VARCHAR(45),
    user_agent  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT cs_audit_events_type_valid CHECK (
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
    )
);

-- "Apa saja yang dilakukan petugas X" — pertanyaan pertama setiap pemeriksaan.
CREATE INDEX idx_cs_audit_events_actor ON cs_audit_events (actor, created_at DESC);

-- "Apa yang terjadi di loket Y" — saat sebuah terminal dicurigai.
CREATE INDEX idx_cs_audit_events_terminal ON cs_audit_events (terminal_id, created_at DESC)
    WHERE terminal_id IS NOT NULL;

-- Pencarian pengawas per jenis, mis. seluruh AGENT_LOGIN_FAILED hari ini.
CREATE INDEX idx_cs_audit_events_type ON cs_audit_events (event_type, created_at DESC);

-- Append-only, fungsi trigger yang sama yang menjaga onboarding_audit_logs dan
-- cs_access_logs. Jejak yang bisa disunting pelakunya bukan jejak.
CREATE TRIGGER trg_cs_audit_events_immutable
    BEFORE UPDATE OR DELETE ON cs_audit_events
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
