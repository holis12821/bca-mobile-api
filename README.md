# BCA Mobile API

Backend API server for the BCA Mobile banking application, built with Go.

## Tech Stack

- **Language:** Go 1.23+
- **Router:** go-chi/chi v5
- **Database:** PostgreSQL 16 with pgx/v5
- **Cache/Session:** Redis 7 (two instances: session + cache)
- **Auth:** JWT RS256 (access/refresh/registration tokens), Argon2id PIN hashing, RSA-OAEP-SHA256 PIN encryption
- **Architecture:** Modular monolith (`internal/domain/*`)

## Project Structure

```
cmd/server/          — Application entrypoint
internal/
  config/            — Environment-based configuration
  domain/            — Business logic (auth, account, transaction, ewallet, qris, registration)
  handler/           — HTTP handlers
  middleware/        — Auth, rate-limit, recovery, logging, CORS
  pkg/               — Shared packages (crypto, apperr, response, pagination, idempotency)
  repository/        — Data access (postgres/, redis/)
  router/            — Route wiring and dependency injection
migrations/          — SQL migration files
scripts/             — Seed data and demo scripts
docs/                — API spec, architecture, and runbook
```

## Quick Start

### Prerequisites

- Go 1.23+
- Docker & Docker Compose (for PostgreSQL and Redis)

### Run

```bash
# Start infrastructure
docker compose up -d

# Run migrations
go run scripts/migrate/main.go

# Seed demo data
go run scripts/seed/main.go

# Start server
go run cmd/server/main.go
```

The server starts at `http://localhost:8080`. Health check: `GET /v1/health`.

### Run Tests

```bash
go test ./... -count=1
```

### Demo Script

```bash
./scripts/demo.sh
```

Runs through 14 API flows (login, profile, balance, transfers, QRIS, e-wallet, etc.) and reports PASS/FAIL.

### Postman

Import both files from `docs/postman/`, pick the `BCA Mobile — Local` environment, and run **Auth → Login (PIN)** first — it stores the token that every other request uses.

The API never accepts a plaintext PIN (RSA-OAEP-SHA256 over `{pin, nonce, ts}`, valid 60 seconds), and Postman cannot do that padding in a pre-request script. Two ways around it:

```bash
make pin PIN=123456          # ciphertext for curl / shell
curl -X POST localhost:8080/v1/dev/encrypt-pin -d '{"pin":"123456"}'
```

`/v1/dev/*` is mounted only when `APP_ENV=development`; the Postman collection calls it automatically. Full walkthrough: [Postman & ngrok](docs/09-POSTMAN-DAN-NGROK.md).

### Menjalankan server

```bash
make dev        # dengan hot reload (air)  — pilihan sehari-hari
make run        # tanpa hot reload (go run)
make stop       # hentikan yang sedang jalan
```

`make dev` dan `make run` menjalankan **server yang sama**, jadi keduanya tidak
bisa hidup bersamaan — yang kedua akan kalah merebut port 8080. Kalau itu
terjadi, Makefile berhenti lebih awal dan menyebut proses yang memegang port
tersebut, alih-alih membiarkan server start lalu mati dengan
`bind: address already in use`.

Butuh instance kedua (misalnya menguji dua versi berdampingan)?

```bash
make run PORT=8081
make stop PORT=8081
```

### Hot reload (air)

Konfigurasinya ada di `.air.toml`. Tanpa file itu air memakai default-nya
(`go build -o ./tmp/main .`) yang membangun paket di **root** — dan root project
ini tidak punya file `.go`, sehingga air gagal dengan `no Go files in ...` lalu
mencoba menjalankan binary yang tidak pernah terbentuk (`exit 127`). Konfig ini
menunjuk `./cmd/server`, mengabaikan `uploads/` (supaya foto KTP yang masuk
tidak me-restart server di tengah request), dan ikut mengawasi `.env` sehingga
mengubah konfigurasi otomatis memuat ulang server.

### Expose to the internet (ngrok)

Untuk menguji dari HP Android atau membagikan API ke tim frontend:

```bash
# terminal 1 — infrastruktur + server
make infra-up && make dev

# terminal 2 — tunnel (biarkan jalan)
make tunnel                       # atau: make tunnel DOMAIN=nama-anda.ngrok-free.app

# terminal 3 — arahkan SIGNALING_BASE_URL ke tunnel
make tunnel-env
```

`make tunnel-env` membaca URL dari agent API ngrok (`127.0.0.1:4040`) dan
menulis `SIGNALING_BASE_URL=wss://<host>` ke `.env`. `air` melihat perubahan itu
dan me-restart server sendiri.

Dua setelan yang wajib benar saat memakai tunnel:

