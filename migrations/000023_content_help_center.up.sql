-- 000023_content_help_center.up.sql
--
-- Isi dua baris terakhir seksi BANTUAN & INFORMASI di layar Profil Saya:
-- Pusat Bantuan (FAQ) dan Kontak CS. Sampai migrasi ini keduanya tidak punya
-- endpoint sama sekali, jadi layarnya menampilkan baris yang tidak bisa ditekan.
--
-- KENAPA DI DATABASE, BUKAN KONSTANTA DI GO
--
-- Nomor CS dan jawaban FAQ berubah tanpa ada kode yang ikut berubah. Menaruhnya
-- sebagai konstanta berarti setiap salah ketik satu huruf menunggu build,
-- review, dan deploy. Alasan yang sama membuat katalog kartu hidup di migrasi
-- 000022: data yang bisa berubah sendiri tidak boleh menumpang siklus rilis.
--
-- Isinya ikut ke SETIAP environment yang dimigrasi. Menaruhnya di seeder akan
-- mengulang kekeliruan yang sama seperti katalog kartu dulu: seeder menolak
-- jalan di luar APP_ENV=development, jadi staging akan menjawab daftar kosong.
--
-- CATATAN: nomor di bawah adalah kontak publik Halo BCA. Proyek ini portofolio;
-- kalau layanan ini pernah dipakai sungguhan, verifikasi ulang nilainya sebelum
-- dipajang ke nasabah.

-- ============================================================
-- CONTENT_HELP_CENTER — satu baris per pertanyaan
-- ============================================================
--
-- Tabel datar, bukan kategori + item di dua tabel. Isinya dibaca sekali,
-- seluruhnya, lalu dikelompokkan di aplikasi; join untuk data sebesar ini
-- hanya menambah bagian yang bisa rusak.
CREATE TABLE content_help_center (
    id             BIGSERIAL PRIMARY KEY,

    -- category_key dipetakan client ke ikon dan judulnya sendiri bila perlu.
    -- category_title tetap dikirim supaya client lama tidak menampilkan kunci
    -- mentah saat ada kategori baru yang belum dikenalnya.
    category_key   TEXT NOT NULL,
    category_title TEXT NOT NULL,
    category_order INT  NOT NULL DEFAULT 1,

    question       TEXT NOT NULL,
    answer         TEXT NOT NULL,
    item_order     INT  NOT NULL DEFAULT 1,

    -- Menonaktifkan pertanyaan dengan is_active=FALSE, bukan DELETE: pertanyaan
    -- yang ditarik sementara sering kembali, dan menghapusnya menghilangkan
    -- jawabannya juga.
    is_active      BOOLEAN NOT NULL DEFAULT TRUE,

    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT content_help_unique_question UNIQUE (category_key, question)
);

CREATE INDEX idx_content_help_active
    ON content_help_center (category_order, item_order)
    WHERE is_active;

COMMENT ON TABLE content_help_center IS
    'FAQ Pusat Bantuan. Dilayani GET /v1/content/help-center, tanpa Authorization.';

-- ============================================================
-- CONTENT_CONTACT_CS — satu baris, selamanya
-- ============================================================
--
-- CHECK (id = 1) memaksa tabel ini hanya punya satu baris. Tanpa itu, baris
-- kedua yang tidak sengaja masuk membuat "nomor CS mana yang benar" jadi
-- pertanyaan yang harus dijawab kode. Pola yang sama dipakai
-- card_catalog_version.
CREATE TABLE content_contact_cs (
    id         SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),

    phone      TEXT NOT NULL,
    phone_free TEXT NOT NULL,
    whatsapp   TEXT NOT NULL,
    email      TEXT NOT NULL,
    chat_url   TEXT NOT NULL,
    hours      TEXT NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE content_contact_cs IS
    'Kontak Halo BCA. Dilayani GET /v1/content/contact-cs, tanpa Authorization: '
    'nasabah yang terkunci di luar aplikasi justru yang paling butuh nomor ini.';

-- ============================================================
-- ISI
-- ============================================================

INSERT INTO content_contact_cs (id, phone, phone_free, whatsapp, email, chat_url, hours)
VALUES (
    1,
    '1500888',
    '+62 21 23588000',
    '+62 811 1500 998',
    'halobca@bca.co.id',
    'https://www.bca.co.id/halobca',
    '24 jam setiap hari'
)
ON CONFLICT (id) DO UPDATE SET
    phone      = EXCLUDED.phone,
    phone_free = EXCLUDED.phone_free,
    whatsapp   = EXCLUDED.whatsapp,
    email      = EXCLUDED.email,
    chat_url   = EXCLUDED.chat_url,
    hours      = EXCLUDED.hours,
    updated_at = NOW();

INSERT INTO content_help_center
    (category_key, category_title, category_order, question, answer, item_order)
