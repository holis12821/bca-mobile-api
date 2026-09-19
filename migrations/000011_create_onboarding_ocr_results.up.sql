-- OCR results for onboarding KTP verification

CREATE TABLE onboarding_ocr_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ocr_id          VARCHAR(32) NOT NULL,
    session_id      VARCHAR(32) NOT NULL,
    photo_path      TEXT NOT NULL,
    accuracy_pct    NUMERIC(5,2),

    -- Extracted & encrypted PII fields (AES-256-GCM, stored as hex)
    nik_enc         TEXT,
    nama_enc        TEXT,
    tempat_lahir    VARCHAR(128),
    tanggal_lahir   DATE,
    jenis_kelamin   VARCHAR(16),
    alamat_enc      TEXT,
    rt_rw           VARCHAR(16),
    kelurahan       VARCHAR(128),
    kecamatan       VARCHAR(128),
    kota            VARCHAR(128),
    provinsi        VARCHAR(128),
    agama           VARCHAR(32),
    status_perkawinan VARCHAR(32),

    -- Verification
    dukcapil_match  BOOLEAN NOT NULL DEFAULT false,

    -- Photo quality
    sharpness       VARCHAR(16),
    glare_detected  BOOLEAN NOT NULL DEFAULT false,
    corners_visible BOOLEAN NOT NULL DEFAULT true,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    auto_delete_at  TIMESTAMPTZ NOT NULL,

    CONSTRAINT fk_ocr_session FOREIGN KEY (session_id)
        REFERENCES onboarding_sessions(session_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_ocr_ocr_id ON onboarding_ocr_results (ocr_id);
CREATE INDEX idx_ocr_session_id ON onboarding_ocr_results (session_id);
CREATE INDEX idx_ocr_auto_delete ON onboarding_ocr_results (auto_delete_at);
