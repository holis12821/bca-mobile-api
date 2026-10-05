---
name: profil-saya-kartu-api
description: Endpoint kartu nasabah dan konten bantuan untuk layar Profil Saya aplikasi Android — GET /account/cards, PUT /account/cards/{id}/settings, POST block, POST replacement, field tier di profil, serta GET /content/help-center dan /content/contact-cs. Gunakan saat mengerjakan kartu milik nasabah pasca-onboarding, sakelar kanal kartu (debit online, luar negeri), blokir kartu, permintaan penggantian kartu, tier nasabah, atau konten FAQ dan kontak CS. Trigger juga pada "layar Profil Saya", "layar Akun", "kartu nasabah", "blokir kartu", "ganti kartu", "kontrol akses kartu", "masked_number", "tier Prioritas", "Halo BCA CS", dan "Pusat Bantuan". JANGAN dipakai untuk katalog kartu pada flow buka rekening (itu `buka-rekening-kartu`), OTP onboarding (itu `buka-rekening-otp`), atau saldo, mutasi, transfer, dan ledger (itu `bca-mobile-backend`).
---

# Profil Saya — Kartu Nasabah & Konten Bantuan

Layar `AkunScreen.kt` di aplikasi Android sudah selesai secara visual, tapi tujuh
bagiannya menampilkan penanda kosong karena tidak ada endpoint yang melayaninya.
Skill ini mendefinisikan endpoint itu.

**Baca `CLAUDE.md` repo ini lebih dulu.** ATURAN #1 di sana berlaku penuh: setiap
file baru — migrasi, entity, handler, repository — disebutkan path-nya dan ditunggu
persetujuannya sebelum dibuat.

---

## 0. Yang sudah ada, jangan dibuat ulang

| Sudah ada | Melayani |
|---|---|
| `card_products` (migrasi `000019`) | Katalog jenis kartu. **Bukan** kartu milik nasabah. |
| `onboarding_card_issuance` (migrasi `000020`) | Permintaan cetak kartu saat buka rekening, terikat `session_id`. Tidak bisa dipakai nasabah lama — tidak punya sesi. |
| `users.biometric_enabled`, `push_notification_enabled`, `email_statement_enabled` (migrasi `000018`) | `PUT /account/settings`. Tiga sakelar ini **sudah lengkap**. |
| `GET·PUT /account/profile`, `POST /account/profile/otp` | Identitas nasabah |
| `GET·PUT /account/transaction-limit` | Baris "Atur Limit" di layar |
| `POST /auth/pin/verify` | Penerbit `verification_token` |

Kartu yang **dimiliki** nasabah setelah rekening aktif belum punya tempat sama sekali.
Itu lubang utamanya.

---

## 1. Satu cacat yang bukan milik repo ini

`PUT /account/settings` membalas `ValidationError` untuk setiap panggilan dari
aplikasi. Penyebabnya bukan di sini:

| Sisi | Field |
|---|---|
| `docs/01-API-SPECIFICATION.md` | `biometric_enabled`, `push_notification_enabled`, `email_statement_enabled` |
| `UpdateSettingsRequest` di repo ini | sama dengan spec — **benar** |
| `SettingsRequest` di client Android | `is_biometric_enabled`, `notification_enabled` — **menyimpang** |

Ketiga field bertipe `*bool`; nama yang tidak cocok jadi `nil`, dan
`Service.UpdateSettings` menolak kalau ketiganya `nil`.

**Jangan "memperbaiki" ini dengan melonggarkan nama field di backend.** Spec adalah
kontraknya dan backend sudah mengikutinya. Perbaikannya di `AccountDto.kt` milik client.
Menerima dua ejaan berarti mengawetkan kekeliruan client selamanya.

---

## 2. Kontrak endpoint

Semua di bawah `/v1`, di dalam blok `r.Group` yang memakai `middleware.Auth`.
Envelope, `X-Device-ID`, dan `X-Request-ID` mengikuti aturan yang sudah berlaku.

### 2.1 `GET /v1/account/cards`

Daftar kartu milik nasabah yang sedang login.

```json
{
  "status": "success",
  "data": {
    "cards": [
      {
        "card_id": "3f2a…",
        "masked_number": "•••• •••• •••• 5678",
        "cardholder_name": "BUDI SANTOSO",
        "card_type": "PASPOR_BLUE",
        "product_name": "Paspor BCA Blue",
        "network": "MASTERCARD",
        "tier_key": "DEBIT",
        "style": "BLUE",
        "valid_thru": "12/28",
        "status": "ACTIVE",
        "is_primary": true,
        "settings": {
          "debit_online_enabled": true,
          "international_enabled": false
        }
      }
    ]
  }
}
```

- `status`: `ACTIVE` | `BLOCKED` | `EXPIRED` | `REPLACEMENT_PENDING`.
  Client memetakan `ACTIVE` ke chip hijau "Aktif & Terhubung".
