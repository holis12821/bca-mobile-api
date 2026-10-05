-- 000025_onboarding_tnc.up.sql
--
-- Isi layar Syarat & Ketentuan pada flow buka rekening, dan — yang lebih
-- penting — catatan resmi tentang APA yang disetujui nasabah pada versi
-- tertentu.
--
-- APA YANG RUSAK SEBELUM MIGRASI INI
--
-- `onboarding_sessions.tnc_version` sudah ada sejak migrasi 000010, dan
-- CreateSession menulisnya apa adanya dari body request. Tapi tidak ada satu
-- baris pun di database yang menjelaskan isi versi itu: teksnya hidup sebagai
-- lima belas entri `strings.xml` di dalam APK Android, dan nomor versinya
-- sebagai konstanta Kotlin `TNC_VERSION = "2026-09-01"`.
--
-- Akibatnya bank menyimpan pernyataan "nasabah menyetujui 2026-09-01" tanpa
-- bisa membuktikan apa yang tertulis di sana. Begitu teksnya diperbarui di
-- rilis berikutnya, bukti itu hilang sama sekali — tidak ada arsipnya, dan
-- APK lama tidak bisa dimintai keterangan. Untuk persetujuan yang dasarnya
-- POJK No. 12/POJK.01/2017, itu bukan kekurangan kosmetik.
--
-- KENAPA DI DATABASE, BUKAN KONSTANTA DI GO
--
-- Alasan yang sama seperti katalog kartu (000022) dan Pusat Bantuan (000023):
-- teks hukum berubah tanpa ada kode yang ikut berubah, dan data yang bisa
-- berubah sendiri tidak boleh menumpang siklus rilis. Bedanya di sini versi
-- lama WAJIB tetap tersimpan, bukan boleh ditimpa — baris sesi yang menunjuk
-- ke versi lama harus selalu bisa dibaca kembali.
--
-- Isinya ikut ke SETIAP environment yang dimigrasi, bukan ke seeder: seeder
-- menolak jalan di luar APP_ENV=development, jadi staging akan menjawab layar
-- S&K kosong dan setiap pembuatan sesi di sana akan ditolak.
--
-- CATATAN: teks di bawah disalin apa adanya dari
-- app/src/main/res/values/strings.xml milik aplikasi Android (buka_rekening_sk_*)
-- supaya nasabah tidak melihat perubahan kata satu pun saat layarnya pindah ke
-- API. Proyek ini portofolio; kalau pernah dipakai sungguhan, teks hukumnya
-- wajib ditinjau ulang sebelum dipajang.

