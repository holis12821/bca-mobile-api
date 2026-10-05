-- 000022_card_catalog_rates.down.sql
--
-- Mengembalikan katalog ke keadaan sebelum 000022: tanpa lineup produk, dan
-- tanpa tarif yang bisa disajikan ke nasabah.

-- Lineup dihapus lebih dulu. Tidak ada yang merujuk baris ini, jadi hapus benar
-- benar bisa dilakukan — dan katalog kosong menjawab CARD_CATALOG_EMPTY, keadaan
-- yang client memang sudah tahu cara menanganinya.
DELETE FROM product_card_options
WHERE card_type IN ('PASPOR_BLUE', 'PASPOR_GOLD', 'PASPOR_PLATINUM')
  AND region_code IS NULL;

-- Baris kartunya TIDAK bisa dihapus tanpa syarat.
--
-- `account_cards.card_type` (migrasi 000021) dan `onboarding_sessions.card_type`
-- (migrasi 000019) keduanya punya foreign key ke sini. DELETE tanpa penjaga akan
-- gagal di database mana pun yang sudah punya satu kartu nasabah atau satu sesi
-- yang memilih kartu — yaitu setiap database yang pernah dipakai. Migrasi down
-- yang gagal di tengah jalan lebih buruk daripada migrasi down yang menyisakan
-- baris: yang pertama meninggalkan skema pada versi yang tidak jelas.
--
-- Jadi: hapus yang tidak dirujuk siapa pun.
DELETE FROM card_products cp
WHERE cp.card_type IN ('PASPOR_BLUE', 'PASPOR_GOLD', 'PASPOR_PLATINUM')
  AND NOT EXISTS (SELECT 1 FROM account_cards ac WHERE ac.card_type = cp.card_type)
  AND NOT EXISTS (SELECT 1 FROM onboarding_sessions s WHERE s.card_type = cp.card_type);

-- Yang masih dirujuk: nolkan tarifnya dan nonaktifkan kartunya. Kartu nonaktif
-- tidak muncul di katalog, jadi tidak ada tarif yang tersaji — tapi foreign key
-- yang menunjuk ke baris ini tetap sah, dan riwayat kartu nasabah tidak hilang.
--
-- Nol di sini berarti "tidak diketahui", bukan "gratis". Itulah alasan
-- is_active = FALSE ikut dipasang: baris tanpa tarif tidak boleh bisa dipilih.
UPDATE card_products
SET fee_monthly_admin        = 0,
    fee_card_issuance        = 0,
    fee_card_replacement     = 0,
    limit_cash_withdrawal    = 0,
    limit_transfer_bca       = 0,
    limit_transfer_interbank = 0,
    limit_debit_purchase     = 0,
    is_active                = FALSE,
    updated_at               = NOW()
WHERE card_type IN ('PASPOR_BLUE', 'PASPOR_GOLD', 'PASPOR_PLATINUM');

COMMENT ON TABLE card_products IS
    'Katalog kartu Paspor. Angka fee dan limit WAJIB berasal dari tarif resmi produk — '
    'jangan menyalin dari strings.xml client. Lihat docs/08-PILIH-KARTU-API-SPEC.md §17.';

-- Katalog berubah lagi, jadi versinya naik lagi. Versi tidak pernah diturunkan:
-- ETag lama yang kembali menjadi sah akan membuat client menyajikan katalog yang
-- sudah dicabut.
UPDATE card_catalog_version
SET version_date = CURRENT_DATE,
    counter      = CASE WHEN version_date = CURRENT_DATE THEN counter + 1 ELSE 1 END,
    updated_at   = NOW()
WHERE id;
