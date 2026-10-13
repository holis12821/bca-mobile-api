-- Katalog jenis rekening tabungan: layar PERTAMA flow buka rekening.
--
-- Sebelum tabel ini, isi layar hidup sebagai 16 entri strings.xml di dalam APK,
-- dirakit fungsi Kotlin defaultJenisRekeningList(). Sisi bank hanya punya enum
-- onboarding_product_type (migrasi 000010) — tiga nama tanpa satu pun atribut.
--
-- Tiga akibat yang ditutup migrasi ini:
--
-- 1. Setoran awal yang DILIHAT nasabah tidak punya arsip. Begitu angkanya diperbarui,
--    tidak ada catatan apa pun tentang angka yang dilihat nasabah yang mendaftar
--    sebelum perubahan — padahal setoran awal adalah komitmen 30 hari kalender yang
--    disebut pasal 4 S&K. Masalah yang sama persis dengan yang ditutup 000025 untuk
--    teks S&K.
-- 2. Produk tidak bisa ditutup tanpa rilis aplikasi. Produk yang tutup tetap dipajang
--    dan baru ditolak di POST /sessions dengan 422 — setelah nasabah menyetujui S&K
--    dan memilih kartu.
-- 3. Client memilih produk berdasarkan POSISI array (ProductType.fromIndex), bukan
--    kode. Begitu server mengurutkan atau menyembunyikan satu produk, indeks itu
--    menunjuk produk yang SALAH dan nasabah membuka rekening yang bukan pilihannya —
--    tanpa error di mana pun. Itu alasan response membawa product_type per item.

CREATE TABLE onboarding_products (
    -- Enum, BUKAN TEXT. TEXT di sini menciptakan sumber kebenaran kedua untuk daftar
    -- produk yang sama — persis yang dihindari komentar di 000019 untuk
    -- product_card_options. Katalog ini MENGISI ATRIBUT nilai enum yang sudah ada,
    -- bukan membuat daftar produk kedua.
    product_type  onboarding_product_type PRIMARY KEY,

    name          TEXT NOT NULL,
    description   TEXT NOT NULL,

    -- Rupiah penuh sebagai integer, bukan string terformat. Pemformatan ("Rp 500.000")
    -- milik client; server yang mengirim string terformat memaksa setiap pembaca
    -- mem-parse-nya kembali untuk berhitung.
    min_initial_deposit BIGINT NOT NULL CHECK (min_initial_deposit >= 0),
    currency            CHAR(3) NOT NULL DEFAULT 'IDR',

    -- Enum tampilan, bukan nilai visual. Tidak ada hex, gradient, nama drawable, atau
    -- URL gambar: client memetakan icon_key ke drawable dan style ke design token.
    -- Mengirim hex membuat client melanggar aturan token repo Android.
    icon_key  TEXT NOT NULL,
    style     TEXT NOT NULL,

    -- badge_key, bukan kalimat "Paling Populer": label statis dikirim sebagai key
    -- supaya client yang sudah menerjemahkannya tidak ikut berubah saat katanya
    -- diperbaiki.
    badge_key TEXT,

    is_popular BOOLEAN NOT NULL DEFAULT FALSE,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,

    -- Baris produk TIDAK PERNAH dihapus: onboarding_sessions.product_type menunjuk ke
    -- sini, dan sesi lama harus tetap bisa menyebut produk yang dipilihnya. Produk yang
    -- dihentikan di-set is_active = FALSE.
    is_active BOOLEAN NOT NULL DEFAULT TRUE,

    -- Urutan tampilan, dan ia BUKAN identitas. Satu-satunya identitas produk adalah
    -- product_type (lihat akibat 3 di atas).
    display_order INT NOT NULL,

    availability_status     TEXT NOT NULL DEFAULT 'AVAILABLE',
    availability_reason_key TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT onboarding_products_icon_key_valid CHECK (
        icon_key IN ('WALLET', 'CARD', 'SAVINGS')
    ),
    CONSTRAINT onboarding_products_style_valid CHECK (
        style IN ('PRIMARY', 'SECONDARY', 'NEUTRAL')
    ),
    CONSTRAINT onboarding_products_badge_key_valid CHECK (
        badge_key IS NULL OR badge_key IN ('MOST_POPULAR')
    ),
    CONSTRAINT onboarding_products_availability_valid CHECK (
        availability_status IN ('AVAILABLE', 'DISABLED', 'COMING_SOON')
    ),
    CONSTRAINT onboarding_products_reason_key_valid CHECK (
        availability_reason_key IS NULL
        OR availability_reason_key IN ('TEMPORARILY_DISABLED', 'MAINTENANCE', 'COMING_SOON')
    ),

    -- Status selain AVAILABLE WAJIB menyebut alasannya. Tanpa ini nasabah hanya melihat
    -- kartu mati tanpa penjelasan, dan tidak ada cara membedakan "sedang maintenance"
    -- dari "belum dibuka untuk umum".
    CONSTRAINT onboarding_products_reason_required CHECK (
        availability_status = 'AVAILABLE' OR availability_reason_key IS NOT NULL
    )
);

