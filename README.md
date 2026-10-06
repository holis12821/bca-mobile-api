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
- Docker & Docker Compose (PostgreSQL, two Redis instances, and coturn for the
  video-call STUN/TURN)

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
| GET | `/v1/auth/pin/public-key` | Public key + key_id for `pin_encrypted` (public, cacheable) |
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
| GET | `/v1/account/cards` | Cards the customer owns (empty array, never 404) |
| PUT | `/v1/account/cards/{card_id}/settings` | Toggle debit-online / international; absent fields are left alone |
| POST | `/v1/account/cards/{card_id}/block` | Block a card (requires a BLOCK_CARD verification token) |
| POST | `/v1/account/cards/{card_id}/replacement` | Request a replacement (REPLACE_CARD token + `X-Idempotency-Key`) |
| GET | `/v1/content/help-center` | FAQ — **no auth** |
| GET | `/v1/content/contact-cs` | Halo BCA contact details — **no auth** |
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
| POST | `/v1/registration/initiate` | **Deprecated** — use `/v1/onboarding/*` |
| POST | `/v1/registration/verify-otp` | **Deprecated** — use `/v1/onboarding/*` |
| POST | `/v1/registration/upload-document` | **Deprecated** — use `/v1/onboarding/*` |
| POST | `/v1/registration/complete` | **Deprecated** — use `/v1/onboarding/*` |

### Operator / CS endpoints (`/internal/v1`)

Behind three guards: `X-Internal-API-Key` (which system), `X-Agent-Employee-ID` +
`X-Agent-API-Key` (which agent, Argon2id against `cs_agents`), and the agent's
`scopes` (what they may do). Full contract in `docs/01-API-SPECIFICATION.md` §11;
client-side guidance in `.claude/skills/cs-desktop-api-integration/SKILL.md`.

| Method | Path | Scope | Description |
|--------|------|-------|-------------|
| GET | `/internal/v1/onboarding/sessions` | `VIDEO_CALL` | Onboarding sessions for the CS monitoring screen — **carries no PII** |
| GET | `/internal/v1/onboarding/sessions/{session_id}` | `CUSTOMER_PII` | One session plus masked personal data; writes `CS_SESSION_VIEWED` to the audit trail |
| GET | `/internal/v1/customers?q=` | `CUSTOMER_PII` | Find a customer by **exact** account number or phone. No name search, no partial match |
| GET | `/internal/v1/customers/{user_id}` | `CUSTOMER_PII` | Customer profile (masked; **no balance**); writes `cs_access_logs` |
| POST,GET | `/internal/v1/tickets` | `TICKET` | Create / list service tickets |
| GET,PATCH | `/internal/v1/tickets/{ticket_number}` | `TICKET` | Ticket detail / update. Addressed by `TKT-YYYYMMDD-NNNNNN`, not UUID |
| POST | `/internal/v1/tickets/{ticket_number}/notes` | `TICKET` | Add a follow-up note |
| GET,PUT | `/internal/v1/cards`, `/cards/{card_type}`, `/products/{product_type}/cards/{card_type}` | `CARD_ADMIN` | Paspor card catalogue admin |

> **Breaking change:** `/internal/v1/cards` used to accept `X-Internal-API-Key`
> alone. It now also requires the two agent headers, and the agent's row in
> `cs_agents` must carry the `CARD_ADMIN` scope. Callers sending only the system
> key get `403`.

