---
name: bca-mobile-backend
description: "Build, review, or QA the BCA Mobile banking backend API (Go + chi + pgx/PostgreSQL + Redis, modular monolith). Use for endpoints (auth/PIN, biometrik, saldo, Beranda, Mutasi, Riwayat, Transfer, E-Wallet, QRIS, notifikasi, registrasi/KYC), migrations, Redis key design and cache invalidation, JWT/session/idempotency/rate-limit logic, double-entry ledger and daily limits, audit trail, or QA test plans for this service."
---

# BCA Mobile Backend API

Go 1.23+ modular monolith. `chi` router, `pgx/v5` + PostgreSQL 16, `go-redis/v9` + Redis 7, `log/slog`, `golang-jwt/v5` (RS256), `go-playground/validator/v10`, `caarlos0/env/v11`.

**Banking-grade means:** fail secure (error → deny), strong consistency for money, every state change audited, every request verified even internally, financial operations idempotent.

---

## 0. Non-negotiables

Never ship a change that violates these. If a request conflicts with one, stop and flag it.

1. **Money is never `float64`.** Use `int64` in **rupiah minor units (sen, 2 decimals)** across Go code, or `pgtype.Numeric`. DB is `DECIMAL(18,2)`. In JSON, emit amounts as a **string** (`"1500000.00"`) or integer minor units — never a JSON float.
2. **Inquiry is the source of truth for a transaction's destination.** `execute`/`topup`/`pay` derive destination, bank, provider, and fee from the stored inquiry by `inquiry_id`. Body fields are cross-check only; a mismatch is `422 VALIDATION_ERROR`, never a silent override.
3. **Every read/write of an account, transaction, receipt, or notification verifies `user_id` ownership** in the SQL `WHERE`, not in Go after fetching. Wrong owner → `404`, never `403` (no existence leak).
4. **Parameterized queries only.** Never `fmt.Sprintf` into SQL.
5. **Idempotency is scoped per user.** Redis `idem:{user_id}:{key}`; DB `UNIQUE (user_id, idempotency_key)`.
6. **Single-use tokens are consumed atomically** (Lua / `GETDEL` / `UPDATE ... WHERE used_at IS NULL RETURNING`). A `GET` then a separate `DEL` is a replay bug.
7. **No sensitive data in logs, error messages, or URLs.** No PIN, no full account number, no token, no PII. Error messages to the client never echo user input.
8. **Audit before responding** for every state change (see §9).
9. **Constant-time comparison** for every secret comparison (`subtle.ConstantTimeCompare`).

---

## 1. Layout

```
cmd/server/main.go              wiring only, no logic
internal/config/                env-based config (caarlos0/env)
internal/domain/{auth,account,transaction,ewallet,notification}/
    entity.go       pure structs + enums, zero external deps
    repository.go   interfaces owned by the domain
    service.go      business logic
internal/handler/               HTTP adapters: decode → validate → call service → respond
internal/middleware/            auth, ratelimit, logging, recovery, cors, requestid, security, audit
internal/repository/postgres/   pgx implementations of domain interfaces
internal/repository/redis/      session_repo, cache_repo, ratelimit_repo
internal/router/router.go
internal/pkg/{crypto,validator,response,pagination,idempotency}/
migrations/                     golang-migrate, NNNNNN_name.{up,down}.sql
```

**Dependency rule:** `domain` imports nothing from `handler`, `repository`, or `router`. Repositories implement interfaces declared in `domain`.

**Handlers contain no business logic.** A handler that computes a fee, checks a balance, or decides a limit is misplaced — move it to the service.

**Notifications and QRIS get their own handlers** (`notification_handler.go`, `qris_handler.go`). *Spec note: `05-PROJECT-SETUP` router hangs them off `TransactionHandler` — that contradicts its own project structure. Use dedicated handlers.*

---

## 2. Response envelope

Always via `internal/pkg/response`. Never `json.NewEncoder(w).Encode(domainStruct)` directly.

```json
{"status":"success","data":{},"meta":{"request_id":"req_...","timestamp":"2026-09-02T10:30:00Z"}}
{"status":"error","error":{"code":"AUTH_INVALID_PIN","message":"Kode akses salah. Silakan coba lagi.","details":null},"meta":{}}
{"status":"success","data":[],"pagination":{"cursor":"...","has_more":true,"limit":20},"meta":{}}
```

`timestamp` is UTC RFC3339. `request_id` from `chimiddleware.GetReqID`.

**Error messages are Indonesian, user-facing, and fixed per code.** Never interpolate user input into them. Machine-readable meaning lives in `code`, not `message`.

