# Redis Caching & Session Strategy

> Key design, TTL strategy, invalidation patterns, dan data structures

> **Status:** Dokumen ini sudah dikoreksi dan konsisten dengan SKILL.md §7 dan §14.

---

## Arsitektur Redis

```
┌─────────────────────────────────┐  ┌──────────────────────────────────┐
│      Redis Session Instance     │  │       Redis Cache Instance       │
│      maxmemory-policy:          │  │       maxmemory-policy:          │
│        noeviction               │  │         allkeys-lru              │
│      AOF: appendfsync everysec  │  │       maxmemory: 512mb          │
│                                 │  │                                  │
│  session:*, refresh:*,          │  │  cache:*, cachever:*,           │
│  bio_challenge:*, vtoken:*,     │  │  rate:*, lock:*, dlock:*        │
│  inquiry:*, idem:*              │  │                                  │
└─────────────────────────────────┘  └──────────────────────────────────┘
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

## 1. Session Management (Session Instance)

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

// Logout semua device (via Set, bukan SCAN)
SMEMBERS sessions:user:usr_abc → DEL each session
DEL sessions:user:usr_abc
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

### Signaling Token (sekali pakai)

```
Key:    signal:jti:{jti}
Type:   String ("1")
TTL:    sisa umur token (maks 5 menit)
Write:  SETNX saat WebSocket signaling tersambung
```

Token signaling video call dibawa di query string `?token=`, jadi ia berakhir di
log proxy dan laporan crash: URL yang tersalin adalah kredensial yang tersalin.
`SETNX` yang gagal berarti token sudah dipakai → koneksi ditolak `401`.

Kalau Redis tidak terjangkau, koneksi ditolak `503`, **tidak** diterima. Ini satu
dari sedikit tempat di mana gagal-terbuka tidak boleh: yang masuk adalah video
call e-KYC yang sedang berjalan.

### User Session Set (untuk logout-all tanpa SCAN)

```
Key:    sessions:user:{user_id}
Type:   Set
TTL:    7 hari
Members: device_id list

Dipakai untuk logout-all tanpa SCAN.
```

### Revoked Refresh Token (reuse detection)

```
Key:    refresh:revoked:{hash}
Type:   String
TTL:    sisa lifetime refresh token
Value:  "revoked"

Kalau hash ini ditemukan saat refresh → compromise detected,
cabut SEMUA sesi user + audit SECURITY_SUSPICIOUS_LOGIN.
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

## 2. Data Cache (Cache Instance)

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
Key:    cache:mutations:{account_id}:v{n}:{period}:{cursor_hash}
Type:   String (JSON)
TTL:    60 detik

period: LAST_7_DAYS | THIS_MONTH | LAST_MONTH | CUSTOM_20260801_20260831

Invalidation:
  - Saat transaksi baru   → INCR cachever:mutations:{account_id}
  - Stale keys expire via TTL (no SCAN needed)
```

### Transaction History

```
Key:    cache:history:{user_id}:v{n}:{type}:{cursor_hash}
Type:   String (JSON)
TTL:    60 detik

Invalidation:
  - Saat transaksi baru → INCR cachever:history:{user_id}
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

### Konten Statis (Pusat Bantuan & Kontak CS)

```
Key:    content:help_center:v1
Key:    content:contact_cs:v1
Type:   String (JSON)
TTL:    24 jam

Invalidation:
  - TIDAK ADA invalidasi eksplisit — dan itu disengaja.
    Isinya diubah lewat SQL (UPDATE content_help_center / content_contact_cs),
    dan siapa pun yang mengubahnya lewat psql tidak akan menjalankan perintah
    invalidasi. TTL 24 jam adalah janji bahwa perubahan PASTI terlihat tanpa
    ada yang perlu diingat.
  - Perlu langsung terlihat? DEL kedua key itu setelah UPDATE.
  - Versi ada di dalam key: mengubah BENTUK response berarti menaikkan v1 → v2,
    dan entri lama kedaluwarsa sendiri.
```

Kegagalan Redis di dua key ini **tidak boleh** menggagalkan permintaan: layanan
jatuh ke Postgres dan mencatat peringatan. Halaman inilah tempat nasabah yang
terkunci di luar aplikasi mencari nomor CS — mematikannya karena cache bermasalah
justru menutup pintu keluarnya.

### Katalog Jenis Rekening Tabungan

