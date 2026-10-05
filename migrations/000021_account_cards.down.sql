-- 000021_account_cards.down.sql
--
-- Urutan dibalik dari up, dan di sini urutannya MENGIKAT: account_card_audit_log
-- dan card_replacement_requests dua-duanya punya foreign key ke
-- account_cards(id). Melepas account_cards lebih dulu gagal dengan
-- "cannot drop table ... because other objects depend on it".
--
-- Tidak ada enum yang disentuh migrasi ini, jadi tidak perlu pembuatan ulang
-- tipe seperti di 000019 down.

DROP TABLE IF EXISTS account_card_audit_log;
DROP TABLE IF EXISTS card_replacement_requests;
DROP TABLE IF EXISTS account_cards;

-- card_products TIDAK ikut terhapus: foreign key account_cards.card_type
-- menunjuk ke sana, bukan sebaliknya. Katalognya milik migrasi 000019.

-- Kolom tier dilepas terakhir. CHECK constraint-nya ikut terbuang bersama
-- kolom, tidak perlu di-drop terpisah.
--
-- Rollback ini MENGHAPUS data tier yang sudah tersimpan. Tidak ada cara
-- menyelamatkannya di jalur down — kolomnya memang belum ada sebelum 000021.
ALTER TABLE users
    DROP COLUMN IF EXISTS tier;
