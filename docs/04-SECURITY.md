# Security Architecture — Banking Grade

> Multi-layer security: transport, application, data, operational

---

## 1. Security Layers Overview

```
Layer 1: Transport Security
├── TLS 1.3 (minimum TLS 1.2)
├── Certificate Pinning di mobile app
├── mTLS untuk internal service-to-service
└── HSTS headers

Layer 2: API Gateway
├── WAF (Web Application Firewall)
├── DDoS protection
├── IP whitelisting (admin APIs)
├── Request size limiting
└── Geographic blocking (opsional)

Layer 3: Application Security
├── JWT (RS256) + refresh token rotation
├── Rate limiting (per-device, per-user, per-IP)
├── Input validation & sanitization
├── Idempotency keys untuk transaksi
├── Request ID tracking
└── CORS strict policy

Layer 4: Data Security
├── PIN: Argon2id hashing
├── PII: AES-256-GCM encryption
├── Sensitive fields: Application-level encryption
├── Database: Encryption at rest (TDE)
└── Backups: Encrypted with separate keys

Layer 5: Audit & Monitoring
├── Immutable audit logs
├── Real-time anomaly detection
├── Failed login alerting
└── Transaction pattern monitoring
```

---

## 2. Authentication Flow Detail

### 2.1 PIN-Based Authentication

```
Mobile App                          Backend Server
    │                                    │
    │  1. POST /auth/login/pin           │
    │  {pin_encrypted, device_id}        │
    │───────────────────────────────────>│
    │                                    │
    │                          2. Decrypt PIN (RSA)
    │                          3. Check rate limit (Redis)
    │                          4. Check account lockout
    │                          5. Verify PIN (Argon2id)
    │                          6. Check device trust
    │                          7. Generate JWT pair
    │                          8. Create session (Redis+PG)
    │                          9. Log audit event
    │                                    │
    │  10. {access_token, refresh_token} │
    │<───────────────────────────────────│
    │                                    │
    │  Setiap request berikutnya:        │
    │  Authorization: Bearer {access}    │
    │───────────────────────────────────>│
    │                          11. Validate JWT signature
    │                          12. Check session exists (Redis)
    │                          13. Check token not revoked
    │                          14. Extend session TTL
    │                                    │
```

### 2.2 Biometric Authentication (FIDO2-style)

```
Mobile App                          Backend Server
    │                                    │
    │  1. GET /auth/biometric/challenge  │
    │  ?device_id=xyz                    │
    │───────────────────────────────────>│
    │                                    │
    │                          2. Generate random challenge
    │                          3. Store in Redis (60s TTL)
    │                                    │
    │  4. {challenge_id, challenge}       │
    │<───────────────────────────────────│
    │                                    │
    │  5. BiometricPrompt → User scans   │
    │     face/finger                    │
    │  6. Android Keystore signs          │
    │     challenge with private key     │
    │                                    │
    │  7. POST /auth/login/biometric     │
    │  {challenge_id, signed_challenge,  │
    │   key_id, device_id}              │
    │───────────────────────────────────>│
    │                                    │
    │                          8. Fetch challenge from Redis
    │                          9. Fetch public key from DB
    │                         10. Verify signature
    │                         11. Consume challenge (one-time)
    │                         12. Generate JWT pair
    │                         13. Create session
    │                         14. Log audit event
    │                                    │
    │  15. {access_token, refresh_token} │
    │<───────────────────────────────────│
```

### 2.3 Token Lifecycle

```
Access Token:
  Algorithm:  RS256
  Lifetime:   15 menit
  Contains:   user_id, device_id, session_id, iat, exp
  Storage:    Mobile app memory only (TIDAK di SharedPreferences)

Refresh Token:
  Algorithm:  RS256
  Lifetime:   7 hari
  Contains:   session_id, device_id, jti (unique ID), iat, exp
  Storage:    Android EncryptedSharedPreferences
  Rotation:   Setiap refresh menghasilkan token baru, token lama invalidated
  Reuse Detection: Jika token lama digunakan → revoke SEMUA session user (compromise detected)
```

### JWT Claims Structure

```json
// Access Token
{
  "sub": "usr_abc123",           // user_id
  "sid": "ses_xyz789",           // session_id
  "did": "dev_456",              // device_id
  "typ": "access",
  "iat": 1725267000,
  "exp": 1725267900              // +15 menit
}

// Refresh Token
{
  "sub": "usr_abc123",
  "sid": "ses_xyz789",
  "did": "dev_456",
  "typ": "refresh",
  "jti": "rtk_unique_id",       // Unique token ID untuk rotation tracking
  "iat": 1725267000,
  "exp": 1725871800              // +7 hari
}
```

---

## 3. PIN Security

### Hashing dengan Argon2id