```
Key:    onboarding:products:v1:catalog        ← katalog aktif
Key:    onboarding:products:v1:ver:{version}  ← snapshot per versi katalog
Type:   String (JSON)
TTL:    24 jam

Invalidation:
  - PUT /internal/v1/onboarding/products melakukan DEL-nya SENDIRI, di
    ProductAdminService.invalidate, sesudah transaksi commit. Jalur admin
    adalah cara yang benar mengubah katalog justru karena ini: dua langkah
    yang mudah terlupakan jadi satu permintaan.
    DEL yang gagal TIDAK menggagalkan penulisan — transaksinya sudah commit,
    dan error di titik itu akan membuat pemanggil mengulang permintaan yang
    sudah berhasil, menaikkan versi sekali lagi untuk perubahan yang sama.
    Kegagalannya dicatat slog.Error beserta perintah DEL yang perlu dijalankan
    manual. Itu satu-satunya keadaan yang menuntut tindakan operator.
  - Mengubah katalog LEWAT SQL MANUAL tetap WAJIB diikuti
    DEL onboarding:products:v1:catalog.
    Kenaikan onboarding_product_catalog_version.counter TIDAK cukup: entri
    :catalog menyimpan catalog_version LAMA di dalamnya, jadi tanpa DEL
    nasabah melihat setoran awal lama DAN ETag lama sampai TTL habis — dan
    karena ETag-nya konsisten dengan isinya, tidak ada error yang terlihat.
    Yang terjadi hanyalah sehari penuh layar yang menampilkan angka keliru,
    dan setoran awal adalah komitmen 30 hari kalender (pasal 4 S&K).
    Keterbatasan yang sama dengan onboarding:tnc:v1:active.
  - Kunci :catalog TIDAK memuat versinya, berbeda dari katalog kartu yang
    kunci-nya memuat versi. Itu sebabnya DEL dibutuhkan di sini dan tidak di
    sana: di katalog kartu, versi baru otomatis menghasilkan kunci baru.
  - Entri :ver:{version} tidak perlu di-DEL: isinya per versi dan versi lama
    tidak berubah lagi. Ia ada supaya product_catalog_version yang sudah
    tersimpan di baris sesi tetap punya isinya.
  - Katalog KOSONG tidak pernah di-cache: menyimpannya berarti menahan jawaban
    503 selama 24 jam setelah migrasi yang mengisinya akhirnya dijalankan.
  - Status maintenance (ONBOARDING_PRODUCTS_MAINTENANCE) diterapkan SESUDAH
    cache dibaca, jadi mengubah env itu langsung terlihat tanpa menunggu TTL.
  - Entri yang tidak bisa di-decode diperlakukan sebagai MISS, bukan kegagalan:
    bentuk response yang berubah tanpa menaikkan v1 akan meninggalkan entri
    rusak, dan menolak permintaan karenanya berarti layar pertama buka rekening
    mati sampai seseorang menghapus kuncinya.
  - Galat Redis apa pun TURUN ke Postgres, bukan ke halaman error. Redis yang
    mati tidak boleh menutup pintu masuk buka rekening.
```

### Syarat & Ketentuan Buka Rekening

```
Key:    onboarding:tnc:v1:active          ← versi yang sedang berlaku
Key:    onboarding:tnc:v1:ver:{version}   ← satu versi tertentu (termasuk yang dicabut)
Type:   String (JSON)
TTL:    24 jam

Invalidation:
  - Mengaktifkan versi baru WAJIB diikuti DEL onboarding:tnc:v1:active.
    Ini satu-satunya key di dokumen ini yang invalidasinya wajib, bukan opsional.
    Tanpa DEL, nasabah melihat teks LAMA sampai TTL habis — dan karena
    ValidateVersion membaca key yang sama, persetujuannya tetap DITERIMA.
    Jadi yang terjadi bukan error yang terlihat, tapi sehari penuh persetujuan
    yang tercatat atas versi yang sudah dicabut. Itu justru kerusakan yang
    seluruh migrasi 000025 dibuat untuk mencegah.
  - Entri :ver:{version} tidak perlu di-DEL: isinya per versi dan versi lama
    tidak berubah lagi.
  - Versi BENTUK response ada di dalam key (v1): mengubah bentuknya berarti
    menaikkan v1 → v2, dan entri lama kedaluwarsa sendiri.
```

