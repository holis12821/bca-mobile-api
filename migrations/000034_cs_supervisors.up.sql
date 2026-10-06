-- Supervisor yang berwenang memberi otorisasi dual-control (SCR-006).
--
-- Tabel tersendiri, bukan kolom `is_supervisor` di cs_agents: kewenangan menandatangani
-- kesiapan orang lain bukan cakupan seperti VIDEO_CALL — ia menyangkut hierarki, punya
-- kredensial sendiri (token), dan pemegangnya belum tentu petugas yang melayani panggilan.
CREATE TABLE cs_supervisors (
    supervisor_id VARCHAR(32)  PRIMARY KEY,
    name          VARCHAR(128) NOT NULL,
    location      VARCHAR(128) NOT NULL,

    -- Token dual-control, Argon2id — format yang sama dengan cs_agents.api_key_hash.
    --
    -- TIDAK pernah disimpan maupun dikembalikan sebagai teks. `#BCA-AUTH-9942` di dokumen
    -- alur adalah RUJUKAN TANDA TERIMA otorisasi, bukan tokennya — lihat
    -- cs_terminal_readiness.authorization_ref. Menyimpan token di kolom yang ikut terbaca
    -- `GET /supervisors` akan menyerahkan kredensial dual-control ke setiap petugas yang
    -- membuka daftar supervisor.
    token_hash    TEXT         NOT NULL,

    is_active     BOOLEAN      NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Daftar supervisor dibuka di layar SCR-006, dan hanya yang aktif yang boleh dipilih.
CREATE INDEX idx_cs_supervisors_active ON cs_supervisors (location)
    WHERE is_active;