| Code | HTTP | Code | HTTP |
|---|---|---|---|
| `VALIDATION_ERROR` | 400 | `TRANSFER_ACCOUNT_NOT_FOUND` | 404 |
| `AUTH_INVALID_PIN` | 401 | `TRANSFER_INSUFFICIENT_BALANCE` | 422 |
| `AUTH_TOKEN_EXPIRED` | 401 | `TRANSFER_LIMIT_EXCEEDED` | 422 |
| `AUTH_TOKEN_INVALID` | 401 | `TRANSFER_SELF_TRANSFER` | 422 |
| `AUTH_BIOMETRIC_NOT_REGISTERED` | 401 | `EWALLET_ACCOUNT_NOT_FOUND` | 404 |
| `AUTH_DEVICE_NOT_RECOGNIZED` | 403 | `EWALLET_INSUFFICIENT_BALANCE` | 422 |
| `AUTH_OLD_PIN_MISMATCH` | 422 | `EWALLET_PROVIDER_DOWN` | 503 |
| `AUTH_ACCOUNT_LOCKED` | 423 | `IDEMPOTENCY_CONFLICT` | 409 |
| `ACCOUNT_NOT_FOUND` | 404 | `RATE_LIMIT_EXCEEDED` | 429 |
| `INQUIRY_EXPIRED` | 422 | `MAINTENANCE_MODE` | 503 |
| `INQUIRY_MISMATCH` | 422 | `INTERNAL_ERROR` | 500 |
| `VERIFICATION_TOKEN_INVALID` | 401 | | |

`INQUIRY_EXPIRED`, `INQUIRY_MISMATCH`, `VERIFICATION_TOKEN_INVALID` are additions to `01-API-SPECIFICATION`'s table — the spec's flows need them but never named them.

---

## 3. Authentication

### Tokens

| | Access | Refresh |
|---|---|---|
| Alg | RS256 | RS256 |
| TTL | 15m | 7d (168h) |
| Claims | `sub, sid, did, typ:"access", iat, exp` | `sub, sid, did, typ:"refresh", jti, iat, exp` |
| Client storage | memory only | EncryptedSharedPreferences |

`typ` **must** be checked — an access token must never validate on the refresh endpoint, or vice versa.

**Rotation with reuse detection.** On refresh: issue a new pair, and mark the old hash `refresh:revoked:{hash}` with TTL = remaining lifetime. Do **not** merely delete it — a deleted key is indistinguishable from an expired one, which destroys reuse detection. If a request presents a hash found in `revoked`, treat it as compromise: revoke **all** the user's sessions, write `SECURITY_SUSPICIOUS_LOGIN` audit, push-notify the device.

### PIN

- Hash: **Argon2id**, time=3, memory=64MB, threads=4, keyLen=32, salt=16 bytes.
- 64MB × 4 threads per verify is heavy. Gate concurrent Argon2 operations behind a **semaphore** (cap ≈ `min(NumCPU, 8)`), otherwise a login burst exhausts memory.
- Transport: RSA-2048 **OAEP with SHA-256**. *Spec note: `04-SECURITY` says only "RSA" — PKCS#1 v1.5 is not acceptable.* The encrypted payload is a JSON object `{"pin":"123456","nonce":"<uuid>","ts":<unix>}`; reject if `ts` skews more than 60s or the nonce was already seen (`pin_nonce:{nonce}`, TTL 120s). Without this, a captured `pin_encrypted` replays forever.
- Private keys live in an HSM in production; encrypted files are dev-only.

### Login by PIN — required flow

1. Rate limit by device (§10). Rejected → `429`.
2. Resolve the user **from `device_id` alone** — this is the project's decision, applied in migration `000009`: `devices.device_id` is unique across all users while `revoked_at IS NULL` (1 active device = 1 user). No active row → `403 AUTH_DEVICE_NOT_RECOGNIZED`. *Spec note: `05-PROJECT-SETUP`'s `GetDevice(ctx, uuid.Nil, req.DeviceID)` was unresolvable under the old `UNIQUE (user_id, device_id)`; it works now.*
   - Uniqueness is **partial**, not global. `revoked_at = NOW()` releases the fingerprint so a resold or re-provisioned handset can be registered to its new owner. Never delete device rows to free a fingerprint — revoke them.
   - `device_id` is now an identity credential, so it must be a stable, unguessable, Keystore-backed fingerprint. It says *who*; the PIN or biometric still proves it.
   - **Known risk this creates:** a spoofed `device_id` steers a login attempt at the victim's account, so an attacker who guesses one can lock that account out by failing PIN five times. Per-IP rate limiting (§10) is therefore mandatory, not optional, and every lockout must push-notify the registered device.
3. Check lockout (`lock:account:{user_id}` and `users.locked_until`). Locked → `423` with `locked_until`.
4. Decrypt PIN, verify with Argon2id.
5. On failure: `UPDATE users SET failed_pin_attempts = failed_pin_attempts + 1 ... RETURNING failed_pin_attempts` and branch on the **returned** value. *Spec note: `05-PROJECT-SETUP` branches on the stale in-memory `user.FailedPINAttempts+1` — a race that lets attempt 6 and 7 through.* At `>= max_pin_attempts`: lock 30m, set `lock:account:{user_id}` EX 1800, push-notify, audit `SECURITY_ACCOUNT_LOCKED`. Three lockouts within 24h → 24h lock + CS notification.
6. Response reveals `attempts_remaining` only for a **known** user; for an unknown device/user return the same `AUTH_INVALID_PIN` shape with `details: null` and comparable timing (do the Argon2 work against a dummy hash) — otherwise the endpoint is a user-enumeration oracle.
7. On success: reset attempts, update `last_login_at`, create session, audit `AUTH_LOGIN_SUCCESS`.

### Biometric (FIDO2-style)

`GET /auth/biometric/challenge` → random 32 bytes, stored `bio_challenge:{challenge_id}` TTL **60s**, holding `challenge`, `device_id`, and (once registered) the expected `key_id`.

