-- Sesi petugas CS: satu giliran kerja di satu terminal.
--
-- "Session Started 08:02 WIB" di dokumen alur (§19) adalah baris di tabel ini. Sesi
-- lahir saat login (SCR-003) dan mati saat logout atau saat tenggatnya lewat.
--
-- Terpisah dari kunci API yang tetap berlaku: sesi menjawab "siapa yang sedang bertugas
-- di loket mana, sejak kapan", pertanyaan yang tidak bisa dijawab kredensial statis.
CREATE TABLE cs_agent_sessions (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Token sesi yang dipegang klien, DI-HASH. SHA-256 cukup dan sengaja bukan Argon2id:
    -- tokennya 256 bit dari crypto/rand, bukan kata sandi yang bisa ditebak, dan jalur ini
    -- dilalui setiap permintaan — Argon2id (64 MB × 4 thread) per permintaan akan
    -- menjadikan verifikasi sesi lebih mahal daripada pekerjaan yang dilindunginya.
    token_hash  CHAR(64)    NOT NULL UNIQUE,

    employee_id VARCHAR(32) NOT NULL REFERENCES cs_agents (employee_id) ON DELETE CASCADE,

    -- Terminal tempat sesi ini dibuka. Sesi tidak bisa berpindah loket: pindah loket
    -- berarti giliran baru, dan jejak auditnya harus memperlihatkan itu.
    terminal_id VARCHAR(40) NOT NULL REFERENCES cs_terminals (terminal_id) ON DELETE CASCADE,

    shift       VARCHAR(32),

    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    ended_at    TIMESTAMPTZ,

    -- Alasan berakhirnya, untuk membedakan logout tertib dari tenggat yang lewat.
    ended_reason VARCHAR(24),

    last_seen_at TIMESTAMPTZ,

    CONSTRAINT cs_agent_sessions_ended_consistent CHECK (
        (ended_at IS NULL) = (ended_reason IS NULL)
    ),
    CONSTRAINT cs_agent_sessions_ended_reason_valid CHECK (
        ended_reason IS NULL OR ended_reason IN ('LOGOUT', 'EXPIRED', 'SUPERSEDED', 'REVOKED')
    )
);

-- Satu sesi hidup per petugas.
--
-- Partial unique: login kedua TIDAK ditolak — ia menutup sesi lama dengan alasan
-- SUPERSEDED lalu membuka yang baru. Petugas yang terminalnya mati tanpa logout harus
-- bisa masuk lagi dari loket lain, dan menolaknya akan membuatnya menunggu tenggat lewat.
CREATE UNIQUE INDEX idx_cs_agent_sessions_one_live
    ON cs_agent_sessions (employee_id)
    WHERE ended_at IS NULL;

-- Verifikasi token terjadi di setiap permintaan ber-sesi; ini jalur terpanasnya.
CREATE INDEX idx_cs_agent_sessions_token ON cs_agent_sessions (token_hash)
    WHERE ended_at IS NULL;

CREATE INDEX idx_cs_agent_sessions_terminal ON cs_agent_sessions (terminal_id, started_at DESC);
