-- 000020_card_issuance_and_catalog_audit.up.sql
--
-- Dua hal yang dituntut docs/08-PILIH-KARTU-API-SPEC.md tapi belum punya tempat
-- menyimpan apa pun:
--
--  1. §10: permintaan cetak kartu ke core banking. Penerbitan yang gagal tidak
--     boleh menggagalkan submit — rekening tetap ACTIVE dan kegagalannya masuk
--     antrean retry. Antrean itu harus tahan restart, jadi Postgres, bukan
--     memori proses.
--
--  2. §13: "Semua perubahan konfigurasi wajib masuk audit trail: siapa, kapan,
--     nilai lama, nilai baru." onboarding_audit_log tidak bisa dipakai —
--     session_id di sana NOT NULL (migrasi 000010) sementara tulisan admin ke
--     katalog tidak punya sesi sama sekali.

-- ============================================================
-- ONBOARDING_CARD_ISSUANCE — satu permintaan cetak per sesi
-- ============================================================
CREATE TABLE onboarding_card_issuance (
    id              BIGSERIAL PRIMARY KEY,

    -- UNIQUE inilah yang membuat submit ulang tidak menghasilkan dua permintaan
    -- cetak (§10). Idempotency-Key menjaga di lapisan Redis; kunci ini menjaga
    -- di lapisan yang tidak bisa hilang karena eviction.
    session_id      VARCHAR(32) NOT NULL UNIQUE,

    account_number  VARCHAR(20) NOT NULL,
    card_type       TEXT        NOT NULL REFERENCES card_products(card_type),

    -- Kode kartu milik core banking, yang belum tentu sama dengan enum kita.
    -- Disalin saat permintaan dibuat: pemetaan di konfigurasi bisa berubah,
    -- dan yang perlu dilacak adalah kode yang BENAR-BENAR dikirim.
    core_banking_code TEXT      NOT NULL,

    status          TEXT        NOT NULL DEFAULT 'REQUESTED'
                    CHECK (status IN ('REQUESTED', 'PRINTING', 'SHIPPED', 'FAILED')),

    -- Nomor kartu tidak pernah disimpan utuh. Hanya bentuk tersamar yang
    -- ditampilkan ke nasabah; PAN lengkap milik core banking, bukan milik
    -- layanan ini (docs/04-SECURITY.md).
    masked_number   TEXT,

    delivery_method TEXT        NOT NULL DEFAULT 'COURIER'
                    CHECK (delivery_method IN ('COURIER', 'BRANCH_PICKUP')),
    estimated_arrival_from DATE,
    estimated_arrival_to   DATE,
    tracking_number TEXT,

    -- Antrean retry. next_retry_at NULL berarti tidak ada yang perlu diulang:
    -- permintaan sudah berhasil, atau menyerah setelah batas percobaan.
    attempts        INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error      TEXT,
    next_retry_at   TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT card_issuance_window_sane
        CHECK (estimated_arrival_from IS NULL
               OR estimated_arrival_to IS NULL
               OR estimated_arrival_from <= estimated_arrival_to)
);

-- Jalur baca pekerja retry: hanya baris yang memang menunggu giliran.
-- Partial index, bukan index penuh: baris yang sudah selesai jumlahnya akan
-- jauh lebih banyak daripada yang gagal, dan tidak satu pun perlu dipindai.
CREATE INDEX idx_card_issuance_retry
    ON onboarding_card_issuance (next_retry_at)
    WHERE next_retry_at IS NOT NULL;

COMMENT ON TABLE onboarding_card_issuance IS
    'Permintaan cetak kartu ke core banking. Satu baris per sesi (UNIQUE session_id) '
    'supaya submit ulang tidak mencetak dua kartu. Lihat docs/08-PILIH-KARTU-API-SPEC.md §10.';

-- ============================================================
-- CARD_CATALOG_AUDIT_LOG — jejak tulisan admin ke katalog
-- ============================================================
CREATE TABLE card_catalog_audit_log (
    id              BIGSERIAL PRIMARY KEY,

    -- Siapa. Model otorisasi di atas X-Internal-API-Key belum diputuskan
    -- (§17 butir 7), jadi aktor disimpan sebagai teks bebas yang diisi
    -- pemanggil — bukan foreign key ke tabel yang belum ada.
    actor           TEXT        NOT NULL,

    action          TEXT        NOT NULL
                    CHECK (action IN ('CARD_UPDATED', 'PRODUCT_CARD_UPDATED')),

    -- Apa yang disentuh. product_type NULL untuk tulisan ke card_products,
    -- yang memang berlaku lintas produk.
    card_type       TEXT        NOT NULL,
    product_type    TEXT,

    -- Nilai lama dan baru, apa adanya. JSONB supaya bentuk baris yang berbeda
    -- (fee/limit vs urutan/badge) tetap muat tanpa menambah kolom tiap kali
    -- katalog bertambah field.
    old_value       JSONB,
    new_value       JSONB       NOT NULL,

    -- Versi katalog SESUDAH tulisan ini. Menghubungkan perubahan dengan
    -- katalog persis yang dilihat nasabah setelahnya.
    catalog_version TEXT        NOT NULL,

    ip_address      INET,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_card_catalog_audit_created
    ON card_catalog_audit_log (created_at DESC);

CREATE INDEX idx_card_catalog_audit_card
    ON card_catalog_audit_log (card_type, created_at DESC);

COMMENT ON TABLE card_catalog_audit_log IS
    'Jejak perubahan katalog kartu lewat admin API: siapa, kapan, nilai lama, nilai baru '
    '(docs/08-PILIH-KARTU-API-SPEC.md §13). Append-only; jangan pasang job pembersihan '
    'sebelum retensi ditetapkan compliance.';