`POST /auth/login/biometric` → consume the challenge **atomically** (`GETDEL`), load the public key by `key_id`, and verify that the key's `device_id` matches both the challenge's and the request's. Verify the signature over the exact challenge bytes. A challenge that is merely read and later deleted is replayable.

The server never receives biometric data — only a signature. Do not add endpoints that accept biometric templates.

### PIN change and verification

- `POST /auth/pin/change` requires the current access token **and** a fresh `verification_token` with purpose `CHANGE_PIN`. A wrong `old_pin` counts toward the same lockout counter as login. On success: **revoke all other sessions**, keep the current one, push-notify, audit `AUTH_PIN_CHANGED`.
- `POST /auth/pin/verify` issues a `verification_token`, TTL **120s**, single-use, bound to `user_id` **and** `purpose`. A token minted for `EWALLET_TOPUP` must be rejected by `/transfer/execute`.
- Allowed purposes are exactly: `TRANSFER`, `EWALLET_TOPUP`, `QRIS_PAYMENT`, `CHANGE_LIMIT`, `CHANGE_PIN`. *Spec note: `01-API-SPECIFICATION` shows `PAYMENT`, which the `verification_tokens.purpose` CHECK constraint rejects. Use `QRIS_PAYMENT`; if a generic payment flow is added, extend the CHECK in a migration first.*
- `transaction_id` on `/auth/pin/verify` is **optional and unused for transfers**, because the transaction does not exist until execute. Bind the token to `purpose` + `user_id` only.

### Verification token / inquiry storage

**Redis is authoritative for liveness; PostgreSQL is the audit record.** Write both, but consume from Redis atomically, then mark the PG row `used_at`. If Redis has no key, the operation fails — never fall back to the PG row, or a Redis flush becomes a replay window.

---

## 4. PII and lookups

- Encrypt at the application level with **AES-256-GCM** (nonce prepended): `nik`, `phone`, `email`, address. Key from env/KMS, never in code.
- Lookups use a separate **HMAC-SHA256** column (`phone_hash`, `email_hash`), lowercased and trimmed before hashing. Plain SHA-256 is rainbow-tableable — always HMAC with `LOOKUP_HMAC_SECRET`.
- Masking is a **presentation** concern: `0812****5678`, `****4567`, `nur****@gmail.com`. Mask in the response mapper, never store masked values.

---

## 5. Executing a financial transaction

This applies to `/transfer/execute`, `/ewallet/topup`, `/qris/pay`. Follow every step, in order.

```
1. Idempotency guard (§6)                       → may short-circuit with cached response or 409
2. Consume verification_token (atomic, purpose-matched, user-matched)
3. Load + consume inquiry (atomic; expired → 422 INQUIRY_EXPIRED)
4. Cross-check body against inquiry → mismatch = 422 INQUIRY_MISMATCH
5. Self-transfer check (source == destination) → 422 TRANSFER_SELF_TRANSFER
6. BEGIN; SET TRANSACTION ISOLATION LEVEL SERIALIZABLE
7.   SELECT ... FOR UPDATE on affected accounts, ordered by account UUID ascending
8.   Check available_balance (= balance - hold_amount) >= amount + admin_fee
9.   Check daily_usage vs transaction_limits, inside this transaction
10.  UPDATE balances
11.  INSERT transactions (status SUCCESS or PENDING for async rails)
12.  INSERT account_mutations (see below)
13.  UPSERT daily_usage
14. COMMIT   — with retry on SQLSTATE 40001
15. Cache invalidation for EVERY affected party (§7)
16. Persist idempotent response
17. Audit + notification (async)
```

**Lock ordering (step 7).** Always `ORDER BY id` when locking multiple accounts. Without it, concurrent A→B and B→A transfers deadlock. *Spec note: `04-SECURITY`'s SQL locks only the source and never mentions ordering.*

**Serialization retry (step 14).** `SERIALIZABLE` **will** raise `40001` under load. Wrap the whole transaction in a retry loop: max 3 attempts, exponential backoff with jitter (≈10ms, 30ms, 90ms). Exhausted → `500 INTERNAL_ERROR`. *Spec note: `04-SECURITY` shows the isolation level with no retry; without it, normal contention surfaces to users as 500s.*

**Double-entry (step 12).** *Spec note: `04-SECURITY` claims every transfer produces two mutations. That holds only for internal BCA→BCA transfers with no fee.* Migration `000009` adds internal accounts — settlement per rail plus fee income, 16 shards each — so every rail balances:

| Rail | Legs |
|---|---|
| Transfer internal | DEBIT source `amount + fee` · CREDIT destination `amount` · CREDIT fee-income `fee` (omit if 0) |
| Transfer external | DEBIT source `amount + fee` · CREDIT `TRANSFER_EXTERNAL` shard `amount` · CREDIT fee-income `fee` |
| E-wallet top-up | DEBIT source `amount + fee` · CREDIT `EWALLET` shard `amount` · CREDIT fee-income `fee` |
| QRIS payment | DEBIT source `amount + fee` · CREDIT `QRIS` shard `amount` · CREDIT fee-income `fee` |

