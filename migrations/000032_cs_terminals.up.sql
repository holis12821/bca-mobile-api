-- Terminal (loket) petugas CS, dan siklus hidupnya di server.
--
-- Rule 4 alur desktop berbunyi "hanya petugas ONLINE yang boleh mengambil antrean".
-- Sebelum tabel ini, server TIDAK TAHU APA PUN tentang terminal: `agent-token` hanya
-- memeriksa kredensial dan cakupan. Artinya Rule 4 hanyalah hiasan — petugas yang
-- melewati layar kesiapan dengan menyunting state klien tetap bisa mengambil panggilan.
-- Untuk aturan yang menentukan siapa boleh melayani nasabah, itu tidak memadai.
--
-- Tabel ini SEKALIGUS allowlist terminal: terminal yang tidak ada barisnya di sini bukan
-- terminal yang diizinkan. Tidak ada tabel allowlist terpisah.
CREATE TABLE cs_terminals (
    -- Format #WKS-SMG-0842 di dokumen alur. Diterbitkan yang memasang terminal, bukan
    -- digenerate server: ia harus cocok dengan label fisik di meja petugas.
    terminal_id   VARCHAR(40) PRIMARY KEY,
    workstation   VARCHAR(64)  NOT NULL,
    location      VARCHAR(128) NOT NULL,

    -- VARCHAR + CHECK, BUKAN enum Postgres: menambah nilai ke enum menuntut migrasi yang
    -- mengunci tabel, dan daftar status di dokumen alur masih akan berubah.
    --
    -- Hanya empat yang DISIMPAN, dan itu disengaja. BUSY/CALL_ACTIVE/PROCESSING di dokumen
    -- alur DITURUNKAN dari panggilan aktif petugasnya (onboarding_video_calls), bukan
    -- disimpan: status tersimpan yang tidak punya satu-satunya sumber kebenaran akan
    -- melenceng dari tabel panggilan, dan yang melenceng akan dipercaya. ERROR tidak ada
    -- karena tidak ada mekanisme yang menyetelnya — kolom yang ada akan diisi.
    status        VARCHAR(16)  NOT NULL DEFAULT 'REGISTERED',

    registered_by VARCHAR(32)  NOT NULL,
    registered_at TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Petugas yang sedang memegang terminal ini. Terisi saat aktivasi, dikosongkan saat
    -- deaktivasi. ON DELETE SET NULL, bukan CASCADE: menghapus petugas tidak boleh
    -- menghapus terminalnya — terminalnya milik cabang, bukan milik orang.
    active_agent_id VARCHAR(32) REFERENCES cs_agents (employee_id) ON DELETE SET NULL,
    activated_at    TIMESTAMPTZ,

    -- Laporan hidup terakhir dari klien. Dipakai mendeteksi terminal yang mati tanpa
    -- logout; TIDAK dipakai sebagai gerbang, karena jam klien bukan jam server.
    last_seen_at  TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT cs_terminals_status_valid CHECK (
        status IN ('REGISTERED', 'READY', 'ONLINE', 'OFFLINE')
    ),

    -- ONLINE menuntut pemegangnya diketahui. Terminal ONLINE tanpa petugas adalah
    -- terminal yang Rule 4 akan meluluskan tanpa bisa menyebut siapa yang dilayani.
    CONSTRAINT cs_terminals_online_has_agent CHECK (
        status <> 'ONLINE' OR (active_agent_id IS NOT NULL AND activated_at IS NOT NULL)
    )
);

-- Satu petugas tidak boleh ONLINE di dua terminal sekaligus.
--
-- Tanpa ini, petugas yang lupa logout di loket lain tetap bisa mengaktifkan loket kedua,
-- dan dua panggilan bisa diambil atas nama satu orang — yang membuat jejak audit
-- menyebut satu nama untuk dua verifikasi yang berjalan bersamaan.
CREATE UNIQUE INDEX idx_cs_terminals_one_online_per_agent
    ON cs_terminals (active_agent_id)
    WHERE status = 'ONLINE' AND active_agent_id IS NOT NULL;

-- "Terminal mana yang sedang melayani" — tampilan pengawas, dan penjaga Rule 4.
CREATE INDEX idx_cs_terminals_status ON cs_terminals (status) WHERE status IN ('READY', 'ONLINE');
