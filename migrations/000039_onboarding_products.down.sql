-- Urutan terbalik dari up: anak sebelum induk, kolom sesi sebelum tabel katalog.
--
-- Kolom di onboarding_sessions dilepas lebih dulu supaya `down` lalu `up` lagi tidak
-- menabrak constraint yang sudah ada — jalur yang benar-benar diuji, bukan dibaca.
ALTER TABLE onboarding_sessions
    DROP CONSTRAINT IF EXISTS onboarding_sessions_deposit_shown_non_negative;

ALTER TABLE onboarding_sessions
    DROP COLUMN IF EXISTS min_initial_deposit_shown,
    DROP COLUMN IF EXISTS product_catalog_version;

DROP TABLE IF EXISTS onboarding_product_catalog_version;
DROP TABLE IF EXISTS onboarding_product_page;

-- features punya FK ke products, jadi ia dulu. ON DELETE CASCADE hanya mengatur
-- penghapusan BARIS; DROP TABLE induk tetap ditolak selama tabel anaknya ada.
DROP TABLE IF EXISTS onboarding_product_features;

DROP INDEX IF EXISTS idx_onboarding_products_active;
DROP INDEX IF EXISTS idx_onboarding_products_one_default;
DROP INDEX IF EXISTS idx_onboarding_products_one_popular;
DROP TABLE IF EXISTS onboarding_products;

-- Enum onboarding_product_type TIDAK dihapus: ia milik migrasi 000010 dan masih
-- dipakai onboarding_sessions serta product_card_options.
