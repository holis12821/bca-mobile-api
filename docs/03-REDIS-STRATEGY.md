# Redis Caching & Session Strategy

> Key design, TTL strategy, invalidation patterns, dan data structures

---

## Arsitektur Redis

```
┌────────────────────────────────────────────────────────┐
│                    Redis Cluster                       │
│                                                        │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐│
│  │   DB 0       │  │   DB 1       │  │   DB 2       ││
│  │   Sessions   │  │   Cache      │  │   Rate Limit ││
│  │   & Tokens   │  │   & Data     │  │   & Locks    ││
│  └──────────────┘  └──────────────┘  └──────────────┘│
└────────────────────────────────────────────────────────┘
```

### Konvensi Penamaan Key

```
{domain}:{entity}:{identifier}:{sub-key}

Contoh:
  session:usr_abc123:device_xyz           → Session data
  cache:dashboard:usr_abc123              → Dashboard cache
  rate:login:device_xyz                   → Login rate limit
  lock:transfer:idk_abc123               → Distributed lock
```

---

## 1. Session Management (DB 0)

### Active Sessions

```
Key:    session:{user_id}:{device_id}
Type:   Hash
TTL:    15 menit (access token lifetime)

Fields:
  access_token_hash  → SHA256 hash of access token
  user_id            → usr_abc123
  device_id          → dev_xyz789
  display_name       → NURHOLIS
  auth_method        → PIN | FINGERPRINT | FACE_ID
  ip_address         → 103.xxx.xxx.xxx
  created_at         → 2026-09-02T10:30:00Z
  last_activity      → 2026-09-02T10:45:00Z
```

**Operasi:**
```go
// Login: simpan session
HSET session:usr_abc:dev_xyz access_token_hash "sha256..." user_id "usr_abc" ...
EXPIRE session:usr_abc:dev_xyz 900  // 15 menit

// Validasi token: extend TTL pada setiap request
HGET session:usr_abc:dev_xyz access_token_hash
EXPIRE session:usr_abc:dev_xyz 900  // Reset 15 menit

// Logout: hapus session
DEL session:usr_abc:dev_xyz

// Logout semua device
SCAN 0 MATCH session:usr_abc:* → DEL each
```

### Refresh Token Mapping

```
Key:    refresh:{refresh_token_hash}
Type:   String
TTL:    7 hari
Value:  user_id:device_id (e.g., "usr_abc:dev_xyz")
```

### Biometric Challenge

```
Key:    bio_challenge:{challenge_id}
Type:   Hash
TTL:    60 detik (single-use, sangat pendek)

Fields:
  challenge     → random 32-byte base64
  device_id     → dev_xyz789
  created_at    → timestamp
```

### Verification Token (PIN untuk transaksi)

```
Key:    vtoken:{token_hash}
Type:   Hash
TTL:    120 detik

Fields:
  user_id        → usr_abc123
  purpose        → EWALLET_TOPUP
  transaction_id → txn_001 (optional)
  created_at     → timestamp
```

---

## 2. Data Cache (DB 1)

### Dashboard (Beranda)

```
Key:    cache:dashboard:{user_id}
Type:   String (JSON)
TTL:    60 detik

Content: Serialized DashboardResponse (balance + promos + notif count)

Invalidation:
  - Saat transaksi berhasil → DEL cache:dashboard:{user_id}
  - Saat notifikasi baru   → DEL cache:dashboard:{user_id}
  - Saat profil berubah    → DEL cache:dashboard:{user_id}
```

### Account Balance

```
Key:    cache:balance:{account_id}
Type:   String (JSON)
TTL:    30 detik (saldo berubah cepat, TTL pendek)

Content: {"balance": 15750000.00, "hold": 500000.00, "available": 15250000.00}

Invalidation:
  - Setiap transaksi yang melibatkan account ini → DEL
  - TTL pendek sebagai fallback
```

### Account Profile

```
Key:    cache:profile:{user_id}
Type:   String (JSON)
TTL:    10 menit

Invalidation:
  - Saat profil di-update → DEL cache:profile:{user_id}
  - Saat settings berubah → DEL cache:profile:{user_id}
```

### Transaction Mutations

```
Key:    cache:mutations:{account_id}:{period}:{cursor_hash}
Type:   String (JSON)
TTL:    60 detik

period: LAST_7_DAYS | THIS_MONTH | LAST_MONTH | CUSTOM_20260801_20260831

Invalidation:
  - Saat transaksi baru   → DEL cache:mutations:{account_id}:*
  - Pattern delete via SCAN
```

### Transaction History