COMMENT ON TABLE onboarding_products IS
    'Katalog jenis rekening tabungan untuk layar pertama buka rekening. '
    'ANGKA min_initial_deposit DAN TEKS name/description DI MIGRASI INI ADALAH DATA '
    'DESAIN yang disalin apa adanya dari strings.xml repo Android (500.000 / 50.000 / '
    '20.000) — BUKAN tarif resmi BCA. Disalin apa adanya supaya nasabah tidak melihat '
    'perubahan kata saat katalog dinyalakan; wajib diganti angka resmi dari product '
    'owner sebelum dipakai di produksi. Pola yang sama dengan 000025_onboarding_tnc. '
    'Baris di tabel ini TIDAK PERNAH dihapus: onboarding_sessions.product_type menunjuk '
    'ke sini. Produk yang dihentikan di-set is_active = FALSE.';

-- Maksimum SATU produk berbadge "Paling Populer", dan maksimum satu default.
--
-- Unique index berekspresi atas konstanta: dua baris is_popular = TRUE membuat
-- jawabannya bergantung ORDER BY, dan jawaban yang bergantung urutan baris bisa
-- berubah sendiri tanpa ada yang menyunting apa pun.
CREATE UNIQUE INDEX idx_onboarding_products_one_popular
    ON onboarding_products ((TRUE)) WHERE is_popular;

CREATE UNIQUE INDEX idx_onboarding_products_one_default
    ON onboarding_products ((TRUE)) WHERE is_default;

-- Jalur panas katalog: produk aktif, terurut tampilan.
CREATE INDEX idx_onboarding_products_active
    ON onboarding_products (display_order) WHERE is_active;

-- Fitur produk: tabel terpisah, BUKAN JSONB dan bukan kolom berpola feature_N.
--
-- Jumlah fiturnya berbeda per produk — layar sudah merender 3, 3, dan 2 fitur — dan
-- berubah tiap revisi materi pemasaran. Menambah fitur keempat tidak boleh berarti
-- menambah kolom. Pola onboarding_tnc_sections.
CREATE TABLE onboarding_product_features (
    id            BIGSERIAL PRIMARY KEY,
    product_type  onboarding_product_type NOT NULL
        REFERENCES onboarding_products (product_type) ON DELETE CASCADE,

    -- Urutan tampil. UNIQUE bersama product_type supaya tidak ada dua fitur yang
    -- memperebutkan posisi yang sama — itu akan membuat urutan fitur bergantung
    -- ORDER BY pada kolom yang tidak menentukan apa pun.
    feature_order INT NOT NULL,
    label         TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT onboarding_product_features_order_positive CHECK (feature_order > 0),
    CONSTRAINT onboarding_product_features_unique_order UNIQUE (product_type, feature_order)
);

COMMENT ON TABLE onboarding_product_features IS
    'Fitur per produk, berurut. Teks di migrasi ini DATA DESAIN dari strings.xml, bukan '
    'janji produk resmi — teks fitur adalah janji produk, jadi salah di sini adalah '
    'salah informasi, bukan salah layout. Wajib dikonfirmasi product owner/marketing.';

