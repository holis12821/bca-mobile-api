-- Penjadwalan ulang video call e-KYC.
--
-- Tombol "Jadwalkan Panggilan Nanti" ada di aplikasi Android sejak awal, dan SELAMA INI
-- DIMATIKAN karena tidak ada endpoint yang melayaninya. Nasabah yang membuka rekening di
-- luar jam operasional, atau yang antreannya terlalu panjang, tidak punya pilihan selain
-- menunggu atau pergi.
--
-- Tabel tersendiri, bukan kolom di onboarding_video_calls: jadwal ada SEBELUM panggilan
-- ada, dan baris onboarding_video_calls baru lahir saat nasabah benar-benar masuk
-- antrean. Menyimpannya di sana akan menuntut baris panggilan yang statusnya bukan
-- panggilan.
CREATE TYPE video_call_schedule_status AS ENUM ('SCHEDULED', 'CANCELLED', 'FULFILLED');

CREATE TABLE onboarding_video_call_schedules (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    schedule_id  VARCHAR(40) NOT NULL UNIQUE,
    session_id   VARCHAR(32) NOT NULL,

    -- Disimpan UTC. Validasi jam operasional dilakukan di aplikasi dalam zona
    -- Asia/Jakarta — seperti batas harian transaksi, dan dengan alasan yang sama:
    -- CURRENT_DATE maupun jam database bukan jam Jakarta.
    scheduled_at TIMESTAMPTZ NOT NULL,

    status       video_call_schedule_status NOT NULL DEFAULT 'SCHEDULED',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Satu jadwal AKTIF per sesi. Partial unique index, bukan UNIQUE biasa: nasabah yang
-- membatalkan lalu menjadwalkan ulang harus bisa, dan riwayat pembatalannya tetap ada.
--
-- Tanpa ini, menekan tombol dua kali menghasilkan dua jadwal dan nasabah tidak tahu mana
-- yang berlaku.
CREATE UNIQUE INDEX idx_vc_schedules_one_active
    ON onboarding_video_call_schedules (session_id)
    WHERE status = 'SCHEDULED';

-- "Siapa yang dijadwalkan dalam satu jam ke depan" — yang dibutuhkan untuk menyiapkan
-- petugas, dan satu-satunya alasan tabel ini berguna bagi sisi CS.
CREATE INDEX idx_vc_schedules_upcoming
    ON onboarding_video_call_schedules (scheduled_at)
    WHERE status = 'SCHEDULED';

CREATE INDEX idx_vc_schedules_session
    ON onboarding_video_call_schedules (session_id, created_at DESC);