```go
// config
const (
    ArgonTime    = 3           // Iterations
    ArgonMemory  = 64 * 1024   // 64 MB
    ArgonThreads = 4
    ArgonKeyLen  = 32
    SaltLen      = 16
)

// Hash PIN
func HashPIN(pin string) (hash string, salt string, err error) {
    saltBytes := make([]byte, SaltLen)
    if _, err = rand.Read(saltBytes); err != nil {
        return
    }

    key := argon2.IDKey(
        []byte(pin),
        saltBytes,
        ArgonTime,
        ArgonMemory,
        ArgonThreads,
        ArgonKeyLen,
    )

    hash = base64.RawStdEncoding.EncodeToString(key)
    salt = base64.RawStdEncoding.EncodeToString(saltBytes)
    return
}

// Verify PIN
func VerifyPIN(pin, hash, salt string) bool {
    saltBytes, _ := base64.RawStdEncoding.DecodeString(salt)
    hashBytes, _ := base64.RawStdEncoding.DecodeString(hash)

    key := argon2.IDKey(
        []byte(pin),
        saltBytes,
        ArgonTime,
        ArgonMemory,
        ArgonThreads,
        ArgonKeyLen,
    )

    return subtle.ConstantTimeCompare(key, hashBytes) == 1
}
```

### PIN Transmission

```
1. Server generates RSA-2048 key pair
2. Public key dikirim ke app saat startup (atau di-embed)
3. App encrypts PIN dengan public key sebelum kirim
4. Server decrypts dengan private key
5. Private key disimpan di HSM (production) atau encrypted file (dev)
```

### Brute-Force Protection

```
Attempt 1-3:  Normal → error message + remaining attempts
Attempt 4:    Warning → "Satu kesempatan lagi sebelum akun terkunci"
Attempt 5:    LOCK → Akun terkunci 30 menit
              → Notifikasi push ke device terdaftar
              → Audit log: SECURITY event
              → Redis: SET lock:account:{user_id} "locked" EX 1800

Setelah lockout:
  - Counter di-reset
  - Attempt dimulai dari 1 lagi
  - Jika terkunci 3x dalam 24 jam → lock 24 jam + notifikasi ke CS
```

---

## 4. Encryption at Application Level

### PII Encryption (AES-256-GCM)

```go
// Sensitive fields yang dienkripsi di database:
// - NIK (Nomor Induk Kependudukan)
// - Phone number (plaintext hanya di memory saat dibutuhkan)
// - Email
// - Alamat

type Encryptor struct {
    key []byte // 32 bytes, dari environment variable atau KMS
}

func (e *Encryptor) Encrypt(plaintext string) ([]byte, error) {
    block, err := aes.NewCipher(e.key)
    if err != nil {
        return nil, err
    }

    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }

    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return nil, err
    }

    return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (e *Encryptor) Decrypt(ciphertext []byte) (string, error) {
    block, err := aes.NewCipher(e.key)
    if err != nil {
        return "", err
    }

    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return "", err
    }

    nonceSize := gcm.NonceSize()
    if len(ciphertext) < nonceSize {
        return "", errors.New("ciphertext too short")
    }

    nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
    plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
    if err != nil {
        return "", err
    }

    return string(plaintext), nil
}
```

### Phone/Email Hash untuk Lookup

```go
// Kita butuh bisa mencari user berdasarkan phone tanpa decrypt semua record.
// Solusi: simpan hash terpisah untuk lookup.

func HashForLookup(value string) string {
    // Gunakan HMAC-SHA256 dengan secret key
    // (bukan plain SHA256 — mencegah rainbow table)
    h := hmac.New(sha256.New, []byte(lookupSecret))
    h.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
    return hex.EncodeToString(h.Sum(nil))
}
```

---

## 5. Transaction Security

### Idempotency

```go
func (h *TransferHandler) Execute(w http.ResponseWriter, r *http.Request) {
    idempotencyKey := r.Header.Get("X-Idempotency-Key")
    if idempotencyKey == "" {
        respondError(w, 400, "VALIDATION_ERROR", "X-Idempotency-Key header required")
        return
    }

    // 1. Check Redis untuk existing result
    cached, err := h.redis.Get(ctx, "idem:"+idempotencyKey).Result()
    if err == nil {
        // Sudah pernah diproses — return cached response
        w.Header().Set("X-Idempotent-Replayed", "true")
        w.Write([]byte(cached))
        return
    }

    // 2. Acquire lock (mencegah concurrent processing)
    locked, err := h.redis.SetNX(ctx, "idem:"+idempotencyKey, "PROCESSING", 30*time.Second).Result()
    if !locked {
        respondError(w, 409, "IDEMPOTENCY_CONFLICT", "Request sedang diproses")
        return
    }

    // 3. Process transaction
    result, err := h.service.ExecuteTransfer(ctx, req)

    // 4. Cache result
    resultJSON, _ := json.Marshal(result)
    h.redis.Set(ctx, "idem:"+idempotencyKey, resultJSON, 24*time.Hour)

    respond(w, 201, result)
}
```