```
Key:    cache:history:{user_id}:{type}:{cursor_hash}
Type:   String (JSON)
TTL:    60 detik

Invalidation:
  - Saat transaksi baru → DEL cache:history:{user_id}:*
```

### Transaction Receipt

```
Key:    cache:receipt:{transaction_id}
Type:   String (JSON)
TTL:    24 jam (receipt immutable, cache lama)

Invalidation: Tidak perlu — receipt tidak berubah
```

### E-Wallet Providers

```
Key:    cache:ewallet:providers
Type:   String (JSON)
TTL:    1 jam

Invalidation:
  - Saat admin update provider → DEL cache:ewallet:providers
  - Long TTL karena jarang berubah
```

### Recent Transfers

```
Key:    cache:transfers:recent:{user_id}
Type:   String (JSON)
TTL:    5 menit

Invalidation:
  - Saat transfer berhasil → DEL cache:transfers:recent:{user_id}
```

### Notifications

```
Key:    cache:notif:{user_id}
Type:   String (JSON)
TTL:    60 detik

Key:    cache:notif:count:{user_id}
Type:   String (integer)
TTL:    60 detik

Invalidation:
  - Saat notif baru     → DEL cache:notif:{user_id}, DEL cache:notif:count:{user_id}
  - Saat notif dibaca   → DEL both
```

### Promotions

```
Key:    cache:promotions:active
Type:   String (JSON)
TTL:    5 menit

Invalidation:
  - Saat promo berubah → DEL cache:promotions:active
```

### Health / App Config

```
Key:    cache:health:config
Type:   String (JSON)
TTL:    5 menit

Content: maintenance_mode, min_version, feature_flags
```

---

## 3. Rate Limiting & Locks (DB 2)

### Login Rate Limit

```
Key:    rate:login:{device_id}
Type:   String (counter)
TTL:    15 menit

Limit:  5 attempts per 15 menit
```

**Implementasi Sliding Window:**

```go
// Sliding window log menggunakan Sorted Set
Key:    rate:login:log:{device_id}
Type:   Sorted Set
Score:  Unix timestamp (milliseconds)
Member: unique request ID

TTL:    15 menit

// Check rate limit
ZREMRANGEBYSCORE rate:login:log:{device_id} 0 {15_menit_lalu}
ZCARD rate:login:log:{device_id}
if count >= 5 → REJECT
ZADD rate:login:log:{device_id} {now} {request_id}
```

### Account Lockout

```
Key:    lock:account:{user_id}
Type:   String
TTL:    30 menit
Value:  "locked"

// Setelah 5x PIN salah → SET lock:account:{user_id} "locked" EX 1800
```

### API Rate Limit (per user, authenticated)

```
Key:    rate:api:{user_id}:{window}
Type:   String (counter)
TTL:    Sesuai window

Windows:
  - Per second:  10 requests
  - Per minute:  120 requests
  - Per hour:    3000 requests
```

### Idempotency Keys

```
Key:    idem:{idempotency_key}
Type:   String (JSON)
TTL:    24 jam

Content: Response dari transaksi pertama

Flow:
  1. Request masuk dengan idempotency_key
  2. SETNX idem:{key} "PROCESSING" EX 30
  3. Jika key sudah ada dan value != "PROCESSING" → return cached response
  4. Jika key sudah ada dan value == "PROCESSING" → return 409 Conflict
  5. Proses transaksi
  6. SET idem:{key} {response_json} EX 86400
```

### Distributed Lock (untuk transaksi)

```
Key:    dlock:transfer:{source_account_id}
Type:   String
TTL:    30 detik (auto-release)
Value:  lock_owner_id (UUID)

// Menggunakan Redlock pattern untuk distributed locking
SET dlock:transfer:{account_id} {owner_id} NX EX 30

// Release
if GET == owner_id → DEL
```

### Transfer Inquiry Cache

```
Key:    inquiry:{inquiry_id}
Type:   Hash
TTL:    5 menit

Fields:
  user_id             → usr_abc
  destination_account → 0987654321
  destination_name    → JOHN DOE
  bank_code           → 014
  amount              → 0 (belum ditentukan saat inquiry)
  admin_fee           → 0
```

---

## 4. Cache Invalidation Patterns

### Pattern 1: Write-Through (untuk data kritis)

```
Tulis ke DB → Update Redis → Return response

Digunakan untuk:
  - Balance updates
  - Session creation/revocation
  - Notification count
```

### Pattern 2: Cache-Aside (untuk data read-heavy)

