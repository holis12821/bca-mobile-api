-- 000022_card_catalog_rates.up.sql
--
-- Mengisi katalog kartu Paspor: biaya, keempat limit, dan kartu mana untuk
-- produk mana. Menjawab docs/08-PILIH-KARTU-API-SPEC.md §17 butir 1, 2, dan 3,
-- serta butir 4 di docs/10-HANDOVER-BLOCKER-BACKEND.md.
--
-- ============================================================
-- ANGKA DI SINI ADALAH DATA PORTOFOLIO, BUKAN TARIF RESMI BCA
-- ============================================================
--
-- Proyek ini portofolio, jadi angkanya diputuskan di dalam proyek sendiri, bukan
-- diterima dari product owner. Yang penting bukan ketepatannya terhadap tarif
-- BCA yang sebenarnya, melainkan bahwa nilainya:
--
--   1. KOHEREN antar tingkat kartu — setiap limit naik dari Blue ke Gold ke
--      Platinum, dan biaya bulanannya ikut naik. Katalog yang tingkatnya tidak
--      berurutan membuat layar pilih kartu kehilangan alasan keberadaannya.
--   2. BUKAN salinan dari `strings.xml` client (Rp14.000 / 16.000 / 19.000).
--      §17 menyebut itu bahaya terbesarnya: angka desain layar yang bocor ke
--      database tidak membuat siapa pun curiga. Nilai di bawah sengaja berbeda.
--   3. BUKAN angka yang jelas palsu seperti 11111 / 22222 yang dipakai seeder
--      sebelumnya. Katalog ini sekarang ikut ke setiap environment, jadi ia
--      harus bisa dipakai — tapi lihat CATATAN PENGGANTIAN di bawah.
--
-- CATATAN PENGGANTIAN: kalau layanan ini pernah dipakai sungguhan, tarif resmi
-- masuk lewat admin API katalog (`PUT /internal/v1/cards/{card_type}`), yang
-- menaikkan `catalog_version` dan menulis nilai lama + baru ke
-- `card_catalog_audit_log`. Jangan menambah migrasi baru untuk itu: perubahan
-- tarif adalah operasi, bukan skema.

-- ============================================================
-- CARD_PRODUCTS
-- ============================================================
--
-- Upsert, bukan UPDATE: di luar development tabel ini KOSONG. Baris kartu dulu
-- hanya dibuat `scripts/seed/main.go`, dan seeder itu menolak jalan kecuali
-- APP_ENV=development — jadi staging tidak akan pernah punya katalog sama
-- sekali. Katalog adalah data referensi; tempatnya di migrasi.
--
-- Angka yang dipilih:
--
--   kartu      admin/bln  terbit  ganti    tarik tunai   transfer BCA   antar bank   debit/hari
--   BLUE          15.000       0  25.000     7.000.000     50.000.000   15.000.000   50.000.000
--   GOLD          17.000       0  25.000    10.000.000     75.000.000   20.000.000   75.000.000
--   PLATINUM      20.000       0  50.000    12.500.000    100.000.000   25.000.000  100.000.000
--
-- Biaya penerbitan nol untuk ketiganya: kartu pertama saat buka rekening tidak
-- ditagih, dan biaya penggantian (kartu hilang atau rusak) yang menanggung
-- kasus kartu kedua. Kalau penerbitan ikut ditagih, nasabah membayar dua kali
-- untuk satu kartu — sekali di sini, sekali di setoran awal.
--
-- Biaya penggantian Platinum dua kali Blue/Gold karena kartunya sendiri lebih
-- mahal dicetak; ini satu-satunya biaya yang tidak naik bertingkat di ketiga
-- kartu, dan itu disengaja.
--
-- `min_age` tetap 17 untuk ketiganya. Menaikkannya untuk Platinum akan
-- menuliskan aturan yang TIDAK ADA yang menegakkan: tidak ada kode yang menolak
-- pemohon di bawah umur per kartu, dan `min_age` hanya ditampilkan client.
-- Penggerbangan nyata dilakukan admin lewat `availability_status` +
-- `availability_reason_key = 'AGE_REQUIREMENT'`. Janji yang tidak ditegakkan
-- lebih buruk daripada tidak berjanji.
--
-- `min_initial_deposit` DIBEDAKAN dan sifatnya informatif dengan cara yang sama:
-- client menampilkannya di kartu Platinum sebagai syarat setoran awal. Angkanya
-- naik bertingkat supaya tingkatan kartunya punya arti.

INSERT INTO card_products (
    card_type, name, network, tier_key, style, currency,
    fee_monthly_admin, fee_card_issuance, fee_card_replacement,
    limit_cash_withdrawal, limit_transfer_bca,
    limit_transfer_interbank, limit_debit_purchase,
    physical_card_available, delivery_days_min, delivery_days_max,
    branch_pickup_available, min_age, min_initial_deposit, is_active
) VALUES
    ('PASPOR_BLUE', 'Blue Mastercard', 'MASTERCARD', 'DEBIT', 'BLUE', 'IDR',
     15000, 0, 25000,
     7000000, 50000000, 15000000, 50000000,
     TRUE, 3, 7, TRUE, 17, 500000, TRUE),

    ('PASPOR_GOLD', 'Gold Mastercard', 'MASTERCARD', 'DEBIT', 'GOLD', 'IDR',
     17000, 0, 25000,
     10000000, 75000000, 20000000, 75000000,
     TRUE, 3, 7, TRUE, 17, 1000000, TRUE),

    ('PASPOR_PLATINUM', 'Platinum Mastercard', 'MASTERCARD', 'PLATINUM_DEBIT', 'PLATINUM', 'IDR',
     20000, 0, 50000,
     12500000, 100000000, 25000000, 100000000,
     TRUE, 5, 10, TRUE, 17, 10000000, TRUE)

