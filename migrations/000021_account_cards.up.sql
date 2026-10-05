-- 000021_account_cards.up.sql
--
-- Kartu yang DIMILIKI nasabah setelah rekening aktif. Sampai migrasi ini tidak
-- ada tempat menyimpannya sama sekali, dan itulah sebab tujuh bagian di
-- AkunScreen.kt menampilkan penanda kosong.
--
-- Dua tabel kartu yang sudah ada TIDAK bisa dipakai untuk ini:
--
--   * card_products (000019) adalah KATALOG — satu baris per jenis kartu,
--     bukan per kartu milik seseorang.
--   * onboarding_card_issuance (000020) berkunci session_id NOT NULL UNIQUE,
--     jadi nasabah lama yang tidak punya sesi onboarding tidak bisa punya baris
--     di sana, dan satu orang tidak bisa punya dua kartu.
--
-- Kontrak endpoint: skill profil-saya-kartu-api §2, docs/01-API-SPECIFICATION.md.

-- ============================================================
-- USERS.TIER — badge "Prioritas" di kartu profil
-- ============================================================
--
-- Satu kolom, bukan endpoint GET /account/tier: layar sudah memanggil
-- GET /account/profile, dan menambah panggilan kedua untuk satu kata adalah
-- perjalanan jaringan yang tidak perlu.
--
-- DEFAULT 'REGULER' membuat seluruh baris users yang sudah ada tetap sah tanpa
-- backfill. Client menyembunyikan badge untuk REGULER.
ALTER TABLE users
    ADD COLUMN tier TEXT NOT NULL DEFAULT 'REGULER'
               CHECK (tier IN ('REGULER', 'PRIORITAS', 'SOLITAIRE'));

COMMENT ON COLUMN users.tier IS
    'Tier layanan nasabah. Menentukan badge di Profil Saya. Nilai visual (warna, '
    'ikon) TIDAK disimpan di sini — client memetakan tier ke design token.';

-- ============================================================
-- ACCOUNT_CARDS — satu baris per kartu fisik milik nasabah
-- ============================================================
CREATE TABLE account_cards (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id),

    -- Kartu menempel pada satu rekening, bukan pada nasabah saja: satu orang
    -- bisa punya beberapa rekening dan kartu yang berbeda untuk masing-masing.
    account_id      UUID NOT NULL REFERENCES accounts(id),

    -- Jenis kartu diambil dari katalog yang sudah ada, bukan enum baru. Dengan
    -- begini nama produk, network, style, dan fee_card_replacement punya satu
    -- sumber kebenaran — tinggal di-join saat menyusun response.
    card_type       TEXT NOT NULL REFERENCES card_products(card_type),

    -- Hanya bentuk tersamar. PAN lengkap milik core banking, bukan milik
    -- layanan ini (docs/04-SECURITY.md, dan aturan yang sama sudah ditulis di
    -- migrasi 000020).
    masked_number   TEXT NOT NULL,

    cardholder_name TEXT NOT NULL,

    -- Disimpan terpisah, bukan sebagai string "MM/YY": bulan/tahun adalah dua
    -- angka yang perlu dibandingkan saat menentukan kartu kedaluwarsa.
    -- Pemformatan ke "12/28" dikerjakan handler.
    valid_thru_month SMALLINT NOT NULL CHECK (valid_thru_month BETWEEN 1 AND 12),
    valid_thru_year  SMALLINT NOT NULL CHECK (valid_thru_year BETWEEN 2000 AND 2100),

    status          TEXT NOT NULL DEFAULT 'ACTIVE'
                    CHECK (status IN ('ACTIVE', 'BLOCKED', 'EXPIRED', 'REPLACEMENT_PENDING')),

    blocked_reason  TEXT
                    CHECK (blocked_reason IS NULL OR blocked_reason IN (
                        'LOST', 'STOLEN', 'DAMAGED', 'SUSPECTED_FRAUD'
                    )),
    blocked_at      TIMESTAMPTZ,

    -- Dua sakelar kanal di layar. Default mengikuti perilaku kartu Paspor
    -- sungguhan: transaksi debit online hidup, transaksi luar negeri mati
    -- sampai nasabah menyalakannya sendiri.
    debit_online_enabled  BOOLEAN NOT NULL DEFAULT TRUE,
    international_enabled BOOLEAN NOT NULL DEFAULT FALSE,

    is_primary      BOOLEAN NOT NULL DEFAULT FALSE,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Penjaga skema terhadap PAN yang lolos karena satu jalur kode lupa
    -- menyamarkan. Nomor kartu utuh punya 16 angka berurutan; bentuk tersamar
    -- paling banyak 4. Tujuh dipilih sebagai batas supaya tetap longgar untuk
    -- format tersamar apa pun, tapi tidak mungkin memuat PAN.
    CONSTRAINT account_card_number_masked
        CHECK (masked_number !~ '[0-9]{7}'),

    -- Dan harus benar-benar ada penanda samarnya, supaya "5678" saja tidak lolos.
    CONSTRAINT account_card_number_has_mask
        CHECK (masked_number ~ '[•*xX]'),

    -- Status BLOCKED tanpa alasan dan waktu membuat audit tidak bisa menjawab
    -- "kenapa kartu ini mati". Pola yang sama dipakai card_option_reason_required
    -- di migrasi 000019.
    CONSTRAINT account_card_block_documented
        CHECK (status <> 'BLOCKED' OR (blocked_reason IS NOT NULL AND blocked_at IS NOT NULL))
);