`{version}` yang ikut ke dalam key dibatasi `^[A-Za-z0-9._-]{1,20}$` sebelum dipakai.
Nilainya datang dari query string dan dari body request, jadi tanpa batas itu setiap nilai
karangan mencetak key baru — dan titik dua di dalamnya bisa menyelipkan pemisah tambahan
ke dalam key. Pelajaran yang sama dari `normalizeRegionCode` pada katalog kartu.

Kegagalan Redis di sini **tidak boleh** menggagalkan permintaan: layanan jatuh ke Postgres
dan mencatat peringatan. Ini layar PERTAMA buka rekening — mematikannya karena cache
bermasalah berarti menutup pintu masuk nasabah baru.

### Notifications

```
Key:    cache:notif:{user_id}:v{n}:{cursor_hash}
Type:   String (JSON)
TTL:    60 detik

Key:    cache:notif:count:{user_id}
Type:   String (integer)
TTL:    60 detik

Invalidation:
  - Saat notif baru     → INCR cachever:notif:{user_id}, DEL cache:notif:count:{user_id}
  - Saat notif dibaca   → INCR cachever:notif:{user_id}, DEL cache:notif:count:{user_id}
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

## 3. Rate Limiting & Locks (Cache Instance)

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
Key:    idem:{user_id}:{idempotency_key}
Type:   String (JSON)
TTL:    24 jam

Content: Response dari transaksi pertama

Flow:
  1. Request masuk dengan idempotency_key
  2. SETNX idem:{user_id}:{key} "PROCESSING" EX 30
  3. Jika key sudah ada dan value != "PROCESSING" → return cached response
  4. Jika key sudah ada dan value == "PROCESSING" → return 409 Conflict
  5. Proses transaksi
  6. SET idem:{user_id}:{key} {response_json} EX 86400
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
Transaction SUCCESS → Invalidate ALL affected parties:
  - cache:balance:{account_id}        → DEL
  - cache:dashboard:{user_id}         → DEL
  - cache:transfers:recent:{user_id}  → DEL
  - cache:notif:count:{user_id}       → DEL
  - cachever:mutations:{account_id}   → INCR
  - cachever:history:{user_id}        → INCR
  - cachever:notif:{user_id}          → INCR
```

### Bulk Invalidation Helper

```go
// InvalidateTransactionCaches invalidates caches for ALL affected parties.
// An internal transfer affects two users — both must be invalidated.
type AffectedParty struct{ UserID, AccountID string }

func (r *RedisRepo) InvalidateTransactionCaches(ctx context.Context, parties []AffectedParty) error {
    pipe := r.client.Pipeline()
    for _, p := range parties {
        pipe.Del(ctx, "cache:balance:"+p.AccountID)
        pipe.Del(ctx, "cache:dashboard:"+p.UserID)
        pipe.Del(ctx, "cache:transfers:recent:"+p.UserID)
        pipe.Del(ctx, "cache:notif:count:"+p.UserID)
        pipe.Incr(ctx, "cachever:mutations:"+p.AccountID)
        pipe.Incr(ctx, "cachever:history:"+p.UserID)
        pipe.Incr(ctx, "cachever:notif:"+p.UserID)
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
| `signal:jti:*` | ≤ 5 menit | Token signaling sekali pakai; TTL = sisa umur token |
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
| `cachever:*` | none | Version counter — INCR to invalidate |
| `sessions:user:*` | 7 hari | Set of device_ids for logout-all |
| `refresh:revoked:*` | remaining | Reuse detection |
| `rate:login:*` | 15 menit | Rate limit window |
| `lock:account:*` | 30 menit | Lockout duration |
| `idem:*` | 24 jam | Idempotency guarantee |
| `dlock:*` | 30 detik | Auto-release lock |
| `onboarding:products:v1:*` | 24 jam | Katalog produk; `PUT /internal/v1/onboarding/products` meng-`DEL …:catalog` sendiri, perubahan SQL manual wajib melakukannya |
| `inquiry:*` | 5 menit | Inquiry validity |

---

## 6. Redis Configuration

```conf
# redis.conf untuk banking app

# Memory
maxmemory 2gb
# Ini config untuk Cache instance. Session instance menggunakan:
#   maxmemory-policy noeviction
#   appendonly yes / appendfsync everysec
maxmemory-policy allkeys-lru

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
4. Evictions       → INFO stats (evicted_keys)             → Alert: > 0 untuk session instance
5. Latency         → LATENCY HISTORY                       → Alert: p99 > 5ms
6. Key Count       → DBSIZE                                → Trending
7. Rate Limit Hits → Custom counter                        → Security alert threshold
```