ON CONFLICT (card_type) DO UPDATE SET
    name                     = EXCLUDED.name,
    network                  = EXCLUDED.network,
    tier_key                 = EXCLUDED.tier_key,
    style                    = EXCLUDED.style,
    currency                 = EXCLUDED.currency,
    fee_monthly_admin        = EXCLUDED.fee_monthly_admin,
    fee_card_issuance        = EXCLUDED.fee_card_issuance,
    fee_card_replacement     = EXCLUDED.fee_card_replacement,
    limit_cash_withdrawal    = EXCLUDED.limit_cash_withdrawal,
    limit_transfer_bca       = EXCLUDED.limit_transfer_bca,
    limit_transfer_interbank = EXCLUDED.limit_transfer_interbank,
    limit_debit_purchase     = EXCLUDED.limit_debit_purchase,
    physical_card_available  = EXCLUDED.physical_card_available,
    delivery_days_min        = EXCLUDED.delivery_days_min,
    delivery_days_max        = EXCLUDED.delivery_days_max,
    branch_pickup_available  = EXCLUDED.branch_pickup_available,
    min_age                  = EXCLUDED.min_age,
    min_initial_deposit      = EXCLUDED.min_initial_deposit,
    is_active                = TRUE,
    updated_at               = NOW();

COMMENT ON TABLE card_products IS
    'Katalog kartu Paspor. Angka fee dan limit saat ini adalah DATA PORTOFOLIO '
    'yang diputuskan di migrasi 000022 — bukan tarif resmi BCA, dan bukan salinan '
    'dari strings.xml client. Penggantian dengan tarif resmi dilakukan lewat '
    'admin API katalog, bukan lewat migrasi baru.';

-- ============================================================
-- PRODUCT_CARD_OPTIONS — lineup per produk (§17 butir 1)
-- ============================================================
--
-- Keputusannya: kartu yang ditawarkan naik bersama kelas produknya.
--
--   TAHAPAN_BCA     (setoran awal 500.000) → ketiga kartu
--   TAHAPAN_XPRESI  (setoran awal  50.000) → Blue, Gold
--   TABUNGANKU      (setoran awal  20.000) → Blue saja
--
-- TabunganKu adalah produk setoran awal Rp20 ribu; menawarinya kartu dengan
-- syarat setoran awal Rp10 juta akan menampilkan pilihan yang pasti tidak bisa
-- diambil nasabahnya. Xpresi berhenti di Gold dengan alasan yang sama.
--
-- Satu default per produk, dan itu selalu Blue: kartu termurah adalah pilihan
-- yang paling tidak mengejutkan bagi nasabah yang menekan lanjut tanpa membaca.
-- Memasang kartu berbiaya bulanan tertinggi sebagai default akan menagih
-- nasabah untuk pilihan yang tidak pernah ia buat.
--
-- `region_code` NULL: katalog nasional. Stok per wilayah diisi admin API saat
-- memang ada perbedaan wilayah — bukan di sini.

INSERT INTO product_card_options (
    product_type, card_type, display_order, is_default, is_popular, badge_key,
    availability_status, region_code
) VALUES
    ('TAHAPAN_BCA',    'PASPOR_BLUE',     1, TRUE,  TRUE,  'RECOMMENDED_BEGINNER', 'AVAILABLE', NULL),
    ('TAHAPAN_BCA',    'PASPOR_GOLD',     2, FALSE, FALSE, 'FLEXIBLE_TRANSACTION', 'AVAILABLE', NULL),
    ('TAHAPAN_BCA',    'PASPOR_PLATINUM', 3, FALSE, FALSE, 'MAX_LIMIT',            'AVAILABLE', NULL),

    ('TAHAPAN_XPRESI', 'PASPOR_BLUE',     1, TRUE,  TRUE,  'RECOMMENDED_BEGINNER', 'AVAILABLE', NULL),
    ('TAHAPAN_XPRESI', 'PASPOR_GOLD',     2, FALSE, FALSE, 'FLEXIBLE_TRANSACTION', 'AVAILABLE', NULL),

    ('TABUNGANKU',     'PASPOR_BLUE',     1, TRUE,  FALSE, 'RECOMMENDED_BEGINNER', 'AVAILABLE', NULL)

ON CONFLICT (product_type, card_type, COALESCE(region_code, '')) DO UPDATE SET
    display_order           = EXCLUDED.display_order,
    is_default              = EXCLUDED.is_default,
    is_popular              = EXCLUDED.is_popular,
    badge_key               = EXCLUDED.badge_key,
    availability_status     = EXCLUDED.availability_status,
    availability_reason_key = NULL,
    updated_at              = NOW();

-- ============================================================
-- CARD_CATALOG_VERSION
-- ============================================================
--
-- Isi katalog berubah, jadi versinya WAJIB naik. Client memegang ETag yang
-- dibentuk dari versi ini; tanpa kenaikan, aplikasi yang sudah menyimpan katalog
-- lama akan menerima 304 dan terus menampilkan katalog tanpa tarif.
--
-- Naik sekali untuk seluruh migrasi, bukan sekali per baris — sesuai komentar
-- tabelnya di migrasi 000019.
UPDATE card_catalog_version
SET version_date = CURRENT_DATE,
    counter      = CASE WHEN version_date = CURRENT_DATE THEN counter + 1 ELSE 1 END,
    updated_at   = NOW()
WHERE id;
