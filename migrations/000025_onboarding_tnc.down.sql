-- 000025_onboarding_tnc.down.sql
--
-- Urutannya terikat: onboarding_tnc_sections menunjuk ke onboarding_tnc_documents,
-- jadi anaknya lebih dulu. (ON DELETE CASCADE mengurus baris, bukan tabel —
-- DROP pada induknya tanpa CASCADE tetap ditolak selama anaknya masih ada.)
--
-- onboarding_sessions.tnc_version TIDAK ikut dihapus, dan memang tidak boleh:
-- kolom itu milik migrasi 000010 dan berisi versi yang disetujui nasabah.
-- Setelah down dijalankan, nilai di sana kehilangan rujukannya — GET
-- /v1/onboarding/tnc menjawab 500 dan setiap POST /sessions ditolak karena
-- tidak ada versi aktif untuk divalidasi. Itu memang arti menurunkan migrasi
-- yang membawa datanya sendiri.
DROP TABLE IF EXISTS onboarding_tnc_sections;
DROP TABLE IF EXISTS onboarding_tnc_documents;