-- Copy halaman: SATU baris, ditegakkan primary key boolean.
--
-- Dilayani server supaya mengubah judul, label setoran, isi kotak Persiapan Dokumen,
-- atau kalimat S&K tidak menuntut rilis APK — sejajar dengan GET /onboarding/tnc yang
-- sudah melayani heading/subtitle/agree_cta miliknya sendiri.
CREATE TABLE onboarding_product_page (
    -- id BOOLEAN DEFAULT TRUE CHECK (id): satu-satunya baris yang mungkin. Tabel
    -- konfigurasi satu baris tanpa penjaga ini akan punya baris kedua yang tidak pernah
    -- terbaca, lalu seseorang menyunting yang salah.
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),

    heading       TEXT NOT NULL,
    subtitle      TEXT NOT NULL,
    deposit_label TEXT NOT NULL,
    cta_label     TEXT NOT NULL,

    notice_icon_key TEXT NOT NULL,
    notice_title    TEXT NOT NULL,
    notice_body     TEXT NOT NULL,

    -- Kalimat persetujuan dipecah TIGA dengan alasan yang sama seperti TNCConsent:
    -- bagian tengahnya dicetak tebal dan berwarna oleh aplikasi. Satu kalimat utuh
    -- memaksa client mencari substring, dan substring itu pecah pada setiap perbaikan
    -- kata.
    consent_prefix TEXT NOT NULL,
    consent_link   TEXT NOT NULL,
    consent_suffix TEXT NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT onboarding_product_page_notice_icon_valid CHECK (
        notice_icon_key IN ('INFO')
    )
);

-- Penanda versi katalog: SATU baris, di Postgres — bukan hanya di Redis.
--
-- Redis adalah cache: restart atau eviction menghapusnya, sementara ETag yang sudah
-- dipegang client — dan product_catalog_version yang sudah tersimpan di baris sesi —
-- tetap merujuk versi itu. Alasan yang sama dengan card_catalog_version.
CREATE TABLE onboarding_product_catalog_version (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),

    version_date DATE NOT NULL DEFAULT CURRENT_DATE,
    counter      INT  NOT NULL DEFAULT 1 CHECK (counter > 0),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Jejak di sesi: dua kolom, keduanya NULLABLE.
--
-- Yang perlu dibuktikan saat sengketa adalah angka YANG DILIHAT NASABAH saat itu, bukan
-- angka hari ini — alasan yang sama dengan monthly_admin_fee_shown pada
-- onboarding_card_selection_log.
--
-- NULLABLE dengan sengaja: APK lama tidak mengenal katalog ini sama sekali dan harus
-- tetap bisa membuat sesi. Katalog yang mati juga tidak boleh mematikan POST /sessions.
--
-- Dua kolom, bukan tabel log: produk dipilih SEKALI dan sesi lahir sesudahnya — tidak
-- ada jalur perubahan yang perlu dirunut, berbeda dari kartu yang bisa diganti lewat
-- PUT /sessions/{id}/card.
ALTER TABLE onboarding_sessions
    ADD COLUMN product_catalog_version   TEXT,
    ADD COLUMN min_initial_deposit_shown BIGINT;

ALTER TABLE onboarding_sessions
    ADD CONSTRAINT onboarding_sessions_deposit_shown_non_negative CHECK (
        min_initial_deposit_shown IS NULL OR min_initial_deposit_shown >= 0
    );

-- --------------------------------------------------------------------------
-- Isi katalog.
--
-- Ikut MIGRASI, bukan seeder: seeder menolak jalan di luar APP_ENV=development, jadi
-- staging akan menjawab layar kosong. Pelajaran dari 000025.
--
-- Semuanya ON CONFLICT DO UPDATE supaya migrasi ini aman dijalankan ulang, dan supaya
-- `down` lalu `up` lagi tidak menabrak baris yang tertinggal.
-- --------------------------------------------------------------------------

