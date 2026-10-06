-- Cakupan kewenangan per petugas, memisahkan video call dari administrasi katalog.
--
-- Sebelum kolom ini, satu INTERNAL_API_KEY membuka SELURUH jalur operator: petugas
-- yang tugasnya melayani video call e-KYC juga bisa mengubah biaya dan limit kartu
-- Paspor untuk seluruh nasabah. Kewenangan itu tidak pernah diberikan kepadanya,
-- hanya kebetulan tidak dipisahkan.
--
-- Pemisahannya di sini, bukan di kunci kedua: cs_agents sudah menjawab "petugas mana
-- yang bertindak", dan menambah satu kunci bersama lagi hanya memindahkan masalah yang
-- sama satu tingkat. Yang dibutuhkan adalah kewenangan yang melekat pada orangnya.
--
-- Empat cakupan, dan CUSTOMER_PII sengaja dipisah dari VIDEO_CALL meski keduanya dipegang
-- petugas yang sama di aplikasi desktop: melayani panggilan menampilkan nasabah yang
-- SEDANG bicara, sementara membuka data pribadi menjangkau nasabah mana pun yang pernah
-- mendaftar. Keduanya kewenangan yang berbeda ukurannya, dan menggabungkannya berarti
-- setiap petugas panggilan diam-diam memegang yang kedua.
ALTER TABLE cs_agents
    ADD COLUMN scopes TEXT[] NOT NULL DEFAULT ARRAY['VIDEO_CALL']::TEXT[];

-- Default VIDEO_CALL, bukan keduanya: setiap baris yang sudah ada dibuat untuk
-- melayani video call, dan migrasi yang diam-diam memberi kewenangan katalog kepada
-- mereka akan melakukan persis hal yang kolom ini ada untuk mencegahnya.

-- Cakupan kosong berarti petugas tanpa kewenangan apa pun — sebuah baris yang tidak
-- berguna tapi terlihat sah, dan penyebab penolakan yang sulit ditelusuri. Dicegah di
-- skema supaya tidak perlu dijaga di setiap jalur tulis.
ALTER TABLE cs_agents
    ADD CONSTRAINT cs_agents_scopes_valid CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY['VIDEO_CALL', 'CARD_ADMIN', 'CUSTOMER_PII', 'TICKET']::TEXT[]
    );