-- ============================================================
-- ONBOARDING_TNC_DOCUMENTS — satu baris per versi S&K
-- ============================================================
CREATE TABLE onboarding_tnc_documents (
    id      BIGSERIAL PRIMARY KEY,

    -- version adalah nilai yang dikirim client sebagai `accepted_tnc_version`
    -- dan yang tersimpan di onboarding_sessions.tnc_version.
    --
    -- Panjangnya dibatasi 20 karena kolom di sisi sesi itu VARCHAR(20).
    -- Tanpa CHECK ini, versi ke-21 karakter lolos di sini lalu MENGGAGALKAN
    -- setiap pembuatan sesi yang menyebutnya — kegagalan yang muncul jauh dari
    -- penyebabnya.
    version TEXT NOT NULL UNIQUE CHECK (length(version) BETWEEN 1 AND 20),

    -- Kepala halaman.
    heading        TEXT NOT NULL,
    subtitle       TEXT NOT NULL,

    -- Banner kepercayaan di atas isi S&K (logo OJK dsb).
    trust_title    TEXT NOT NULL,
    trust_subtitle TEXT NOT NULL,

    -- Kotak PENTING di bawah isi S&K. Bukan bagian teks hukum, tapi tetap di
    -- sini: isinya ikut berubah tiap kali langkah berikutnya berubah.
    notice_label   TEXT NOT NULL,
    notice_body    TEXT NOT NULL,

    -- Tiga potongan kalimat di samping checkbox. Dipecah tiga, bukan satu
    -- kalimat utuh, karena bagian tengahnya dicetak tebal dan berwarna oleh
    -- aplikasi. Menyatukannya berarti client harus mencari substring untuk
    -- tahu bagian mana yang ditebalkan — dan substring itu pecah pada setiap
    -- perbaikan kata.
    consent_prefix TEXT NOT NULL,
    consent_link   TEXT NOT NULL,
    consent_suffix TEXT NOT NULL DEFAULT '.',

    -- Label tombol persetujuan. Ikut dilayani supaya mengubah "Setuju &
    -- Lanjutkan" tidak menuntut rilis aplikasi.
    agree_cta      TEXT NOT NULL,

    -- is_active menandai versi yang WAJIB dipakai sesi baru. Versi yang sudah
    -- dicabut tidak dihapus: baris sesi lama menunjuk ke sana, dan menghapusnya
    -- berarti menghapus bukti persetujuannya.
    is_active      BOOLEAN NOT NULL DEFAULT FALSE,

    effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Hanya SATU versi boleh aktif pada satu waktu.
--
-- Indeks unik pada ekspresi konstanta adalah cara Postgres menyatakan "paling
-- banyak satu baris yang memenuhi WHERE". Tanpa ini, dua versi aktif membuat
-- "versi S&K yang benar hari ini" jadi pertanyaan yang harus dijawab ORDER BY
-- — dan jawaban yang bergantung pada urutan baris adalah jawaban yang bisa
-- berubah sendiri. Pola yang sama dengan CHECK (id = 1) di content_contact_cs,
-- hanya saja di sini barisnya berganti, bukan tetap.
CREATE UNIQUE INDEX idx_onboarding_tnc_single_active
    ON onboarding_tnc_documents ((TRUE))
    WHERE is_active;

COMMENT ON TABLE onboarding_tnc_documents IS
    'Versi Syarat & Ketentuan buka rekening. Dilayani GET /v1/onboarding/tnc '
    'tanpa Authorization (layar S&K tampil sebelum sesi lahir). Baris lama '
    'TIDAK PERNAH dihapus: onboarding_sessions.tnc_version menunjuk ke sini.';

-- ============================================================
-- ONBOARDING_TNC_SECTIONS — pasal-pasal, berurut
-- ============================================================
--
-- Tabel terpisah, bukan kolom JSONB atau lima kolom berpola section_N_*.
-- Jumlah pasalnya berubah tiap revisi teks hukum, dan menambah pasal keenam
-- tidak boleh berarti menambah kolom.
CREATE TABLE onboarding_tnc_sections (
    id          BIGSERIAL PRIMARY KEY,

    -- ON DELETE CASCADE: satu-satunya penghapusan yang sah di sini adalah
    -- `migrate down`, dan membiarkan pasal tanpa induk hanya menyisakan sampah.
    document_id BIGINT NOT NULL
        REFERENCES onboarding_tnc_documents (id) ON DELETE CASCADE,

    section_order INT NOT NULL,

    -- icon_key dipetakan client ke drawable-nya sendiri. Bukan nama file dan
    -- bukan URL: aplikasi Android memuat ikonnya dari APK, jadi mengirim path
    -- hanya menciptakan tautan yang bisa putus. Client yang tidak mengenal
    -- kunci baru menampilkan ikon bawaan — teksnya tetap terbaca.
    icon_key    TEXT NOT NULL,

    title       TEXT NOT NULL,
    body        TEXT NOT NULL,

    CONSTRAINT onboarding_tnc_sections_order_unique
        UNIQUE (document_id, section_order)
);

CREATE INDEX idx_onboarding_tnc_sections_doc
    ON onboarding_tnc_sections (document_id, section_order);

COMMENT ON TABLE onboarding_tnc_sections IS
    'Pasal-pasal satu versi S&K, berurut. Dibaca bersama induknya dalam satu '
    'permintaan GET /v1/onboarding/tnc.';

-- ============================================================
-- ISI — versi 2026-09-01
-- ============================================================
--
-- Nomor versinya sengaja sama dengan konstanta TNC_VERSION di aplikasi Android
-- yang sudah beredar. Memberi nomor baru di sini akan membuat setiap
-- pembukaan rekening dari APK lama ditolak TNC_VERSION_OUTDATED pada hari
-- migrasi ini jalan.

INSERT INTO onboarding_tnc_documents (
    version, heading, subtitle,
    trust_title, trust_subtitle,
    notice_label, notice_body,
    consent_prefix, consent_link, consent_suffix,
    agree_cta, is_active, effective_from
) VALUES (
    '2026-09-01',
    'Syarat & Ketentuan Pembukaan Rekening',
    'Mohon baca dan pahami syarat dan ketentuan pembukaan rekening digital BCA sebelum melanjutkan.',
    'Persetujuan Resmi Nasabah',
    'Terdaftar dan diawasi oleh Otoritas Jasa Keuangan (OJK)',
    'PENTING',
    'Pastikan Anda berada di tempat tenang dan pencahayaan cukup untuk verifikasi video call pada tahap berikutnya.',
    'Saya telah membaca, memahami, dan menyetujui seluruh ',
    'Syarat & Ketentuan Pembukaan Rekening BCA',
    '.',
    'Setuju & Lanjutkan',
    TRUE,
    '2026-09-01 00:00:00+07'
)
ON CONFLICT (version) DO UPDATE SET
    heading        = EXCLUDED.heading,
    subtitle       = EXCLUDED.subtitle,
    trust_title    = EXCLUDED.trust_title,
    trust_subtitle = EXCLUDED.trust_subtitle,
    notice_label   = EXCLUDED.notice_label,
    notice_body    = EXCLUDED.notice_body,
    consent_prefix = EXCLUDED.consent_prefix,
    consent_link   = EXCLUDED.consent_link,
    consent_suffix = EXCLUDED.consent_suffix,
    agree_cta      = EXCLUDED.agree_cta,
    updated_at     = NOW();

-- Pasal-pasalnya. document_id dicari lewat version, bukan ditulis 1: migrasi
-- yang mengandaikan nilai BIGSERIAL tertentu pecah begitu ada yang menjalankan
-- down lalu up kembali.
INSERT INTO onboarding_tnc_sections (document_id, section_order, icon_key, title, body)
SELECT d.id, v.section_order, v.icon_key, v.title, v.body
FROM onboarding_tnc_documents d
CROSS JOIN (VALUES
    (1, 'ACCOUNT_BOX',
     '1. Ketentuan Umum Pembukaan Rekening Digital',
     'Calon nasabah merupakan Warga Negara Indonesia (WNI) dengan usia minimal 17 tahun dan memiliki e-KTP fisik asli yang masih berlaku serta belum pernah terdaftar pada nomor CIF rekening tabungan BCA sejenis sebelumnya. Setiap data yang dimasukkan harus sesuai identitas kependudukan resmi Republik Indonesia.'),

    (2, 'VERIFIED_USER',
     '2. Kebijakan Privasi & Penggunaan Data',
     'Data pribadi Anda dilindungi kerahasiaannya dan hanya digunakan untuk kepentingan verifikasi identitas, kepatuhan Anti Pencucian Uang dan Pencegahan Pendanaan Terorisme (APU-PPT), serta pelaporan sesuai ketentuan OJK POJK No. 12/POJK.01/2017 dan regulasi perbankan yang berlaku.'),

    (3, 'VIDEO_CALL',
     '3. Ketentuan eKYC & Video Call',
     'Verifikasi tatap muka digital wajib dilakukan secara langsung dengan petugas Halo BCA. Calon nasabah wajib memperlihatkan e-KTP asli secara jelas dan menjawab pertanyaan verifikasi langsung tanpa diwakilkan oleh pihak mana pun demi jaminan keamanan rekening.'),

    (4, 'SAVINGS',
     '4. Komitmen Setoran Awal',
     'Setoran awal minimal sesuai jenis rekening yang dipilih (Tahapan BCA, Tahapan Xpresi, atau TabunganKu) wajib disetorkan dalam waktu maksimal 30 hari kalender sejak rekening dinyatakan aktif untuk menghindari penutupan otomatis oleh sistem perbankan.'),

    (5, 'LOCK',
     '5. Penggunaan Fasilitas m-BCA',
     'Nasabah bertanggung jawab penuh menjaga kerahasiaan Kode Akses, PIN transaksi, dan kode OTP. Bank BCA tidak pernah meminta kode OTP atau PIN Anda melalui telepon, SMS, WhatsApp, maupun media sosial resmi lainnya.')
) AS v (section_order, icon_key, title, body)
WHERE d.version = '2026-09-01'
ON CONFLICT (document_id, section_order) DO UPDATE SET
    icon_key = EXCLUDED.icon_key,
    title    = EXCLUDED.title,
    body     = EXCLUDED.body;
