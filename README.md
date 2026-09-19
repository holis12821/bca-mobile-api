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

### Expose to the internet (ngrok)

```bash
make tunnel                                   # random URL
make tunnel DOMAIN=your-name.ngrok-free.app   # stable free domain
```

Then set `SIGNALING_BASE_URL=wss://<host>` in `.env` and restart, or WebSocket signaling will hand clients a `localhost` URL. A web frontend also needs its origin in `CORS_ALLOWED_ORIGINS` — the default trusts no browser origin at all.

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/auth/login/pin` | Login with PIN |
| POST | `/v1/auth/login/biometric` | Login with biometric |
| POST | `/v1/auth/logout` | Logout |
| POST | `/v1/auth/token/refresh` | Refresh access token |
| POST | `/v1/auth/pin/verify` | Verify PIN (issues verification token) |
| POST | `/v1/auth/pin/change` | Change PIN |
| POST | `/v1/auth/biometric/register` | Register biometric key |
| GET | `/v1/account/profile` | Get user profile |
| PUT | `/v1/account/profile` | Update profile (email) |
| GET | `/v1/account/balance` | Get account balances |
| GET | `/v1/account/dashboard` | Get dashboard data |
| PUT | `/v1/account/settings` | Update account settings |
| PUT | `/v1/account/transaction-limit` | Update transaction limits |
| GET | `/v1/transactions/mutations` | List account mutations |
| GET | `/v1/transactions/history` | List transaction history |
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
| `INTERNAL_API_KEY` | `dev-internal-key` | `X-Internal-API-Key` for the CS endpoints |

## Documentation

- [API Specification](docs/01-API-SPECIFICATION.md)
- [Architecture](docs/02-ARCHITECTURE.md)
- [Database Schema](docs/03-DATABASE-SCHEMA.md)
- [Redis Key Design](docs/04-REDIS-KEY-DESIGN.md)
- [Security](docs/05-SECURITY.md)
- [Testing Strategy](docs/06-TESTING-STRATEGY.md)
- [Setup Runbook](docs/07-SETUP-RUNBOOK.md)
- [Postman & ngrok](docs/09-POSTMAN-DAN-NGROK.md)