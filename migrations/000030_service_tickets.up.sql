-- Tiket layanan Halo BCA.
--
-- Dibuat petugas, bukan nasabah: tidak ada endpoint nasabah yang menulis ke sini. Tiket
-- lahir saat petugas mengangkat telepon atau menyelesaikan panggilan video dan ada yang
-- perlu ditindaklanjuti setelahnya.

CREATE TYPE ticket_status   AS ENUM ('OPEN', 'IN_PROGRESS', 'RESOLVED', 'CLOSED');
CREATE TYPE ticket_priority AS ENUM ('LOW', 'NORMAL', 'HIGH', 'URGENT');
CREATE TYPE ticket_category AS ENUM (
    'KARTU', 'TRANSAKSI', 'AKUN', 'BUKA_REKENING', 'APLIKASI', 'LAINNYA'
);

-- Nomor tiket yang dibaca manusia: TKT-20261005-000123.
--
-- Dari sequence, bukan dari COUNT(*)+1 maupun dari angka acak. COUNT akan memberi nomor
-- yang sama kepada dua petugas yang membuat tiket bersamaan; angka acak menghasilkan
-- nomor yang tidak bisa disebutkan lewat telepon. Sequence aman terhadap keduanya dan
-- tidak ikut mundur saat transaksi dibatalkan — celah nomor jauh lebih murah daripada
-- tabrakan nomor.
CREATE SEQUENCE service_ticket_number_seq;

CREATE TABLE service_tickets (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_number VARCHAR(32)     NOT NULL UNIQUE,

    -- Keduanya nullable, dan keduanya bisa terisi sekaligus. Penelepon yang belum punya
    -- rekening hanya punya session_id onboarding; nasabah lama hanya punya user_id; dan
    -- orang yang gagal di tengah pembukaan rekening lalu menelepon punya dua-duanya.
    -- Menuntut salah satunya akan menolak tiket yang paling perlu dicatat.
    user_id       UUID            REFERENCES users (id) ON DELETE SET NULL,
    session_id    VARCHAR(32),

    category      ticket_category NOT NULL DEFAULT 'LAINNYA',
    priority      ticket_priority NOT NULL DEFAULT 'NORMAL',
    status        ticket_status   NOT NULL DEFAULT 'OPEN',

    subject       VARCHAR(200)    NOT NULL,
    description   TEXT            NOT NULL DEFAULT '',

    created_by_agent  VARCHAR(32) NOT NULL,
    assigned_to_agent VARCHAR(32),

    resolved_at   TIMESTAMPTZ,
    closed_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT now(),

    -- Subjek kosong menghasilkan daftar tiket yang tidak bisa dibaca sama sekali.
    CONSTRAINT service_tickets_subject_not_blank CHECK (btrim(subject) <> ''),

    -- Stempel waktu harus sejalan dengan status. Tanpa ini, tiket bisa berstatus
    -- RESOLVED tanpa resolved_at, dan setiap laporan waktu penyelesaian akan diam-diam
    -- melewatkannya.
    CONSTRAINT service_tickets_resolved_consistent CHECK (
        (status IN ('RESOLVED', 'CLOSED')) = (resolved_at IS NOT NULL)
    ),
    CONSTRAINT service_tickets_closed_consistent CHECK (
        (status = 'CLOSED') = (closed_at IS NOT NULL)
    )
);

-- Daftar tiket diurut created_at DESC, id DESC dan diambil lewat keyset — pola yang
-- sama dengan daftar sesi CS.
CREATE INDEX idx_service_tickets_list ON service_tickets (created_at DESC, id DESC);

-- "Tiket saya" dan "tiket yang masih terbuka" adalah dua tampilan pertama di layar mana
-- pun yang menampilkan tiket.
CREATE INDEX idx_service_tickets_assignee ON service_tickets (assigned_to_agent, status)
    WHERE assigned_to_agent IS NOT NULL;
CREATE INDEX idx_service_tickets_open ON service_tickets (status, created_at DESC)
    WHERE status IN ('OPEN', 'IN_PROGRESS');

-- "Nasabah ini pernah mengadu apa saja" — pertanyaan pertama saat dia menelepon lagi.
CREATE INDEX idx_service_tickets_user ON service_tickets (user_id, created_at DESC)
    WHERE user_id IS NOT NULL;

-- Catatan tindak lanjut. Terpisah dari kolom description supaya riwayat penanganan tidak
-- saling menimpa: satu petugas yang menyunting deskripsi akan menghapus apa yang ditulis
-- petugas sebelumnya, dan di tiket keluhan itu justru bagian yang paling perlu utuh.
CREATE TABLE service_ticket_notes (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id  UUID        NOT NULL REFERENCES service_tickets (id) ON DELETE CASCADE,
    author     VARCHAR(32) NOT NULL,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT service_ticket_notes_body_not_blank CHECK (btrim(body) <> '')
);

CREATE INDEX idx_service_ticket_notes_ticket
    ON service_ticket_notes (ticket_id, created_at ASC);