VALUES
    -- Kartu: kategori pertama karena skrip ini lahir bersama layar kartu.
    ('CARD', 'Kartu Paspor', 1,
     'Bagaimana cara memblokir kartu yang hilang?',
     'Buka Profil Saya, pilih kartu yang hilang, lalu tekan Blokir Kartu. Anda akan diminta memasukkan PIN. Kartu langsung tidak bisa dipakai bertransaksi. Pemblokiran tidak bisa dibatalkan dari aplikasi — untuk membukanya kembali, hubungi Halo BCA atau datang ke cabang.',
     1),
    ('CARD', 'Kartu Paspor', 1,
     'Berapa lama kartu pengganti sampai?',
     'Untuk kartu Blue dan Gold, perkiraannya 3 sampai 7 hari kerja. Kartu Platinum dicetak khusus sehingga membutuhkan 5 sampai 10 hari kerja. Anda juga bisa memilih mengambil sendiri di cabang.',
     2),
    ('CARD', 'Kartu Paspor', 1,
     'Apa beda transaksi debit online dan transaksi luar negeri?',
     'Debit online mengizinkan kartu dipakai di toko daring dalam negeri. Transaksi luar negeri mengizinkan kartu dipakai di merchant dan ATM di luar Indonesia. Keduanya bisa dinyalakan dan dimatikan sendiri kapan saja dari halaman kartu, tanpa biaya.',
     3),
    ('CARD', 'Kartu Paspor', 1,
     'Berapa biaya penggantian kartu?',
     'Kartu Blue dan Gold dikenai Rp25.000, kartu Platinum Rp50.000. Biaya didebet dari rekening yang terhubung dengan kartu saat permintaan disetujui. Penerbitan kartu pertama saat buka rekening tidak dikenai biaya.',
     4),

    ('TRANSACTION', 'Transaksi & Limit', 2,
     'Kenapa transfer saya ditolak padahal saldo cukup?',
     'Saldo yang dipakai bertransaksi adalah saldo tersedia, yaitu saldo dikurangi dana tertahan. Selain itu setiap jenis transaksi punya limit harian sendiri. Periksa Atur Limit di Profil Saya untuk melihat sisa limit hari ini.',
     1),
    ('TRANSACTION', 'Transaksi & Limit', 2,
     'Bagaimana cara mengubah limit transaksi harian?',
     'Buka Profil Saya lalu Atur Limit. Pilih jenis transaksi, masukkan limit baru, dan konfirmasi dengan PIN. Limit baru berlaku seketika, dan tidak bisa melebihi plafon maksimum yang ditetapkan bank.',
     2),
    ('TRANSACTION', 'Transaksi & Limit', 2,
     'Kapan batas limit harian saya kembali penuh?',
     'Limit harian dihitung per tanggal Waktu Indonesia Barat dan kembali penuh setiap tengah malam WIB.',
     3),

    ('SECURITY', 'Keamanan & PIN', 3,
     'Apa beda kode akses dan PIN?',
     'Kode akses dipakai untuk masuk ke aplikasi. PIN dipakai untuk menyetujui transaksi seperti transfer dan pembayaran. Keduanya berbeda dan sebaiknya tidak sama.',
     1),
    ('SECURITY', 'Keamanan & PIN', 3,
     'Kartu atau akun saya terblokir setelah salah PIN. Apa yang harus dilakukan?',
     'Demi keamanan, akun dikunci sementara setelah beberapa kali PIN salah. Tunggu sampai masa kunci berakhir, lalu coba lagi. Bila Anda lupa PIN, hubungi Halo BCA di 1500888 untuk mengatur ulang.',
     2),
    ('SECURITY', 'Keamanan & PIN', 3,
     'Apakah aman menyalakan login dengan sidik jari atau wajah?',
     'Aman. Data biometrik tidak pernah dikirim ke bank — yang tersimpan di ponsel hanyalah kunci yang dilindungi perangkat keras, dan bank hanya memeriksa tanda tangannya. Mendaftarkan sidik jari baru di ponsel akan menghapus pendaftaran biometrik lama secara otomatis.',
     3),

    ('ACCOUNT', 'Rekening & Profil', 4,
     'Bagaimana cara mengubah nomor ponsel atau email?',
     'Buka Profil Saya lalu pilih data yang ingin diubah. Perubahan email dan nomor ponsel memerlukan kode OTP, dan sesi di perangkat lain akan diputus demi keamanan.',
     1),
    ('ACCOUNT', 'Rekening & Profil', 4,
     'Apa arti badge Prioritas di halaman profil saya?',
     'Badge menandakan tingkat layanan nasabah. Nasabah Prioritas dan Solitaire mendapat layanan khusus di cabang dan jalur Halo BCA tersendiri. Nasabah reguler tidak menampilkan badge apa pun.',
     2),
    ('ACCOUNT', 'Rekening & Profil', 4,
     'Bagaimana cara melihat mutasi lebih dari 7 hari?',
     'Di layar Mutasi, ubah filter periode. Tersedia 7 hari, 30 hari, 90 hari, bulan ini, bulan lalu, dan rentang tanggal pilihan Anda sendiri.',
     3)
ON CONFLICT (category_key, question) DO UPDATE SET
    category_title = EXCLUDED.category_title,
    category_order = EXCLUDED.category_order,
    answer         = EXCLUDED.answer,
    item_order     = EXCLUDED.item_order,
    is_active      = TRUE,
    updated_at     = NOW();