INSERT INTO onboarding_products (
    product_type, name, description, min_initial_deposit,
    icon_key, style, badge_key, is_popular, is_default, is_active, display_order
) VALUES
    ('TAHAPAN_BCA', 'Tahapan BCA',
     'Tabungan utama untuk kemudahan transaksi harian dan proteksi finansial keluarga.',
     500000, 'WALLET', 'PRIMARY', 'MOST_POPULAR', TRUE, TRUE, TRUE, 1),

    ('TAHAPAN_XPRESI', 'Tahapan Xpresi',
     'Tabungan digital untuk anak muda, serba praktis tanpa ribet buku tabungan.',
     50000, 'CARD', 'SECONDARY', NULL, FALSE, FALSE, TRUE, 2),

    ('TABUNGANKU', 'TabunganKu',
     'Tabungan perorangan dengan persyaratan sangat mudah, terjangkau, dan hemat.',
     20000, 'SAVINGS', 'NEUTRAL', NULL, FALSE, FALSE, TRUE, 3)
ON CONFLICT (product_type) DO UPDATE
SET name                = EXCLUDED.name,
    description         = EXCLUDED.description,
    min_initial_deposit = EXCLUDED.min_initial_deposit,
    icon_key            = EXCLUDED.icon_key,
    style               = EXCLUDED.style,
    badge_key           = EXCLUDED.badge_key,
    is_popular          = EXCLUDED.is_popular,
    is_default          = EXCLUDED.is_default,
    is_active           = EXCLUDED.is_active,
    display_order       = EXCLUDED.display_order,
    updated_at          = now();

-- Fitur dicari lewat KUNCI ALAMI (product_type, feature_order), bukan lewat id
-- BIGSERIAL: menulis angka id apa pun akan pecah begitu ada yang menjalankan `down`
-- lalu `up` lagi dan sequence-nya sudah bergerak.
INSERT INTO onboarding_product_features (product_type, feature_order, label) VALUES
    ('TAHAPAN_BCA',    1, 'Debit Mastercard'),
    ('TAHAPAN_BCA',    2, 'm-BCA & KlikBCA'),
    ('TAHAPAN_BCA',    3, 'Bebas tarik tunai di ribuan ATM'),

    ('TAHAPAN_XPRESI', 1, 'Desain Kartu Custom'),
    ('TAHAPAN_XPRESI', 2, 'm-Banking 24/7'),
    ('TAHAPAN_XPRESI', 3, 'Biaya admin bulanan sangat ringan'),

    ('TABUNGANKU',     1, 'Tanpa biaya administrasi bulanan'),
    ('TABUNGANKU',     2, 'Bunga tabungan kompetitif')
ON CONFLICT (product_type, feature_order) DO UPDATE
SET label = EXCLUDED.label;

INSERT INTO onboarding_product_page (
    id, heading, subtitle, deposit_label, cta_label,
    notice_icon_key, notice_title, notice_body,
    consent_prefix, consent_link, consent_suffix
) VALUES (
    TRUE,
    'Pilih Jenis Rekening',
    'Pilih jenis rekening yang sesuai dengan kebutuhan dan gaya hidup Anda.',
    'Setoran Awal Minimum',
    'Lanjut',
    'INFO',
    'Persiapan Dokumen',
    'Siapkan e-KTP fisik Anda dan pastikan berada di area dengan koneksi internet yang stabil untuk kelancaran video verifikasi.',
    'Dengan melanjutkan, Anda menyetujui ',
    'Syarat & Ketentuan',
    ' pembukaan rekening BCA.'
)
ON CONFLICT (id) DO UPDATE
SET heading         = EXCLUDED.heading,
    subtitle        = EXCLUDED.subtitle,
    deposit_label   = EXCLUDED.deposit_label,
    cta_label       = EXCLUDED.cta_label,
    notice_icon_key = EXCLUDED.notice_icon_key,
    notice_title    = EXCLUDED.notice_title,
    notice_body     = EXCLUDED.notice_body,
    consent_prefix  = EXCLUDED.consent_prefix,
    consent_link    = EXCLUDED.consent_link,
    consent_suffix  = EXCLUDED.consent_suffix,
    updated_at      = now();

-- Versi awal. Tanggalnya CURRENT_DATE dan bukan tanggal keras: migrasi yang dijalankan
-- bulan depan tidak semestinya mengaku katalognya terbit hari ini.
INSERT INTO onboarding_product_catalog_version (id, version_date, counter)
VALUES (TRUE, CURRENT_DATE, 1)
ON CONFLICT (id) DO UPDATE
SET version_date = EXCLUDED.version_date,
    counter      = onboarding_product_catalog_version.counter + 1,
    updated_at   = now();
