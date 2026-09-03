# 07 — Setup Runbook

> Dari folder kosong sampai demo end-to-end jalan. Scope: **portfolio**, ledger dimiliki sendiri, deploy Docker Compose di satu VM.

---

## Cara Pakai Dokumen Ini

Delapan fase, berurutan. Tiap fase punya **Definition of Done (DoD)** dan **perintah verifikasi**. Jangan lanjut ke fase berikutnya sebelum DoD-nya hijau — tiap fase menjadi fondasi fase setelahnya, dan bug fondasi yang lolos akan muncul lagi tiga fase kemudian dengan wajah yang lebih membingungkan.

Kalau dikerjakan dengan Claude Code CLI, skill `bca-mobile-backend` sudah memuat semua konvensi. Prompt yang efektif per langkah: sebutkan endpoint dan fase-nya, biarkan skill yang menyuplai aturannya.

**Estimasi:** ~10–14 hari kerja fokus untuk satu orang. Kalau butuh demo cepat, lihat [Jalur Minimum Demo](#jalur-minimum-demo) di bagian akhir — 3–4 hari.

### Tiga catatan sebelum mulai

1. **Nama dan branding.** Repo ini memakai nama BCA dan domain `api.bcamobile.id`. Untuk portfolio publik itu berisiko: merek dagang milik pihak lain, dan orang bisa salah mengira ini sistem BCA sungguhan. Sebelum di-publish, ganti ke nama netral (mis. `nusantara-mobile-api`), atau beri disclaimer besar di README bahwa ini latihan arsitektur tanpa afiliasi. Skema, endpoint, dan logikanya tetap sama.

2. **Ledger dimiliki sendiri.** PostgreSQL di sini adalah sumber kebenaran saldo, jadi aturan double-entry di `06-LEDGER-AND-DEVICE-BINDING.md` berlaku penuh. Tetap bungkus akses saldo di balik `Repository` interface — bukan untuk mengejar integrasi core banking, tapi karena itu yang membuat service layer bisa diuji tanpa database.

3. **Yang tidak dikejar di scope ini:** HSM, pentest, PCI-DSS/OJK, DR drill, multi-region. Private key disimpan sebagai file di luar git. Ini pilihan sadar untuk portfolio — dan `README.md` harus menyebutnya, karena "tahu apa yang belum aman" justru bagian dari nilai portfolio-nya.

---

## Peta Fase

| Fase | Isi | Hari |
|---|---|---|
| 0 | Bootstrap: tools, skeleton, infra lokal, migrasi jalan | 1 |
| 1 | Fondasi: config, pool, response, middleware, `/health` | 1–2 |
| 2 | Auth: PIN, JWT, sesi, rate limit, biometrik, lockout | 2–3 |
| 3 | Account: profil, saldo, dashboard, limit, notifikasi, cache | 2 |
| 4 | Transaksi: mutasi, riwayat, transfer, ledger 3-leg, struk | 3 |
| 5 | E-Wallet & QRIS | 1–2 |
| 6 | Registrasi & KYC stub | 1 |
| 7 | Hardening & rilis: test, seed, Docker, deploy, README | 2 |

---

## Fase 0 — Bootstrap

### 0.1 Tools

```bash
go version                      # butuh 1.23+
docker --version && docker compose version
psql --version                  # opsional, untuk inspeksi manual

go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
go install github.com/air-verse/air@latest

export PATH=$PATH:$(go env GOPATH)/bin
migrate -version && golangci-lint --version && air -v
```

### 0.2 Skeleton

```bash
mkdir bca-mobile-api && cd bca-mobile-api
git init
go mod init github.com/<user>/bca-mobile-api

mkdir -p cmd/server
mkdir -p internal/{config,handler,middleware,router}
mkdir -p internal/domain/{auth,account,transaction,ewallet,notification}
mkdir -p internal/repository/{postgres,redis}
mkdir -p internal/pkg/{crypto,validator,response,pagination,idempotency}
mkdir -p migrations scripts deployments docs keys
```

**`.gitignore` — tulis ini sebelum commit pertama, bukan sesudah:**

```gitignore
keys/
.env
bin/
tmp/
coverage.out
coverage.html
*.pem
```

> Kunci privat yang sempat masuk satu commit tetap ada di histori git selamanya, meskipun dihapus di commit berikutnya. Urutan ini bukan formalitas.
ivas66r
### 0.3 Dependencies

```bash
go get github.com/go-chi/chi/v5 github.com/go-chi/cors
go get github.com/jackc/pgx/v5 github.com/jackc/pgx/v5/pgxpool
go get github.com/redis/go-redis/v9
go get github.com/golang-jwt/jwt/v5
go get github.com/go-playground/validator/v10
go get github.com/caarlos0/env/v11
go get golang.org/x/crypto golang.org/x/sync
go get github.com/google/uuid
go get github.com/stretchr/testify
go get github.com/testcontainers/testcontainers-go/modules/postgres
go get github.com/testcontainers/testcontainers-go/modules/redis
```

Dua tambahan di luar daftar `05-PROJECT-SETUP`:

- `golang.org/x/sync/semaphore` — membatasi Argon2id konkuren (§3 skill). 64 MB × 4 thread per login; 100 login barengan = 6,4 GB.
- `testcontainers-go` menggantikan `go-sqlmock`. Logika transfer bergantung pada `SERIALIZABLE`, row lock, dan retry `40001` — hal-hal yang **tidak** direproduksi oleh mock SQL. Menguji ledger dengan mock berarti menguji mock-nya, bukan kodenya.

### 0.4 Infra lokal & migrasi

Salin `deployments/docker-compose.yml` dari `05-PROJECT-SETUP`, lalu:

```bash
docker compose -f deployments/docker-compose.yml up -d
```

Taruh migrasi `000001`–`000008` dari `02-DATABASE-SCHEMA.md` ke `migrations/`, lalu tambahkan `000009_ledger_and_device_binding.{up,down}.sql`.

```bash
make keys                       # RSA untuk JWT + PIN
cp .env.example .env
make migrate-up
```

### 0.5 Redis: dua instance

`03-REDIS-STRATEGY` menggambarkan Redis Cluster dengan DB 0/1/2 dan eviction policy per-DB. Keduanya tidak bisa dijalankan (Cluster hanya punya DB 0; `maxmemory-policy` berlaku se-instance). Ganti service `redis` di compose menjadi dua:

```yaml
  redis-session:
    image: redis:7-alpine
    command: >
      redis-server --requirepass localdev_redis_123
      --maxmemory-policy noeviction
      --appendonly yes --appendfsync everysec
    ports: ["6379:6379"]
    volumes: [redis_session_data:/data]
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "localdev_redis_123", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis-cache:
    image: redis:7-alpine
    command: >
      redis-server --requirepass localdev_redis_123
      --maxmemory 512mb --maxmemory-policy allkeys-lru
    ports: ["6380:6379"]
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "localdev_redis_123", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5
```

Tambahkan ke `.env.example`:

```bash
REDIS_SESSION_HOST=localhost
REDIS_SESSION_PORT=6379
REDIS_CACHE_HOST=localhost
REDIS_CACHE_PORT=6380
REDIS_PASSWORD=localdev_redis_123
```

Cache boleh hilang. Sesi tidak.

### DoD Fase 0

```bash
docker compose -f deployments/docker-compose.yml ps      # 3 service healthy
make migrate-status                                       # versi 9
psql "$DB_URL" -c "SELECT count(*) FROM accounts WHERE owner_type='INTERNAL';"   # 64
psql "$DB_URL" -f scripts/000009_verify.sql | grep -c FAIL                       # 0
go build ./... && git log --oneline | head -1
```

---

## Fase 1 — Fondasi

Tidak ada endpoint bisnis di fase ini. Yang dibangun adalah hal-hal yang salah kalau ditambal belakangan.

### 1.1 Urutan pengerjaan

1. `internal/config/config.go` — sesuai `05-PROJECT-SETUP`, plus `RedisSession` dan `RedisCache` terpisah.
2. `internal/repository/postgres/pool.go` — `pgxpool` + `Ping` saat start. Gagal connect = `os.Exit(1)`, bukan retry diam-diam.
3. `internal/repository/redis/client.go` — dua client, dua `Ping`.
4. `internal/pkg/response/response.go` — envelope dari `05-PROJECT-SETUP`.
5. `internal/pkg/apperr/` — **tambahan, tidak ada di spec.** Satu tempat memetakan domain error → `(httpStatus, code, message)`.
6. Middleware: `requestid`, `logging`, `recovery`, `security`, `cors`, `timeout`.
7. `internal/router/router.go`.
8. `internal/handler/health_handler.go`.

### 1.2 Kenapa `apperr` sejak awal

Tanpa satu tempat pemetaan, tiap handler menulis `switch` sendiri, dan tabel error code di `01-API-SPECIFICATION` pelan-pelan menyimpang per endpoint. Bentuknya:

```go
package apperr

type Error struct {
    Status  int
    Code    string
    Message string   // Bahasa Indonesia, tetap per code, tidak pernah diisi input user
    Details any
}

var (
    InvalidPIN     = Error{401, "AUTH_INVALID_PIN",     "Kode akses salah. Silakan coba lagi.", nil}
    AccountLocked  = Error{423, "AUTH_ACCOUNT_LOCKED",  "Akun terkunci karena terlalu banyak percobaan.", nil}
    InquiryExpired = Error{422, "INQUIRY_EXPIRED",      "Sesi transaksi sudah kedaluwarsa. Silakan ulangi.", nil}
    // ... satu entry untuk setiap code di §2 skill
)

func From(err error) Error   // errors.Is/As → Error, default INTERNAL_ERROR
```

Satu aturan yang gampang dilanggar: `INTERNAL_ERROR` **tidak pernah** membawa pesan error asli ke client. Log-nya lengkap, response-nya generik.

### 1.3 `/health` dipecah dua

```go
// Liveness — TIDAK di-cache. Ping DB + Redis, timeout 1 detik.
// Config  — di-cache 5 menit di cache:health:config.
```

Spek meng-cache seluruh payload 5 menit, sehingga `/health` bisa melaporkan `database: "ok"` selama lima menit setelah database mati. Untuk portfolio pun ini layak dibenahi: reviewer teknis sering membuka endpoint health duluan.

### DoD Fase 1

```bash
make dev
curl -s localhost:8080/v1/health | jq
docker compose stop postgres && curl -s localhost:8080/v1/health | jq '.data.database'   # "error"
docker compose start postgres

curl -si localhost:8080/v1/health | grep -i 'x-request-id\|strict-transport\|x-frame'
curl -si localhost:8080/v1/nope | jq '.error.code'      # "NOT_FOUND", envelope tetap konsisten
make check
```

---

## Fase 2 — Autentikasi

Fase terpanjang dan paling banyak jebakan. Kerjakan berurutan; tiap langkah bisa diverifikasi sendiri.

### 2.1 Kripto dulu (`internal/pkg/crypto/`)

| File | Isi | Jangan salah |
|---|---|---|
| `hash.go` | Argon2id: time=3, mem=64MB, threads=4, keyLen=32, salt=16 | Bungkus dengan `semaphore.Weighted(min(NumCPU,8))` |
| `aes.go` | AES-256-GCM, nonce di-prepend | Nonce baru tiap enkripsi, tidak pernah dipakai ulang |
| `hmac.go` | HMAC-SHA256 untuk `phone_hash` / `email_hash` | Lowercase + trim sebelum hash; bukan SHA-256 polos |
| `rsa.go` | RSA-2048 **OAEP-SHA256** untuk PIN | Bukan PKCS#1 v1.5 |
| `jwt.go` | RS256, claim `sub/sid/did/typ`, generate + verify | Verify **wajib** cek `typ` |

Payload PIN terenkripsi berisi `{"pin":"123456","nonce":"<uuid>","ts":<unix>}`. Tolak kalau `ts` meleset >60 detik atau nonce sudah pernah dipakai (`pin_nonce:{nonce}`, TTL 120s). Tanpa ini, satu `pin_encrypted` yang tersadap bisa diputar ulang selamanya.

Tulis unit test kripto **sekarang**, bukan nanti. Semuanya fungsi murni — termurah untuk diuji, termahal kalau salah.

### 2.2 Rate limit & lockout

Sliding window sorted set (`ZREMRANGEBYSCORE → ZCARD → ZADD → EXPIRE` dalam satu pipeline).

Tiga layer yang wajib ada di fase ini:

| Layer | Limit |
|---|---|
| Login per device | 5 / 15 menit |
| **Login per IP** | 20 / 15 menit |
| PIN verify per user | 5 / 15 menit, berbagi counter lockout dengan login |

Layer per-IP bukan opsional. Karena login me-resolve user dari `device_id`, penyerang yang menebak `device_id` orang lain bisa mengunci akun korban dengan 5× PIN salah. Per-IP limit yang membatasi kerusakannya.

Redis mati → auth endpoint **fail-closed** (tolak). Endpoint read-only boleh fail-open.

### 2.3 Login PIN

Ikuti tujuh langkah di §3 skill. Tiga yang paling sering salah:

- **Langkah 5:** `UPDATE users SET failed_pin_attempts = failed_pin_attempts + 1 ... RETURNING failed_pin_attempts`, lalu bercabang pada nilai yang **dikembalikan**. Membaca `user.FailedPINAttempts + 1` dari memori adalah race yang meloloskan percobaan ke-6 dan ke-7.
- **Langkah 6:** device tak dikenal harus mengembalikan bentuk **dan waktu** respons yang sebanding dengan PIN salah — lakukan verifikasi Argon2 terhadap hash dummy. Kalau tidak, selisih waktu respons membocorkan device mana yang terdaftar.
- **Langkah 2:** `device_id` tidak ada baris aktif → `403 AUTH_DEVICE_NOT_RECOGNIZED`.

### 2.4 Sesi & refresh rotation

Simpan sesi di PostgreSQL (`sessions`) **dan** Redis (`session:{user_id}:{device_id}`, TTL 15 menit). Tambah `sessions:user:{user_id}` sebagai Set berisi device_id — dipakai untuk logout-all, supaya tidak perlu `SCAN`.

Rotation: terbitkan pasangan baru, lalu tandai hash lama `refresh:revoked:{hash}` dengan TTL sisa umurnya. **Jangan hanya dihapus** — key yang hilang tidak bisa dibedakan dari key yang kedaluwarsa, dan reuse detection jadi mustahil. Kalau hash yang masuk ditemukan di `revoked`: cabut **semua** sesi user, audit `SECURITY_SUSPICIOUS_LOGIN`, kirim push.

### 2.5 Biometrik

Challenge 32 byte acak, `bio_challenge:{challenge_id}` TTL 60 detik. Konsumsi dengan `GETDEL` — atomik. Challenge yang dibaca dulu lalu dihapus belakangan bisa diputar ulang. Verifikasi bahwa `key_id` milik baris device yang sama dengan challenge dan request.

### 2.6 Audit

Buffered channel + worker pool tetap, bukan `go func()` per entry. Fallback ke `slog` kalau insert DB gagal **atau** buffer penuh. Audit gagal tidak boleh menggagalkan transaksi; audit hilang diam-diam juga tidak boleh.

### DoD Fase 2

```bash
# login sukses
curl -s -XPOST localhost:8080/v1/auth/login/pin \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"...","pin_encrypted":"...","device_info":{...}}' | jq

# 5x PIN salah → 423, dan yang ke-6 tetap 423 walau PIN benar
for i in $(seq 1 6); do curl -s -o /dev/null -w "%{http_code}\n" -XPOST .../login/pin -d '<pin salah>'; done

# refresh token dipakai di endpoint access token → 401 (cek typ)
# refresh token lama dipakai ulang setelah rotasi → semua sesi tercabut
psql "$DB_URL" -c "SELECT action FROM audit_logs ORDER BY created_at DESC LIMIT 5;"
```

Uji juga: challenge biometrik dipakai dua kali → yang kedua ditolak.

---

## Fase 3 — Account & Dashboard

### 3.1 Urutan

1. Enkripsi/dekripsi PII di repository layer, bukan di service. Service melihat plaintext; database menyimpan ciphertext.
2. `GET /account/profile` — cache 10 menit.
3. `GET /account/balance` — cache 30 detik.
4. `GET /account/dashboard` — cache 60 detik, agregasi saldo + promo + jumlah notif.
5. `PUT /account/settings`, `PUT /account/transaction-limit`.
6. Notifikasi: list, mark-read, mark-all-read.

### 3.2 Aturan cache yang gampang dilanggar

- **Cursor wajib masuk cache key.** `cache:notif:{user_id}` untuk endpoint paginated berarti halaman 2 mengembalikan isi halaman 1. Pakai `cache:notif:{user_id}:v{n}:{cursor_hash}`.
- **Invalidasi pakai version counter (`cachever:*`), bukan `SCAN` pattern.** `SCAN` di keyspace produksi itu O(N).
- **Setiap penambahan cache ditulis bersama jalur invalidasinya, dalam commit yang sama.** Cache tanpa invalidasi adalah bug yang baru muncul saat demo.

### 3.3 Query yang menyentuh nomor rekening

Filter `owner_type = 'CUSTOMER'` secara eksplisit. Query yang di-scope `WHERE user_id = $1` aman otomatis, tapi lookup **berdasarkan nomor rekening tidak**: `9902000000` adalah shard settlement, dan tanpa filter itu nasabah bisa transfer ke ledger internal bank.

### DoD Fase 3

```bash
curl -s -H "$AUTH" localhost:8080/v1/account/dashboard | jq
psql "$DB_URL" -c "SELECT phone_encrypted FROM users LIMIT 1;"   # bytea, bukan teks terbaca
curl -s -H "$AUTH" '.../notifications?limit=2' | jq '.data.notifications[].id'          # halaman 1
curl -s -H "$AUTH" '.../notifications?limit=2&cursor=<cursor>' | jq '.data.notifications[].id'  # harus beda
curl -s -XPUT -H "$AUTH" .../account/transaction-limit -d '{"limits":{"ewallet_daily":999000000}}' | jq '.error.code'  # VALIDATION_ERROR
```

---

## Fase 4 — Transaksi

Inti aplikasi. Kerjakan `transfer/execute` **paling akhir** di fase ini, setelah mutasi dan inquiry sudah benar.

### 4.1 Urutan

1. `GET /transactions/mutations` — keyset pagination.
2. `GET /transactions/history`.
3. `GET /transfer/recent`.
4. `POST /transfer/inquiry`.
5. `internal/pkg/idempotency/` — guard `SETNX`.
6. `POST /transfer/execute` — resep 17 langkah §5 skill.
7. `GET /transactions/{id}/receipt` + `/pdf`.

### 4.2 Keyset pagination

Tuple cursor harus persis sama dengan `ORDER BY`, dengan `id` sebagai tiebreaker:

```sql
WHERE account_id = $1
  AND (transaction_date, created_at, id) < ($2, $3, $4)
ORDER BY transaction_date DESC, created_at DESC, id DESC
LIMIT $5 + 1                  -- ambil limit+1 untuk menghitung has_more
```

Cursor `{id, date}` seperti di spec akan menjatuhkan baris ketika beberapa mutasi berbagi tanggal yang sama.

### 4.3 Idempotency

Key dibaca dari header `X-Idempotency-Key` saja. `SETNX` dulu, **jangan `GET` dulu** — handler di `04-SECURITY` mengembalikan literal string `PROCESSING` sebagai body 200 saat ada replay konkuren.

```go
key := "idem:" + userID + ":" + idemKey    // selalu di-scope per user
ok, _ := rdb.SetNX(ctx, key, "PROCESSING", 30*time.Second).Result()
if !ok {
    v, _ := rdb.Get(ctx, key).Result()
    if v == "PROCESSING" { return conflict409 }
    return replay(v)
}
defer func() { if failed { rdb.Del(ctx, key) } }()
```

Setelah TTL 24 jam habis, replay menabrak `uq_txn_user_idempotency`. Pada unique violation itu: **ambil transaksi yang sudah ada dan kembalikan responsnya**, bukan 409.

### 4.4 Transfer execute — yang paling sering salah

| Jebakan | Yang benar |
|---|---|
| Percaya `destination_account` dari body | Derive dari `inquiry_id`; body hanya cross-check, mismatch → `422 INQUIRY_MISMATCH` |
| Lock hanya rekening sumber | Lock **semua** rekening terdampak, `ORDER BY id` |
| `SERIALIZABLE` tanpa retry | Retry `40001`: 3 percobaan, backoff berjitter (~10/30/90 ms) |
| Dua mutation untuk semua rail | Rail keluar butuh **tiga** leg (§4.5) |
| `CURRENT_DATE` untuk `transaction_date` | Hitung tanggal WIB di aplikasi; kolomnya sudah tidak punya default |
| Uang sebagai `float64` | `int64` satuan minor |

### 4.5 Posting ledger

```
Transfer internal:  DEBIT sumber (amount+fee) · CREDIT tujuan (amount) · CREDIT fee-income (fee, kalau >0)
Transfer eksternal: DEBIT sumber (amount+fee) · CREDIT settlement TRANSFER_EXTERNAL (amount) · CREDIT fee-income (fee)
```

Akun lawan: `settlement_account_id(rail, transaction_id)`. Nomor referensi: `next_reference_number(<tanggal WIB>)`.

### 4.6 Panggilan provider

Untuk rail asinkron: insert `PENDING` → COMMIT → panggil provider → update `SUCCESS`/`FAILED`. **Jangan pernah** menahan transaksi `SERIALIZABLE` terbuka selama panggilan jaringan ke pihak ketiga.

### DoD Fase 4

```bash
# transfer internal berhasil, saldo penerima berubah, dua mutasi
# key sama diulang → response identik + X-Idempotent-Replayed: true, tetap satu baris
# 20 transfer konkuren melebihi saldo → saldo tidak pernah negatif
psql "$DB_URL" -c "SELECT * FROM v_unbalanced_transactions;"     # kosong
psql "$DB_URL" -c "SELECT * FROM v_ledger_reconciliation;"       # kosong
go test -race ./internal/domain/transaction/...
```

---

## Fase 5 — E-Wallet & QRIS

Polanya identik dengan transfer, jadi fase ini cepat kalau Fase 4 benar.

- `GET /ewallet/providers` — cache 1 jam.
- `POST /ewallet/inquiry` — validasi nomor + hitung biaya, simpan inquiry.
- `POST /ewallet/topup` — request-nya **hanya** `idempotency_key` + `inquiry_id` + `verification_token`. Ini bentuk yang benar; `transfer/execute` seharusnya mengikuti pola ini, bukan sebaliknya.
- `POST /qris/decode` — parse payload EMVCo. Untuk portfolio, dukung field wajib (merchant name, city, amount, amount-fixed flag) dan tolak sisanya dengan jelas.
- `POST /qris/pay`.

Stub provider e-wallet: satu `EWalletProvider` interface dengan implementasi fake yang mengembalikan nama pemilik dari tabel seed, plus jalur error yang bisa dipicu (nomor tertentu → `EWALLET_PROVIDER_DOWN`). Jalur gagal yang bisa didemokan lebih berharga daripada stub yang selalu sukses.

### DoD Fase 5

Top-up Rp 100.000 (fee 1.000) menghasilkan **tiga** mutasi berjumlah nol, saldo nasabah turun 101.000, dan kedua view rekonsiliasi tetap kosong.

---

## Fase 6 — Registrasi & KYC Stub

- `POST /registration/initiate` → OTP (di dev: log OTP-nya, jangan kirim SMS).
- `POST /registration/verify-otp`.
- `POST /registration/upload-document` — multipart. Validasi content-type **dari isi file**, bukan dari header. Batasi ukuran. Simpan ke disk lokal atau MinIO, jangan ke database.
- `POST /registration/complete` — set PIN awal, buat user + rekening. Trigger `000009` otomatis mengisi limit default.

Registration token: JWT ber-scope pendek (`typ: "registration"`, TTL 30 menit) yang hanya diterima oleh endpoint registrasi. Spec menyebut "Registration Token" tanpa mendefinisikannya.

> Contoh NIK di spec ditulis tersamar (`3201****0001`). Untuk KYC itu keliru — input harus NIK lengkap; penyamaran hanya terjadi saat menampilkan kembali.

---

## Fase 7 — Hardening & Rilis

### 7.1 Test

```bash
go test -race -cover ./...
```

Target realistis untuk portfolio: **domain/service ≥80%**, sisanya apa adanya. Coverage total 90% bukan tujuan; yang dinilai adalah *apa* yang diuji.

Yang wajib ada:

- Unit: seluruh cabang `crypto`, `apperr`, `pagination`, `idempotency`.
- Integration (testcontainers): transfer sukses/gagal, saldo tidak cukup, limit terlampaui, replay idempotency, inquiry mismatch, konkurensi.
- Satu test konkurensi yang benar-benar menjalankan N goroutine transfer dari satu rekening dan membuktikan saldo tidak pernah negatif. Ini satu test yang paling banyak dilihat reviewer.

### 7.2 Seed data

`scripts/seed/main.go` yang membuat: 3 user dengan PIN diketahui, masing-masing 1–2 rekening bersaldo, riwayat mutasi 30 hari, beberapa promo dan notifikasi, favorit transfer. Demo yang meyakinkan butuh data yang terlihat hidup.

### 7.3 Dockerfile

```dockerfile
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
```

Distroless + nonroot: image kecil, tanpa shell, dan menunjukkan kesadaran supply-chain — murah dilakukan, terlihat oleh reviewer.

### 7.4 Compose produksi

`deployments/docker-compose.prod.yml`: `api`, `postgres`, `redis-session`, `redis-cache`, `nginx`. Aturannya:

- Hanya nginx yang mem-publish port ke host. Postgres dan Redis **tidak** — cukup di network internal.
- Semua secret dari file `.env` di luar git, bukan literal di compose.
- `restart: unless-stopped` di semua service.
- Migrasi dijalankan sebagai langkah terpisah sebelum `api` naik, bukan di dalam entrypoint aplikasi. Aplikasi yang bermigrasi sendiri saat start akan saling berebut begitu ada dua replika.

### 7.5 VM

```
1. Ubuntu 22.04/24.04, buat user non-root, nonaktifkan SSH password login.
2. ufw: izinkan 22, 80, 443 saja.
3. Install docker + compose plugin.
4. Clone repo, buat .env produksi (password baru, bukan salinan localdev_*).
5. make keys  → chmod 600 keys/*.pem
6. docker compose -f deployments/docker-compose.prod.yml run --rm migrate
7. docker compose -f deployments/docker-compose.prod.yml up -d
8. certbot untuk TLS (Let's Encrypt); paksa TLS 1.2+, redirect 80 → 443.
9. Cron: pg_dump harian terenkripsi + tes restore mingguan.
```

Restore yang tidak pernah diuji bukan backup — dan mengujinya sekali sudah cukup untuk portfolio.

### 7.6 Observability minimum

- Log JSON via `slog`, `request_id` di setiap baris.
- `/metrics` Prometheus: latency per route, rate error, hit rate cache, kedalaman buffer audit.
- Satu cron 5 menit yang query `v_unbalanced_transactions` dan `v_ledger_reconciliation`, log level ERROR kalau tidak kosong.

Yang terakhir itu yang paling penting. Untuk sistem yang memegang uang, invariant yang diperiksa terus-menerus lebih berharga daripada dashboard yang cantik.

### 7.7 README

Bagian yang benar-benar dibaca orang:

1. Apa ini, dan **disclaimer** bahwa ini latihan arsitektur tanpa afiliasi dengan bank mana pun.
2. Diagram arsitektur (satu gambar).
3. Quick start: `make setup && make dev`, lalu 3 perintah curl yang langsung berhasil.
4. Keputusan desain dan alasannya — ledger tiga-leg, device binding, idempotency per-user, sharding settlement. **Ini bagian paling bernilai**; sedikit portfolio yang menjelaskan *kenapa*.
5. Apa yang sengaja tidak dikerjakan: HSM, pentest, compliance, HA. Ditulis eksplisit, bukan disembunyikan.

---

## Demo Script End-to-End

Simpan sebagai `scripts/demo.sh`. Ini yang dijalankan saat menunjukkan aplikasinya.

```
 1. GET  /v1/health                          → service ok, feature flags
 2. POST /v1/auth/login/pin                  → access + refresh token
 3. GET  /v1/account/dashboard               → saldo, promo, jumlah notifikasi
 4. GET  /v1/transactions/mutations?period=LAST_7_DAYS
 5. GET  /v1/transactions/mutations?cursor=<dari langkah 4>   → halaman 2 berbeda
 6. POST /v1/transfer/inquiry                → nama pemilik rekening tujuan
 7. POST /v1/auth/pin/verify                 → verification_token
 8. POST /v1/transfer/execute                → 201, nomor referensi
 9. POST /v1/transfer/execute (key sama)     → response identik + X-Idempotent-Replayed
10. GET  /v1/account/balance                 → saldo sudah berkurang (cache ter-invalidasi)
11. GET  /v1/transactions/{id}/receipt       → struk
12. POST /v1/ewallet/inquiry → /v1/ewallet/topup  → 3 leg
13. SELECT * FROM v_unbalanced_transactions  → kosong
14. 5x PIN salah                             → 423 + locked_until
```

Langkah 9, 13, dan 14 adalah yang membedakan demo ini dari CRUD biasa. Jangan dilewat.

---

## Jalur Minimum Demo

Kalau butuh sesuatu yang bisa ditunjukkan dalam 3–4 hari, kerjakan ini saja dan katakan terus terang di README bahwa sisanya belum:

```
Fase 0 penuh
Fase 1 penuh
Fase 2: login PIN + JWT + sesi + rate limit          (lewati biometrik, ganti PIN, registrasi)
Fase 3: dashboard + balance + profile                 (lewati limit, notifikasi)
Fase 4: mutations + inquiry + execute + idempotency   (lewati riwayat, struk PDF)
Fase 7: seed + Dockerfile + README + demo.sh
```

Transfer yang benar secara ledger, idempotent, dan tahan konkurensi jauh lebih berkesan daripada dua puluh endpoint yang masing-masing setengah jadi.

---

## Checklist Rilis

Sebelum repo di-publish:

```
Kode
  [ ] make check hijau (lint + vet + test -race)
  [ ] Tidak ada secret di histori git: git log -p | grep -iE 'BEGIN.*PRIVATE KEY|password.*='
  [ ] .env dan keys/ ada di .gitignore sejak commit pertama
  [ ] Tidak ada endpoint debug, tidak ada handler yang berisi TODO panic

Fungsional
  [ ] scripts/demo.sh jalan dari awal sampai akhir di environment bersih
  [ ] Kedua view rekonsiliasi kosong setelah demo penuh
  [ ] Test konkurensi lewat 20 kali berturut-turut: go test -race -count=20 -run Concurrent ./...

Deploy
  [ ] docker compose prod naik dari nol di VM bersih
  [ ] TLS aktif, HTTP redirect ke HTTPS
  [ ] Postgres dan Redis tidak terekspos ke publik: nmap -p 5432,6379 <ip> → filtered
  [ ] Restore dari backup sudah diuji sekali

Dokumen
  [ ] README memuat disclaimer non-afiliasi
  [ ] Keputusan desain dan alasannya tertulis
  [ ] Batasan yang diketahui ditulis eksplisit
  [ ] Docs 00-07 konsisten dengan kode yang benar-benar ada
```

Baris terakhir itu yang paling sering meleset. Dokumen yang menjanjikan sesuatu yang tidak ada di kode lebih merugikan daripada dokumen yang lebih pendek.
