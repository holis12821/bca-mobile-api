-- 000019_card_products.up.sql
--
-- Sisipan langkah "Pilih Kartu" pada flow buka rekening.
-- Kontrak: docs/08-PILIH-KARTU-API-SPEC.md §11.
--
-- Tiga hal di bawah sengaja berbeda dari SQL contoh di §11, karena SQL di sana
-- tidak bisa dijalankan apa adanya di PostgreSQL. Alasannya ditulis di tempatnya
-- masing-masing supaya tidak dikira improvisasi.

-- ============================================================
-- ENUM: sisipkan CARD_SELECTION ke onboarding_step
-- ============================================================
--
-- §11 tidak menyebut ini sama sekali, padahal onboarding_sessions.current_step
-- bertipe enum onboarding_step (migrasi 000010), bukan TEXT. Tanpa baris ini
-- setiap penulisan current_step = 'CARD_SELECTION' gagal dengan
-- "invalid input value for enum".
--
-- AFTER 'TNC' menempatkan nilainya pada posisi ordinal yang benar; urutan enum
-- di sini punya arti, lihat §6 "current_step — urutan baru".
ALTER TYPE onboarding_step ADD VALUE IF NOT EXISTS 'CARD_SELECTION' AFTER 'TNC';

-- ============================================================
-- CARD_PRODUCTS — katalog kartu, satu baris per jenis kartu
-- ============================================================
CREATE TABLE card_products (
    card_type                TEXT PRIMARY KEY,
    name                     TEXT        NOT NULL,
    network                  TEXT        NOT NULL DEFAULT 'MASTERCARD',
    tier_key                 TEXT        NOT NULL
                             CHECK (tier_key IN ('DEBIT', 'PLATINUM_DEBIT')),
    -- style adalah satu-satunya nilai visual yang boleh dikirim ke client.
    -- Client memetakannya ke design token; hex warna dan URL gambar dilarang
    -- disimpan maupun dikirim (aturan wajib #1 skill buka-rekening-kartu).
    style                    TEXT        NOT NULL
                             CHECK (style IN ('BLUE', 'GOLD', 'PLATINUM')),
    currency                 CHAR(3)     NOT NULL DEFAULT 'IDR',

    -- Nominal disimpan integer (rupiah penuh), bukan string terformat.
    fee_monthly_admin        BIGINT      NOT NULL CHECK (fee_monthly_admin >= 0),
    fee_card_issuance        BIGINT      NOT NULL DEFAULT 0 CHECK (fee_card_issuance >= 0),
    fee_card_replacement     BIGINT      NOT NULL DEFAULT 0 CHECK (fee_card_replacement >= 0),

    limit_cash_withdrawal    BIGINT      NOT NULL CHECK (limit_cash_withdrawal >= 0),
    limit_transfer_bca       BIGINT      NOT NULL CHECK (limit_transfer_bca >= 0),
    limit_transfer_interbank BIGINT      NOT NULL CHECK (limit_transfer_interbank >= 0),
    limit_debit_purchase     BIGINT      NOT NULL CHECK (limit_debit_purchase >= 0),

    physical_card_available  BOOLEAN     NOT NULL DEFAULT TRUE,
    delivery_days_min        INT         CHECK (delivery_days_min >= 0),
    delivery_days_max        INT         CHECK (delivery_days_max >= 0),
    branch_pickup_available  BOOLEAN     NOT NULL DEFAULT TRUE,

    min_age                  INT         NOT NULL DEFAULT 17 CHECK (min_age >= 0),
    min_initial_deposit      BIGINT      NOT NULL DEFAULT 0 CHECK (min_initial_deposit >= 0),

    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT card_delivery_window_sane
        CHECK (delivery_days_min IS NULL
               OR delivery_days_max IS NULL
               OR delivery_days_min <= delivery_days_max)
);

COMMENT ON TABLE card_products IS
    'Katalog kartu Paspor. Angka fee dan limit WAJIB berasal dari tarif resmi produk — '
    'jangan menyalin dari strings.xml client. Lihat docs/08-PILIH-KARTU-API-SPEC.md §17.';

-- ============================================================
-- CARD_CATALOG_VERSION — penanda versi katalog
-- ============================================================
--
-- §11 tidak menyebut tabel ini, tapi versinya harus tahan lama: §12 menyimpan
-- catalog_version di Redis sebagai onb:cards:version, dan Redis adalah cache —
-- restart atau eviction akan menghapusnya, sementara ETag yang sudah dipegang
-- client tetap merujuk versi itu.
--
-- Satu baris saja. CHECK (id) pada kolom BOOLEAN PRIMARY KEY adalah cara
-- standar mengunci tabel pada maksimum satu baris.
CREATE TABLE card_catalog_version (
    id           BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    version_date DATE        NOT NULL,
    counter      INT         NOT NULL CHECK (counter > 0),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO card_catalog_version (id, version_date, counter)
VALUES (TRUE, CURRENT_DATE, 1);

COMMENT ON TABLE card_catalog_version IS
    'Versi katalog kartu, format YYYY-MM-DD.n. Dinaikkan sekali per transaksi '
    'penulisan katalog — bukan per baris.';

-- ============================================================
-- PRODUCT_CARD_OPTIONS — kartu mana untuk produk mana
-- ============================================================
--
-- product_type memakai enum onboarding_product_type, bukan TEXT seperti contoh
-- §11. Kolom yang sama di onboarding_sessions sudah bertipe enum itu, jadi TEXT
-- di sini akan menciptakan dua sumber kebenaran untuk daftar produk yang sama.
CREATE TABLE product_card_options (
    -- Kunci surogat. §11 menulis
    --     PRIMARY KEY (product_type, card_type, COALESCE(region_code, ''))
    -- yang bukan SQL yang sah: PostgreSQL tidak menerima ekspresi di dalam
    -- constraint PRIMARY KEY, hanya nama kolom. Keunikan yang dimaksud tetap
    -- ditegakkan, lewat unique index berekspresi di bawah — di sanalah
    -- COALESCE memang diizinkan.
    id                      BIGSERIAL PRIMARY KEY,

    product_type            onboarding_product_type NOT NULL,
    card_type               TEXT    NOT NULL REFERENCES card_products(card_type),
    display_order           INT     NOT NULL,
    is_default              BOOLEAN NOT NULL DEFAULT FALSE,
    is_popular              BOOLEAN NOT NULL DEFAULT FALSE,
    badge_key               TEXT
                            CHECK (badge_key IS NULL OR badge_key IN (
                                'RECOMMENDED_BEGINNER',
                                'FLEXIBLE_TRANSACTION',
                                'MAX_LIMIT'
                            )),
    availability_status     TEXT    NOT NULL DEFAULT 'AVAILABLE'
                            CHECK (availability_status IN (
                                'AVAILABLE', 'OUT_OF_STOCK', 'DISABLED', 'NOT_ELIGIBLE'
                            )),
    availability_reason_key TEXT
                            CHECK (availability_reason_key IS NULL OR availability_reason_key IN (
                                'STOCK_EMPTY_IN_REGION',
                                'TEMPORARILY_DISABLED',
                                'PRODUCT_MISMATCH',
                                'AGE_REQUIREMENT'
                            )),
    region_code             TEXT,   -- NULL = berlaku nasional
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Status selain AVAILABLE harus menyebut alasannya: client menampilkan
    -- reason_key itu, dan tanpa alasan nasabah hanya melihat kartu yang mati
    -- tanpa penjelasan.
    CONSTRAINT card_option_reason_required
        CHECK (availability_status = 'AVAILABLE' OR availability_reason_key IS NOT NULL)
);

-- Keunikan yang dimaksud §11: satu baris per (produk, kartu, wilayah),
-- dengan NULL region_code diperlakukan sebagai satu nilai tersendiri.
CREATE UNIQUE INDEX idx_product_card_unique
    ON product_card_options (product_type, card_type, COALESCE(region_code, ''));

-- Satu kartu default per produk per wilayah. Penjaga terhadap kesalahan
-- konfigurasi admin yang paling sering terjadi.
CREATE UNIQUE INDEX idx_product_card_default
    ON product_card_options (product_type, COALESCE(region_code, ''))
    WHERE is_default;

-- Jalur baca panas: katalog per produk, terurut.
CREATE INDEX idx_product_card_lookup
    ON product_card_options (product_type, display_order);

-- ============================================================
-- ONBOARDING_SESSIONS — kolom pilihan kartu
-- ============================================================
--
-- Semua nullable dengan default aman: sesi yang dibuat client lama (tanpa
-- card_type) harus tetap sah (aturan wajib #7).
ALTER TABLE onboarding_sessions
    ADD COLUMN card_type            TEXT REFERENCES card_products(card_type),
    ADD COLUMN card_selected_at     TIMESTAMPTZ,
    ADD COLUMN card_catalog_version TEXT;

-- ============================================================
-- AUDIT — jejak pemilihan dan perubahan kartu
-- ============================================================
CREATE TABLE onboarding_card_selection_log (
    id              BIGSERIAL PRIMARY KEY,
    session_id      VARCHAR(32) NOT NULL,
    from_card_type  TEXT,
    to_card_type    TEXT        NOT NULL,
    catalog_version TEXT        NOT NULL,
    -- Disalin, bukan di-join: yang perlu dibuktikan saat sengketa adalah biaya
    -- YANG DILIHAT NASABAH saat itu, bukan biaya hari ini.
    monthly_admin_fee_shown BIGINT NOT NULL,
    actor           TEXT        NOT NULL DEFAULT 'CUSTOMER',
    ip_address      INET,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- session_id VARCHAR(32) menyamai onboarding_sessions.session_id (migrasi
-- 000010), bukan TEXT seperti contoh §11 — supaya join tidak perlu cast.
CREATE INDEX idx_card_selection_log_session
    ON onboarding_card_selection_log (session_id, created_at DESC);

CREATE INDEX idx_card_selection_log_created
    ON onboarding_card_selection_log (created_at DESC);

COMMENT ON TABLE onboarding_card_selection_log IS
    'Jejak audit pemilihan kartu. Retensi BELUM ditetapkan compliance '
    '(docs/08-PILIH-KARTU-API-SPEC.md §17) — jangan pasang job pembersihan apa pun.';