- The admin fee is revenue and needs its own credit leg. Debiting `amount + fee` from the customer while crediting only `amount` leaves the fee unaccounted for.
- Resolve the counter-account with `settlement_account_id(rail, transaction_id)` — a deterministic shard, sharded because a single settlement row would serialise the bank's entire outbound throughput behind one lock. Shards join the `ORDER BY id` locking like any other account.
- **Internal transfer:** resolve the destination `account_id` by `account_number` **inside** the transaction and store it in `transactions.destination_account_id`. `destination_account` stays denormalized text for receipt immutability.
- `balance_before` and `balance_after` must be the values observed under the row lock, per row.
- Reversal is a **new** transaction with status `REVERSED` and opposite legs — never an UPDATE.
- Invariants, checkable at any time: `v_unbalanced_transactions` (per-transaction debits = credits) and `v_ledger_reconciliation` (account balance = latest `balance_after`) must both be empty. Alert on either.

**Limits (step 9).** Migration `000009` seeds five `transaction_limits` rows per user via an `AFTER INSERT ON users` trigger, so the rows always exist. Still treat a missing row as `0` (deny), never unlimited. Map API keys → `limit_type`: `transfer_internal_daily`→`TRANSFER_INTERNAL`, `transfer_external_daily`→`TRANSFER_EXTERNAL`, `ewallet_daily`→`EWALLET`, plus `QRIS` and `PAYMENT`. Customer-raised limits are capped server-side (transfer ≤ Rp 100jt/day, e-wallet ≤ Rp 20jt/day, QRIS ≤ Rp 5jt per transaction); reject them in the service with `422` before the DB — `ck_transaction_limit_ceiling` is the second net, not the error path users should see.

**Reference number (step 11).** Call `next_reference_number(<WIB date>)` — `REF` + `YYYYMMDD` + 8 digits from a sequence passed through a multiply-modulo bijection, so numbers are collision-free but not a readable global counter. Pass the WIB date explicitly; the function must never see `CURRENT_DATE`.

**Async rails.** For external transfers or a provider call, insert `PENDING`, call the provider, then move to `SUCCESS`/`FAILED` — never hold a `SERIALIZABLE` transaction open across a network call to a third party.

---

## 6. Idempotency

Read the key from the **`X-Idempotency-Key` header only**. *Spec note: `01-API-SPECIFICATION` also puts `idempotency_key` in the body. Two sources = ambiguity. If the body field is present and differs from the header, return `400 VALIDATION_ERROR`; otherwise ignore it.* Missing header on a mutating endpoint → `400`.

```go
key := "idem:" + userID + ":" + idemKey   // ALWAYS user-scoped

// 1. Claim the slot atomically. Never GET first.
ok, _ := rdb.SetNX(ctx, key, statusProcessing, 30*time.Second).Result()
if !ok {
    v, _ := rdb.Get(ctx, key).Result()
    if v == statusProcessing {
        return conflict("IDEMPOTENCY_CONFLICT", "Transaksi sedang diproses.")
    }
    w.Header().Set("X-Idempotent-Replayed", "true")
    return writeRaw(v)          // stored envelope of the original response
}
// 2. Process. On ANY failure path: DEL the key so the client can retry.
// 3. On success: SET key <response_json> EX 86400
```

*Spec note: `04-SECURITY`'s handler does `Get` first and writes whatever it finds — so a concurrent replay receives the literal string `PROCESSING` as a 200 response body. `03-REDIS`'s sequence is the correct one.*

**After the 24h Redis TTL**, a replay reaches the DB and hits `UNIQUE (user_id, idempotency_key)`. On that unique violation, **fetch the existing transaction and return its response**, not `409`. A client retrying an old key must never be able to create a second transfer.

---

## 7. Redis

**Naming:** `{domain}:{entity}:{identifier}:{sub-key}`.

**Deployment.** *Spec note: `03-REDIS` shows a Redis **Cluster** with DB 0/1/2 and `maxmemory-policy allkeys-lru` plus "DB 0 is noeviction". Both are impossible: Cluster supports only DB 0, and the eviction policy is instance-wide, so sessions would be silently evicted under memory pressure.* Use **two instances**:

- **Session instance** — `maxmemory-policy noeviction`, AOF `appendfsync everysec`. Holds `session:*`, `refresh:*`, `bio_challenge:*`, `vtoken:*`, `inquiry:*`, `idem:*`.
- **Cache instance** — `allkeys-lru`. Holds `cache:*`, `rate:*`, `lock:*`, `dlock:*`.

Losing a cache key must never lose money or a session.

### Key registry