-- Jalur baca satu-satunya di layar: semua kartu milik satu nasabah.
CREATE INDEX idx_account_cards_user
    ON account_cards (user_id, created_at DESC);

CREATE INDEX idx_account_cards_account
    ON account_cards (account_id);

-- Satu kartu utama per nasabah. Menyamai idx_accounts_primary (migrasi 000002),
-- tapi TIDAK disaring status: kartu utama yang diblokir tetap kartu utama
-- sampai penggantinya terbit — kalau disaring, nasabah yang memblokir kartunya
-- akan bisa punya dua kartu utama.
CREATE UNIQUE INDEX idx_account_cards_primary
    ON account_cards (user_id)
    WHERE is_primary;

COMMENT ON TABLE account_cards IS
    'Kartu milik nasabah pasca-onboarding. HANYA masked_number — PAN lengkap tidak '
    'pernah disimpan di sini maupun dicatat ke log. Nilai visual diwakili '
    'card_products.style, bukan hex warna.';

-- ============================================================
-- CARD_REPLACEMENT_REQUESTS — permintaan ganti kartu
-- ============================================================
--
-- Mencerminkan onboarding_card_issuance (000020) termasuk antrean retry-nya,
-- tapi berkunci card_id + idempotency key, bukan session_id: yang minta ganti
-- kartu adalah nasabah yang rekeningnya sudah aktif dan tidak punya sesi.
CREATE TABLE card_replacement_requests (
    -- Dikirim ke client sebagai request_id, jadi UUID bukan BIGSERIAL: id
    -- berurutan membocorkan berapa banyak permintaan yang masuk hari itu.
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),

    card_id         UUID NOT NULL REFERENCES account_cards(id),
    user_id         UUID NOT NULL REFERENCES users(id),

    -- X-Idempotency-Key dari client. Redis menjaga di lapisan cepat; kunci unik
    -- di bawah menjaga di lapisan yang tidak bisa hilang karena eviction —
    -- alasan yang sama dengan UNIQUE session_id di migrasi 000020.
    idempotency_key TEXT NOT NULL,

    reason          TEXT NOT NULL
                    CHECK (reason IN ('DAMAGED', 'LOST', 'UPGRADE')),

    -- Enum yang sama dengan onboarding_card_issuance, bukan daftar baru.
    delivery_method TEXT NOT NULL DEFAULT 'COURIER'
                    CHECK (delivery_method IN ('COURIER', 'BRANCH_PICKUP')),

    -- Disalin dari card_products.fee_card_replacement saat permintaan dibuat,
    -- bukan di-join saat dibaca: yang perlu dibuktikan saat sengketa adalah
    -- biaya YANG DILIHAT NASABAH waktu itu, bukan tarif hari ini. Alasan yang
    -- sama dengan monthly_admin_fee_shown di migrasi 000019.
    fee             BIGINT NOT NULL CHECK (fee >= 0),

    status          TEXT NOT NULL DEFAULT 'REQUESTED'
                    CHECK (status IN ('REQUESTED', 'PRINTING', 'SHIPPED', 'FAILED', 'CANCELLED')),

    core_banking_code TEXT,
    masked_number     TEXT,

    estimated_arrival_from DATE,
    estimated_arrival_to   DATE,
    tracking_number        TEXT,

    -- Antrean retry, persis pola 000020: next_retry_at NULL berarti tidak ada
    -- yang perlu diulang.
    attempts        INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error      TEXT,
    next_retry_at   TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT card_replacement_window_sane
        CHECK (estimated_arrival_from IS NULL
               OR estimated_arrival_to IS NULL
               OR estimated_arrival_from <= estimated_arrival_to),

    -- Sama seperti account_cards: kalau nomor tersamar sudah terbit, jangan
    -- sampai yang tersimpan justru PAN.
    CONSTRAINT card_replacement_number_masked
        CHECK (masked_number IS NULL OR masked_number !~ '[0-9]{7}')
);

