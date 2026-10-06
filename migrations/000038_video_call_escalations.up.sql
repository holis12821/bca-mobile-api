-- Eskalasi verifikasi e-KYC: hasil NEED_REVIEW (SCR-039).
--
-- KEPUTUSAN yang menentukan bentuk tabel ini: nasabah NEED_REVIEW tetap di langkah
-- VIDEO_CALL. Tidak ada nilai baru di enum onboarding_step, jadi aplikasi Android tidak
-- menemui `current_step` yang tidak dikenalnya.
--
-- Yang menahannya supaya tidak mengantre lagi adalah baris di sini: JoinQueue menolak
-- selama ada eskalasi PENDING, dengan pesan berbahasa Indonesia yang sudah tampil apa
-- adanya lewat kontrak envelope. Tanpa penahan itu, nasabah akan dilayani Tier 1 lagi dan
-- hasilnya sama — eskalasinya jadi hiasan.
CREATE TABLE onboarding_video_call_escalations (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    escalation_id VARCHAR(40) NOT NULL UNIQUE,

    session_id    VARCHAR(32) NOT NULL
        REFERENCES onboarding_sessions (session_id) ON DELETE CASCADE,

    -- Panggilan yang menghasilkan eskalasi ini.
    queue_id      VARCHAR(32) NOT NULL,

    -- Antrean tujuan. VARCHAR + CHECK: daftar tier akan bertambah, dan enum menuntut
    -- migrasi yang mengunci tabel.
    escalation_queue VARCHAR(32) NOT NULL DEFAULT 'TIER_2_VERIFICATION',

    status        VARCHAR(16) NOT NULL DEFAULT 'PENDING',

    -- Alasan eskalasi, teks bebas dari petugas. Berbeda dari rejection_reason yang
    -- ber-enum: penolakan dihitung dan dilaporkan, sementana eskalasi dibaca manusia
    -- yang akan menanganinya.
    reason        TEXT        NOT NULL,

    raised_by_agent VARCHAR(32) NOT NULL,
    raised_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Penyelesaian oleh Tier 2.
    resolved_by_agent VARCHAR(32),
    resolved_at       TIMESTAMPTZ,
    resolution        VARCHAR(16),

    CONSTRAINT vc_escalations_status_valid CHECK (
        status IN ('PENDING', 'IN_REVIEW', 'RESOLVED', 'CANCELLED')
    ),
    CONSTRAINT vc_escalations_queue_valid CHECK (
        escalation_queue IN ('TIER_2_VERIFICATION', 'FRAUD_REVIEW', 'COMPLIANCE_REVIEW')
    ),
    CONSTRAINT vc_escalations_resolution_valid CHECK (
        resolution IS NULL OR resolution IN ('APPROVED', 'REJECTED')
    ),

    -- Selesai menuntut penyelesai, waktu, dan keputusannya — ketiganya atau tidak satu pun.
    CONSTRAINT vc_escalations_resolved_consistent CHECK (
        (status = 'RESOLVED') =
        (resolved_at IS NOT NULL AND resolved_by_agent IS NOT NULL AND resolution IS NOT NULL)
    )
);

-- Penjaga di JoinQueue membaca ini pada setiap percobaan antre. Partial unique sekaligus
-- mencegah dua eskalasi hidup untuk satu sesi.
CREATE UNIQUE INDEX idx_vc_escalations_one_open
    ON onboarding_video_call_escalations (session_id)
    WHERE status IN ('PENDING', 'IN_REVIEW');

-- Antrean kerja Tier 2: yang terlama lebih dulu.
CREATE INDEX idx_vc_escalations_queue
    ON onboarding_video_call_escalations (escalation_queue, raised_at ASC)
    WHERE status IN ('PENDING', 'IN_REVIEW');
