# CLAUDE.md — BCA Mobile API

Panduan kerja untuk Claude Code di repo ini. Baca sebelum mengubah apa pun.

---

## ATURAN #1 — Konfirmasi sebelum membuat file baru

**Jangan pernah membuat file baru tanpa persetujuan eksplisit dari user lebih
dulu.** Ini berlaku untuk semua jenis file: `.go`, `.sql`, `.md`, `.sh`,
`.toml`, `.yml`, skrip sementara, file test, apa pun.

Alurnya:

1. Sebutkan **path lengkap** file yang mau dibuat.
2. Jelaskan **isinya** dan **kenapa file baru**, bukan menambah ke file yang sudah ada.
3. **Tunggu jawaban user.** Diam bukan berarti setuju.
4. Baru buat setelah user menjawab ya.

Yang **tidak** perlu konfirmasi:

- Mengedit file yang sudah ada (`Edit`) — itu pekerjaan normal.
- File sementara di direktori scratchpad milik sesi (di luar repo).

Kalau ragu sebuah perubahan butuh file baru atau tidak: **default-nya edit file
yang ada**. Repo ini sudah punya tempat untuk hampir semua hal — lihat peta
struktur di bawah.

Alasannya bukan formalitas. Repo ini punya konvensi ketat soal di mana sesuatu
diletakkan (lapisan domain/handler/repository, penamaan migrasi berurutan,
satu paket per konteks). File baru di tempat yang salah akan memecah lapisan
atau menabrak nomor migrasi orang lain, dan itu mahal untuk dibereskan.

---

## Apa ini

Backend API m-BCA: Go 1.26 modular monolith, `chi` + `pgx/v5` (PostgreSQL 16)
+ `go-redis/v9` (Redis 7). Melayani aplikasi Android. Sekitar 20 ribu baris Go
di luar test.

Skill di `.claude/skills/` **lebih detail** daripada file ini — pakai itu
untuk pekerjaan mendalam:

| Skill | Untuk |
|---|---|
| `bca-mobile-backend` | auth/PIN, saldo, Beranda, mutasi, transfer, e-wallet, QRIS, ledger, migrasi, Redis |
| `buka-rekening-backend` | seluruh `/v1/onboarding/*` — OCR, Dukcapil, biometrik, kredensial, submit |
| `buka-rekening-video-call-backend` | video call e-KYC — antrean, WebSocket signaling, siklus panggilan, sisi CS |
| `buka-rekening-otp` | OTP onboarding — penerbitan, verifikasi, blokir, kirim ulang |
| `buka-rekening-kartu` | katalog kartu + pemilihan kartu pada flow buka rekening |
| `buka-rekening-produk` | katalog jenis rekening tabungan — `GET /v1/onboarding/products`, setoran awal minimum, fitur produk, copy layar Pilih Jenis Rekening |
| `profil-saya-kartu-api` | kartu milik nasabah, `tier`, konten Pusat Bantuan & Kontak CS |
| `push-notification-api` | `POST /account/device/push-token`, `internal/pkg/notify` + `push`, filter `type` notifikasi, transport FCM |
| `twilio-sms-otp` | setup & integrasi provider SMS: kredensial Twilio, `SMS_*`, `internal/pkg/sms`, normalisasi E.164, menelusuri OTP yang tidak sampai |
| `cs-desktop-api-integration` | kontrak 14 endpoint CS yang **sudah terbit** — auth tiga lapis, cakupan petugas, signaling, penyamaran PII |
| `cs-desktop-workflow-backend` | **analisis selisih** untuk alur desktop CS 19 layar: identitas petugas, siklus hidup terminal, gerbang kesiapan, KPI, `NEED_REVIEW` — apa yang belum ada dan kontraknya |

File ini hanya memuat yang **tidak** ada di skill: workflow harian, jebakan
nyata di repo ini, dan aturan di atas.

---

## Workflow

### Pertama kali

```bash
make setup      # salin .env, generate kunci RSA, infra up, migrate, seed
```

### Harian

