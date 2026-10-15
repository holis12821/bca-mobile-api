-- Kunci meja petugas CS (clean desk policy).
--
-- Tiga perubahan dalam satu migrasi karena ketiganya satu keputusan: PIN yang
-- mengangkat tirai, status terminal yang menahan antrean selama tirai turun, dan
-- jejak yang membuktikan keduanya terjadi. Memisahnya akan menghasilkan migrasi
-- yang masing-masing tidak berguna sendirian.
--
-- Sebelum ini, penguncian layar hanya hidup di renderer aplikasi desktop: tirai
-- blur dengan PIN yang disimpan di memori proses. Tirai itu menahan mata orang
-- lewat, tapi tidak menahan ANTREAN — server tidak tahu petugasnya tidak di
-- tempat, jadi nasabah tetap diarahkan ke loket kosong — dan tidak meninggalkan
-- satu baris pun yang bisa menjawab "jam berapa loket itu ditinggalkan, dan siapa
-- yang membukanya kembali". Pertanyaan kedua itulah yang diajukan pemeriksaan
-- kepatuhan UU PDP No. 27 Tahun 2022.

-- (a) PIN meja: kredensial KETIGA petugas, di samping api_key_hash dan
--     password_hash yang sudah dijelaskan 000033.
--
-- Kenapa bukan memakai ulang password_hash: kata sandi dipakai SEKALI per giliran,
-- PIN dipakai belasan kali per hari. Memaksa mengetik kata sandi minimal 12
-- karakter setiap kembali dari toilet adalah cara paling pasti membuat petugas
-- memilih kata sandi lemah — atau tidak mengunci layarnya sama sekali. Keduanya
-- juga membuka hal berbeda: kata sandi menerbitkan sesi baru di loket mana pun,
-- PIN hanya mengangkat tirai atas giliran yang SUDAH terbuka. Kredensial yang
-- membuka lebih sedikit boleh lebih murah.
ALTER TABLE cs_agents
    ADD COLUMN desk_pin_hash   TEXT,
    ADD COLUMN desk_pin_set_at TIMESTAMPTZ;

-- Penghitung gagal membuka tirai, TERPISAH dari failed_login_attempts.
--
-- Menempel di petugas, bukan di terminal: yang beku adalah tirai milik orang itu,
-- dan petugas pengganti yang login dengan kata sandinya sendiri harus mendapat
-- loket bersih.
--
-- TIDAK ADA desk_locked_until yang menyertainya, dan itu disengaja. Lockout
-- berbasis waktu salah tempat di sini: petugas yang lupa PIN lalu harus menunggu
-- lima belas menit di depan mejanya sendiri adalah loket yang mati lima belas
-- menit. Setelah tiga kali gagal, loketnya beku sampai SUPERVISOR membukanya —
-- orang, bukan jam.
ALTER TABLE cs_agents
    ADD COLUMN failed_unlock_attempts INTEGER NOT NULL DEFAULT 0;

-- desk_pin_hash NULL berarti petugas belum pernah menyetel PIN — keadaan sah bagi
-- setiap baris yang sudah ada. `terminals/lock` menolaknya dengan kode tersendiri
-- (AGENT_DESK_PIN_NOT_SET), bukan "PIN salah": mengunci layar tanpa punya PIN
-- berarti mengunci diri sendiri di luar.
--
-- CHECK-nya mengikat stempel ke nilainya, sama seperti cs_agents_password_consistent.
ALTER TABLE cs_agents
    ADD CONSTRAINT cs_agents_desk_pin_consistent CHECK (
        (desk_pin_hash IS NULL) = (desk_pin_set_at IS NULL)
    );

