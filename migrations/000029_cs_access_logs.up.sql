-- Jejak setiap kali petugas CS menyentuh data nasabah.
--
-- Terpisah dari onboarding_audit_logs, dan bukan karena rapi-rapi: jejak itu ber-kunci
-- session_id onboarding, sementara nasabah yang sudah punya rekening tidak punya sesi
-- onboarding lagi. Pencarian nasabah juga tidak punya subjek sampai hasilnya ditemukan,
-- jadi tidak ada session_id maupun user_id yang bisa dipakai sebagai kunci.
--
-- Yang dijawab tabel ini: siapa membuka data siapa, kapan, dan dari mana.
CREATE TABLE cs_access_logs (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_employee_id VARCHAR(32) NOT NULL,
    action            VARCHAR(48) NOT NULL,

    -- NULL untuk pencarian yang tidak menemukan apa pun: tidak ada data siapa pun yang
    -- terbuka, dan baris dengan subjek palsu akan membuat jejak satu nasabah memuat
    -- pencarian yang bukan tentang dia.
    subject_user_id   UUID        REFERENCES users (id) ON DELETE SET NULL,

    -- query_kind menyimpan JENIS kunci pencarian (ACCOUNT_NUMBER / PHONE), BUKAN
    -- nilainya. Ini keputusan yang paling mudah salah di tabel seperti ini: menyimpan
    -- kata kuncinya akan menumpuk nomor rekening dan nomor HP nasabah di tabel log yang
    -- tidak terenkripsi dan jarang ditinjau — memindahkan kebocoran, bukan mencatatnya.
    query_kind        VARCHAR(24),

    result_count      INTEGER     NOT NULL DEFAULT 0,
    ip_address        VARCHAR(45),
    user_agent        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT cs_access_logs_action_valid CHECK (
        action IN ('CUSTOMER_SEARCH', 'CUSTOMER_VIEWED')
    ),
    CONSTRAINT cs_access_logs_query_kind_valid CHECK (
        query_kind IS NULL OR query_kind IN ('ACCOUNT_NUMBER', 'PHONE')
    )
);

-- "Apa saja yang dibuka petugas X minggu ini" — pertanyaan pertama setiap pemeriksaan.
CREATE INDEX idx_cs_access_logs_agent ON cs_access_logs (agent_employee_id, created_at DESC);

-- "Siapa saja yang pernah membuka data nasabah Y" — pertanyaan saat nasabah mengadu.
CREATE INDEX idx_cs_access_logs_subject ON cs_access_logs (subject_user_id, created_at DESC)
    WHERE subject_user_id IS NOT NULL;

-- prevent_audit_mutation dipakai bersama onboarding_audit_logs, tapi pesannya dulu
-- menyebut nama tabel itu secara harfiah. Dipakai ulang apa adanya, penolakan di
-- cs_access_logs akan berbunyi "onboarding_audit_logs is append-only" — dan orang yang
-- menelusurinya akan mencari di tabel yang salah. TG_TABLE_NAME menyebut tabel yang
-- benar-benar ditolak.
CREATE OR REPLACE FUNCTION prevent_audit_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

-- Append-only, dengan fungsi trigger yang sama yang menjaga onboarding_audit_logs.
-- Jejak akses yang bisa disunting oleh pemiliknya bukan jejak.
CREATE TRIGGER trg_cs_access_logs_immutable
    BEFORE UPDATE OR DELETE ON cs_access_logs
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