| Variabel | Kenapa |
|---|---|
| `SIGNALING_BASE_URL` | Diserahkan apa adanya ke klien sebagai `signaling_url` untuk WebSocket video call. Kalau masih `ws://localhost:8080`, HP akan menyambung ke dirinya sendiri. Harus `wss://` — `config.Validate` juga menolak start di luar development kalau bukan wss |
| `TRUSTED_PROXIES` | ngrok meneruskan dari loopback. Tanpa `127.0.0.1/32,::1/128`, header `X-Forwarded-For` diabaikan dan **semua** request tercatat sebagai `::1`: satu jatah rate limit untuk seluruh tester, dan audit log mencatat tunnel, bukan penelepon |

`CORS_ALLOWED_ORIGINS` tidak perlu diisi untuk Android — aplikasi native tidak
mengirim header `Origin`. Isi hanya kalau ada frontend web di browser.

Di Postman, set `base_url` ke `https://<host>/v1`.

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/auth/login/pin` | Login with PIN |
| POST | `/v1/auth/login/biometric` | Login with biometric |
| POST | `/v1/auth/logout` | Logout |
| POST | `/v1/auth/token/refresh` | Refresh access token |
| POST | `/v1/auth/pin/verify` | Verify PIN (issues verification token) |
| POST | `/v1/auth/pin/change` | Change the transaction PIN |
| POST | `/v1/auth/access-code/change` | Change the kode akses (login credential) |
| GET,POST | `/v1/auth/biometric/challenge` | Biometric challenge (both verbs served) |
| POST | `/v1/auth/biometric/register` | Register biometric key |
| GET | `/v1/account/profile` | Get user profile |
| POST | `/v1/account/profile/otp` | Request the OTP that authorises a profile change |
| PUT | `/v1/account/profile` | Update profile (email), OTP-verified |
| GET | `/v1/account/balance` | Get account balances |
| GET | `/v1/account/dashboard` | Get dashboard data (balance, promos, quick actions) |
| PUT | `/v1/account/settings` | Update account settings |
| GET | `/v1/account/transaction-limit` | Limits plus today's usage (WIB) |
| PUT | `/v1/account/transaction-limit` | Update limits (requires a CHANGE_LIMIT verification token) |
| POST | `/v1/account/device/push-token` | Register the device's FCM token |
| GET | `/v1/transactions/mutations` | List account mutations |
| GET | `/v1/transactions/history` | List transaction history |
| GET | `/v1/transactions/{id}/receipt` | Transaction receipt (JSON) |
| GET | `/v1/transactions/{id}/receipt/pdf` | Transaction receipt (PDF) |
| POST | `/v1/transfer/inquiry` | Transfer inquiry |
| POST | `/v1/transfer/execute` | Execute transfer |
| GET | `/v1/ewallet/providers` | List e-wallet providers |
| POST | `/v1/ewallet/inquiry` | E-wallet top-up inquiry |
| POST | `/v1/ewallet/topup` | Execute e-wallet top-up |
| POST | `/v1/qris/decode` | Decode QRIS QR code |
| POST | `/v1/qris/pay` | Execute QRIS payment |
| GET | `/v1/notifications` | List notifications |
| PUT | `/v1/notifications/{id}/read` | Mark notification as read |
| PUT | `/v1/notifications/read-all` | Mark all notifications as read |
| POST | `/v1/registration/initiate` | Start registration |
| POST | `/v1/registration/verify-otp` | Verify registration OTP |
| POST | `/v1/registration/upload-document` | Upload KYC document |
| POST | `/v1/registration/complete` | Complete registration |

## Environment Variables

See `internal/config/config.go` for all supported variables. Key ones:

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | (required) | PostgreSQL connection string |
| `REDIS_SESSION_URL` | `localhost:6379/0` | Redis for sessions |
| `REDIS_CACHE_URL` | `localhost:6379/1` | Redis for cache |
| `JWT_PRIVATE_KEY_PATH` | (required) | RSA private key for JWT signing |
| `JWT_PUBLIC_KEY_PATH` | (required) | RSA public key for JWT verification |
| `PIN_PRIVATE_KEY_PATH` | (required) | RSA private key for PIN decryption |
| `PIN_PUBLIC_KEY_PATH` | (required) | RSA public key for PIN encryption |
| `AES_KEY` | (required) | Hex-encoded 32-byte key for PII encryption |
| `UPLOAD_DIR` | `uploads` | Directory for document uploads |
| `APP_ENV` | `development` | `development` also mounts `/v1/dev/*` helpers |
| `SIGNALING_BASE_URL` | `ws://localhost:8080` | Host handed to clients in `signaling_url` |
| `CORS_ALLOWED_ORIGINS` | (empty) | Comma-separated browser origins; empty trusts none |
| `INTERNAL_API_KEY` | _(none)_ | `X-Internal-API-Key` for the CS endpoints. No default: unset means those endpoints deny every request, and the server refuses to start outside development |
| `TRUSTED_PROXIES` | _(none)_ | Comma-separated CIDRs allowed to set `X-Forwarded-For`/`X-Real-IP`. Empty means the headers are ignored and the peer address is used. **Behind ngrok, set `127.0.0.1/32,::1/128`** — otherwise every tunnelled request is attributed to `::1`, so all testers share one rate-limit bucket and the audit trail records the tunnel instead of the caller |
| `LOOKUP_HMAC_SECRET` | _(none)_ | HMAC key for the phone/email lookup hashes. Required to create users — without it registration and onboarding submit both refuse |
| `MAINTENANCE_MODE` | `false` | Served by `GET /v1/health/config`; flips the app's maintenance screen |
| `MIN_APP_VERSION` | `1.0.0` | Served by `GET /v1/health/config`; drives force-update |
| `FEATURE_*` | `true` | Feature flags in `GET /v1/health/config` (`FEATURE_BIOMETRIC_LOGIN`, `FEATURE_QRIS_PAYMENT`, `FEATURE_EWALLET_TOPUP`, `FEATURE_ONBOARDING`) |
| `FEATURE_CARD_SELECTION` | `false` | Turns the Paspor card-selection step on. Off keeps the old flow whole: sessions start at `OCR`, the catalog answers `CARD_CATALOG_EMPTY`, and submit does not demand a card. This is only the default — the Redis key `flag:onboarding:card_selection:enabled` overrides it without a restart |
| `ONBOARDING_CARD_CORE_BANKING_CODES` | (empty) | Maps `card_type` to the core banking card code, e.g. `PASPOR_BLUE:CB-BLUE,PASPOR_GOLD:CB-GOLD`. Verified **at startup** when `FEATURE_CARD_SELECTION` is on: an active catalogued card with no mapping refuses to start, so a hole in the config surfaces at deploy rather than after a nasabah's account exists without a card. The values currently in `.env.example` are dev placeholders |
| `ONBOARDING_CARD_LEGACY_APP_VERSION` | (empty) | `X-App-Version` threshold below which a session with no `card_type` gets the product's default card instead of stopping at `CARD_SELECTION`. Empty disables the fallback — pushing a default card, and its monthly fee, onto a nasabah who never chose it is a product decision |
| `MAX_REQUEST_BODY_BYTES` | `1048576` | Cap on any JSON request body. Upload endpoints set their own larger limit |
| `REQUEST_TIMEOUT` | `30s` | Per-handler timeout |

### Environment-gated behaviour

`APP_ENV=development` is the only setting that enables any of the following.
Everything else refuses rather than pretending:

| Area | development | anything else |
|------|-------------|---------------|
| `/v1/dev/*` helpers | mounted | route does not exist (404) |
| SMS OTP | logged to stdout, and returned as `otp_debug` in the response | not delivered; the code never reaches the log |
| OCR, Dukcapil, biometrics, object storage, core banking | mock implementations | `503 PROVIDER_NOT_CONFIGURED` |
| Push notifications | logged with the resolved device tokens | stored in-app only until an FCM provider is wired in |

## Documentation

**Desain**

- [Architecture Overview](docs/00-ARCHITECTURE-OVERVIEW.md) — lapisan, arah impor, peta paket
- [Database Schema](docs/02-DATABASE-SCHEMA.md)
- [Redis Strategy](docs/03-REDIS-STRATEGY.md) — desain key + invalidasi
- [Security](docs/04-SECURITY.md) — model ancaman, alur auth
- [Ledger & Device Binding](docs/06-LEDGER-AND-DEVICE-BINDING.md) — double-entry, invarian saldo

**Kontrak API**

- [API Specification](docs/01-API-SPECIFICATION.md) — auth, saldo, mutasi, transfer, e-wallet, QRIS
- [Buka Rekening (KYC)](docs/06-BUKA-REKENING-API-SPEC.md) — seluruh `/v1/onboarding/*`
- [Pilih Jenis Kartu Paspor](docs/08-PILIH-KARTU-API-SPEC.md) — sisipan `CARD_SELECTION`
- [Base URL & Endpoint Pendukung](docs/10-BASE-URL-DAN-ENDPOINT.md) — alamat API per lingkungan, health, `/internal/v1`, WebSocket signaling, integrasi Android

**Menjalankan**

- [Project Setup](docs/05-PROJECT-SETUP.md) — dari nol sampai server hidup
- [Setup Runbook](docs/07-SETUP-RUNBOOK.md) — operasi harian, troubleshooting
- [Postman & ngrok](docs/09-POSTMAN-DAN-NGROK.md) — testing dan membuka API ke internet
- [Pemasangan Skill & Katalog Prompt](docs/08-SKILL-SETUP-AND-PROMPTS.md)

Strategi testing belum punya dokumen tersendiri; yang berlaku ada di
[CLAUDE.md](CLAUDE.md) §Testing.