-- (b) LOCKED: status KELIMA cs_terminals.
--
-- Dua model yang mungkin, dan pilihannya menentukan segalanya. Kalau mengunci
-- berarti menutup sesi, petugas harus login ulang penuh: terminal jatuh ke
-- OFFLINE, ketiga gerbang kesiapan berumur 8 jam harus diulang, dan panggilan
-- yang sedang berjalan putus. Kalau mengunci adalah STATUS TERMINAL, sesinya tetap
-- hidup beserta tenggatnya, gerbangnya tidak batal, panggilannya tidak putus, dan
-- antreannya tetap berhenti mengalir. Yang kedua yang dipilih.
--
-- VARCHAR + CHECK, bukan enum, dengan alasan yang sama seperti 000032: menambah
-- nilai ke enum menuntut migrasi yang mengunci tabel.
ALTER TABLE cs_terminals DROP CONSTRAINT cs_terminals_status_valid;

ALTER TABLE cs_terminals
    ADD CONSTRAINT cs_terminals_status_valid CHECK (
        status IN ('REGISTERED', 'READY', 'ONLINE', 'OFFLINE', 'LOCKED')
    );

ALTER TABLE cs_terminals
    ADD COLUMN locked_at     TIMESTAMPTZ,
    ADD COLUMN locked_reason VARCHAR(24);

ALTER TABLE cs_terminals
    ADD CONSTRAINT cs_terminals_locked_consistent CHECK (
        (status = 'LOCKED') = (locked_at IS NOT NULL)
    );

-- AGENT        → petugas menekan tombol kunci.
-- IDLE         → aplikasi desktop mengunci sendiri setelah diam. Pemicunya milik
--                KLIEN: server tidak tahu petugasnya diam, dan penghitung idle di
--                backend hanya akan menebak.
-- FAILED_UNLOCK → beku setelah tiga kali PIN salah. Bukan status keenam, melainkan
--                ALASAN terkunci: status keenam akan memaksa setiap pembaca status
--                memeriksa dua hal, dan yang lupa memeriksa akan meluluskan loket
--                beku.
ALTER TABLE cs_terminals
    ADD CONSTRAINT cs_terminals_locked_reason_valid CHECK (
        locked_reason IS NULL OR locked_reason IN ('AGENT', 'IDLE', 'FAILED_UNLOCK')
    );

-- cs_terminals_online_has_agent SENGAJA tidak diubah: ia hanya mengikat status
-- ONLINE, dan terminal LOCKED tetap memegang active_agent_id — petugasnya masih
-- bertugas, hanya sedang tidak di tempat. Melepas ikatannya saat mengunci akan
-- membuat `unlock` tidak tahu lagi siapa yang berhak membuka.
--
-- Tapi idx_cs_terminals_one_online_per_agent HARUS ikut melebar. Ia menjaga "satu
-- petugas tidak ONLINE di dua loket sekaligus"; tanpa LOCKED di dalamnya, petugas
-- yang meninggalkan loket terkunci bisa mengaktifkan loket kedua, dan dua panggilan
-- bisa diambil atas nama satu orang — tepat yang dicegah 000032.
DROP INDEX idx_cs_terminals_one_online_per_agent;

CREATE UNIQUE INDEX idx_cs_terminals_one_online_per_agent
    ON cs_terminals (active_agent_id)
    WHERE status IN ('ONLINE', 'LOCKED') AND active_agent_id IS NOT NULL;

-- idx_cs_terminals_status sengaja TIDAK diperluas ke LOCKED. Indeks itu melayani
-- pertanyaan "terminal mana yang sedang melayani"; loket terkunci bukan kandidat
-- penugasan, dan itulah inti seluruh perubahan ini.

-- (c) Empat peristiwa audit baru.
--
-- DESK_UNLOCK_FAILED ditulis pada SETIAP kegagalan, bukan hanya yang ketiga: pola
-- percobaan yang berhenti di dua, berulang tiap hari, di loket yang sama, adalah
-- justru yang dicari pemeriksa.
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
            'TERMINAL_DEACTIVATED',
            'DESK_PIN_SET',
            'DESK_LOCKED',
            'DESK_UNLOCKED',
            'DESK_UNLOCK_FAILED'
        )
    );
