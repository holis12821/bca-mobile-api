-- 000023_content_help_center.down.sql
--
-- Tidak ada foreign key yang menunjuk ke dua tabel ini, jadi urutannya bebas.
-- Endpoint /v1/content/* akan menjawab 500 setelah down dijalankan tanpa
-- menurunkan versi aplikasi — itu memang arti menurunkan migrasi yang membawa
-- datanya sendiri.
DROP TABLE IF EXISTS content_help_center;
DROP TABLE IF EXISTS content_contact_cs;