```bash
make infra-up   # Postgres + Redis session + Redis cache + coturn (STUN/TURN)
make dev        # server + hot reload (air)
make stop       # hentikan server
```

`make dev` dan `make run` menjalankan **server yang sama** — tidak bisa
bersamaan, yang kedua kalah merebut port 8080. Makefile sudah menghentikannya
lebih awal dengan pesan yang jelas. Butuh dua instance: `make run PORT=8081`.

### Sebelum bilang "selesai"

```bash
make check      # lint + vet + test
```

Atau minimal `go build ./... && go vet ./... && go test ./...`.

### Database

```bash
make migrate-up
make migrate-status
make migrate-create NAME=add_foo   # bikin pasangan .up.sql + .down.sql
make ledger-check                  # invarian ledger; dua-duanya harus nol baris
make seed
```

### Online untuk tester Android

```bash
make tunnel      # terminal lain, biarkan jalan
make tunnel-env  # arahkan SIGNALING_BASE_URL ke tunnel
```

Dua variabel wajib benar saat pakai tunnel — lihat README bagian
*Expose to the internet*. Yang paling gampang terlewat: tanpa
`TRUSTED_PROXIES=127.0.0.1/32,::1/128`, **semua** request lewat tunnel
dihitung sebagai satu IP, jadi semua tester berbagi satu jatah rate limit.

---

## Struktur & aturan lapisan

```
cmd/server/          main — hanya wiring, config, graceful shutdown
internal/
  router/            SATU tempat semua rute didaftarkan + dependency injection
  handler/           HTTP: decode, validasi bentuk, panggil service, encode
  domain/<konteks>/  logika bisnis + interface repository (TIDAK tahu SQL/Redis)
  repository/
    postgres/        implementasi interface domain di atas Postgres
    redis/           implementasi cache/session
  middleware/        auth, rate limit, CORS, body limit, timeout, audit
  pkg/               util lintas konteks: apperr, crypto, response, notify, pdf…
  websocket/         hub signaling video call
migrations/          000001…000025, berpasangan .up.sql / .down.sql
```

**Arah impor satu arah, dan saat ini bersih — jaga tetap begitu:**

```
handler  →  domain  ←  repository
              ↑
             pkg
```

- `domain/` **tidak boleh** mengimpor `repository/` atau `handler/`.
- `handler/` **tidak boleh** mengimpor `repository/` — cukup domain + `pkg/apperr`.
  (Kalau repository perlu memberi tahu error spesifik ke handler, kembalikan
  `apperr.X`, bukan sentinel lokal paket.)
- Interface repository didefinisikan di `domain/`, diimplementasikan di `repository/`.

Cek cepat kalau ragu:

```bash
grep -rn "internal/repository" internal/domain/ internal/handler/ | grep -v _test
# harus kosong
```

**Dua direktori kosong yang menipu:** `internal/domain/notification/` dan
`internal/pkg/validator/` hanya berisi `.gitkeep`. Notifikasi sebenarnya ada di
`internal/pkg/notify/`. Jangan bingung — dan jangan isi keduanya tanpa tanya.

---

## Jebakan nyata di repo ini

Hal-hal yang sudah pernah menggigit. Jangan diulang.

### Uang

- **Selalu cek kepemilikan rekening.** `source_account_id` dan `account_id`
  datang dari request; executor mengunci dan mendebet UUID apa pun yang
  diberikan tanpa filter `user_id`. Gunakan
  `accounts.FindOwnedByID(ctx, userID, accountID)` sebelum menyentuh saldo.
  Ini pernah jadi lubang: satu user bisa mendebet rekening user lain.
- **Saldo tersedia = `balance - hold_amount`**, bukan `balance`.
- **Baris limit yang hilang berarti NOL (tolak), bukan tak terbatas.**
- **Tanggal WIB dihitung di aplikasi**, jangan `CURRENT_DATE` — batas harian
  berganti tengah malam Jakarta.
- Transaksi uang: `SERIALIZABLE` + kunci rekening urut UUID + retry 40001.

### Auth

