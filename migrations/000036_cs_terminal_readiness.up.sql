-- Tiga gerbang kesiapan terminal (Rule 3), per sesi petugas.
--
-- Per SESI, bukan per terminal: gerbangnya menyatakan "orang INI, di loket INI, pada
-- giliran INI sudah diperiksa". Menyimpannya per terminal akan membuat petugas giliran
-- berikutnya mewarisi pakta integritas yang ditandatangani orang lain.
CREATE TABLE cs_terminal_readiness (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    session_id UUID        NOT NULL REFERENCES cs_agent_sessions (id) ON DELETE CASCADE,

    -- SUPERVISOR_AUTH | DEVICE_HEALTHCHECK | PII_ACK
    gate       VARCHAR(24) NOT NULL,

    passed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Gerbang yang kedaluwarsa dihitung BELUM lolos. Healthcheck kemarin tidak
    -- menyatakan apa pun tentang kamera hari ini.
    expires_at TIMESTAMPTZ NOT NULL,

    -- Rujukan tanda terima otorisasi supervisor (#BCA-AUTH-9942 di dokumen alur).
    -- Terisi hanya pada gerbang SUPERVISOR_AUTH. BUKAN tokennya.
    authorization_ref VARCHAR(32),
    supervisor_id     VARCHAR(32) REFERENCES cs_supervisors (supervisor_id) ON DELETE SET NULL,

    -- Laporan klien untuk gerbang DEVICE_HEALTHCHECK, apa adanya.
    --
    -- JSONB karena isinya ditentukan klien dan akan berubah tanpa rilis server. Server
    -- TIDAK mengukur kamera atau mikrofon di meja petugas — yang bisa dilakukannya hanya
    -- mencatat PERNYATAAN klien beserta waktunya. Jangan pernah menampilkan isi kolom ini
    -- di laporan kepatuhan seolah server yang mengukurnya.
    details    JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT cs_terminal_readiness_gate_valid CHECK (
        gate IN ('SUPERVISOR_AUTH', 'DEVICE_HEALTHCHECK', 'PII_ACK')
    ),

    -- Otorisasi supervisor tanpa penandatangan bukan dual-control.
    CONSTRAINT cs_terminal_readiness_supervisor_signed CHECK (
        gate <> 'SUPERVISOR_AUTH'
        OR (supervisor_id IS NOT NULL AND authorization_ref IS NOT NULL)
    )
);

-- Satu baris lolos per gerbang per sesi. Mengulang gerbang yang sama MENIMPA barisnya
-- (upsert), bukan menumpuk — kalau tidak, healthcheck yang diulang akan meninggalkan
-- baris kedaluwarsa yang tetap terbaca sebagai lolos.
CREATE UNIQUE INDEX idx_cs_readiness_session_gate
    ON cs_terminal_readiness (session_id, gate);
