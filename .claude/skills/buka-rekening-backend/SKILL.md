---
name: buka-rekening-backend
description: >-
  Backend onboarding service untuk flow buka rekening BCA — session management,
  OCR e-KTP, verifikasi Dukcapil, biometrik, video call signaling, credential
  hashing, dan account creation. Gunakan saat membuat atau memodifikasi endpoint
  /v1/onboarding/*, service OCR/biometric/video-call, WebSocket signaling server,
  queue management, atau audit trail onboarding. JANGAN gunakan untuk endpoint
  auth (login/PIN), endpoint transaksi (transfer/e-wallet), atau UI/frontend
  Android (itu project terpisah).
---

# Skill: Buka Rekening — Backend Onboarding Service

Backend service untuk seluruh flow pembukaan rekening baru BCA.
Mencakup: session lifecycle, OCR e-KTP + Dukcapil, data pribadi + OTP,
biometrik, video call queue + WebSocket signaling, credential encryption,
account creation, dan audit trail.

**Trigger**: saat menyentuh endpoint `/v1/onboarding/*`, service OCR,
biometric, video call queue, WebSocket signaling server, credential hashing,
session state machine, atau audit logging onboarding.

**Jangan trigger** untuk: endpoint auth/login/PIN (itu skill `auth`),
endpoint transaksi/transfer/e-wallet (itu skill `transaction`),
UI/frontend Android (itu project terpisah dengan skill `buka-rekening-native-android`).

> **Setup di project backend:**
> ```
> .claude/skills/
> └── buka-rekening-backend/
>     └── SKILL.md   ← skill definition + prompt instructions (file ini)
>
> docs/
> ├── 00-ARCHITECTURE-OVERVIEW.md
> ├── 04-SECURITY.md
> └── 06-BUKA-REKENING-API-SPEC.md   ← referensi API spec onboarding
> ```

---

## Arsitektur

### Tech Stack
- **Language**: Go (Golang)
- **Database**: PostgreSQL (strong consistency)
- **Cache/Queue**: Redis (session, antrean video call)
- **Object Storage**: S3-compatible (foto KTP, wajah — via `ObjectStorage` interface)
- **OCR Engine**: Google Cloud Vision API atau Tesseract + custom Indonesian KTP parser (via `OCREngine` interface)
- **Face Matching**: On-premise model atau cloud API (via `BiometricEngine` interface)

### Service Boundaries

Mengikuti convention project (`internal/domain/{feature}/`):

```
internal/
├── domain/onboarding/           ← Business logic & entities
│   ├── entity.go                ← Domain models (Session, OCRResult, PersonalData, etc.)
│   ├── repository.go            ← Repository & external service interfaces
│   ├── session_service.go       ← Session lifecycle (create/get/cancel/transition)
│   ├── ocr_service.go           ← OCR pipeline (upload, extract, Dukcapil verify)
│   ├── personal_data_service.go ← Personal data + OTP (save, verify, resend)
│   ├── biometric_service.go     ← Face liveness + matching pipeline
│   ├── video_call_service.go    ← Queue management + result submission
│   ├── credential_service.go    ← Access code + PIN (decrypt, validate, hash)
│   ├── submit_service.go        ← Final submit + core banking account creation
│   ├── monitoring_service.go    ← Audit trail retrieval + alert evaluation
│   ├── ktp_parser.go            ← Indonesian KTP text parser + NIK validator
│   ├── dukcapil_client.go       ← Mock Dukcapil client (implements DukcapilClient)
│   ├── sms_gateway.go           ← Mock SMS gateway (implements SMSGateway)
│   ├── biometric_engine.go      ← Mock biometric engine (implements BiometricEngine)
│   └── core_banking.go          ← Mock core banking client (implements CoreBankingClient)
├── handler/
│   └── onboarding_handler.go    ← HTTP handlers (Chi) untuk /v1/onboarding/*
├── repository/
│   ├── postgres/
│   │   └── onboarding_repo.go   ← PostgreSQL queries (sessions, OCR, personal data, etc.)
│   └── redis/
│       └── onboarding_cache.go  ← Session cache, OTP, rate limiters, queue, idempotency
├── middleware/
│   └── onboarding_audit.go      ← Audit middleware (scoped to /v1/onboarding sub-router)
├── websocket/                   ← Video call signaling (upgrade protocol)
│   ├── hub.go
│   ├── client.go
│   └── message.go
└── pkg/
    └── crypto/                   ← Reuse existing RSA, AES-256-GCM, Argon2id utilities
```

### Database Tables

```sql
-- Tabel utama
onboarding_sessions      -- session lifecycle
onboarding_ocr_results   -- hasil OCR per session
onboarding_personal_data -- data pribadi nasabah (PII encrypted AES-256-GCM)
onboarding_biometrics    -- hasil biometrik
onboarding_video_calls   -- rekaman & hasil video call
onboarding_credentials   -- hash kode akses & PIN (Argon2id)
onboarding_audit_logs    -- immutable audit trail

-- Redis keys
onboarding:session:{session_id}           -- session cache (TTL 24h)
onboarding:ocr_rate:{session_id}          -- OCR attempt counter (10/hour)
onboarding:otp:{session_id}              -- hashed OTP (TTL 5m)
onboarding:otp_attempt:{session_id}      -- OTP attempt counter
onboarding:otp_block:{session_id}        -- OTP block flag (TTL 30m)
onboarding:bio_rate:{session_id}         -- biometric attempt counter (5/hour)
onboarding:queue:active                  -- video call sorted set (score = join timestamp)
onboarding:queue:counter:{YYYY-MM-DD}   -- daily sequential queue number
onboarding:idem:{idempotency_key}        -- submit idempotency cache (TTL 24h)
```

### Session State Machine

```
TNC → OCR → PERSONAL_DATA → OTP_VERIFY → BIOMETRIC → VIDEO_CALL → CREDENTIALS → REVIEW → COMPLETED
```

- Session dimulai di step `OCR` (TNC accepted saat create session)
- Setiap transisi hanya boleh maju 1 step (enforced oleh `CanTransition()`)
- TTL rolling 24 jam — diperpanjang di setiap step transition
- Side states: session bisa di-soft-delete (cancel) atau expired (lazy check)

### API Routes

Semua route di bawah `/v1/onboarding/` sub-router dengan `OnboardingAudit` middleware:

**Public endpoints (nasabah):**
| Method | Path | Handler |
|--------|------|---------|
| POST | `/sessions` | CreateSession |
| GET | `/sessions/{session_id}` | GetSession |
| DELETE | `/sessions/{session_id}` | CancelSession |
| POST | `/ocr` | ProcessOCR (multipart) |
| GET | `/ocr/{session_id}` | GetOCRResult |
| POST | `/personal-data` | SavePersonalData |
| POST | `/verify-otp` | VerifyOTP |
| POST | `/resend-otp` | ResendOTP |
| POST | `/biometric` | ProcessBiometric (multipart) |
| POST | `/video-call/queue` | JoinVideoCallQueue |
| GET | `/video-call/signal` | HandleSignaling (WebSocket) |
| GET | `/credentials/public-key` | GetEncryptionPublicKey |
| POST | `/credentials` | SetCredentials |
| POST | `/submit` | Submit |

**Internal endpoints (X-Internal-API-Key required):**
| Method | Path | Handler |
|--------|------|---------|
| POST | `/video-call/result` | SubmitVideoCallResult |
| GET | `/sessions/{session_id}/audit` | GetAuditTrail |
| GET | `/monitoring` | GetMonitoringStatus |

## Aturan Wajib

1. **Idempotency** — Submit endpoint menggunakan `X-Idempotency-Key`; credential endpoint idempotent by session
2. **Audit trail** — Setiap perubahan state session masuk `onboarding_audit_logs` (append-only)
3. **PII encryption** — NIK, nama, alamat, phone, email di-encrypt AES-256-GCM (hex-encoded ciphertext)
4. **Credential hashing** — Kode akses dan PIN di-hash Argon2id, JANGAN simpan plaintext
5. **Credential transport** — RSA-OAEP-SHA256 encrypt dari mobile, server decrypt sebelum hash
6. **Auto-expiry** — Session expire 24 jam (lazy check + rolling TTL extension per step)
7. **Rate limiting** — Per-device (3 sessions/hour), per-session OCR (10/hour), biometric (5/hour)
8. **OTP security** — SHA-256 hash, 5-min TTL, block setelah 5 gagal (30 min), regen setelah 3 gagal
9. **Dukcapil validation** — NIK wajib divalidasi ke API Dukcapil sebelum lanjut
10. **Step enforcement** — Setiap endpoint validasi `session.CurrentStep` sebelum proses

---

# Prompt Instructions — AI Agent Backend Onboarding

> Instruksi step-by-step untuk AI agent mengimplementasi backend buka rekening.
> Jalankan satu prompt per sesi, secara berurutan.
> AI agent **wajib** membaca skill di atas dan `06-BUKA-REKENING-API-SPEC.md` sebelum mulai.

---

## Prompt 1: Session Management Service

```
Implement the onboarding session management service in Go.

Requirements:
- POST /v1/onboarding/sessions — create new session
  - Validate product_type enum (TAHAPAN_BCA, TAHAPAN_XPRESI, TABUNGANKU)
  - Generate unique session_id with prefix "onb_"
  - Store in PostgreSQL `onboarding_sessions` table
  - Cache in Redis with TTL 24 hours (key: onboarding:session:{id})
  - Rate limit: max 3 sessions per device_id per hour
  - Return product details from ProductCatalog map
  - Initial step = OCR (TNC accepted at creation time)

- GET /v1/onboarding/sessions/{session_id} — resume/check status
  - Return ProductInfo, current_step, and steps_completed
  - Check Redis cache first, fallback to PostgreSQL (backfill cache)

- DELETE /v1/onboarding/sessions/{session_id} — cancel session
  - Soft-delete session (set deleted_at)
  - Remove from Redis cache
  - Write audit log entry (SESSION_CANCELLED)

State machine:
- Session has `current_step` field — enforce ordering via CanTransition()
- Step order: TNC → OCR → PERSONAL_DATA → OTP_VERIFY → BIOMETRIC → VIDEO_CALL → CREDENTIALS → REVIEW → COMPLETED
- Each step transition writes to `onboarding_audit_logs`
- Session expires after 24 hours (lazy check, rolling TTL extension)

Database schema for `onboarding_sessions`:
- id (UUID primary key)
- session_id (unique, indexed, prefix "onb_")
- device_id (indexed)
- product_type (enum)
- current_step (enum)
- tnc_version (string)
- steps_completed (JSONB)
- created_at, updated_at, expires_at (timestamps)
- deleted_at (nullable, soft delete)

Use the repository pattern: SessionRepository (PostgreSQL), SessionCache (Redis).
Service layer handles business logic. Follow project conventions.
```

---

## Prompt 2: OCR Service & Dukcapil Integration

```
Implement the OCR processing service for Indonesian e-KTP.

Requirements:
- POST /v1/onboarding/ocr — accept multipart upload (max 10 MB)
  - Validate session exists and current_step == "OCR"
  - Rate limit: 10 attempts per hour per session (key: onboarding:ocr_rate:{session_id})
  - Save photo to S3-compatible storage (encrypted AES-256-GCM at rest)
  - Extract text via OCR engine (OCREngine interface)
  - Parse Indonesian KTP fields from raw OCR text via ParseKTPFromText()
  - Validate NIK format (16 digits) via ValidateNIK()
  - Call Dukcapil API to verify NIK + nama match
  - Assess photo quality (sharpness, glare, corners)
  - Encrypt PII (NIK, nama, alamat) with AES-256-GCM before storing
  - Store results in `onboarding_ocr_results` table
  - Transition session: OCR → PERSONAL_DATA
  - Extend session TTL in cache

- GET /v1/onboarding/ocr/{session_id} — retrieve OCR result
  - Decrypt PII fields before returning

Error codes:
- OCR_PHOTO_BLURRY, OCR_GLARE_DETECTED, OCR_NOT_KTP
- OCR_CORNERS_MISSING, OCR_DUKCAPIL_TIMEOUT, OCR_DUKCAPIL_MISMATCH

Encryption:
- PII stored as hex-encoded AES-256-GCM ciphertext in PostgreSQL
- Photo encrypted before S3 upload
- Auto-delete schedule: photo after 30 days
```

---

## Prompt 3: Personal Data & OTP Service

```
Implement personal data submission and OTP verification.

Requirements:
- POST /v1/onboarding/personal-data
  - Validate session current_step == "PERSONAL_DATA"
  - Validate enums: Pekerjaan, Penghasilan, SumberDana
  - Validate nomor_hp format (regex: ^(\+62|62|0)8[0-9]{8,12}$)
  - Validate email format
  - Cross-validate NIK and nama with OCR results (decrypt OCR PII for comparison)
  - Idempotent: if personal data exists for session, update instead of create
  - Encrypt PII (NIK, nama, phone, email) with AES-256-GCM
  - Store in `onboarding_personal_data` table
  - Generate 6-digit OTP (crypto/rand), hash with SHA-256
  - Store OTP hash in Redis (key: onboarding:otp:{session_id}, TTL 5 minutes)
  - Send OTP via SMS gateway (SMSGateway interface)
  - Transition: PERSONAL_DATA → OTP_VERIFY

- POST /v1/onboarding/verify-otp
  - Validate session current_step == "OTP_VERIFY"
  - Check if blocked (key: onboarding:otp_block:{session_id})
  - Compare SHA-256 hash of input against stored hash
  - Track attempts (key: onboarding:otp_attempt:{session_id})
  - After 3 failures: regenerate new OTP and send via SMS
  - After 5 failures: block for 30 minutes, return retry_after_seconds in error Details
  - On success: transition OTP_VERIFY → BIOMETRIC

- POST /v1/onboarding/resend-otp
  - Validate session current_step == "OTP_VERIFY"
  - Check if blocked
  - Decrypt phone from personal data, generate new OTP, send SMS
```

---

## Prompt 4: Biometric Verification Service

```
Implement biometric face liveness verification service.

Requirements:
- POST /v1/onboarding/biometric — multipart upload (max 50 MB)
  - Validate session current_step == "BIOMETRIC"
  - Rate limit: 5 attempts per hour (key: onboarding:bio_rate:{session_id})
  - Accept: face_photo (main), liveness_frames (3-5 challenge frames)
  - Encrypt and upload photos to S3
  - Run BiometricEngine.Analyze() with face photo, frames, and KTP photo

Evaluation thresholds:
  - liveness_score >= 90 (livenessThreshold)
  - face_match_score >= 85 (faceMatchThreshold)
  - Exactly 1 face detected
  - No spoof detected
  - Quality != "LOW"

Error codes:
  - BIO_MULTIPLE_FACES, BIO_LOW_QUALITY, BIO_SPOOF_DETECTED
  - BIO_LIVENESS_FAILED, BIO_FACE_NOT_MATCH

Store results in `onboarding_biometrics` table.
Auto-delete biometric photos after 7 days (UU PDP compliance).
On success: transition BIOMETRIC → VIDEO_CALL
```

---

## Prompt 5: Video Call Queue & Signaling Server

```
Implement video call queue management and WebRTC signaling server.

PART A: Queue Management
- POST /v1/onboarding/video-call/queue — join queue
  - Validate session current_step == "VIDEO_CALL"
  - Check operating hours (06:00-22:00 WIB, injectable clock)
  - Idempotent: if already queued, return current position
  - Generate queue_id (prefix "q_") and queue_number (format: A-NNN, daily counter)
  - Store in PostgreSQL `onboarding_video_calls` table
  - Add to Redis sorted set (key: onboarding:queue:active, score = join timestamp)
  - Daily counter key: onboarding:queue:counter:{YYYY-MM-DD}
  - Return signaling WebSocket URL with short-lived JWT token (1 hour TTL)

- POST /v1/onboarding/video-call/result — submit result (internal, requires X-Internal-API-Key)
  - Validate queue_id and session_id match
  - Validate result: APPROVED or REJECTED
  - Update video call record in PostgreSQL
  - Remove from Redis queue
  - If APPROVED: transition VIDEO_CALL → CREDENTIALS

PART B: WebSocket Signaling Server
- GET /v1/onboarding/video-call/signal?token=<jwt>
  - Authenticate JWT from query param
  - WebRTC signaling relay: offer/answer SDP, ICE candidates
  - Server messages: queue_update, agent_assigned, instruction, call_ended

Do NOT implement WebRTC media — backend only handles signaling.
```

---

## Prompt 6: Credential Storage Service

```
Implement credential (access code + PIN) storage service.

Requirements:
- GET /v1/onboarding/credentials/public-key
  - Return RSA public key PEM for credential encryption
  - Dev mode: return empty key with note

- POST /v1/onboarding/credentials
  - Idempotent: if credentials already exist for session, return existing
  - Validate session current_step == "CREDENTIALS"
  - Decrypt access_code and pin using RSA-OAEP-SHA256 private key
  - Dev mode: treat encrypted values as plaintext

  Validation rules:
  - Access code: exactly 6 alphanumeric, not sequential, not all same char
  - PIN: exactly 6 digits, not sequential, not all same digit
  - PIN must differ from access code

  Hash with Argon2id (crypto.DefaultArgon2Params):
  - memory: 64MB, iterations: 3, parallelism: 4
  - unique salt per credential

  Store hashes in `onboarding_credentials` table.
  NEVER log or store plaintext credentials.
  Transition: CREDENTIALS → REVIEW
```

---

## Prompt 7: Final Submit & Account Creation

```
Implement the final submission endpoint that creates the bank account.

Requirements:
- POST /v1/onboarding/submit
  - Idempotency via X-Idempotency-Key header (key: onboarding:idem:{key}, TTL 24h)
  - If same key received: return cached JSON response
  - Validate session current_step == "REVIEW"
  - Validate agreement_accepted == true and agreement_version not empty
  - Validate ALL steps completed:
    tnc_accepted, ocr_verified, personal_data_saved, otp_verified,
    biometric_verified, video_call_verified, credentials_set
  - If any step incomplete: return ONBOARDING_INCOMPLETE error
  - Fetch and decrypt personal data (holder name, NIK)
  - Verify credentials exist
  - Call CoreBankingClient.CreateAccount() to create bank account
  - Generate m-BCA user_id with prefix "mbca_"
  - Transition: REVIEW → COMPLETED
  - Return: account info, m-BCA info, created_at
  - Cache response for idempotency
  - Write audit: ACCOUNT_CREATED
```

---

## Prompt 8: Audit Trail & Monitoring

```
Implement comprehensive audit logging and monitoring for the onboarding flow.

Audit trail:
- GET /v1/onboarding/sessions/{session_id}/audit (internal, requires X-Internal-API-Key)
  - Returns all audit events for a session from onboarding_audit_logs

- Table `onboarding_audit_logs` (append-only, no UPDATE/DELETE):
  - id (UUID), session_id (indexed), event_type, actor, details (JSONB)
  - ip_address, user_agent, created_at (indexed)

- Event types: SESSION_CREATED, SESSION_CANCELLED, SESSION_EXPIRED,
  STEP_TRANSITION, OCR_UPLOADED, OCR_VERIFIED,
  PERSONAL_DATA_SAVED, OTP_SENT, OTP_VERIFIED, OTP_FAILED,
  BIOMETRIC_UPLOADED, BIOMETRIC_VERIFIED, BIOMETRIC_FAILED,
  VIDEO_CALL_QUEUED, VIDEO_CALL_STARTED, VIDEO_CALL_ENDED,
  CREDENTIALS_SET, SUBMITTED, ACCOUNT_CREATED

- Actor format: "system", "nasabah:{device_id}", "agent:{employee_id}"

Monitoring:
- GET /v1/onboarding/monitoring (internal, requires X-Internal-API-Key)
  - Evaluates alert rules, returns active_sessions, stuck_sessions, queue_length, alerts
  - Alert: QUEUE_LENGTH_HIGH when queue > 10 people

Audit middleware:
- OnboardingAudit middleware scoped to /v1/onboarding sub-router
- Captures: request_id, ip, user_agent, method, path, duration
- Logs with severity based on response status (info/warn/error)

Data retention (POJK regulation):
- Audit logs: 7 years
- Session data: 30 days after completion
- KTP photos: 30 days
- Biometric photos: 7 days (UU PDP)
- Video recordings: 5 years
- Credentials: until account closed
```