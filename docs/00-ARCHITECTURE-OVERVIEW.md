# Backend Architecture Overview — BCA Mobile API

> Golang + PostgreSQL + Redis | Banking-Grade Security & Performance

---

## Daftar Isi

1. [Filosofi Arsitektur](#1-filosofi-arsitektur)
2. [Screen-to-API Mapping](#2-screen-to-api-mapping)
3. [High-Level Architecture](#3-high-level-architecture)
4. [Project Structure](#4-project-structure)
5. [Dokumen Terkait](#5-dokumen-terkait)

---

## 1. Filosofi Arsitektur

Aplikasi perbankan memiliki tuntutan yang **berbeda** dari aplikasi biasa:

| Aspek | Standar Umum | Banking-Grade |
|-------|-------------|---------------|
| Downtime tolerance | Beberapa menit OK | Near-zero downtime, 99.99% SLA |
| Data consistency | Eventual OK | **Strong consistency** untuk transaksi finansial |
| Security | HTTPS + JWT cukup | Multi-layer: mTLS, HSM, encryption at rest, WAF |
| Audit | Optional logging | **Wajib** — setiap aksi tercatat, immutable audit trail |
| Compliance | Best effort | **PCI-DSS**, OJK, BI regulations |

### Prinsip Dasar

```
1. Defense in Depth     — Tidak ada satu titik keamanan saja
2. Fail Secure          — Error = deny access, bukan grant
3. Least Privilege      — Setiap service hanya akses yang dibutuhkan
4. Idempotency          — Transaksi finansial harus idempotent
5. Auditability         — Semua perubahan state tercatat
6. Zero Trust           — Verifikasi setiap request, bahkan internal
```

---

## 2. Screen-to-API Mapping

### Analisis: Mana yang Butuh API, Mana yang Tidak

| # | Screen | Butuh API? | Alasan |
|---|--------|-----------|--------|
| 1 | **Splash** | Ya (ringan) | Health check + config fetch (force update, maintenance mode) |
| 2 | **Login (Welcome)** | Tidak | Pure UI, hanya navigasi ke auth method |
| 3 | **Kode Akses** | **Ya** | Validasi PIN ke server, rate limiting, brute-force protection |
| 4 | **Face ID** | **Tidak*** | Biometrik diproses di device (Android BiometricPrompt). Server hanya menerima token hasil biometrik yang sudah terdaftar |
| 5 | **Touch ID** | **Tidak*** | Sama seperti Face ID — device-level auth |
| 6 | **Buka Rekening** | **Ya** | KYC flow, upload dokumen, validasi data nasabah |
| 7 | **Ganti Kode Akses** | **Ya** | Validasi kode lama + set kode baru |
| 8 | **Beranda (Home)** | **Ya** | Fetch saldo, info akun, promo, notifikasi |
| 9 | **Mutasi** | **Ya** | Query riwayat transaksi dengan filter tanggal |
| 10 | **Riwayat** | **Ya** | Histori transaksi yang pernah dilakukan |
| 11 | **Akun** | **Ya** | Profil user, pengaturan, toggle biometrik |
| 12 | **Transfer** | **Ya** | Daftar transfer terakhir, validasi rekening tujuan |
| 13 | **Transfer Antar Rekening** | **Ya** | Inquiry rekening, submit transfer, PIN validation |
| 14 | **Rentang Waktu** | Tidak | Pure UI date picker, data dikirim ke API Mutasi |
| 15 | **E-Wallet Pilih** | **Ya** | List e-wallet providers, validasi nomor |
| 16 | **E-Wallet Konfirmasi** | **Ya** | Inquiry biaya admin, konfirmasi detail |
| 17 | **E-Wallet PIN** | **Ya** | Validasi PIN transaksi |
| 18 | **Bukti Transaksi** | **Ya** | Fetch detail bukti, generate PDF |

> **\*Catatan Face ID & Touch ID:** Biometrik diproses 100% di device via Android Keystore + BiometricPrompt. Yang dikirim ke server adalah **signed challenge/token** yang membuktikan biometrik berhasil — bukan data biometrik itu sendiri. Ini sesuai FIDO2/WebAuthn standard.

### Flow Autentikasi — Biometrik vs PIN

```
┌─────────────────────────────────────────────────────────┐
│                    DEVICE SIDE                          │
│                                                         │
│  Face ID / Touch ID                                     │
│  ┌──────────┐    ┌───────────────┐    ┌──────────────┐ │
│  │ BiometricP│───>│ Android       │───>│ Sign Challenge│ │
│  │ rompt     │    │ Keystore      │    │ with Private  │ │
│  └──────────┘    └───────────────┘    │ Key           │ │
│                                       └──────┬───────┘ │
│                                              │          │
│  Kode Akses (PIN)                            │          │
│  ┌──────────┐                                │          │
│  │ 6-digit  │────────────────────────────────┤          │
│  │ Input    │  (encrypted with server pubkey)│          │
│  └──────────┘                                │          │
└──────────────────────────────────────────────┼──────────┘
                                               │
                                               ▼
┌─────────────────────────────────────────────────────────┐
│                    SERVER SIDE                          │
│                                                         │
│  ┌──────────────┐   ┌──────────────┐   ┌─────────────┐│
│  │ Verify       │──>│ Issue JWT    │──>│ Return       ││
│  │ Signature/PIN│   │ Access+Refresh│  │ Session      ││
│  └──────────────┘   └──────────────┘   └─────────────┘│
└─────────────────────────────────────────────────────────┘
```

---

## 3. High-Level Architecture

```
                    ┌─────────────────┐
                    │   Mobile App    │
                    │  (Android/iOS)  │
                    └────────┬────────┘
                             │ HTTPS + Certificate Pinning
                             ▼
                    ┌─────────────────┐
                    │   API Gateway   │
                    │  (Rate Limit,   │
                    │   WAF, mTLS)    │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              ▼              ▼              ▼
     ┌────────────┐  ┌────────────┐  ┌────────────┐
     │   Auth     │  │ Transaction│  │  Account   │
     │  Service   │  │  Service   │  │  Service   │
     └─────┬──────┘  └─────┬──────┘  └─────┬──────┘
           │               │               │
           ▼               ▼               ▼
     ┌─────────────────────────────────────────┐
     │              Message Queue              │
     │         (for async operations)          │
     └─────────────────────────────────────────┘
           │               │               │
     ┌─────┴──────┐  ┌────┴───────┐  ┌───┴────────┐
     │ PostgreSQL │  │   Redis    │  │  External  │
     │ (Primary)  │  │  (Cache +  │  │  Services  │
     │            │  │   Session) │  │ (Core Bank)│
     └────────────┘  └────────────┘  └────────────┘
```

### Untuk project ini (monolith-first approach):

Karena ini tahap awal, kita gunakan **modular monolith** — satu binary Go dengan domain yang terpisah secara internal. Ini lebih mudah di-deploy, di-debug, dan di-refactor ke microservices nanti jika diperlukan.

```
┌──────────────────────────────────────────────────────────┐
│                    BCA Mobile API                        │
│                  (Single Go Binary)                      │
│                                                          │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────────┐ │
│  │   Auth   │ │ Account  │ │ Transfer │ │  E-Wallet  │ │
│  │  Domain  │ │  Domain  │ │  Domain  │ │   Domain   │ │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └─────┬──────┘ │
│       │            │            │              │         │
│  ┌────┴────────────┴────────────┴──────────────┴──────┐ │
│  │              Shared Infrastructure                  │ │
│  │  (DB Pool, Redis Client, Logger, Middleware)        │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

---

## 4. Project Structure

```
bca-mobile-api/
├── cmd/
│   └── server/
│       └── main.go                 # Entry point
├── internal/
│   ├── config/
│   │   ├── config.go               # App configuration
│   │   └── config_test.go
│   ├── domain/                     # Business entities (no external deps)
│   │   ├── auth/
│   │   │   ├── entity.go           # User, Session, BiometricKey
│   │   │   ├── repository.go       # Interface
│   │   │   └── service.go          # Business logic
│   │   ├── account/
│   │   │   ├── entity.go           # Account, Balance
│   │   │   ├── repository.go
│   │   │   └── service.go
│   │   ├── transaction/
│   │   │   ├── entity.go           # Transaction, Transfer, Mutation
│   │   │   ├── repository.go
│   │   │   └── service.go
│   │   ├── ewallet/
│   │   │   ├── entity.go           # EWalletProvider, TopUp
│   │   │   ├── repository.go
│   │   │   └── service.go
│   │   └── notification/
│   │       ├── entity.go
│   │       ├── repository.go
│   │       └── service.go
│   ├── handler/                    # HTTP handlers (adapters)
│   │   ├── auth_handler.go
│   │   ├── account_handler.go
│   │   ├── transaction_handler.go
│   │   ├── ewallet_handler.go
│   │   ├── notification_handler.go
│   │   └── health_handler.go
│   ├── middleware/
│   │   ├── auth.go                 # JWT validation
│   │   ├── ratelimit.go            # Rate limiting
│   │   ├── logging.go              # Structured logging
│   │   ├── recovery.go             # Panic recovery
│   │   ├── cors.go                 # CORS
│   │   ├── requestid.go            # Request ID tracking
│   │   ├── security.go             # Security headers
│   │   └── audit.go                # Audit trail
│   ├── repository/                 # Database implementations
│   │   ├── postgres/
│   │   │   ├── auth_repo.go
│   │   │   ├── account_repo.go
│   │   │   ├── transaction_repo.go
│   │   │   └── ewallet_repo.go
│   │   └── redis/
│   │       ├── session_repo.go
│   │       ├── cache_repo.go
│   │       └── ratelimit_repo.go
│   ├── router/
│   │   └── router.go              # Route definitions
│   └── pkg/                       # Internal shared packages
│       ├── crypto/
│       │   ├── aes.go              # AES-256 encryption
│       │   ├── hash.go             # Argon2id hashing
│       │   └── jwt.go              # JWT operations
│       ├── validator/
│       │   └── validator.go        # Input validation
│       ├── response/
│       │   └── response.go         # Standardized API responses
│       ├── pagination/
│       │   └── pagination.go       # Cursor-based pagination
│       └── idempotency/
│           └── idempotency.go      # Idempotency key handling
├── migrations/
│   ├── 000001_create_users.up.sql
│   ├── 000001_create_users.down.sql
│   ├── 000002_create_accounts.up.sql
│   ├── ...
├── scripts/
│   ├── setup.sh                    # Dev environment setup
│   ├── migrate.sh                  # Run migrations
│   └── seed.sh                     # Seed test data
├── deployments/
│   ├── Dockerfile
│   ├── docker-compose.yml          # Local dev (Postgres + Redis)
│   └── k8s/                        # Kubernetes manifests
├── .env.example
├── .gitignore
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## 5. Dokumen Terkait

| File | Isi |
|------|-----|
| [01-API-SPECIFICATION.md](./01-API-SPECIFICATION.md) | Semua endpoint API, request/response, status codes |
| [02-DATABASE-SCHEMA.md](./02-DATABASE-SCHEMA.md) | PostgreSQL schema, indexes, constraints |
| [03-REDIS-STRATEGY.md](./03-REDIS-STRATEGY.md) | Caching patterns, key design, TTL strategy |
| [04-SECURITY.md](./04-SECURITY.md) | Authentication, encryption, rate limiting, audit |
| [05-PROJECT-SETUP.md](./05-PROJECT-SETUP.md) | Step-by-step setup guide, Makefile, Docker |