### Double-Entry Bookkeeping

```
Setiap transfer menghasilkan DUA mutation entries:

Transfer Rp 1.500.000 dari A ke B:

account_mutations:
  1. account_id=A, type=DEBIT,  amount=1500000, balance_before=15750000, balance_after=14250000
  2. account_id=B, type=CREDIT, amount=1500000, balance_before=5000000,  balance_after=6500000

Kedua entry dalam SATU database transaction (ACID).
Jika satu gagal → keduanya rollback.
```

### Transaction Isolation

```sql
-- Transfer menggunakan SERIALIZABLE isolation untuk mencegah race condition
BEGIN;
SET TRANSACTION ISOLATION LEVEL SERIALIZABLE;

-- Lock source account row
SELECT balance, available_balance FROM accounts
WHERE id = $1 FOR UPDATE;

-- Verify sufficient balance
-- Debit source
UPDATE accounts SET balance = balance - $amount WHERE id = $source_id;

-- Credit destination
UPDATE accounts SET balance = balance + $amount WHERE id = $dest_id;

-- Insert transaction record
INSERT INTO transactions (...) VALUES (...);

-- Insert mutations
INSERT INTO account_mutations (...) VALUES (...), (...);

-- Update daily usage
INSERT INTO daily_usage (...) VALUES (...)
ON CONFLICT (user_id, usage_date, limit_type)
DO UPDATE SET total_amount = daily_usage.total_amount + $amount,
             transaction_count = daily_usage.transaction_count + 1;

COMMIT;
```

---

## 6. Rate Limiting Strategy

### Multi-Layer Rate Limits

```
Layer 1: Per-IP (API Gateway / Nginx)
  ├── 100 req/second burst
  └── 1000 req/minute sustained

Layer 2: Per-Device (unauthenticated)
  ├── Login: 5 attempts / 15 menit
  └── Registration: 3 attempts / 1 jam

Layer 3: Per-User (authenticated)
  ├── General API: 10 req/second, 120 req/minute
  ├── Balance check: 2 req/second
  ├── Transfer execute: 3 req/minute
  └── PIN verify: 5 attempts / 15 menit

Layer 4: Per-Transaction-Type
  ├── Transfer: daily limit (user-configurable, max Rp 100jt)
  ├── E-Wallet: daily limit (max Rp 20jt)
  └── QRIS: per-transaction limit (max Rp 5jt)
```

### Implementation (Sliding Window)

```go
func (m *RateLimitMiddleware) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        key := m.keyFunc(r) // e.g., "rate:api:{user_id}"
        window := m.window  // e.g., 1 minute
        limit := m.limit    // e.g., 120

        now := time.Now().UnixMilli()
        windowStart := now - window.Milliseconds()

        pipe := m.redis.Pipeline()
        pipe.ZRemRangeByScore(r.Context(), key, "0", strconv.FormatInt(windowStart, 10))
        pipe.ZCard(r.Context(), key)
        pipe.ZAdd(r.Context(), key, redis.Z{Score: float64(now), Member: uuid.New().String()})
        pipe.Expire(r.Context(), key, window)
        results, _ := pipe.Exec(r.Context())

        count := results[1].(*redis.IntCmd).Val()
        if count >= int64(limit) {
            w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
            w.Header().Set("X-RateLimit-Remaining", "0")
            w.Header().Set("Retry-After", "60")
            respondError(w, 429, "RATE_LIMIT_EXCEEDED", "Terlalu banyak request")
            return
        }

        w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
        w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(int64(limit)-count-1, 10))
        next.ServeHTTP(w, r)
    })
}
```

---

## 7. Security Headers

```go
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("X-XSS-Protection", "0")  // Modern browsers: CSP instead
        w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
        w.Header().Set("Content-Security-Policy", "default-src 'none'")
        w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
        w.Header().Set("Pragma", "no-cache")
        w.Header().Set("Referrer-Policy", "no-referrer")
        w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
        next.ServeHTTP(w, r)
    })
}
```

---

## 8. Input Validation