Customer-facing video call rescheduling (serves the Android "Jadwalkan Panggilan
Nanti" button, which was disabled until these existed):

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/onboarding/video-call/schedule` | Book a slot. `scheduled_at` must carry a timezone offset; 06:00–22:00 WIB, 15 min–7 days ahead |
| GET | `/v1/onboarding/video-call/schedule?session_id=` | Active booking, or `schedule: null` — **not 404** |
| DELETE | `/v1/onboarding/video-call/schedule?session_id=` | Cancel, so the customer can rebook |

Booking does **not** put the customer in the queue. It is a promise, not a place —
they still call `/video-call/queue` when the time comes.

Development agent credentials (`make seed`, gated on `APP_ENV=development`):

| `X-Agent-Employee-ID` | `X-Agent-API-Key` | Scopes |
|---|---|---|
| `CS-1042` | `dev-agent-key` | `VIDEO_CALL` |
| `OPS-2001` | `dev-cardadmin-key` | `CARD_ADMIN` |
| `SPV-3001` | `dev-spv-key` | `VIDEO_CALL`, `CUSTOMER_PII`, `TICKET` |

`/v1/registration/*` and `/v1/onboarding/*` describe the same feature with two
contracts. `/v1/onboarding/*` is the one the Android app implements and the one
that is maintained; the registration family still works for older builds and
every response carries `Deprecation: true` plus a `Link` to its successor. New
integrations: use `/v1/onboarding/*`.

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
| `PIN_KEY_ID` | `pin-key-v1` | Names the active PIN key pair. Published by `GET /v1/auth/pin/public-key` and compared against the client's `encryption_key_id`: a mismatch answers `422 AUTH_PIN_KEY_UNKNOWN` instead of "wrong PIN". Bump it whenever the key pair is rotated — `make pin-public-key` prints the PEM to hand to the Android build |
| `STUN_URLS` | (empty) | Comma-separated STUN URLs served in `ice_servers` on the video-call queue response. `.env.example` points at the local `coturn` (`stun:localhost:3478`), which `make infra-up` starts |
| `TURN_URLS` | (empty) | Comma-separated TURN URLs. Without `TURN_USERNAME` + `TURN_CREDENTIAL` they are **ignored and logged as an error** — a TURN server with no credentials refuses every allocation. Empty `ice_servers` means video call fails behind strict NAT, which is most mobile networks. From a real phone, replace `localhost` with this machine's LAN address |
| `TURN_USERNAME` | (empty) | TURN credential username (`bcadev` for the local coturn) |
| `TURN_CREDENTIAL` | (empty) | TURN credential secret (local coturn: `localdev_turn_123`, dev-only) |
| `TURN_CREDENTIAL_TTL` | `12h` | Reported lifetime of the TURN credential |
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
| `FEATURE_CARD_SELECTION` | `false` | Turns the Paspor card-selection step on. Off keeps the old flow whole: sessions start at `OCR`, the catalog answers `CARD_CATALOG_EMPTY`, and submit does not demand a card. This is only the default — the Redis key `flag:onboarding:card_selection:enabled` overrides it without a restart. `.env.example` sets it to `true`: the catalog now carries real numbers (migration `000022`), so the screen has something to show. The code default stays `false` so a deployment that has not migrated cannot serve an empty catalog as a feature |
| `ONBOARDING_CARD_CORE_BANKING_CODES` | (empty) | Maps `card_type` to the core banking card code, e.g. `PASPOR_BLUE:CB-BLUE,PASPOR_GOLD:CB-GOLD`. Verified **at startup** when `FEATURE_CARD_SELECTION` is on: an active catalogued card with no mapping refuses to start, so a hole in the config surfaces at deploy rather than after a nasabah's account exists without a card. The values currently in `.env.example` are dev placeholders |
| `ONBOARDING_CARD_LEGACY_APP_VERSION` | (empty) | `X-App-Version` threshold below which a session with no `card_type` gets the product's default card instead of stopping at `CARD_SELECTION`. Empty disables the fallback — pushing a default card, and its monthly fee, onto a nasabah who never chose it is a product decision |
| `SMS_PROVIDER` | (empty) | OTP delivery transport. `twilio` is the only one implemented. Empty is allowed **only** in development (the gateway logs the code); outside development the boot is refused, because a process with no provider generates, stores and audits every OTP while no nasabah can get past `OTP_VERIFY`. An unknown value also fails the boot rather than falling back to silence |
| `SMS_ACCOUNT_SID` | (empty) | Twilio account SID (`AC…`), used as the basic-auth user. An API key SID (`SK…`) is rejected at startup |
| `SMS_AUTH_TOKEN` | (empty) | Twilio auth token. A password — never logged, never echoed into an error, never committed |
| `SMS_SENDER` | (empty) | A Twilio number in E.164 (`+1555…`) or a Messaging Service SID (`MG…`). Prefer the messaging service for Indonesian traffic: it picks the route and sender id per destination operator. The prefix decides which Twilio parameter is sent |
| `SMS_BASE_URL` | (empty) | Overrides the API host; for tests and a future on-premise aggregator. Leave unset in real deployments |
| `SMS_TIMEOUT` | `10s` | Bounds one send. The nasabah is watching a spinner, so a slow aggregator is cut off |
| `SMS_VERIFY_SERVICE_SID` | (empty) | Twilio Verify service SID (`VA…`). Required when `SMS_PROVIDER=twilio_verify`, ignored for `twilio`. A nearly-right value fails the boot rather than 404ing every verification |
| `SMS_VERIFY_CHANNELS` | `sms` | Verify channels this deployment may use: `sms`, `call`, or both, comma-separated. A channel outside the list is refused with `400 OTP_CHANNEL_NOT_ALLOWED` before Twilio is called. An unknown name fails the boot — `voice` is a plausible typo for `call`, and dropping it silently would leave voice looking enabled. `call` also needs **Voice** Geo Permissions for Indonesia, not just Messaging |
| `SMS_VERIFY_LOCALE` | `id` | Language Verify renders the code in. Honoured for `sms`. Verify's voice template does not cover Indonesian, so a `call` is spoken in English and the transport omits the parameter instead of sending one Twilio will not honour |
| `SMS_VERIFY_CODE_TTL` | `10m` | Must match the code expiry configured on the Verify service in the console. Nothing here enforces it — it is what `otp_expires_at` reports, so a mismatch is a countdown that disagrees with the code in the nasabah's hand |
| `SMS_VERIFY_BASE_URL` | (empty) | Overrides the **Verify** host; tests only. Deliberately separate from `SMS_BASE_URL`, which is the Messages host: Verify used to read that one, so setting it sent every verification to `api.twilio.com`, earned a 404, and a 404 from Verify means "nothing pending" — every nasabah saw `OTP_EXPIRED` with correct credentials |
| `MAX_REQUEST_BODY_BYTES` | `1048576` | Cap on any JSON request body. Upload endpoints set their own larger limit |
| `REQUEST_TIMEOUT` | `30s` | Per-handler timeout |

### Environment-gated behaviour

`APP_ENV=development` is the only setting that enables any of the following.
Everything else refuses rather than pretending:

| Area | development | anything else |
|------|-------------|---------------|
| `/v1/dev/*` helpers | mounted | route does not exist (404) |
| SMS OTP, with no `SMS_PROVIDER` | logged to stdout, and returned as `otp_debug` in the response | the boot is refused — see `SMS_PROVIDER` above. With a provider configured, the OTP is sent for real in every environment and `otp_debug` still appears only in development |
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