- Access token **harus** dicek ke sesi (`middleware.Auth` + `SessionValidator`),
  bukan cuma verifikasi tanda tangan. Tanpa itu logout tidak mematikan apa pun.
- **Kode akses ≠ PIN.** Kode akses = login (`users.access_code_hash`, fallback
  ke `pin_hash` untuk user lama). PIN = otorisasi transaksi. Beda endpoint.
- Operasi yang mengubah keamanan (ganti PIN/kode akses, ubah limit, ubah email)
  wajib lewat `verification_token` atau OTP asli, dan mencabut sesi lain.
- Cek PIN apa pun harus kena rate limit + lockout + nonce.

### Cache

- **Cek kepemilikan dulu, baca cache kemudian.** Cache struk di-key per
  `transaction_id` saja; membacanya sebelum cek pemilik membocorkan struk
  orang lain.
- Invalidasi cache **semua pihak** yang terdampak, bukan hanya pengirim.

### Environment

- Integrasi eksternal (OCR, Dukcapil, biometrik, object storage, core
  banking, SMS) hanya punya **mock**. Dipilih lewat `APP_ENV`:
  development → mock; selain itu → tolak `503 PROVIDER_NOT_CONFIGURED`.
  **Jangan pernah** memasang mock tanpa gerbang environment.
- **Kecuali push notification**, yang punya transport sungguhan: `FCMPusher`
  dipakai begitu `FCM_CREDENTIALS_FILE` diisi, kosong → `LoggingPusher` di
  development dan `NoopPusher` + peringatan boot di lingkungan lain. Terisi tapi
  tidak sah **menggagalkan boot** — itu sebabnya `router.New` mengembalikan
  `(http.Handler, error)`. Berkas kredensialnya tinggal di `keys/` dan tidak
  pernah ikut commit.
- `otp_debug` di response **hanya** saat `APP_ENV=development`.

### Skema

- Setiap kolom yang ditulis kode **harus** ada di `migrations/`. Pernah ada dua
  endpoint yang 500 permanen karena menulis kolom yang tidak pernah dibuat.
  Verifikasi dengan menjalankan migrasi sungguhan, bukan membaca sekilas.
- Migrasi selalu berpasangan `.up.sql` + `.down.sql`, dan `down` harus benar
  (uji `migrate down 1` lalu `up` lagi).

---

## Testing

- `make test` — `-race`, semua paket.
- Test Redis/idempotency pakai **testcontainers → butuh Docker jalan.** Kalau
  Docker mati, test itu gagal dengan `rootless Docker not found`. Itu masalah
  lingkungan, **bukan** regresi kode — jangan "perbaiki" kode karenanya.
- Domain di-test dengan mock in-memory di paket test yang sama. Menambah method
  ke interface repository berarti **semua** mock di test ikut diperbarui.
- `make test-concurrent` untuk jalur uang — flaky di sana bug sungguhan.

---

## Dokumen

| File | Isi |
|---|---|
| `README.md` | setup, menjalankan, ngrok, daftar endpoint, variabel env |
| `docs/01-API-SPECIFICATION.md` | kontrak API — **update saat mengubah response** |
| `docs/02-DATABASE-SCHEMA.md` | skema |
| `docs/03-REDIS-STRATEGY.md` | desain key + invalidasi |
| `docs/04-SECURITY.md` | model ancaman, alur auth |
| `docs/06-BUKA-REKENING-API-SPEC.md` | kontrak onboarding |
| `docs/postman/` | koleksi Postman — update bersama spec |

Mengubah bentuk response tanpa memperbarui spec + Postman akan memecah build
Android. Keduanya pernah melenceng jauh dari kode; jangan biarkan terulang.

---

## Gaya

- Bahasa Indonesia untuk pesan error yang dilihat nasabah; Inggris untuk komentar kode.
- Komentar menjelaskan **kenapa**, bukan apa. Kalau sebuah baris ada karena
  pernah jadi bug, tulis bug-nya.
- `gofmt` wajib. `slog` terstruktur, bukan `fmt.Println`.
- Jangan pernah mencatat PIN, OTP, token, atau PII ke log.
