# 06 — Ledger & Device Binding

> Menutup dua keputusan terbuka dari review spec 00–05. Menyertai `migrations/000009_ledger_and_device_binding.{up,down}.sql`.

---

## Daftar Isi

1. [Keputusan A — Device Binding](#1-keputusan-a--device-binding)
2. [Keputusan B — Settlement & Fee Ledger](#2-keputusan-b--settlement--fee-ledger)
3. [Perubahan Lain di 000009](#3-perubahan-lain-di-000009)
4. [Aturan Posting per Rail](#4-aturan-posting-per-rail)
5. [Dampak ke Kontrak API](#5-dampak-ke-kontrak-api)
6. [Rollout](#6-rollout)
7. [Test Plan](#7-test-plan)
8. [Hasil Verifikasi](#8-hasil-verifikasi)

---

## 1. Keputusan A — Device Binding

**Diputuskan:** `devices.device_id` menjadi identitas yang dipakai API. Satu device aktif = satu user.

**Masalah yang diselesaikan.** `05-PROJECT-SETUP` melakukan `GetDevice(ctx, uuid.Nil, req.DeviceID)` saat login PIN, tapi `devices` unique-nya `(user_id, device_id)` — satu fingerprint sah dimiliki banyak user, sehingga lookup itu tidak dapat menentukan user mana yang dimaksud.

**Implementasi.**

```sql
ALTER TABLE devices DROP CONSTRAINT devices_user_id_device_id_key;
CREATE UNIQUE INDEX idx_devices_device_id_active
    ON devices (device_id) WHERE revoked_at IS NULL;
```

**Kenapa partial, bukan unique penuh.** Unique global akan **membakar fingerprint selamanya**: handset yang dijual, di-factory-reset, atau dipindahtangankan tidak akan pernah bisa didaftarkan ke pemilik barunya, dan tidak ada jalan keluar selain menghapus baris histori yang justru dibutuhkan untuk audit. Dengan partial index, `revoked_at = NOW()` melepas fingerprint dan pemilik baru bisa mendaftar; baris lama tetap tersimpan untuk fraud review.

**Konsekuensi yang harus diterima tim:**

| Konsekuensi | Penanganan |
|---|---|
| Satu HP tidak bisa dipakai dua nasabah bersamaan (suami–istri berbagi perangkat) | Produk harus menyatakan ini eksplisit. Alternatifnya user kedua me-*revoke* device dari perangkatnya sendiri lebih dulu. |
| `device_id` naik jadi kredensial identitas, bukan sekadar telemetri | Wajib fingerprint yang stabil dan sulit ditebak (Android ID + Keystore-backed, bukan IMEI atau nilai yang dapat dipilih klien). Jangan pernah percaya `device_id` sebagai satu-satunya faktor — ia menentukan *siapa*, PIN/biometrik yang membuktikan. |
| `device_id` yang dipalsukan mengarahkan login ke akun korban | Login tetap gagal tanpa PIN yang benar, **tetapi** percobaan itu menaikkan `failed_pin_attempts` korban → penyerang bisa mengunci akun orang lain (DoS). **Mitigasi wajib:** rate limit login juga per-IP, dan lockout mengirim push ke device terdaftar. |
| Re-install aplikasi menghasilkan fingerprint baru | Alur `POST /auth/biometric/register` ulang; device lama menganggur lalu di-revoke oleh job kedaluwarsa (`last_active_at` > 180 hari). |

> **Catatan risiko:** baris ketiga di atas adalah kerentanan baru yang lahir dari keputusan ini. Sebelumnya penyerang butuh nomor HP korban; sekarang cukup `device_id`. Rate limit per-IP di §10 skill bukan lagi opsional.

---

## 2. Keputusan B — Settlement & Fee Ledger

**Diputuskan:** tambahkan akun internal (settlement per rail + fee income), 16 shard masing-masing.

**Masalah yang diselesaikan.** `04-SECURITY` mengklaim setiap transfer menghasilkan dua mutation. Itu hanya benar untuk transfer internal BCA→BCA. Untuk transfer antar bank, top-up e-wallet, dan QRIS, uang **keluar dari sistem** — tidak ada akun lawan di database ini. Tanpa akun lawan, `SUM(account_mutations)` tidak akan pernah rekonsiliasi dengan `SUM(accounts.balance)`, dan biaya admin yang dipotong dari nasabah tidak tercatat sebagai pendapatan di mana pun.

**Struktur.**

```
accounts.owner_type = 'INTERNAL'
  settlement_rail ∈ (TRANSFER_EXTERNAL, EWALLET, QRIS, FEE_INCOME)
  shard_index     ∈ 0..15
  user_id         = NULL
```

Nomor rekening internal: `99` + rail(2) + shard(4) + `00` — di luar rentang nomor rekening nasabah.

| Rail | Kode | Rekening | Isi |
|---|---|---|---|
| `TRANSFER_EXTERNAL` | 01 | `9901000000`–`9901001500` | Dana transfer antar bank yang belum disettle ke bank tujuan |
| `EWALLET` | 02 | `9902000000`–`9902001500` | Dana top-up yang belum disettle ke provider |
| `QRIS` | 03 | `9903000000`–`9903001500` | Dana pembayaran QRIS yang belum disettle ke acquirer |
| `FEE_INCOME` | 09 | `9909000000`–`9909001500` | Pendapatan biaya admin |

**Kenapa 16 shard.** Setiap transaksi keluar mengunci baris settlement dengan `SELECT ... FOR UPDATE`. Satu baris per rail berarti **seluruh throughput e-wallet bank antre di belakang satu row lock** — pada isolasi `SERIALIZABLE` ini bukan sekadar lambat, ia menjadi sumber `40001` yang tak henti. 16 shard yang dipilih dengan `hashtext(transaction_id) % 16` menyebar kontensinya. Saldo rail = `SUM(balance)` atas shard-nya.

Shard ikut dalam `ORDER BY id` saat locking, jadi tidak menambah risiko deadlock.

```sql
SELECT settlement_account_id('EWALLET', :transaction_id);  -- deterministik
```

> Mengubah jumlah shard berarti mengubah **modulus di fungsi** dan **seed di migrasi** bersamaan. Mengubah salah satu saja akan mengarahkan transaksi ke shard yang tidak ada (`NULL`) dan menggagalkan posting.

---

## 3. Perubahan Lain di 000009

| Fix | Perubahan | Alasan |
|---|---|---|
| C | `UNIQUE (user_id, idempotency_key)` menggantikan unique global | Key milik satu nasabah bisa bertabrakan dengan nasabah lain, dan 409 yang muncul membocorkan keberadaan transaksi orang lain |
| D | `transactions.destination_account_id` (FK) | Leg CREDIT transfer internal butuh account id asli; `destination_account` hanya teks denormalisasi untuk keabadian struk |
| E | `next_reference_number(wib_date)` + sequence | Nomor referensi unik tanpa membocorkan penghitung transaksi global |
| F | `set_updated_at()` + 8 trigger | `updated_at` di 000001–000008 tidak pernah berubah — semuanya sebenarnya `created_at` |
| G | `seed_default_transaction_limits()` + trigger + ceiling CHECK | Tanpa baris limit, kesalahan koding yang wajar adalah membacanya sebagai *tak terbatas* |
| H | `DROP DEFAULT` pada `transaction_date`, `transaction_time`, `usage_date` | `CURRENT_DATE` mengikuti timezone server; transaksi 06:00 WIB = 23:00 UTC hari sebelumnya → salah hari laporan dan salah bucket limit harian |
| — | View `v_ledger_reconciliation`, `v_unbalanced_transactions` | Deteksi drift saldo dan posting satu sisi |

**Tentang nomor referensi.** `REF` + `YYYYMMDD` (WIB) + 8 digit. Digitnya berasal dari sequence yang dilewatkan bijeksi `(n × 1327144003) mod 10⁸`. 1327144003 ganjil dan tidak habis dibagi 5, jadi koprima dengan 10⁸, jadi pemetaannya bijektif — bebas tabrakan dalam satu siklus 10⁸, tapi dua transaksi berurutan tidak menghasilkan nomor berurutan.

---

## 4. Aturan Posting per Rail

Semua leg dalam **satu** database transaction. Kunci semua akun terdampak dengan `ORDER BY id`.

### Transfer internal (BCA → BCA)

```
DEBIT   rekening sumber      amount + admin_fee
CREDIT  rekening tujuan      amount
CREDIT  FEE_INCOME shard     admin_fee        (hanya jika admin_fee > 0)
```

### Transfer antar bank

```
DEBIT   rekening sumber              amount + admin_fee
CREDIT  TRANSFER_EXTERNAL shard      amount
CREDIT  FEE_INCOME shard             admin_fee
```

### Top-up e-wallet

```
DEBIT   rekening sumber      amount + admin_fee
CREDIT  EWALLET shard        amount
CREDIT  FEE_INCOME shard     admin_fee
```

### Pembayaran QRIS

```
DEBIT   rekening sumber      amount + admin_fee
CREDIT  QRIS shard           amount
CREDIT  FEE_INCOME shard     admin_fee
```

### Reversal

Transaksi tidak pernah di-UPDATE. Pembalikan adalah transaksi **baru** berstatus `REVERSED` dengan leg yang berlawanan arah, merujuk transaksi asli lewat `provider_ref` atau kolom relasi tersendiri.

### Invarian

- Untuk setiap `transaction_id`: `Σ DEBIT = Σ CREDIT`. Dijaga oleh `v_unbalanced_transactions`.
- Untuk setiap akun yang punya mutasi: `accounts.balance = balance_after` mutasi terakhir. Dijaga oleh `v_ledger_reconciliation`.
- `balance_before`/`balance_after` adalah nilai yang **dibaca di bawah row lock**, bukan hasil hitung ulang setelah commit.

---

## 5. Dampak ke Kontrak API

Keputusan A **tidak mengubah** bentuk request/response mana pun. `POST /auth/login/pin` tetap menerima `device_id` + `pin_encrypted` persis seperti `01-API-SPECIFICATION`. Yang berubah hanya cara server me-resolve user dari `device_id` — sekarang punya definisi yang deterministik.

Yang berubah di sisi server:

| Endpoint | Perubahan |
|---|---|
| `POST /auth/login/pin` | Resolve user dari `device_id` aktif. Tidak ada baris aktif → `403 AUTH_DEVICE_NOT_RECOGNIZED` |
| `POST /auth/login/biometric` | Idem, plus `key_id` harus milik baris device yang sama |
| `POST /auth/logout` dengan `revoke_all_devices: true` | Set `revoked_at` pada semua device user — melepas fingerprint-nya |
| `POST /transfer/execute`, `POST /ewallet/topup`, `POST /qris/pay` | Posting 3 leg (§4). `X-Idempotency-Key` di-scope per user |
| `PUT /account/transaction-limit` | Nilai di atas ceiling ditolak `422 VALIDATION_ERROR` sebelum menyentuh DB; CHECK constraint adalah jaring pengaman kedua |

Endpoint baru untuk operasi (internal, tidak dipakai aplikasi mobile):

| Endpoint | Guna |
|---|---|
| `GET /internal/ledger/reconciliation` | Isi `v_ledger_reconciliation`. Alert jika tidak kosong |
| `GET /internal/ledger/unbalanced` | Isi `v_unbalanced_transactions`. Alert jika tidak kosong |

---

## 6. Rollout

```
1. Jalankan scripts/000009_precheck_devices.sql di target environment.
   → Query 1 kosong? Lanjut ke langkah 3.

2. Ada konflik: review daftarnya bersama tim risk, lalu jalankan blok resolusi
   (query 2, masih ter-komentar). Ini me-revoke device, biometric key, dan sesi
   milik user yang kalah — mereka harus login ulang dengan Kode Akses.
   Kirim notifikasi sebelum, bukan sesudah.

3. make migrate-up   → 000009 punya pre-check sendiri dan akan abort jika
                        langkah 1–2 terlewat.

4. Jalankan scripts/000009_verify.sql di staging. Semua baris harus PASS.

5. Seed limit untuk user existing sudah dilakukan oleh migrasi (backfill).
   Verifikasi: SELECT count(*) FROM users u WHERE NOT EXISTS
     (SELECT 1 FROM transaction_limits l WHERE l.user_id = u.id);  -- harus 0

6. Deploy aplikasi yang sudah menulis 3 leg. JANGAN deploy migrasi lebih dulu
   lalu aplikasi belakangan dalam jeda panjang: di antara keduanya, kode lama
   masih menulis posting satu sisi ke skema yang sudah punya akun settlement,
   dan v_unbalanced_transactions akan terisi.
```

**Rollback.** `migrate down 1` sengaja **gagal** jika (a) dua user sudah memakai `idempotency_key` yang sama, atau (b) sudah ada mutasi terhadap akun settlement. Keduanya berarti rollback akan menghancurkan data transaksi hidup. Jangan dipaksa — perbaiki maju.

---

## 7. Test Plan

Format mengikuti `evidence-convention.md`. Verifikasi satu per satu, jangan diborong.

### 7.1 Migrasi & Skema — otomatis via `scripts/000009_verify.sql`

| ID | Deskripsi / Langkah | Expected Result | Evidence |
|---|---|---|---|
| M-01 | Jalankan base schema 000001–000008, lalu 000009 up | Selesai tanpa error | log psql |
| M-02 | Insert user baru | 5 baris `transaction_limits` otomatis terbentuk | `G1 PASS` |
| M-03 | Naikkan `daily_limit` EWALLET ke Rp 999jt | Ditolak `check_violation` | `G2 PASS` |
| M-04 | UPDATE baris `users` | `updated_at` maju | `F1 PASS` |
| M-05 | Insert `device_id` sama untuk user kedua (aktif) | Ditolak `unique_violation` | `A1 PASS` |
| M-06 | Revoke device lalu insert `device_id` sama untuk user lain | Diterima | `A2 PASS` |
| M-07 | Hitung akun internal | 64 (4 rail × 16 shard), semuanya `user_id IS NULL` | `B1`, `B2 PASS` |
| M-08 | Insert akun INTERNAL tanpa `settlement_rail` | Ditolak `check_violation` | `B3 PASS` |
| M-09 | Panggil `settlement_account_id` 5× dengan key sama | Hasil identik | `B4 PASS` |
| M-10 | 200 UUID acak → shard | Semua ter-resolve, tidak ada NULL | `B5 PASS` |
| M-11 | `idempotency_key` sama untuk dua user berbeda | Keduanya diterima | `C1 PASS` |
| M-12 | `idempotency_key` sama diulang untuk user yang sama | Ditolak `unique_violation` | `C2 PASS` |
| M-13 | Generate 20.000 nomor referensi | 20.000 unik, nol tabrakan | `E1 PASS` |
| M-14 | Format nomor referensi | Cocok `^REF20260902\d{8}$` | `E2 PASS` |
| M-15 | Dua nomor referensi berurutan | Selisih ≠ 1 (tidak sekuensial) | `E3 PASS` |
| M-16 | Insert mutasi tanpa `transaction_date` | Ditolak `not_null_violation` | `H1 PASS` |
| M-17 | Posting top-up 3 leg | Commit sukses, Σ debit = Σ kredit | `L1`, `L2 PASS` |
| M-18 | Cek `v_ledger_reconciliation` | Kosong | `L3 PASS` |
| M-19 | Saldo nasabah setelah top-up 100.000 + fee 1.000 | 15.750.000 → 15.649.000 | `L4 PASS` |
| M-20 | Cek `v_unbalanced_transactions` | Kosong | `L5 PASS` |
| M-21 | Posting satu sisi yang disengaja | Terdeteksi oleh view | `L6 PASS` |
| M-22 | UPDATE `audit_logs` | Ditolak trigger imutabilitas | `X1 PASS` |
| M-23 | 000009 down di DB bersih, lalu up lagi | Keduanya sukses | log psql |
| M-24 | 000009 down setelah ada mutasi settlement | Gagal dengan pesan FK yang jelas | log psql |

### 7.2 Aplikasi — perlu ditulis saat implementasi

| ID | Deskripsi / Langkah | Expected Result | Evidence |
|---|---|---|---|
| A-01 | Login PIN dengan `device_id` yang tidak terdaftar | `403 AUTH_DEVICE_NOT_RECOGNIZED` | HTTP log |
| A-02 | Login PIN dengan `device_id` yang sudah di-revoke | `403`, bukan resolve ke user lama | HTTP log |
| A-03 | 5× PIN salah dari `device_id` korban | Akun terkunci **dan** push terkirim ke device terdaftar | log + screenshot notif |
| A-04 | Rate limit per-IP saat menyerang banyak `device_id` | `429` sebelum lockout massal terjadi | HTTP log |
| A-05 | `revoke_all_devices: true` lalu daftarkan fingerprint yang sama ke user lain | Berhasil | HTTP log |
| L-01 | Transfer internal Rp 1.500.000, fee 0 | 2 mutasi; saldo penerima naik | query mutasi |
| L-02 | Transfer antar bank Rp 50.000, fee 6.500 | 3 mutasi: DEBIT 56.500, CREDIT settlement 50.000, CREDIT fee 6.500 | query mutasi |
| L-03 | Top-up e-wallet Rp 100.000, fee 1.000 | 3 mutasi, Σ = 0 | query mutasi |
| L-04 | 200 top-up konkuren | Tidak ada deadlock; retry `40001` tidak bocor jadi 500 | log + metrik |
| L-05 | Transfer A→B dan B→A konkuren | Keduanya selesai, tidak ada deadlock | log |
| L-06 | Setelah semua skenario di atas | `v_ledger_reconciliation` dan `v_unbalanced_transactions` kosong | query view |
| L-07 | Saldo rail = `SUM(balance)` 16 shard vs total transaksi keluar hari itu | Cocok | query rekonsiliasi |
| T-01 | Transaksi 06:00 WIB | `transaction_date` = hari WIB berjalan, bukan hari UTC sebelumnya | query mutasi |
| T-02 | Limit harian pada 23:30 WIB lalu 00:30 WIB | Reset di tengah malam WIB | query `daily_usage` |
| I-01 | Replay `X-Idempotency-Key` setelah TTL Redis 24 jam habis | Transaksi asli dikembalikan, **bukan** 409, dan tetap satu baris | HTTP log + query |
| I-02 | `X-Idempotency-Key` sama dari user berbeda | Diperlakukan sebagai request baru | HTTP log |

---

## 8. Hasil Verifikasi

Dijalankan pada PostgreSQL 16.13.

```
base schema 000001-000008 ......... OK
000009 up ......................... OK
scripts/000009_verify.sql ......... 23 PASS, 0 FAIL
000009 down (DB bersih) ........... OK
000009 up ulang ................... OK
000009 down (ada mutasi settlement) ABORT — FK menolak, sesuai desain
000009 down (idempotency_key dipakai 2 user) ABORT — guard menyala, sesuai desain

objek terbentuk: 8 trigger updated_at, 64 akun internal, 2 view, 4 function
```

**Satu bug ditemukan dan diperbaiki saat verifikasi:** `v_ledger_reconciliation` versi pertama memakai `LEFT JOIN` + `COALESCE(balance_after, 0)`, sehingga setiap rekening dengan saldo pembukaan tapi belum punya mutasi ikut ter-flag sebagai drift. View yang selalu berisik adalah view yang akhirnya diabaikan orang. Sekarang memakai `JOIN` — rekening tanpa histori ledger memang di luar cakupan.