-- Idempotency di-scope per nasabah, bukan global: kunci dipilih client, dan
-- dua nasabah yang kebetulan memakai kunci sama tidak boleh saling menolak.
CREATE UNIQUE INDEX idx_card_replacement_idem
    ON card_replacement_requests (user_id, idempotency_key);

-- Satu permintaan terbuka per kartu. Penjaga kedua setelah X-Idempotency-Key:
-- retry dengan kunci BARU (mis. aplikasi dipasang ulang) tetap tidak boleh
-- menghasilkan dua kartu berbayar. Status akhir tidak ikut dikunci supaya
-- permintaan yang FAILED bisa diajukan ulang.
CREATE UNIQUE INDEX idx_card_replacement_open
    ON card_replacement_requests (card_id)
    WHERE status IN ('REQUESTED', 'PRINTING');

-- Partial index untuk pekerja retry, bukan index penuh: baris yang sudah
-- selesai akan jauh lebih banyak dan tidak satu pun perlu dipindai.
CREATE INDEX idx_card_replacement_retry
    ON card_replacement_requests (next_retry_at)
    WHERE next_retry_at IS NOT NULL;

CREATE INDEX idx_card_replacement_user
    ON card_replacement_requests (user_id, created_at DESC);

COMMENT ON TABLE card_replacement_requests IS
    'Permintaan penggantian kartu. Berbiaya (card_products.fee_card_replacement), jadi '
    'wajib idempoten: UNIQUE (user_id, idempotency_key) plus satu permintaan terbuka '
    'per kartu.';

-- ============================================================
-- ACCOUNT_CARD_AUDIT_LOG — jejak perubahan kartu nasabah
-- ============================================================
--
-- onboarding_audit_log TIDAK bisa dipakai: session_id di sana NOT NULL
-- (migrasi 000010) sementara nasabah yang mengubah kartunya tidak punya sesi.
-- Masalah yang sama diselesaikan migrasi 000020 untuk tulisan admin ke katalog;
-- ini mengikuti caranya.
CREATE TABLE account_card_audit_log (
    id              BIGSERIAL PRIMARY KEY,

    -- FK ke kartu memang menghalangi DELETE baris kartu. Itu disengaja: jejak
    -- audit tidak boleh bisa dihapus dengan cara menghapus kartunya. Kartu
    -- yang berakhir masuk status EXPIRED, tidak dihapus.
    card_id         UUID NOT NULL REFERENCES account_cards(id),
    user_id         UUID NOT NULL REFERENCES users(id),

    -- Siapa. 'CUSTOMER' untuk aksi dari aplikasi; teks bebas supaya aktor
    -- lain (petugas cabang, job sistem) bisa dicatat tanpa migrasi baru.
    actor           TEXT NOT NULL DEFAULT 'CUSTOMER',

    action          TEXT NOT NULL
                    CHECK (action IN (
                        'CARD_SETTINGS_UPDATED',
                        'CARD_BLOCKED',
                        'CARD_REPLACEMENT_REQUESTED'
                    )),

    -- JSONB, bukan kolom per field: bentuk baris berbeda per action (sakelar
    -- vs alasan blokir vs pengiriman) dan semuanya harus muat tanpa menambah
    -- kolom setiap kali ada aksi kartu baru.
    old_value       JSONB,
    new_value       JSONB NOT NULL,

    ip_address      INET,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_account_card_audit_card
    ON account_card_audit_log (card_id, created_at DESC);

CREATE INDEX idx_account_card_audit_user
    ON account_card_audit_log (user_id, created_at DESC);

CREATE INDEX idx_account_card_audit_created
    ON account_card_audit_log (created_at DESC);

COMMENT ON TABLE account_card_audit_log IS
    'Jejak perubahan kartu nasabah: siapa, kapan, nilai lama, nilai baru. Append-only; '
    'jangan pasang job pembersihan sebelum retensi ditetapkan compliance.';