| Key | Type | TTL | Notes |
|---|---|---|---|
| `session:{user_id}:{device_id}` | Hash | 15m | access_token_hash, user_id, device_id, display_name, auth_method, ip, created_at, last_activity |
| `sessions:user:{user_id}` | Set | 7d | device_ids — use instead of `SCAN` for logout-all |
| `refresh:{hash}` | String | 7d | `user_id:device_id` |
| `refresh:revoked:{hash}` | String | remaining | reuse detection |
| `bio_challenge:{challenge_id}` | Hash | 60s | single-use, `GETDEL` |
| `vtoken:{hash}` | Hash | 120s | user_id, purpose; single-use |
| `inquiry:{inquiry_id}` | Hash | 5m | single-use |
| `idem:{user_id}:{key}` | String | 24h | `PROCESSING` then response JSON |
| `cache:health:config` | String | 5m | **config only**, never liveness |
| `cache:dashboard:{user_id}` | String | 60s | |
| `cache:balance:{account_id}` | String | 30s | |
| `cache:profile:{user_id}` | String | 10m | |
| `cache:mutations:{account_id}:v{n}:{period}:{cursor_hash}` | String | 60s | versioned |
| `cache:history:{user_id}:v{n}:{type}:{cursor_hash}` | String | 60s | versioned |
| `cache:notif:{user_id}:v{n}:{cursor_hash}` | String | 60s | versioned — **cursor must be in the key** |
| `cache:notif:count:{user_id}` | String | 60s | |
| `cache:receipt:{transaction_id}` | String | 24h | immutable, no invalidation |
| `cache:ewallet:providers` | String | 1h | |
| `cache:transfers:recent:{user_id}` | String | 5m | |
| `cache:promotions:active` | String | 5m | |
| `cachever:mutations:{account_id}` | String (int) | none | INCR to invalidate |
| `cachever:history:{user_id}` | String (int) | none | |
| `cachever:notif:{user_id}` | String (int) | none | |
| `rate:login:log:{device_id}` | ZSet | 15m | sliding window |
| `rate:api:{user_id}:{window}` | ZSet | window | |
| `lock:account:{user_id}` | String | 30m | lockout |
| `dlock:transfer:{account_id}` | String | 30s | `SET NX EX`, release only if value == owner (Lua) |

### Invalidation

**Use version counters, not `SCAN`.** `SCAN` with a pattern over a production keyspace is O(N) and blocks throughput. Bump `cachever:*` with `INCR`; stale keys expire on their own TTL.

**Invalidate every affected party.** *Spec note: `03-REDIS`'s `InvalidateTransactionCaches` takes a single `userID`/`accountID` — an internal transfer changes two accounts belonging to two different users, and the recipient's balance and dashboard stay stale for their whole TTL.* The helper must take a list:

```go
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

Settlement and fee-income shards have no customer-facing cache — do not add them to the invalidation list.

Invalidate **after** `COMMIT`, never inside the transaction. A failed invalidation is logged and retried, never fatal to the transfer.

Patterns: write-through for balance and session; cache-aside for listings, profile, promotions; event-driven invalidation on transaction success.

**Never cache a paginated endpoint without the cursor in the key.**

---

## 8. Database

Migrations `000001`–`000008` are the spec's schema; `000009_ledger_and_device_binding` applies the corrections below and is documented in `docs/06-LEDGER-AND-DEVICE-BINDING.md`. Anything numbered `000010`+ is new work.

- UUID PKs (`uuid_generate_v4()`), `TIMESTAMPTZ` in UTC everywhere, soft delete via `deleted_at` for users/accounts.
- `accounts.owner_type` splits `CUSTOMER` from `INTERNAL` (settlement and fee-income shards, `user_id IS NULL`). **Every customer-facing lookup must filter `owner_type = 'CUSTOMER'`.** Ownership-scoped queries (`WHERE user_id = $1`) exclude them for free, but lookups **by account number do not**: `POST /transfer/inquiry` against `9902000000` would otherwise resolve a settlement shard and let a customer transfer into the bank's own ledger. Filter explicitly in every by-account-number query.
- **Transactions and mutations are immutable — INSERT only.** A correction is a new reversing entry with status `REVERSED`, never an `UPDATE`.
- `audit_logs` is protected by a trigger that rejects UPDATE/DELETE. Do not disable it; do not write migrations that alter its rows.
- `updated_at` is maintained by the shared `set_updated_at()` BEFORE UPDATE trigger (000009). Never set it by hand. A new table with an `updated_at` column needs that trigger attached in its own migration.
- Retention: transactions indefinite; mutations 7 years; audit 10 years; sessions 30d after expiry; OTP 24h; inquiries 1h; verification tokens 5m — expiry cleanup by cron.
- `audit_logs` BRIN index assumes append-only physical order. At 10-year retention, plan monthly `RANGE` partitioning on `created_at`; same for `account_mutations`.

### Timezone

DB is UTC. The product is WIB (Asia/Jakarta, UTC+7). `CURRENT_DATE` follows the **server's** timezone, so a transfer at 06:00 WIB (23:00 UTC the previous day) would land on the wrong statement day and the wrong daily-limit bucket.

Migration `000009` removed the defaults from `account_mutations.transaction_date`, `account_mutations.transaction_time` and `daily_usage.usage_date` precisely so this cannot happen silently — the application computes the WIB calendar date and passes it explicitly. Never reintroduce `CURRENT_DATE`/`CURRENT_TIME` anywhere. The daily limit resets at WIB midnight. `LAST_7_DAYS` means the 7 WIB calendar days ending today, inclusive.

### Cursor pagination

The cursor tuple must match `ORDER BY` exactly, with `id` as the final tiebreaker.

```sql
-- ORDER BY transaction_date DESC, created_at DESC, id DESC
WHERE account_id = $1
  AND (transaction_date, created_at, id) < ($2, $3, $4)
