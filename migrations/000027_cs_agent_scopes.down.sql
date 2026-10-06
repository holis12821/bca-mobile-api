-- PERINGATAN: rollback ini MENGHAPUS data kewenangan, tidak hanya kolomnya.
--
-- Menjalankan down lalu up lagi membuat setiap baris cs_agents kembali ke DEFAULT
-- ARRAY['VIDEO_CALL'] — petugas ber-scope CARD_ADMIN kehilangan kewenangannya, dan
-- setiap petugas mendadak memegang VIDEO_CALL. Ini bukan teori: terjadi saat migrasi ini
-- diuji, dan gejalanya adalah 403 yang tampak seperti bug kode.
--
-- Sebelum rollback di lingkungan yang dipakai: simpan dulu isinya.
--   COPY (SELECT employee_id, scopes FROM cs_agents) TO '/tmp/cs_scopes.csv' CSV;
ALTER TABLE cs_agents DROP CONSTRAINT IF EXISTS cs_agents_scopes_valid;
ALTER TABLE cs_agents DROP COLUMN IF EXISTS scopes;
