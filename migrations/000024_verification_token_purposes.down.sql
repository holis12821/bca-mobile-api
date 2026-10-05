-- 000024_verification_token_purposes.down.sql
--
-- Mengembalikan daftar lima purpose seperti migrasi 000003.
--
-- Baris yang memakai tiga purpose baru dihapus lebih dulu, kalau tidak ADD
-- CONSTRAINT akan gagal karena data yang sudah ada melanggarnya. Yang terhapus
-- hanyalah token berumur pendek (5 menit) yang belum dipakai; tidak ada riwayat
-- transaksi di tabel ini.
DELETE FROM verification_tokens
WHERE purpose IN ('CHANGE_PROFILE', 'BLOCK_CARD', 'REPLACE_CARD');

ALTER TABLE verification_tokens
    DROP CONSTRAINT IF EXISTS verification_tokens_purpose_check;

ALTER TABLE verification_tokens
    ADD CONSTRAINT verification_tokens_purpose_check
    CHECK (purpose IN ('TRANSFER', 'EWALLET_TOPUP', 'QRIS_PAYMENT',
           'CHANGE_LIMIT', 'CHANGE_PIN'));