ORDER BY transaction_date DESC, created_at DESC, id DESC
LIMIT $5 + 1                 -- fetch limit+1 to compute has_more
```

Cursor = base64 of `{"d":"2026-09-01","c":"...","i":"..."}`. *Spec note: `01-API-SPECIFICATION`'s cursor holds only `{id, date}`, which drops rows when several mutations share a date.* Cursors are opaque to clients; validate and reject malformed ones with `400`.

### What migration 000009 already provides

Do not re-implement these; call them.

| Object | Use |
|---|---|
| `settlement_account_id(rail, txn_id)` | Resolve the settlement/fee shard. Rails: `TRANSFER_EXTERNAL`, `EWALLET`, `QRIS`, `FEE_INCOME` |
| `next_reference_number(wib_date)` | Generate `reference_number` |
| `seed_default_transaction_limits(user_id)` | Idempotent; also fires automatically on user insert |
| `set_updated_at()` triggers | `updated_at` is maintained — do not set it by hand |
| `uq_txn_user_idempotency` | `UNIQUE (user_id, idempotency_key)` |
| `transactions.destination_account_id` | FK, internal transfers only |
| `idx_devices_device_id_active` | Partial unique on `device_id WHERE revoked_at IS NULL` |
| `v_ledger_reconciliation`, `v_unbalanced_transactions` | Ledger invariants; both must stay empty |
| `ck_transaction_limit_ceiling` | Server-side limit caps |

`transaction_date`, `transaction_time` and `usage_date` have **no defaults** — the application must pass the WIB values. A `not_null_violation` there means a timezone bug, not a missing column.

Changing the settlement shard count means changing the modulus inside `settlement_account_id` **and** the seeded shard rows together. Changing one alone resolves transactions to a shard that does not exist.

---

## 9. Audit

Log every: login success/failure, logout, PIN change, biometric register/revoke, transfer executed/failed, e-wallet top-up, QRIS payment, balance viewed, profile updated, limit changed, account locked, suspicious login.

Audit writes are **async and non-blocking** — an audit failure must never fail a transfer. But *spec note: `04-SECURITY` spawns a bare `go func()` per entry, which is unbounded under load and loses everything queued on crash.* Use a **buffered channel + fixed worker pool**, with a file fallback via `slog` when the DB insert fails and when the buffer is full.

A balance view is audited **even when the response was served from cache**.

Audit entries carry `request_id` so a log line, an audit row, and a client report can be joined.

---

## 10. Rate limiting

Sliding window over a Redis sorted set: `ZREMRANGEBYSCORE` → `ZCARD` → `ZADD` → `EXPIRE`, in one pipeline. Reject when count ≥ limit, and always set `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `Retry-After` on rejection.

| Layer | Limit |
|---|---|
| Per IP (gateway) | 100 req/s burst, 1000 req/min |
| Login, per device | 5 / 15 min → then 30 min account lockout |
| Login, per IP | required, not optional — blunts the `device_id` lockout-DoS (§3) |
| Registration, per device | 3 / hour |
| PIN verify, per user | 5 / 15 min, shares the login lockout counter |
| General API, per user | 10 req/s, 120 req/min, 3000 req/hour |
| Balance, per user | 2 req/s |
| Transfer execute, per user | 3 / min |

Count the attempt **before** knowing the outcome (the `ZADD` happens on every attempt), so a burst of successful logins also throttles. Rate-limit state failure (Redis down) is **fail-closed** on auth endpoints — deny — and fail-open on read-only endpoints.

---