```go
// Semua input HARUS divalidasi sebelum processing

type LoginPINRequest struct {
    DeviceID     string     `json:"device_id" validate:"required,uuid"`
    PINEncrypted string     `json:"pin_encrypted" validate:"required,base64"`
    DeviceInfo   DeviceInfo `json:"device_info" validate:"required"`
}

type TransferRequest struct {
    IdempotencyKey     string  `json:"idempotency_key" validate:"required,uuid"`
    InquiryID          string  `json:"inquiry_id" validate:"required,uuid"`
    SourceAccountID    string  `json:"source_account_id" validate:"required,uuid"`
    DestinationAccount string  `json:"destination_account" validate:"required,numeric,min=10,max=16"`
    BankCode           string  `json:"bank_code" validate:"required,numeric,len=3"`
    TransferType       string  `json:"transfer_type" validate:"required,oneof=INTERNAL EXTERNAL VIRTUAL_ACCOUNT"`
    Amount             float64 `json:"amount" validate:"required,gt=0,lte=100000000"`
    Notes              string  `json:"notes" validate:"max=500"`
    VerificationToken  string  `json:"verification_token" validate:"required"`
}

// Custom validators
func validateAccountNumber(fl validator.FieldLevel) bool {
    v := fl.Field().String()
    // Hanya angka, 10-16 digit
    matched, _ := regexp.MatchString(`^\d{10,16}$`, v)
    return matched
}

// SQL Injection prevention: SELALU gunakan parameterized queries
// JANGAN: fmt.Sprintf("SELECT * FROM users WHERE id = '%s'", userID)
// BENAR:  db.QueryRow("SELECT * FROM users WHERE id = $1", userID)
```

---

## 9. Audit Trail

```go
// Setiap aksi penting HARUS dicatat

type AuditAction string

const (
    AuditLoginSuccess     AuditAction = "AUTH_LOGIN_SUCCESS"
    AuditLoginFailed      AuditAction = "AUTH_LOGIN_FAILED"
    AuditLogout           AuditAction = "AUTH_LOGOUT"
    AuditPINChanged       AuditAction = "AUTH_PIN_CHANGED"
    AuditBiometricAdded   AuditAction = "AUTH_BIOMETRIC_REGISTERED"
    AuditTransferExecuted AuditAction = "TXN_TRANSFER_EXECUTED"
    AuditTransferFailed   AuditAction = "TXN_TRANSFER_FAILED"
    AuditEWalletTopUp     AuditAction = "TXN_EWALLET_TOPUP"
    AuditBalanceViewed    AuditAction = "ACCOUNT_BALANCE_VIEWED"
    AuditProfileUpdated   AuditAction = "ACCOUNT_PROFILE_UPDATED"
    AuditLimitChanged     AuditAction = "ACCOUNT_LIMIT_CHANGED"
    AuditAccountLocked    AuditAction = "SECURITY_ACCOUNT_LOCKED"
    AuditSuspiciousLogin  AuditAction = "SECURITY_SUSPICIOUS_LOGIN"
)

type AuditEntry struct {
    UserID       string
    SessionID    string
    Action       AuditAction
    ResourceType string
    ResourceID   string
    IPAddress    string
    UserAgent    string
    RequestID    string
    OldValues    map[string]interface{}
    NewValues    map[string]interface{}
    Metadata     map[string]interface{}
}

// Audit logger — async, non-blocking (jangan sampai audit failure menggagalkan transaksi)
func (a *AuditService) Log(ctx context.Context, entry AuditEntry) {
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()

        if err := a.repo.Insert(ctx, entry); err != nil {
            // Fallback ke structured log file jika DB gagal
            a.logger.Error("audit_log_failed",
                "action", entry.Action,
                "user_id", entry.UserID,
                "error", err,
            )
        }
    }()
}
```

---

## 10. Security Checklist

```
Transport:
  [x] TLS 1.3 enforced
  [x] Certificate pinning di mobile app
  [x] HSTS preload
  [x] No sensitive data in URL/query params

Authentication:
  [x] Argon2id for PIN hashing
  [x] RSA-2048 for PIN encryption in transit
  [x] JWT RS256 (not HS256) — asymmetric signing
  [x] Refresh token rotation with reuse detection
  [x] Biometric: FIDO2-style challenge-response
  [x] Multi-device session management
  [x] Auto-expire sessions (15 min access, 7 day refresh)

Authorization:
  [x] User can only access own resources
  [x] Account ownership verified on every transaction
  [x] PIN verification required for financial operations
  [x] Transaction limits enforced server-side

Data Protection:
  [x] PII encrypted at application level (AES-256-GCM)
  [x] Database encryption at rest
  [x] No plaintext secrets in code/config
  [x] Secrets via environment variables or KMS
  [x] No sensitive data in logs

Rate Limiting:
  [x] Per-IP, per-device, per-user layers
  [x] Account lockout after failed attempts
  [x] Transaction limits (daily/monthly)
  [x] Sliding window implementation

Monitoring:
  [x] Immutable audit trail
  [x] Failed login alerting
  [x] Anomaly detection (unusual transaction patterns)
  [x] Real-time dashboard for security events

Code:
  [x] Parameterized queries (no SQL injection)
  [x] Input validation on all endpoints
  [x] No user input in error messages (info leakage)
  [x] Constant-time comparison for secrets
  [x] No debug endpoints in production
```