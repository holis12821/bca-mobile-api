-- Personal data collected during onboarding (encrypted PII)

CREATE TABLE onboarding_personal_data (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    personal_data_id      VARCHAR(32) NOT NULL,
    session_id            VARCHAR(32) NOT NULL,

    -- Encrypted PII fields (AES-256-GCM, stored as hex)
    nik_enc               TEXT NOT NULL,
    nama_enc              TEXT NOT NULL,
    tempat_lahir          VARCHAR(128),
    tanggal_lahir         DATE,
    jenis_kelamin         VARCHAR(16),

    -- KTP address
    alamat_lengkap_enc    TEXT,
    rt_rw                 VARCHAR(16),
    kode_pos              VARCHAR(10),
    kelurahan             VARCHAR(128),
    kecamatan             VARCHAR(128),
    kota                  VARCHAR(128),
    provinsi              VARCHAR(128),

    alamat_domisili_sama  BOOLEAN NOT NULL DEFAULT true,

    -- Employment & financial
    pekerjaan             VARCHAR(32) NOT NULL,
    penghasilan_per_bulan VARCHAR(32) NOT NULL,
    sumber_dana_utama     VARCHAR(32) NOT NULL,

    -- Contact (encrypted)
    nomor_hp_enc          TEXT NOT NULL,
    email_enc             TEXT NOT NULL,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT fk_pd_session FOREIGN KEY (session_id)
        REFERENCES onboarding_sessions(session_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_pd_personal_data_id ON onboarding_personal_data (personal_data_id);
CREATE INDEX idx_pd_session_id ON onboarding_personal_data (session_id);