## 11. Security headers and CORS

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
X-XSS-Protection: 0
Strict-Transport-Security: max-age=63072000; includeSubDomains; preload
Content-Security-Policy: default-src 'none'
Cache-Control: no-store, no-cache, must-revalidate, private
Pragma: no-cache
Referrer-Policy: no-referrer
Permissions-Policy: camera=(), microphone=(), geolocation=()
```

Mobile-only API: no browser origins allowed. Allowed headers: `Authorization`, `Content-Type`, `X-Device-ID`, `X-Request-ID`, `X-Idempotency-Key`.

`GET /health` **must split**: uncached liveness (real DB ping + Redis ping, ~1s timeout) and the 5-minute-cached config block (maintenance mode, min version, feature flags). *Spec note: caching the whole payload lets `/health` report `database: "ok"` while the database is down.*

`GET /transactions/{id}/receipt/pdf` requires the Bearer token, so `receipt_url` is not openable in a browser. Either document that the client must fetch it with the header, or issue a short-lived signed URL — do not leave it ambiguous.

Ledger health endpoints (`/internal/ledger/reconciliation`, `/internal/ledger/unbalanced`) are operations-only: never routed under the mobile auth group, IP-allowlisted at the gateway.

---

## 12. Implementing an endpoint — checklist

1. Confirm the contract against `01-API-SPECIFICATION` (path, method, auth, request, response, error codes) and note any deviation explicitly.
2. `entity.go`: types and enums. Money as `int64` minor units.
3. `repository.go`: extend the domain interface. Keep it narrow.
4. `service.go`: business logic, domain errors (`ErrXxx`), no HTTP types.
5. `postgres/`: parameterized queries, ownership in the `WHERE`, `owner_type = 'CUSTOMER'` on by-account-number lookups, explicit column lists (never `SELECT *`).
6. `redis/`: cache read + write + invalidation together — never add a cache without its invalidation path.
7. `handler/`: decode, `validate` struct tags, map domain error → error code + HTTP status, respond via `pkg/response`.
8. `router.go`: register under the right auth group.
9. Migration if schema changes — **both** `.up.sql` and `.down.sql`.
10. Audit entry for any state change.
11. Rate limit tier if the endpoint is sensitive.
12. Tests (§13).
13. `make check` (lint + vet + race tests) must pass.

### Validation tags in use

```go
DeviceID           string `validate:"required,uuid"`
PINEncrypted       string `validate:"required,base64"`
DestinationAccount string `validate:"required,numeric,min=10,max=16"`
BankCode           string `validate:"required,numeric,len=3"`
TransferType       string `validate:"required,oneof=INTERNAL EXTERNAL VIRTUAL_ACCOUNT"`
Amount             int64  `validate:"required,gt=0,lte=10000000000"` // minor units
Notes              string `validate:"max=500"`
```

### Writing a migration

- Number sequentially after `000009`. Both `.up.sql` and `.down.sql`, always.
- If the migration adds a constraint that existing data might violate, open with a `DO $$ ... RAISE EXCEPTION $$` pre-check that names the offending row count. An opaque failure halfway through a production migration is far worse than a clear refusal at the top.
- A `down` that would destroy live financial data should refuse, loudly, rather than succeed. Fix forward.
- Verify against a real PostgreSQL 16 before delivering: apply up, run behavioural checks, apply down, apply up again.

---

## 13. QA test plan

When asked to test, verify, or QA a feature here, produce the plan **before** any code, as a checklist plus a test-case table using the team's `evidence-convention.md` shape:

| ID | Deskripsi / Langkah | Expected Result | Evidence |
|---|---|---|---|

State the scope and the logic under test first, wait for confirmation, then execute. Do not batch-check acceptance criteria — verify them one at a time.

### Mandatory cases for a financial endpoint

**Happy path** — success, correct ledger rows, correct balances.

**Idempotency**
- Same key replayed after success → identical response, `X-Idempotent-Replayed: true`, exactly one transaction row.
- Same key sent concurrently → one succeeds, the other gets `409`, never `PROCESSING` as a body.
- Same key replayed after the 24h Redis TTL → original transaction returned, still one row.
- Same key from a **different user** → treated as a fresh request, no cross-user leak.

**Inquiry binding**
- `inquiry_id` valid but body destination differs → `422 INQUIRY_MISMATCH`, no transaction created.
- Expired inquiry → `422 INQUIRY_EXPIRED`.
- Inquiry reused after a successful transfer → rejected.
- Inquiry belonging to another user → `404`.

**Verification token**
- Missing / expired / already used → `401`.
- Purpose mismatch (`EWALLET_TOPUP` token on `/transfer/execute`) → `401`.
- Token belonging to another user → `401`.

**Balance and limits**
- `amount + admin_fee` exactly equals `available_balance` → succeeds.
- One rupiah over → `422 TRANSFER_INSUFFICIENT_BALANCE`, balance unchanged.
- `hold_amount` reduces the usable amount (balance sufficient, available_balance not) → rejected.
- Amount crosses the daily limit → `422 TRANSFER_LIMIT_EXCEEDED` with correct `remaining`.
- Limit resets at **WIB** midnight, not UTC.
- User with no `transaction_limits` row → denied, not unlimited.
- Raising a limit above the server-side ceiling → `422` from the service, before the DB constraint fires.

**Concurrency**
- N concurrent transfers from one account with total > balance → balance never negative, exactly the affordable subset succeeds.
- Concurrent A→B and B→A → both complete, no deadlock (verifies lock ordering).
- Forced `40001` → retried transparently, no 500 surfaced.
- Sustained concurrent load on one rail → settlement contention spread across shards, no single hot row.

**Ledger**
- Internal transfer, no fee → exactly two mutations, `balance_before`/`balance_after` continuous per account.
- External / e-wallet / QRIS → three mutations: customer DEBIT `amount + fee`, settlement CREDIT `amount`, fee-income CREDIT `fee`.
- Per transaction, `Σ DEBIT = Σ CREDIT`; `v_unbalanced_transactions` stays empty.
- Per account, balance equals the latest `balance_after`; `v_ledger_reconciliation` stays empty.
- Rail total equals `SUM(balance)` over its 16 shards, and matches the day's outbound volume on that rail.
- A settlement account number (`99…`) submitted to `/transfer/inquiry` → `404`, never a valid destination.
- Reversal posts opposite legs as a new `REVERSED` transaction; the original row is untouched.

**Device binding**
- Registering an active `device_id` to a second user → rejected.
- Revoking the device, then registering the same `device_id` to another user → accepted.
- Login with a revoked `device_id` → `403`, not a resolve to the old owner.
- Five failed PINs against a spoofed `device_id` → account locks **and** the registered device is push-notified.
- An attacker sweeping many `device_id` values → per-IP `429` before mass lockout.

**Cache**
- After an internal transfer, **the recipient's** `/account/balance` and `/account/dashboard` are fresh, not stale.
- Mutasi page 2 differs from page 1 (cursor is in the cache key).
- Notification list page 2 differs from page 1.

**Auth and authorization**
- Another user's `transaction_id`, `account_id`, `inquiry_id`, `notification_id` → `404`, not `403`.
- Expired access token → `401 AUTH_TOKEN_EXPIRED`.
- Refresh token used on an access-token endpoint → `401` (`typ` check).
- Reused rotated refresh token → all sessions revoked, security audit written.

**Rate limit and lockout**
- 6th login attempt in 15 min → `429`.
- 5th wrong PIN → `423` with `locked_until`; the 6th request stays `423` even with the correct PIN.
- Attempt counter reflects the DB return value, not a stale read (regression test for the race).

**Timezone**
- A transaction at 06:00 WIB lands on the correct WIB statement date, not the previous UTC day.
- `LAST_7_DAYS` boundaries are WIB.

**Audit**
- Every state change writes a row with the matching `request_id`.
- Audit-insert failure does not fail the transaction.
- A cached balance read is still audited.

### Test layers

- **Unit** — services with mocked repositories; every branch of every domain error.
- **Integration** — real Postgres + Redis (docker-compose), full transaction paths, concurrency with `t.Parallel()` and `-race`.
- **Contract** — every documented response shape and error code actually reachable.
- **Migration** — apply up, assert behaviour, apply down, apply up again, on a real PostgreSQL 16.
- `go test -race -cover ./...`; race detector is not optional for this service.

---

## 14. Spec corrections — quick reference

The source documents contain these defects. Follow the right-hand column. Items marked ✓ are already applied in migration `000009`.

| # | Document says | Do instead |
|---|---|---|
| 1 | `/transfer/execute` takes destination + amount in body alongside `inquiry_id` | Derive from inquiry; body is cross-check only |
| 2 | Idempotency handler `GET`s first and writes the value | `SETNX` first; `PROCESSING` → 409 |
| 3 | `idem:{key}`, `transactions.idempotency_key` UNIQUE | ✓ Scope both by `user_id` |
| 4 | Login resolves device with `uuid.Nil` user | ✓ `device_id` unique among active rows; resolve the user from it |
| 5 | `Amount float64` | `int64` minor units / `pgtype.Numeric` |
| 6 | `/auth/pin/verify` purpose `PAYMENT` | `QRIS_PAYMENT` (CHECK constraint) |
| 7 | Inquiry & vtoken in both PG and Redis, no precedence | Redis authoritative + atomic consume; PG is audit |
| 8 | Idempotency key in header *and* body | Header only |
| 9 | Notifications/QRIS on `TransactionHandler` | Dedicated handlers |
| 10 | Redis Cluster with DB 0/1/2, per-DB eviction policy | Two instances: sessions `noeviction`, cache `allkeys-lru` |
| 11 | `SCAN`-pattern invalidation | Version counters (`cachever:*`) |
| 12 | `cache:notif:{user_id}` for a paginated endpoint | Cursor in the key |
| 13 | `InvalidateTransactionCaches(userID, accountID)` | Take a list of affected parties |
| 14 | `SERIALIZABLE` with no retry | Retry `40001`, 3 attempts, jittered backoff |
| 15 | Locks source account only | Lock all affected accounts ordered by `id` |
| 16 | "Every transfer = two mutations" | ✓ Internal-with-no-fee only; outbound rails post three legs against the settlement and fee-income shards |
| 17 | Branches on stale `FailedPINAttempts+1` | `UPDATE ... RETURNING` |
| 18 | "RSA" for PIN transport | RSA-2048 **OAEP-SHA256** + nonce/timestamp anti-replay |
| 19 | Rotation deletes the old refresh token | Mark revoked with TTL, so reuse is detectable |
| 20 | Argon2id 64MB unbounded | Semaphore-limited concurrency |
| 21 | `/health` fully cached 5 min | Split liveness (uncached) from config (cached) |
| 22 | `transaction_date DEFAULT CURRENT_DATE` | ✓ Defaults removed; the application passes the WIB date |
| 23 | Cursor `{id, date}` | Full `ORDER BY` tuple with `id` tiebreaker |
| 24 | `updated_at` never updated | ✓ BEFORE UPDATE trigger |
| 25 | Audit via bare `go func()` per entry | Buffered channel + worker pool + file fallback |
| 26 | No `transaction_limits` seed data | ✓ Seeded per user by trigger, with server-side ceilings |
| 27 | Reference number generation undefined | ✓ `next_reference_number(wib_date)` |

---

## 15. Reminders

- **Never deploy without a security review.** These documents are architecture guidance; production needs penetration testing, a security audit, and PCI-DSS / OJK / BI compliance review.
- Private keys belong in an HSM, not the filesystem.
- Balance and mutations ultimately come from the core banking system. This API is a **middleware layer** — design each endpoint so the data source can move behind the repository interface without touching the service.
- Face ID and Touch ID need no dedicated endpoints beyond challenge/verify — biometrics are processed entirely on-device.
- Ask before changing a documented contract. Suggest the fix, name the spec defect, and wait — a mobile client is already built against these shapes.
- Migration `000009` is deliberately hard to roll back: it aborts if settlement mutations exist or if two users already share an idempotency key. Both mean a rollback would destroy live transaction data. Fix forward; never force it.
- Deploy `000009` and the three-leg posting code together. In any window where the old single-leg code runs against the new schema, `v_unbalanced_transactions` fills with real, unbalanced money movements.