```
Read Redis → Miss? → Read DB → Write Redis → Return

Digunakan untuk:
  - Mutations listing
  - Transaction history
  - Profile data
  - Promotions
```

### Pattern 3: Event-Driven Invalidation

```
Transaction SUCCESS → Publish event → Invalidate:
  - cache:balance:{source_account}
  - cache:balance:{dest_account}
  - cache:dashboard:{user_id}
  - cache:mutations:{account_id}:*
  - cache:history:{user_id}:*
  - cache:transfers:recent:{user_id}
```

### Bulk Invalidation Helper

```go
// InvalidateTransactionCaches menghapus semua cache terkait transaksi
func (r *RedisRepo) InvalidateTransactionCaches(ctx context.Context, userID, accountID string) error {
    keys := []string{
        fmt.Sprintf("cache:balance:%s", accountID),
        fmt.Sprintf("cache:dashboard:%s", userID),
        fmt.Sprintf("cache:transfers:recent:%s", userID),
    }

    // Pattern-based deletion untuk mutations dan history
    patterns := []string{
        fmt.Sprintf("cache:mutations:%s:*", accountID),
        fmt.Sprintf("cache:history:%s:*", userID),
    }

    pipe := r.client.Pipeline()
    for _, key := range keys {
        pipe.Del(ctx, key)
    }

    for _, pattern := range patterns {
        iter := r.client.Scan(ctx, 0, pattern, 100).Iterator()
        for iter.Next(ctx) {
            pipe.Del(ctx, iter.Val())
        }
    }

    _, err := pipe.Exec(ctx)
    return err
}
```

---

## 5. TTL Summary Table

| Key Pattern | TTL | Rationale |
|-------------|-----|-----------|
| `session:*` | 15 menit | Access token lifetime |
| `refresh:*` | 7 hari | Refresh token lifetime |
| `bio_challenge:*` | 60 detik | Single-use, security |
| `vtoken:*` | 120 detik | Transaction window |
| `cache:balance:*` | 30 detik | Saldo berubah cepat |
| `cache:dashboard:*` | 60 detik | Agregasi balance + notif |
| `cache:profile:*` | 10 menit | Jarang berubah |
| `cache:mutations:*` | 60 detik | Data aktif |
| `cache:history:*` | 60 detik | Data aktif |
| `cache:receipt:*` | 24 jam | Immutable |
| `cache:ewallet:providers` | 1 jam | Master data |
| `cache:transfers:recent:*` | 5 menit | Semi-aktif |
| `cache:notif:*` | 60 detik | Real-time feel |
| `cache:promotions:active` | 5 menit | Marketing data |
| `cache:health:config` | 5 menit | App config |
| `rate:login:*` | 15 menit | Rate limit window |
| `lock:account:*` | 30 menit | Lockout duration |
| `idem:*` | 24 jam | Idempotency guarantee |
| `dlock:*` | 30 detik | Auto-release lock |
| `inquiry:*` | 5 menit | Inquiry validity |

---

## 6. Redis Configuration

```conf
# redis.conf untuk banking app

# Memory
maxmemory 2gb
maxmemory-policy allkeys-lru    # Untuk cache DB
# DB 0 (sessions): noeviction — sessions tidak boleh hilang tiba-tiba

# Persistence
appendonly yes                   # AOF untuk durability
appendfsync everysec             # Fsync setiap detik
auto-aof-rewrite-percentage 100
auto-aof-rewrite-min-size 64mb

# Security
requirepass <strong_password>
rename-command FLUSHALL ""       # Disable dangerous commands
rename-command FLUSHDB ""
rename-command DEBUG ""
rename-command CONFIG ""

# Network
bind 127.0.0.1                   # Hanya local atau internal network
protected-mode yes
timeout 300

# TLS (wajib untuk production)
tls-port 6380
tls-cert-file /etc/redis/tls/redis.crt
tls-key-file /etc/redis/tls/redis.key
tls-ca-cert-file /etc/redis/tls/ca.crt

# Limits
maxclients 10000
tcp-backlog 511
```

---

## 7. Monitoring & Alerting

### Key Metrics to Monitor

```
1. Hit Rate        → cache:hit / (cache:hit + cache:miss)  → Target: > 90%
2. Memory Usage    → INFO memory                           → Alert: > 80%
3. Connected Clients → INFO clients                        → Alert: > 8000
4. Evictions       → INFO stats (evicted_keys)             → Alert: > 0 untuk DB 0
5. Latency         → LATENCY HISTORY                       → Alert: p99 > 5ms
6. Key Count       → DBSIZE                                → Trending
7. Rate Limit Hits → Custom counter                        → Security alert threshold
```