- `valid_thru` dikirim **sudah terformat** `MM/YY`. Layar menampilkannya apa adanya.
- Nasabah tanpa kartu membalas `{"cards": []}`, bukan `404`. Layar punya empty state.

### 2.2 `PUT /v1/account/cards/{card_id}/settings`

Dua sakelar kanal di layar. Tanpa PIN — keduanya bisa dikembalikan nasabah sendiri.

```json
{ "debit_online_enabled": true, "international_enabled": false }
```

Keduanya `*bool`. Semua `nil` → `apperr.ValidationError`, sama seperti
`UpdateSettings`. Balasan: objek kartu yang sudah diperbarui, supaya client tidak
perlu memanggil §2.1 lagi.

Kartu ber-status `BLOCKED` menolak perubahan dengan `apperr.CardLocked`.

### 2.3 `POST /v1/account/cards/{card_id}/block`

```json
{ "reason": "LOST", "verification_token": "…" }
```

- `reason`: `LOST` | `STOLEN` | `DAMAGED` | `SUSPECTED_FRAUD`.
- `verification_token` **wajib**, dikonsumsi dengan purpose `"BLOCK_CARD"` lewat
  `ConsumeVerificationToken` — pola yang sama dengan `CHANGE_LIMIT` di
  `service.go:295`. Memblokir kartu adalah aksi keamanan; tanpa PIN, siapa pun
  yang memegang ponsel tak terkunci bisa mematikan kartu orang.
- Idempoten secara alami: memblokir kartu yang sudah `BLOCKED` membalas `200`
  dengan keadaan yang sama, bukan error.

Buka blokir **tidak** ada di layar dan **tidak** dibuat di sini. Membuka blokir
kartu yang dilaporkan hilang adalah keputusan cabang, bukan tombol di aplikasi.

### 2.4 `POST /v1/account/cards/{card_id}/replacement`

Melayani dua hal di layar sekaligus: aksi "Ganti Kartu" dan baris "Permintaan
Penggantian Kartu".

```json
{ "reason": "DAMAGED", "delivery_method": "COURIER", "verification_token": "…" }
```

- `reason`: `DAMAGED` | `LOST` | `UPGRADE`.
- `delivery_method`: `COURIER` | `BRANCH_PICKUP` — enum yang sama dengan
  `onboarding_card_issuance`.
- Header `X-Idempotency-Key` **wajib**, seperti `/transfer/execute`, `/ewallet/topup`,
  dan `/qris/pay`. Penggantian kartu berbiaya (`card_products.fee_card_replacement`);
  retry jaringan tidak boleh menghasilkan dua kartu.
- `verification_token` dengan purpose `"REPLACE_CARD"`.

Balasan memuat `request_id`, `status`, `estimated_arrival_from`, `estimated_arrival_to`,
dan `fee`.

### 2.5 Tier nasabah — tambahan field, bukan endpoint

Badge "Prioritas" di kartu profil. Tambahkan ke response `GET /account/profile`:

```json
{ "tier": "PRIORITAS" }
```

Nilai: `REGULER` | `PRIORITAS` | `SOLITAIRE`. Kosong atau tidak dikirim berarti
badge disembunyikan — client sudah menanganinya begitu. **Jangan** membuat
`GET /account/tier`; satu field pada endpoint yang sudah dipanggil sudah cukup.

### 2.6 `GET /v1/content/help-center` dan `GET /v1/content/contact-cs`

Dua baris terakhir di seksi BANTUAN & INFORMASI. Keduanya boleh diakses tanpa
`Authorization` — isinya bukan data nasabah, dan nasabah yang terkunci di luar
aplikasi justru yang paling butuh nomor CS.

`help-center` membalas daftar kategori berisi item `{question, answer}`.
`contact-cs` membalas `{phone, phone_free, whatsapp, email, chat_url, hours}`.

Keduanya kandidat kuat cache Redis ber-TTL panjang: isinya jarang berubah dan
sama untuk semua orang.

---

## 3. Skema data

Satu migrasi baru. Nomor berikutnya **`000021`** — periksa ulang `ls migrations/`
sebelum menulis, nomor migrasi tidak boleh bertabrakan.

Tabel `account_cards`, minimal:

| Kolom | Catatan |
|---|---|
| `id` | UUID, primary key |
| `user_id`, `account_id` | referensi pemilik |
| `card_type` | `REFERENCES card_products(card_type)` — pakai katalog yang sudah ada |
| `masked_number` | **hanya bentuk tersamar** |
| `cardholder_name` | |
| `valid_thru_month`, `valid_thru_year` | disimpan terpisah, diformat `MM/YY` di handler |
| `status` | CHECK: `ACTIVE`, `BLOCKED`, `EXPIRED`, `REPLACEMENT_PENDING` |
| `blocked_reason`, `blocked_at` | |
| `debit_online_enabled` | `BOOLEAN NOT NULL DEFAULT TRUE` |
| `international_enabled` | `BOOLEAN NOT NULL DEFAULT FALSE` |
| `is_primary` | |
| `created_at`, `updated_at` | |

