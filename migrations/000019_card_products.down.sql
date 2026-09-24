-- 000019_card_products.down.sql
--
-- Urutan dibalik dari up: yang bergantung dilepas lebih dulu, enum terakhir.

DROP TABLE IF EXISTS onboarding_card_selection_log;

ALTER TABLE onboarding_sessions
    DROP COLUMN IF EXISTS card_catalog_version,
    DROP COLUMN IF EXISTS card_selected_at,
    DROP COLUMN IF EXISTS card_type;

DROP TABLE IF EXISTS product_card_options;
DROP TABLE IF EXISTS card_products;
DROP TABLE IF EXISTS card_catalog_version;

-- ============================================================
-- ENUM: buang CARD_SELECTION dari onboarding_step
-- ============================================================
--
-- PostgreSQL tidak punya "ALTER TYPE ... DROP VALUE", jadi enum-nya dibuat
-- ulang. Ini satu-satunya cara membuat down benar-benar mengembalikan skema,
-- bukan sekadar menghapus tabel dan meninggalkan enum yang sudah melar.
--
-- Sesi yang sedang berada di CARD_SELECTION dipulangkan ke TNC lebih dulu:
-- tanpa itu cast di bawah gagal, dan step itu memang tidak lagi punya arti
-- setelah sisipan ini dicabut.
UPDATE onboarding_sessions
SET current_step = 'TNC'
WHERE current_step = 'CARD_SELECTION';

-- Penjaga: penukaran tipe di bawah hanya menangani
-- onboarding_sessions.current_step. Kalau kelak ada kolom lain bertipe
-- onboarding_step, rollback ini akan meninggalkannya menunjuk tipe yatim
-- onboarding_step_old — rusak dan sulit dilacak. Lebih baik berhenti di sini
-- dengan pesan yang menyebut kolomnya.
DO $$
DECLARE
    stray TEXT;
BEGIN
    SELECT string_agg(table_name || '.' || column_name, ', ')
      INTO stray
      FROM information_schema.columns
     WHERE udt_name = 'onboarding_step'
       AND NOT (table_name = 'onboarding_sessions' AND column_name = 'current_step');

    IF stray IS NOT NULL THEN
        RAISE EXCEPTION
            'migrasi 000019 down tidak bisa menukar tipe onboarding_step: kolom lain masih memakainya (%). Tambahkan kolom itu ke blok penukaran tipe di bawah.', stray;
    END IF;
END $$;

ALTER TABLE onboarding_sessions ALTER COLUMN current_step DROP DEFAULT;

-- Index ini (migrasi 000016) punya predikat
--     WHERE current_step = 'COMPLETED'
-- yang tersimpan di katalog sebagai 'COMPLETED'::onboarding_step. Begitu tipe
-- di-rename, literal itu menunjuk tipe LAMA sementara kolomnya bertipe baru,
-- dan penggantian tipe gagal dengan
--     operator does not exist: onboarding_step = onboarding_step_old
-- Jadi index dilepas dulu, dipasang lagi setelah tipe selesai ditukar.
DROP INDEX IF EXISTS idx_onboarding_sessions_completed_cleanup;

ALTER TYPE onboarding_step RENAME TO onboarding_step_old;

-- Daftar ini harus sama persis dengan definisi di migrasi 000010 —
-- satu nilai yang meleset membuat rollback diam-diam mengubah skema.
CREATE TYPE onboarding_step AS ENUM (
    'TNC',
    'OCR',
    'PERSONAL_DATA',
    'OTP_VERIFY',
    'BIOMETRIC',
    'VIDEO_CALL',
    'CREDENTIALS',
    'REVIEW',
    'COMPLETED'
);

ALTER TABLE onboarding_sessions
    ALTER COLUMN current_step TYPE onboarding_step
    USING current_step::text::onboarding_step;

ALTER TABLE onboarding_sessions
    ALTER COLUMN current_step SET DEFAULT 'TNC';

DROP TYPE onboarding_step_old;

-- Dipasang kembali persis seperti definisi aslinya di migrasi 000016.
CREATE INDEX IF NOT EXISTS idx_onboarding_sessions_completed_cleanup
    ON onboarding_sessions (current_step, updated_at)
    WHERE current_step = 'COMPLETED' AND deleted_at IS NULL;
