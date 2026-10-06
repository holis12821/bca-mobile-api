-- Kata sandi petugas CS, terpisah dari kunci API.
--
-- KEPUTUSAN: dua kredensial dengan dua guna berbeda.
--   api_key_hash   → sistem-ke-sistem, statis, dipakai di header tiap permintaan
--   password_hash  → login petugas (SCR-003), menghasilkan sesi bertenggat
--
-- Keduanya hidup berdampingan karena menjawab hal berbeda. Kunci API tetap menjadi
-- sumber kebenaran `actor` bagi integrasi yang sudah berjalan; kata sandi melayani orang
-- yang berdiri di depan terminal dan menutup gilirannya saat pulang.
ALTER TABLE cs_agents
    ADD COLUMN password_hash   TEXT,
    ADD COLUMN password_set_at TIMESTAMPTZ;

-- Lockout, meniru users.failed_pin_attempts + locked_until.
--
-- Tanpa ini, kata sandi petugas adalah ruang tebak yang tidak berbatas di belakang satu
-- INTERNAL_API_KEY yang dipegang setiap terminal. Jalur PIN nasabah sudah ber-lockout
-- justru karena alasan yang sama.
ALTER TABLE cs_agents
    ADD COLUMN failed_login_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN locked_until          TIMESTAMPTZ;

-- password_hash NULL berarti petugas belum pernah menyetel kata sandi — keadaan sah bagi
-- setiap baris yang sudah ada. Login akan menolaknya dengan kode tersendiri, bukan
-- "kata sandi salah": petugas yang belum punya kata sandi tidak sedang salah mengetik.
--
-- CHECK-nya mengikat stempel ke nilainya, supaya tidak ada baris yang punya hash tanpa
-- jejak kapan disetel.
ALTER TABLE cs_agents
    ADD CONSTRAINT cs_agents_password_consistent CHECK (
        (password_hash IS NULL) = (password_set_at IS NULL)
    );
