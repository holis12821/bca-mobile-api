-- 000024_verification_token_purposes.up.sql
--
-- Melebarkan daftar purpose verification_tokens dari lima menjadi delapan.
--
-- KENAPA
--
-- transaction.ValidPurposes di Go dan CHECK constraint di migrasi 000003 adalah
-- dua salinan dari satu daftar, dan keduanya sudah melenceng:
--
--   * CHANGE_PROFILE sudah ada di Go sejak lama, tapi tidak pernah ada di
--     constraint. Akibatnya POST /auth/pin/verify dengan purpose itu menjawab
--     500 — bukan 422, bukan pesan yang bisa dibaca — dan PUT /account/profile
--     yang membutuhkannya tidak pernah bisa diselesaikan. Ini bukan regresi
--     baru; ia sudah begitu dan tidak ada test yang menyentuhnya karena test
--     domain memakai mock, bukan constraint sungguhan.
--
--   * BLOCK_CARD dan REPLACE_CARD dibutuhkan endpoint kartu nasabah
--     (POST /account/cards/{id}/block dan .../replacement). Tanpa baris ini
--     keduanya gagal pada langkah penerbitan token, sebelum sempat menyentuh
--     kartu.
--
-- Constraint-nya sengaja TETAP tertutup. Menggantinya dengan kolom bebas akan
-- menghilangkan satu-satunya hal yang memastikan token untuk "transfer" tidak
-- bisa dipakai memblokir kartu.

ALTER TABLE verification_tokens
    DROP CONSTRAINT IF EXISTS verification_tokens_purpose_check;

ALTER TABLE verification_tokens
    ADD CONSTRAINT verification_tokens_purpose_check
    CHECK (purpose IN (
        'TRANSFER',
        'EWALLET_TOPUP',
        'QRIS_PAYMENT',
        'CHANGE_LIMIT',
        'CHANGE_PIN',
        -- Sudah dipakai kode sejak lama, tidak pernah ada di constraint.
        'CHANGE_PROFILE',
        -- Kartu milik nasabah (migrasi 000021).
        'BLOCK_CARD',
        'REPLACE_CARD'
    ));

COMMENT ON COLUMN verification_tokens.purpose IS
    'Tujuan token, terikat saat diterbitkan POST /auth/pin/verify dan dicocokkan '
    'saat dikonsumsi. Daftarnya tertutup dan HARUS sama dengan '
    'transaction.ValidPurposes di Go — dua salinan yang melenceng adalah sebab '
    'migrasi ini ada.';