Ditambah `card_replacement_requests` (mencerminkan `onboarding_card_issuance`, tapi
berkunci `card_id` + idempotency key, bukan `session_id`), dan satu kolom
`users.tier` dengan default `'REGULER'`.

---

## 4. Empat aturan yang mengikat

1. **PAN lengkap tidak pernah disimpan maupun dikirim.** Hanya `masked_number`.
   Aturan ini sudah ditulis di migrasi `000020` — nomor kartu utuh milik core
   banking, bukan milik layanan ini.
2. **Tidak ada nilai visual di response.** Kartu memakai `style`
   (`BLUE`/`GOLD`/`PLATINUM`) yang dipetakan client ke design token. Hex warna dan
   URL gambar dilarang, sama seperti aturan di `buka-rekening-kartu`.
3. **Aksi yang mengubah uang atau keamanan wajib `verification_token`.**
   Blokir dan penggantian kartu masuk kategori itu. Sakelar kanal tidak.
4. **Setiap perubahan status kartu masuk audit trail:** siapa, kapan, nilai lama,
   nilai baru. `onboarding_audit_log` **tidak bisa dipakai** — `session_id` di sana
   `NOT NULL`. Masalah yang sama sudah diselesaikan migrasi `000020` untuk katalog;
   ikuti caranya.

---

## 5. Berkas yang disentuh

Mengikuti lapisan yang sudah ada. Semua **file baru** butuh persetujuan lebih dulu.

| Lapisan | Berkas |
|---|---|
| Migrasi | `migrations/000021_account_cards.{up,down}.sql` |
| Entity | `internal/domain/card/entity.go` |
| Interface repo | `internal/domain/card/repository.go` |
| Service | `internal/domain/card/service.go` |
| Repo Postgres | `internal/repository/postgres/card_repo.go` |
| Cache Redis | `internal/repository/redis/card_cache.go` |
| Handler | `internal/handler/card_handler.go` |
| Konten | `internal/handler/content_handler.go` |
| Rute | `internal/router/router.go` — **edit**, bukan file baru |
| Tier | `internal/domain/account/entity.go` + `account_repo.go` — **edit** |

Paket `card` terpisah dari `account` karena kartu punya siklus hidupnya sendiri
(blokir, penggantian, kedaluwarsa) dan sudah punya katalog sendiri. Menumpuknya
ke `account.Service` yang sudah besar akan menyulitkan.

---

## 6. Urutan pengerjaan

1. Migrasi `000021` + seed kartu untuk user demo. Tanpa ini semua endpoint kosong.
2. `GET /v1/account/cards` — layar langsung berhenti menampilkan penanda kosong.
3. Field `tier` di `GET /account/profile` — paling murah, satu kolom.
4. `PUT .../settings` — dua sakelar mulai berfungsi.
5. `POST .../block` — butuh purpose token baru.
6. `POST .../replacement` — butuh idempotency dan antrean retry.
7. `GET /v1/content/*` — bisa paralel, tidak bergantung pada yang lain.

Jangan menggabung 2–7 dalam satu PR.

---

## 7. Definition of Done

- [ ] Migrasi `up` **dan** `down` dua-duanya jalan; nomor tidak bertabrakan.
- [ ] `masked_number` tidak pernah memuat PAN lengkap, termasuk di log.
- [ ] Tidak ada hex warna atau URL gambar di response.
- [ ] Blokir dan penggantian menolak request tanpa `verification_token` yang sah.
- [ ] Penggantian tanpa `X-Idempotency-Key` ditolak; dengan kunci sama tidak
      menghasilkan dua permintaan.
- [ ] Perubahan status kartu tercatat di audit trail.
- [ ] Cache profil di-invalidasi saat `tier` berubah — pola `IncrVersion` yang sama
      dengan `UpdateSettings`.
- [ ] `docs/01-API-SPECIFICATION.md` diperbarui di commit yang sama.
- [ ] `make test` lulus; handler baru punya test.
- [ ] Nasabah tanpa kartu membalas array kosong, bukan `404`.

---

## 8. Batas dengan skill lain

| Kebutuhan | Skill |
|---|---|
| Katalog kartu pada flow buka rekening, aturan `style` | `buka-rekening-kartu` |
| Seluruh `/v1/onboarding/*` | `buka-rekening-backend` |
| OTP onboarding | `buka-rekening-otp` |
| Auth, PIN, saldo, mutasi, transfer, e-wallet, QRIS, ledger, Redis, migrasi | `bca-mobile-backend` |

Sisi client: skill `bca-api-integrasi-semua-layar` di repo `BcaMobile`, §9.
