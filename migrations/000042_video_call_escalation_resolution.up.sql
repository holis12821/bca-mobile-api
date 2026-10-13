-- Penyelesaian eskalasi oleh Tier 2.
--
-- Migrasi 000038 melahirkan barisnya dan JoinQueue membacanya, tapi tidak ada satu pun
-- jalur yang MENUTUPNYA — penyelesaiannya masih lewat SQL langsung. Akibatnya nyata dan
-- buruk: nasabah yang eskalasinya tidak pernah ditutup ditolak VIDEO_CALL_UNDER_REVIEW
-- setiap kali ia mencoba mengantre, selamanya, dan satu-satunya pertolongan adalah
-- seseorang yang mengetik UPDATE di database produksi.
--
-- Yang ditambahkan di sini adalah kolom yang dibutuhkan supaya penutupan itu bisa
-- dipertanggungjawabkan: siapa yang memegang perkaranya, siapa yang memutuskan, dan atas
-- alasan apa.

-- Pemegang perkara. IN_REVIEW tanpa nama pemegang adalah perkara yang tidak bisa
-- dijawab "sedang ditangani siapa", dan dua peninjau akan mengerjakannya bersamaan tanpa
-- ada yang tahu.
ALTER TABLE onboarding_video_call_escalations
    ADD COLUMN claimed_by_agent VARCHAR(32),
    ADD COLUMN claimed_at       TIMESTAMPTZ;

-- Alasan penolakan Tier 2, ber-ENUM yang SAMA dengan rejection_reason milik Tier 1.
--
-- Bukan teks bebas, dan bukan enum tersendiri: penolakan verifikasi identitas dihitung
-- dan dilaporkan, dan dua daftar alasan untuk tindakan yang sama akan membuat laporan
-- yang sama harus menjumlahkan dua kategori untuk satu hal.
ALTER TABLE onboarding_video_call_escalations
    ADD COLUMN resolution_reason VARCHAR(32);

-- Keterangan Tier 2, teks bebas, WAJIB saat perkaranya ditutup.
--
-- Alasannya cermin dari `reason`: Tier 1 wajib menerangkan supaya Tier 2 tidak perlu
-- mengulang seluruh panggilan, dan Tier 2 wajib menerangkan supaya pemeriksaan
-- berikutnya — atau nasabah yang menyengketakan hasilnya — tahu atas dasar apa sebuah
-- rekening akhirnya dibuka atau ditolak.
ALTER TABLE onboarding_video_call_escalations
    ADD COLUMN resolution_notes TEXT;

-- Keduanya atau tidak satu pun: claimed_at tanpa pemegang adalah waktu tanpa peristiwa.
ALTER TABLE onboarding_video_call_escalations
    ADD CONSTRAINT vc_escalations_claimed_consistent CHECK (
        (claimed_by_agent IS NULL) = (claimed_at IS NULL)
    );

-- IN_REVIEW menuntut pemegang. Statusnya SENDIRI yang menyatakan ada orang yang
-- memegangnya; tanpa penjaga ini status itu bisa benar tanpa ada orangnya, persis seperti
-- cs_terminals_online_has_agent mencegah loket ONLINE tanpa petugas.
ALTER TABLE onboarding_video_call_escalations
    ADD CONSTRAINT vc_escalations_in_review_has_agent CHECK (
        status <> 'IN_REVIEW' OR claimed_by_agent IS NOT NULL
    );

-- Daftar alasan yang sama dengan onboarding.RejectionReason.
ALTER TABLE onboarding_video_call_escalations
    ADD CONSTRAINT vc_escalations_resolution_reason_valid CHECK (
        resolution_reason IS NULL OR resolution_reason IN (
            'IDENTITY_MISMATCH',
            'INVALID_DOCUMENT',
            'FACE_MISMATCH',
            'SUSPICIOUS_ACTIVITY',
            'INCOMPLETE_INFORMATION',
            'OTHER'
        )
    );

-- Penolakan WAJIB beralasan. IS DISTINCT FROM, bukan <>: resolution NULL pada perkara
-- yang belum ditutup akan membuat `resolution <> 'REJECTED'` bernilai NULL, dan CHECK
-- yang bernilai NULL dianggap lolos — penjaga yang diam-diam tidak menjaga apa pun.
ALTER TABLE onboarding_video_call_escalations
    ADD CONSTRAINT vc_escalations_rejection_has_reason CHECK (
        resolution IS DISTINCT FROM 'REJECTED' OR resolution_reason IS NOT NULL
    );

-- Perkara yang ditutup WAJIB berketerangan, dan spasi tidak dihitung sebagai keterangan.
ALTER TABLE onboarding_video_call_escalations
    ADD CONSTRAINT vc_escalations_resolved_has_notes CHECK (
        status <> 'RESOLVED'
        OR (resolution_notes IS NOT NULL AND btrim(resolution_notes) <> '')
    );

-- "Perkara yang sedang saya pegang" — pertanyaan pertama layar Tier 2 setelah daftar
-- antreannya. Partial: perkara yang sudah ditutup tidak pernah ditanyakan lewat jalur ini.
CREATE INDEX idx_vc_escalations_claimed
    ON onboarding_video_call_escalations (claimed_by_agent, raised_at ASC)
    WHERE status = 'IN_REVIEW';
