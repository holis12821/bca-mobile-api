-- 000020_card_issuance_and_catalog_audit.down.sql
--
-- Tidak ada enum maupun kolom pada tabel lain yang disentuh migrasi ini, jadi
-- rollback-nya benar-benar hanya melepas kedua tabel. Urutan tidak penting:
-- keduanya berdiri sendiri, dan tidak ada tabel lain yang merujuk mereka.
--
-- card_products TIDAK ikut terhapus — foreign key di onboarding_card_issuance
-- menunjuk ke sana, bukan sebaliknya.

DROP TABLE IF EXISTS card_catalog_audit_log;
DROP TABLE IF EXISTS onboarding_card_issuance;
