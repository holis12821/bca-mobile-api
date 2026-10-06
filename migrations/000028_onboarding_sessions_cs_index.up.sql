-- Indeks untuk daftar sesi di layar pemantauan CS.
--
-- GET /internal/v1/onboarding/sessions mengurut created_at DESC, id DESC dan diambil
-- lewat keyset. Tanpa indeks ini rencananya Seq Scan + Sort atas SELURUH
-- onboarding_sessions — tabel yang tidak pernah menyusut, karena setiap percobaan
-- pendaftaran meninggalkan satu baris selamanya. Dan daftar ini di-polling tiap lima
-- detik oleh setiap petugas yang sedang bertugas.
--
-- Urutan kolomnya sama persis dengan ORDER BY, termasuk arahnya: indeks (created_at, id)
-- naik tetap bisa dipakai Postgres untuk urutan menurun, tapi menuliskannya sejajar
-- dengan query membuat EXPLAIN berikutnya tidak perlu ditafsirkan.
--
-- Partial WHERE deleted_at IS NULL: setiap query daftar menyertakan syarat itu, dan sesi
-- yang di-soft-delete tidak pernah dilihat siapa pun lewat jalur ini.
CREATE INDEX idx_onboarding_sessions_cs_list
    ON onboarding_sessions (created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
