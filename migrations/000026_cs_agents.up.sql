-- Petugas CS yang berwenang melayani video call e-KYC.
--
-- Sebelum tabel ini, `agent_employee_id` dan `agent_name` murni field body yang
-- dipercaya apa adanya, dengan satu INTERNAL_API_KEY yang sama untuk semua
-- pemanggil. Siapa pun yang memegang key itu bisa mengaku sebagai pegawai mana
-- pun — dan string itulah yang masuk audit trail sebagai `actor` serta tampil di
-- layar nasabah lewat `agent_assigned`. Untuk verifikasi identitas yang hasilnya
-- membuka pembukaan rekening, jejak auditnya harus bisa dipertanggungjawabkan ke
-- orang, bukan ke sebuah payload.
--
-- api_key_hash memakai PHC Argon2id yang sama dengan users.pin_hash. Karena hash
-- Argon2 ber-salt tidak bisa dicari balik, pemanggil menyebut dirinya lewat
-- X-Agent-Employee-ID lalu membuktikannya dengan X-Agent-API-Key: baris dicari
-- dengan employee_id, hash-nya diverifikasi constant-time oleh Argon2.
CREATE TABLE cs_agents (
    employee_id  VARCHAR(32) PRIMARY KEY,
    name         VARCHAR(128) NOT NULL,
    api_key_hash TEXT        NOT NULL,

    -- Pencabutan hak tanpa menghapus baris: panggilan lama tetap punya rujukan
    -- nama petugasnya, sementara kredensialnya berhenti berlaku seketika.
    is_active    BOOLEAN     NOT NULL DEFAULT true,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Setiap permintaan agent menyentuh jalur ini, dan hanya yang aktif yang dicari.
CREATE INDEX idx_cs_agents_active ON cs_agents (employee_id) WHERE is_active;
