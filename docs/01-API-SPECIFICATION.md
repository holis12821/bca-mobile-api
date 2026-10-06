# API Specification — BCA Mobile Backend

> Semua endpoint, request/response contracts, error codes

> **Status:** Dokumen ini sudah dikoreksi dan konsisten dengan SKILL.md §14.

---

## Konvensi Umum

### Base URL
```
Production:  https://api.bcamobile.id/v1
Staging:     https://api-staging.bcamobile.id/v1
```

### Standard Response Envelope

Semua response menggunakan format yang konsisten:

```json
// Success
{
  "status": "success",
  "data": { ... },
  "meta": {
    "request_id": "req_abc123",
    "timestamp": "2026-09-02T10:30:00Z"
  }
}

// Error
{
  "status": "error",
  "error": {
    "code": "AUTH_INVALID_PIN",
    "message": "Kode akses salah. Silakan coba lagi.",
    "details": null
  },
  "meta": {
    "request_id": "req_abc123",
    "timestamp": "2026-09-02T10:30:00Z"
  }
}

// Paginated
{
  "status": "success",
  "data": [ ... ],
  "pagination": {
    "cursor": "eyJpZCI6MTAwfQ==",
    "has_more": true,
    "limit": 20
  },
  "meta": { ... }
}
```

### Authentication Header
```
Authorization: Bearer <access_token>
X-Device-ID: <device_fingerprint>
X-Request-ID: <uuid_v4>
X-Idempotency-Key: <uuid_v4>  (untuk mutating operations)
```

### Standard HTTP Status Codes
| Code | Meaning |
|------|---------|
| 200 | Success |
| 201 | Created |
| 400 | Bad Request — validation error |
| 401 | Unauthorized — token expired/invalid |
| 403 | Forbidden — insufficient permission |
| 404 | Not Found |
| 409 | Conflict — duplicate/idempotency |
| 422 | Unprocessable Entity — business rule violation |
| 429 | Too Many Requests — rate limited |
| 500 | Internal Server Error |
| 503 | Service Unavailable — maintenance |

---

## 1. Health & Config

### `GET /health`
**Auth:** None
**Purpose:** Health check + app config untuk Splash screen
**Cache:** Liveness (DB + Redis ping) tidak di-cache; config (maintenance, version, feature flags) di-cache Redis 5 menit

```json
// Response 200
{
  "status": "success",
  "data": {
    "service": "ok",
    "database": "ok",
    "cache": "ok",
    "maintenance_mode": false,
    "maintenance_message": null,
    "minimum_app_version": "5.8.0",
    "current_app_version": "5.9.1",
    "force_update": false,
    "feature_flags": {
      "ewallet_enabled": true,
      "qris_enabled": true,
      "transfer_antar_bank_enabled": true,
      "virtual_account_enabled": true
    }
  }
}
```

---

## 2. Authentication

### `GET /auth/pin/public-key`
**Auth:** None (public)
**Purpose:** Kunci publik untuk mengenkripsi `pin_encrypted`
**Cache:** `Cache-Control: public, max-age=300`

Bentuk respons identik dengan `GET /onboarding/credentials/public-key`, jadi satu
jalur kode client bisa melayani keduanya. Kunci yang sama juga dibundel di APK
sebagai `assets/pin_public.pem`; endpoint ini yang membuat rotasi kunci mungkin
tanpa rilis baru.

```json
// Response 200
{
  "status": "success",
  "data": {
    "algorithm": "RSA-OAEP-SHA256",
    "key_id": "pin-key-v1",
    "public_key_pem": "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkq...\n-----END PUBLIC KEY-----\n",
    "payload_shape": "{\"pin\":\"123456\",\"nonce\":\"<uuid-v4>\",\"ts\":<unix-seconds>}",
    "encoding": "base64(RSA-OAEP-SHA256(json))",
    "max_skew_sec": 60
  }
}
```

**Aturan enkripsi PIN — berlaku untuk `/auth/login/pin`, `/auth/pin/verify`,
`/auth/pin/change`, `/auth/access-code/change`, `/registration/complete`, dan
`/onboarding/credentials`:**

| Hal | Nilai |
|---|---|
| Algoritma | `RSA/ECB/OAEPWithSHA-256AndMGF1Padding` (RSA-OAEP, hash SHA-256, MGF1-SHA-256, label kosong) |
| Padding lain | **Ditolak.** PKCS#1 v1.5 tidak akan pernah terdekripsi |
| Plaintext | JSON `{"pin":"123456","nonce":"<uuid-v4>","ts":<unix-seconds>}` |
| Wire | base64 standar dari ciphertext |
| `nonce` | sekali pakai, diingat server 120 detik |
| `ts` | skew maksimal 60 detik |
| `encryption_key_id` | opsional; bila dikirim dan bukan kunci aktif → `422 AUTH_PIN_KEY_UNKNOWN` beserta `details.expected_key_id` |

`encryption_key_id` kosong tetap diterima — build lama belum mengirimkannya.
Mengirimkannya adalah cara membedakan "PIN salah" dari "kunci sudah dirotasi",
yang tanpa itu tampak sama dari sisi client.

`make pin-public-key` mencetak PEM yang sama dari sisi server, untuk diserahkan
sekali ke repo Android.

### `POST /auth/login/pin`
**Auth:** None (public)
**Purpose:** Login dengan kode akses (PIN) — Screen: **Kode Akses**
**Rate Limit:** 5 attempts / 15 menit per device, lockout 30 menit setelah 5x gagal

```json
// Request
{
  "device_id": "d4e5f6a7-...",
  "pin_encrypted": "base64_encrypted_pin_with_server_public_key",
  "encryption_key_id": "pin-key-v1",
  "device_info": {
    "model": "Samsung Galaxy S24",
    "os_version": "Android 15",
    "app_version": "5.9.1"
  }
}

// Response 200
{
  "status": "success",
  "data": {
    "access_token": "eyJhbGciOiJSUzI1NiIs...",
    "refresh_token": "eyJhbGciOiJSUzI1NiIs...",
    "token_type": "Bearer",
    "expires_in": 900,
    "user": {
      "id": "usr_abc123",
      "display_name": "NURHOLIS",
      "masked_account": "****4567"
    }
  }
}

// Response 401 — PIN salah
{
  "status": "error",
  "error": {
    "code": "AUTH_INVALID_PIN",
    "message": "Kode akses salah. Silakan coba lagi.",
    "details": {
      "attempts_remaining": 3
    }
  }
}

// Response 423 — Akun terkunci
{
  "status": "error",
  "error": {
    "code": "AUTH_ACCOUNT_LOCKED",
    "message": "Akun terkunci karena terlalu banyak percobaan. Coba lagi dalam 30 menit.",
    "details": {
      "locked_until": "2026-09-02T11:00:00Z"
    }
  }
}
```

### `POST /auth/login/biometric`
**Auth:** None (public)
**Purpose:** Login dengan biometrik (Face ID / Touch ID)
**Note:** Device mengirim signed challenge, bukan data biometrik

#### Kontrak tanda tangan

| Hal | Nilai |
|---|---|
| Jenis kunci | **EC P-256** (`secp256r1` / `prime256v1`). RSA dan kurva lain ditolak saat register dengan `422 AUTH_BIOMETRIC_KEY_UNSUPPORTED` |
| Algoritma tanda tangan | **`SHA256withECDSA`** |
| Yang ditandatangani | **32 byte challenge mentah** — base64-*decode* field `challenge` lebih dulu, lalu tandatangani byte itu. Tidak ada penggabungan: tanpa `device_id`, tanpa prefiks panjang |
| Format `signed_challenge` | **base64 dari DER ASN.1** — keluaran bawaan `java.security.Signature`. Raw `r‖s` 64 byte juga masih diterima, tapi DER yang didokumentasikan |
| Format `public_key` | **base64 X.509 `SubjectPublicKeyInfo`** tanpa header PEM. PEM tetap diterima |
| Nama field | `signed_challenge` **atau** `signature` — keduanya dilayani, isinya sama |
| `biometric_type` di body login | Diterima dan dicatat, tapi tidak dipercaya: jenis biometrik yang membuka kunci adalah urusan perangkat, dan kunci terdaftarnya sudah menyimpannya |

#### Vektor uji

Nilai tetap ini diverifikasi oleh `TestBiometricLogin_PublishedTestVector` di
`internal/domain/auth/biometric_service_test.go`. Sisi Android bisa mencocokkan
implementasinya tanpa menunggu server:

```
public_key  (base64 SPKI, EC P-256)
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEo6b/4hWQuDCQW3mrgm1MT3+R4IN/KBMlOIfCncd+r0AELAmBaWsMxOp0AyRkxsK+8LWUeLjiCHmiO0IQiWJa3Q==

challenge   (base64, 32 byte)
dF8Omhv+NpbgVq+JhUxtMqU408OSzYke75J1RTwH5d0=

signed_challenge  (base64 DER, SHA256withECDSA)
MEUCIQDDEuePW+/ce5/R/9MySwoCKr4MhcNv3B+MqbscMMkYaQIgGZUUEYi2qyw/UJKTGFepdsLBlijYeSxpEwOINsK5bTc=
```

ECDSA tidak deterministik, jadi tanda tangan Anda sendiri **tidak** akan sama
dengan baris di atas — yang harus sama adalah hasil verifikasinya. Kunci privat
pasangan vektor ini hanya untuk uji dan tidak dipakai di lingkungan mana pun.

```json
// Request
{
  "device_id": "d4e5f6a7-...",
  "biometric_type": "FACE_ID",       // atau "FINGERPRINT"
  "challenge_id": "ch_abc123",
  "signed_challenge": "base64_signature",
  "key_id": "key_abc123"
}

// Response 200 — sama dengan login/pin
{
  "status": "success",
  "data": {
    "access_token": "...",
    "refresh_token": "...",
    "token_type": "Bearer",
    "expires_in": 900,
    "user": {
      "id": "usr_abc123",
      "display_name": "NURHOLIS",
      "masked_account": "****4567"
    }
  }
}

// Response 401 — Biometrik tidak terdaftar
{
  "status": "error",
  "error": {
    "code": "AUTH_BIOMETRIC_NOT_REGISTERED",
    "message": "Biometrik belum terdaftar. Gunakan kode akses untuk masuk."
  }
}
```

### `GET /auth/biometric/challenge` · `POST /auth/biometric/challenge`
**Auth:** None
**Purpose:** Request challenge untuk biometric authentication
**Note:** Challenge berlaku 60 detik, single-use.
**Note:** Kedua verb dilayani. GET memakai query `?device_id=`, POST memakai
body JSON `{"device_id": "..."}`. Aplikasi boleh memilih salah satu.

```json
// Request Query (GET)
GET /auth/biometric/challenge?device_id=d4e5f6a7-...

// Request Body (POST)
{ "device_id": "d4e5f6a7-..." }

// Response 200
{
  "status": "success",
  "data": {
    "challenge_id": "ch_abc123",
    "challenge": "random_32_byte_base64_string",
    "expires_in": 60,
    "expires_at": "2026-09-02T10:31:00Z",
    "algorithm": "SHA256withECDSA",
    "signature_format": "base64(DER ASN.1) of SHA256withECDSA over the raw 32 challenge bytes"
  }
}
```

`challenge` adalah base64 dari 32 byte acak. Challenge **sekali pakai**: server
mengambilnya dengan `GETDEL`, jadi percobaan kedua dengan `challenge_id` yang
sama dijawab `401 AUTH_TOKEN_INVALID` — bukan hanya yang sudah kedaluwarsa.

### `POST /auth/biometric/register`
**Auth:** Bearer Token (harus sudah login via PIN)
**Purpose:** Daftarkan biometrik baru untuk device

```json
// Request
{
  "device_id": "d4e5f6a7-...",
  "biometric_type": "FINGERPRINT",
  "public_key": "base64_public_key_from_android_keystore",
  "key_id": "key_abc123",
  "attestation": "base64_key_attestation"
}

// Response 201
{
  "status": "success",
  "data": {
    "biometric_id": "9f1c6d2e-...",
    "key_id": "key_abc123",
    "registered_at": "2026-09-02T10:30:00Z",
    "replaced_keys": 1
  }
}
```

**Perilaku yang perlu diketahui client:**

- `device_id` di body **tidak** menentukan apa pun; pengikatan diambil dari
  access token. Body yang menyebut device lain dijawab
  `403 AUTH_DEVICE_NOT_RECOGNIZED`.
- **Pendaftaran ulang MENGGANTI.** Semua kunci aktif nasabah ini pada perangkat
  ini dicabut lebih dulu, dan jumlahnya dilaporkan di `replaced_keys`. Ini yang
  dibutuhkan saat `setInvalidatedByBiometricEnrollment(true)` menghanguskan kunci
  karena sidik jari baru didaftarkan: client hapus kunci, daftar lagi, dan kunci
  lama berhenti berlaku pada saat yang sama.
- **Beberapa perangkat per nasabah tetap boleh.** Pencabutan dibatasi satu
  perangkat, jadi mendaftar di tablet tidak mematikan biometrik di ponsel.
- `key_id` unik lintas tabel. Mendaftar ulang dengan `key_id` yang sama pada
  perangkat dan nasabah yang sama memperbarui barisnya; `key_id` yang sudah
  dipakai nasabah atau perangkat lain ditolak.
- `attestation` (rantai Android Key Attestation, base64, maksimal 16 KB)
  **disimpan, tidak diverifikasi** ke akar Google, dan tidak ada syarat
  `security_level` minimal — lihat `docs/04-SECURITY.md`. Perangkat tanpa
  dukungan attestation tidak ditolak.

### `POST /auth/token/refresh`
**Auth:** Refresh Token
**Purpose:** Perbarui access token yang expired

```json
// Request
{
  "refresh_token": "eyJhbGciOiJSUzI1NiIs...",
  "device_id": "d4e5f6a7-..."
}

// Response 200
{
  "status": "success",
  "data": {
    "access_token": "new_access_token",
    "refresh_token": "new_refresh_token",
    "expires_in": 900
  }
}
```

### `POST /auth/logout`
**Auth:** Bearer Token
**Purpose:** Logout, invalidate semua token

```json
// Request
{
  "device_id": "d4e5f6a7-...",
  "revoke_all_devices": false
}

// Response 200
{
  "status": "success",
  "data": {
    "message": "Berhasil keluar"
  }
}
```

### `POST /auth/pin/change`
**Auth:** Bearer Token
**Purpose:** Ganti kode akses — Screen: **Ganti Kode Akses**

```json
// Request
{
  "old_pin_encrypted": "base64_encrypted",
  "new_pin_encrypted": "base64_encrypted",
  "device_id": "d4e5f6a7-..."
}

// Response 200
{
  "status": "success",
  "data": {
    "message": "Kode akses berhasil diubah"
  }
}

// Response 422 — PIN lama salah
{
  "status": "error",
  "error": {
    "code": "AUTH_OLD_PIN_MISMATCH",
    "message": "Kode akses lama tidak sesuai."
  }
}
```

### `POST /auth/access-code/change`
**Auth:** Bearer Token
**Purpose:** Ubah **kode akses** (kredensial login) — Screen: **Akun → Ubah Kode Akses**

Kode akses dan PIN adalah dua rahasia berbeda:
- **Kode akses** diverifikasi oleh `POST /auth/login/pin`.
- **PIN** diverifikasi oleh `POST /auth/pin/verify` untuk mengotorisasi transaksi,
  dan diubah oleh `POST /auth/pin/change`.

Keduanya mencabut **seluruh sesi lain** setelah berhasil, sehingga token yang
sudah terlanjur bocor ikut mati.

```json
// Request
{
  "old_pin_encrypted": "base64_rsa_oaep...",
  "new_pin_encrypted": "base64_rsa_oaep..."
}

// Response 200
{
  "status": "success",
  "data": { "message": "Kode akses berhasil diubah." }
}
```

Catatan: untuk nasabah lama yang belum punya kode akses (mis. akun seed),
`POST /auth/login/pin` jatuh kembali memverifikasi PIN.

### `POST /auth/pin/verify`
**Auth:** Bearer Token
**Purpose:** Verifikasi PIN untuk otorisasi transaksi (E-Wallet PIN, Transfer PIN)

```json
// Request
{
  "pin_encrypted": "base64_encrypted",
  "transaction_id": "txn_abc123",
  "purpose": "EWALLET_TOPUP"
}

// Response 200
{
  "status": "success",
  "data": {
    "verification_token": "vtk_abc123",
    "expires_in": 120
  }
}
```

**Daftar `purpose` yang diterima** — tertutup, dan token terikat pada satu
tujuan: token transfer tidak bisa dipakai memblokir kartu.

| `purpose` | Dipakai oleh |
|---|---|
| `TRANSFER` | `POST /transfer/execute` |
| `EWALLET_TOPUP` | `POST /ewallet/topup` |
| `QRIS_PAYMENT` | `POST /qris/pay` |
| `CHANGE_LIMIT` | `PUT /account/transaction-limit` |
| `CHANGE_PIN` | `POST /auth/pin/change` |
| `CHANGE_PROFILE` | `PUT /account/profile` |
| `BLOCK_CARD` | `POST /account/cards/{card_id}/block` |
| `REPLACE_CARD` | `POST /account/cards/{card_id}/replacement` |

Daftar ini ditegakkan **dua kali**: `transaction.ValidPurposes` di Go dan CHECK
constraint `verification_tokens_purpose_check` di database (migrasi `000024`).
Menambah purpose baru berarti mengubah keduanya — sampai migrasi itu ada,
`CHANGE_PROFILE` hanya terdaftar di Go dan endpoint ini menjawab `500` untuknya.

---

## 3. Account

### `GET /account/profile`
**Auth:** Bearer Token
**Purpose:** Data profil user — Screen: **Akun**
**Cache:** Redis 10 menit, invalidate on update

```json
// Response 200
{
  "status": "success",
  "data": {
    "user_id": "usr_abc123",
    "full_name": "NURHOLIS MAJID",
    "display_name": "NURHOLIS",
    "phone_number": "0812****5678",
    "email": "nur****@gmail.com",
    "is_biometric_enabled": true,
    "biometric_type": "FINGERPRINT",
    "accounts": [
      {
        "account_id": "acc_001",
        "account_number": "1234567890",
        "account_type": "TAHAPAN",
        "account_label": "Tahapan BCA",
        "is_primary": true
      }
    ],
    "app_version": "5.9.1",
    "last_login": "2026-09-02T10:00:00Z",
    "tier": "PRIORITAS"
  }
}
```

- `tier`: `PRIORITAS` | `SOLITAIRE`. **Tidak dikirim sama sekali** untuk nasabah
  reguler — badge disembunyikan saat field-nya tidak ada, jadi client tidak perlu
  belajar mengabaikan satu nilai. Tidak ada `GET /account/tier`; satu field pada
  endpoint yang sudah dipanggil sudah cukup.

### `POST /account/profile/otp`
**Auth:** Bearer Token
**Purpose:** Minta OTP untuk mengubah data profil — Screen: **Akun → Ubah Profil**

OTP dikirim ke **nomor HP yang terdaftar**, bukan ke alamat yang ada di
request — pengecekan yang bisa dipenuhi sendiri oleh penyerang bukan
pengecekan. Berlaku 5 menit, maksimal 5 kali salah lalu kode dihanguskan.

```json
// Response 200
{
  "status": "success",
  "data": {
    "sent_to": "0812****7890",
    "expires_in": 300
  }
}
```

**Dev only:** saat `APP_ENV=development` **dan** `SMS_PROVIDER` kosong,
response juga memuat `otp_debug` berisi kodenya, karena gateway di keadaan itu
hanya menulis log. Field ini tidak pernah muncul di environment lain.

**`503 OTP_DELIVERY_FAILED`:** provider SMS menolak kiriman. Kodenya tetap
terbit dan tersimpan, tapi response tidak lagi menjawab `200` dengan
`expires_in` untuk SMS yang tidak pernah diserahkan — nasabah dulu menunggu
pesan yang tidak akan datang. Client menampilkan pesan error dan menawarkan
"coba lagi".

### `PUT /account/profile`
**Auth:** Bearer Token + OTP
**Purpose:** Update data profil — Screen: **Akun → Ubah Profil**

`otp_code` diverifikasi terhadap kode yang diterbitkan `POST /account/profile/otp`.

```json
// Request
{
  "email": "nurholis.baru@gmail.com",
  "otp_code": "123456"
}

// Response 200
{
  "status": "success",
  "data": { "message": "Profil berhasil diperbarui" }
}
```

| Error | Arti |
|---|---|
| `OTP_EXPIRED` | Belum minta OTP, atau kodenya sudah kedaluwarsa |
| `OTP_INVALID` | Kode salah |
| `OTP_BLOCKED` | 5 kali salah — minta OTP baru |

### `GET /account/balance`
**Auth:** Bearer Token
**Purpose:** Saldo rekening — Screen: **Beranda** (toggle saldo), **Transfer**
**Cache:** Redis 30 detik (saldo berubah cepat)

```json
// Response 200
{
  "status": "success",
  "data": {
    "accounts": [
      {
        "account_id": "acc_001",
        "account_number": "1234567890",
        "account_type": "TAHAPAN",
        "balance": "15750000.00",
        "currency": "IDR",
        "available_balance": "15250000.00",
        "hold_amount": "500000.00"
      }
    ],
    "total_balance": "15750000.00"
  }
}
```

### `GET /account/dashboard`
**Auth:** Bearer Token
**Purpose:** Aggregated data untuk Home screen — Screen: **Beranda**
**Cache:** Redis 1 menit (invalidasi lewat version counter)
**Note:** Menggabungkan balance + promo + notif count untuk mengurangi round-trip

`promotions` dibaca dari tabel `promotions` (aktif dan masih dalam rentang
`valid_from`..`valid_until`, urut `priority` menurun). Kalau tidak ada promo
aktif, nilainya `[]` — bukan `null`.

Kunci `primary_account`, `accounts` dan `unread_notification_count` masih
dikirim untuk kompatibilitas dengan build lama, dan akan dihapus setelah
aplikasi berpindah ke `balance` dan `unread_notifications`.

```json
// Response 200
{
  "status": "success",
  "data": {
    "user": {
      "display_name": "NURHOLIS",
      "masked_account": "****4567",
      "last_login_at": "2026-09-22T03:14:00Z"
    },
    "balance": {
      "total": "15750000.00",
      "currency": "IDR",
      "primary_account": "1234567890"
    },
    "unread_notifications": 3,
    "promotions": [
      {
        "id": "0f3a...",
        "title": "Cashback 50% Top Up GoPay",
        "image_url": "https://cdn.bca.co.id/promo/001.webp",
        "deep_link": "bcamobile://promo/001",
        "valid_until": "2026-09-30"
      }
    ],
    "quick_actions": [
      { "id": "M_INFO",   "label": "m-Info",   "icon": "ic_info",     "enabled": true },
      { "id": "TRANSFER", "label": "Transfer", "icon": "ic_transfer", "enabled": true },
      { "id": "E_WALLET", "label": "e-Wallet", "icon": "ic_ewallet",  "enabled": true },
      { "id": "QRIS",     "label": "QRIS",     "icon": "ic_qris",     "enabled": true }
    ],

    "primary_account": { "account_id": "…", "account_number": "1234567890", "balance": "15750000.00", "…": "…" },
    "accounts": [ { "…": "…" } ],
    "unread_notification_count": 3
  }
}
```

### `PUT /account/settings`
**Auth:** Bearer Token
**Purpose:** Update pengaturan akun — Screen: **Akun**

```json
// Request
{
  "biometric_enabled": true,
  "push_notification_enabled": true,
  "email_statement_enabled": false
}

// Response 200
{
  "status": "success",
  "data": {
    "message": "Pengaturan berhasil diperbarui"
  }
}
```

### `GET /account/transaction-limit`
**Auth:** Bearer Token
**Purpose:** Lihat limit berlaku beserta pemakaian hari ini (WIB)

```json
// Response 200
{
  "status": "success",
  "data": {
    "limits": {
      "transfer_internal_daily": "50000000.00",
      "transfer_internal_used_today": "1500000.00",
      "transfer_internal_remaining_today": "48500000.00",
      "ewallet_daily": "20000000.00",
      "ewallet_used_today": "0.00",
      "ewallet_remaining_today": "20000000.00",
      "qris_daily": "5000000.00",
      "qris_per_transaction": "5000000.00",
      "qris_used_today": "0.00",
      "qris_remaining_today": "5000000.00"
    }
  }
}
```

### `PUT /account/transaction-limit`
**Auth:** Bearer Token + **PIN Verification (wajib)**
**Purpose:** Atur limit transaksi — Screen: **Akun → Atur Limit**

`verification_token` wajib: ambil dulu dari `POST /auth/pin/verify` dengan
`purpose: "CHANGE_LIMIT"`. Token sekali pakai, berlaku 120 detik. Tanpa itu
request ditolak `VERIFICATION_TOKEN_INVALID` — menaikkan plafon harian adalah
keputusan keamanan, bukan sekadar pengaturan.

`limits` menerima **dua bentuk**; keduanya setara:

```json
// Bentuk datar
{
  "verification_token": "a1b2c3...",
  "limits": {
    "transfer_internal_daily": 50000000,
    "transfer_external_daily": 25000000,
    "ewallet_daily": 10000000
  }
}

// Bentuk bersarang
{
  "verification_token": "a1b2c3...",
  "limits": {
    "TRANSFER_INTERNAL": { "daily_limit": 50000000 },
    "QRIS": { "daily_limit": 5000000, "per_transaction_limit": 2000000 }
  }
}
```

Response 200 sama persis dengan `GET /account/transaction-limit` di atas
(limit baru + pemakaian hari ini).

Plafon maksimum yang dipaksakan server: TRANSFER_INTERNAL & TRANSFER_EXTERNAL
100 juta/hari, EWALLET 20 juta/hari, QRIS 20 juta/hari dan 5 juta/transaksi.

### `GET /account/cards`
**Auth:** Bearer Token
**Purpose:** Kartu yang DIMILIKI nasabah — Screen: **Profil Saya** (Manajemen Kartu Paspor)

**Dari mana barisnya datang.** Satu kartu tercatat di sini ketika core banking
menjawab permintaan cetak pada `POST /v1/onboarding/submit` — nomor tersamar dan
masa berlakunya berasal dari jawaban itu, tidak dihitung oleh layanan ini. Kartu
pertama seorang nasabah otomatis menjadi kartu utama (`is_primary`).

Dua hal yang perlu diketahui client:

- **Permintaan cetak yang gagal tidak memunculkan kartu di sini.** Kartunya masuk
  antrean retry dan respons submit tetap melaporkan `REQUESTED`, tapi daftar ini
  baru berisi setelah penerbitnya benar-benar menjawab. Daftar kosong pada
  nasabah yang baru buka rekening adalah keadaan yang sah, bukan error.
- **Status `PRINTING` dan `SHIPPED` pada respons submit tidak berpindah sendiri
  ke sini.** Perpindahan status fisik dan pemenuhan permintaan penggantian
  (`POST /account/cards/{card_id}/replacement`) datang dari core banking, dan
  layanan ini belum punya kanal masuk untuk itu — tidak ada callback, tidak ada
  pekerja yang menaikkan status. Selama itu belum ada, kartu pengganti tidak
  muncul sebagai kartu baru di daftar ini dan kartu di sini tetap `ACTIVE`
  sampai nasabah sendiri memblokirnya.

Tidak ada parameter apa pun. Pemilik diambil dari klaim `sub` pada access token,
jadi tidak ada jalan meminta kartu nasabah lain.

```json
// Response 200
{
  "status": "success",
  "data": {
    "cards": [
      {
        "card_id": "3f2a7c10-aaaa-4bbb-8ccc-ddddeeeeffff",
        "masked_number": "•••• •••• •••• 7890",
        "cardholder_name": "NURHOLIS MAJID",
        "card_type": "PASPOR_GOLD",
        "product_name": "Gold Mastercard",
        "network": "MASTERCARD",
        "tier_key": "DEBIT",
        "style": "GOLD",
        "valid_thru": "12/29",
        "status": "ACTIVE",
        "is_primary": true,
        "settings": {
          "debit_online_enabled": true,
          "international_enabled": false
        }
      }
    ]
  }
}
```

Catatan kontrak yang mengikat client:

- `status`: `ACTIVE` | `BLOCKED` | `EXPIRED` | `REPLACEMENT_PENDING`. Client
  memetakan `ACTIVE` ke chip hijau "Aktif & Terhubung".
- `status` **dihitung server saat dibaca**, bukan sekadar isi kolom. Kartu yang
  masa berlakunya sudah lewat dilaporkan `EXPIRED` walau baris di database masih
  `ACTIVE` — tidak ada job yang membalik kolom itu. `BLOCKED` menang atas
  kedaluwarsa: kartu yang dilaporkan hilang harus tetap berkata begitu.
- `blocked_reason` hanya muncul saat `status` = `BLOCKED`
  (`LOST` | `STOLEN` | `DAMAGED` | `SUSPECTED_FRAUD`).
- `valid_thru` sudah terformat `MM/YY`. Tampilkan apa adanya, jangan diolah lagi.
- `masked_number` **selalu** tersamar. Nomor kartu utuh tidak pernah keluar dari
  layanan ini (docs/04-SECURITY.md), dan migrasi `000021` menegakkannya dengan
  CHECK constraint.
- Tidak ada nilai visual: `style` (`BLUE` | `GOLD` | `PLATINUM`) dipetakan client
  ke design token. Server tidak mengirim hex warna atau URL gambar.
- Nasabah tanpa kartu membalas `200` dengan `{"cards": []}` — **bukan** `404`.
  Layar punya empty state untuk keadaan itu.

### `PUT /account/cards/{card_id}/settings`
**Auth:** Bearer Token
**Purpose:** Dua sakelar kanal kartu — Screen: **Profil Saya**

```json
// Request — keduanya OPSIONAL
{ "debit_online_enabled": true, "international_enabled": false }

// Response 200 — kartu yang sudah diperbarui
{
  "status": "success",
  "data": { "card": { "card_id": "3f2a…", "…": "bentuknya sama dengan GET /account/cards" } }
}
```

- **Field yang tidak dikirim tidak diubah.** Keduanya `*bool`: mengirim hanya
  `international_enabled` membiarkan `debit_online_enabled` apa adanya. Client
  tidak perlu mengirim keadaan lengkap.
- Body yang tidak menyebut satu pun field dijawab `422 VALIDATION_ERROR`.
  Menjawab `200` akan memberi tahu layar bahwa penulisan terjadi padahal
  nama field-nya salah ketik.
- **Tanpa `verification_token`.** Keduanya bisa dikembalikan nasabah sendiri,
  dan meminta PIN untuk hal sepele melatih orang memasukkan PIN tanpa berpikir.
- Kartu `BLOCKED` menolak perubahan dengan `409 CARD_BLOCKED`.
- Menyalakan sakelar yang memang sudah menyala tidak menulis apa pun dan tetap
  dijawab `200` — jejak audit hanya untuk perubahan.
- Balasannya kartu utuh, jadi client tidak perlu memanggil `GET /account/cards`
  lagi setelahnya.

### `POST /account/cards/{card_id}/block`
**Auth:** Bearer Token + `verification_token` (purpose `BLOCK_CARD`)
**Purpose:** Blokir kartu hilang/dicuri — Screen: **Profil Saya**

```json
// Request
{ "reason": "LOST", "verification_token": "vt_…" }

// Response 200 — kartu dengan status BLOCKED
{
  "status": "success",
  "data": { "card": { "status": "BLOCKED", "blocked_reason": "LOST", "…": "…" } }
}
```

- `reason`: `LOST` | `STOLEN` | `DAMAGED` | `SUSPECTED_FRAUD`. Nilai lain
  dijawab `422 VALIDATION_ERROR`.
- `verification_token` **wajib**, diterbitkan `POST /auth/pin/verify` dengan
  `purpose = "BLOCK_CARD"`. Tanpa itu, siapa pun yang memegang ponsel tak
  terkunci bisa mematikan kartu.
- **Idempoten:** memblokir kartu yang sudah `BLOCKED` dijawab `200` dengan
  keadaan yang sama, bukan error. Nasabah yang panik menekan dua kali tidak
  boleh diberi tahu ada yang gagal.
- **Tidak ada endpoint buka blokir.** Membuka kartu yang dilaporkan hilang
  adalah keputusan cabang, bukan tombol di aplikasi.

### `POST /account/cards/{card_id}/replacement`
**Auth:** Bearer Token + `verification_token` (purpose `REPLACE_CARD`)
**Header wajib:** `X-Idempotency-Key`
**Purpose:** Permintaan kartu pengganti — Screen: **Profil Saya**

```json
// Request
{ "reason": "DAMAGED", "delivery_method": "COURIER", "verification_token": "vt_…" }

// Response 201
{
  "status": "success",
  "data": {
    "request_id": "918656e0-b1c7-4412-9a21-feb2f9f9b6fb",
    "card_id": "3f2a…",
    "status": "REQUESTED",
    "reason": "DAMAGED",
    "delivery_method": "COURIER",
    "fee": 50000,
    "estimated_arrival_from": "2026-09-30",
    "estimated_arrival_to": "2026-10-05",
    "masked_number": "•••• •••• •••• 1188"
  }
}
```

- `reason`: `DAMAGED` | `LOST` | `UPGRADE`.
- `delivery_method`: `COURIER` | `BRANCH_PICKUP`, default `COURIER`. Kartu yang
  katalognya tidak melayani pengambilan di cabang menjawab
  `422 CARD_DELIVERY_UNAVAILABLE`.
- **`X-Idempotency-Key` wajib** — penggantian berbiaya, dan retry jaringan tidak
  boleh mencetak dua kartu. Tanpa header: `422 VALIDATION_ERROR` dengan
  `details.missing_header`.
- **Retry dengan kunci yang sama dijawab `200`** beserta header
  `X-Idempotent-Replayed: true` dan `request_id` yang sama. Retry ini **tidak**
  memerlukan `verification_token` yang masih hidup: percobaan pertama sudah
  menghanguskannya, dan meminta yang baru berarti retry tidak akan pernah
  berhasil.
- Kunci **berbeda** untuk kartu yang permintaannya masih `REQUESTED` atau
  `PRINTING` dijawab `409 CARD_REPLACEMENT_IN_PROGRESS`.
- `fee` dibekukan dari katalog saat permintaan dibuat. Perubahan tarif
  berikutnya tidak mengubah angka yang sudah diberitahukan ke nasabah.
- Jendela estimasi berasal dari `card_products.delivery_days_min/max` — Blue dan
  Gold 3–7 hari, Platinum 5–10 hari. Tanggalnya dihitung di WIB.
- Kartu berpindah ke `REPLACEMENT_PENDING`, **kecuali** kartu yang sudah
  `BLOCKED` — kartu yang dilaporkan hilang tetap terblokir.
- **Permintaan berhenti di `REQUESTED`.** Pemenuhannya — kartu dicetak, dikirim,
  lalu terbit sebagai kartu baru di `GET /account/cards` — datang dari core
  banking, dan layanan ini belum punya kanal masuk untuk itu (tidak ada callback
  maupun pekerja yang menaikkan status). Client sebaiknya menampilkan permintaan
  yang sedang berjalan dari respons ini, bukan menunggu kartu penggantinya
  muncul di daftar kartu.

### `POST /account/device/push-token`
**Auth:** Bearer Token
**Purpose:** Daftarkan FCM token perangkat untuk notifikasi push

Perangkat diambil dari klaim `did` pada access token, **bukan** dari body —
klien tidak boleh menempelkan token ke perangkat yang bukan sedang dipakainya.

```json
// Request
{ "push_token": "fcm_token_dari_firebase" }

// Response 200
{
  "status": "success",
  "data": { "message": "Token notifikasi berhasil didaftarkan." }
}
```

- `push_token` wajib, maksimal 512 karakter. Kosong atau lebih panjang dijawab
  `400 VALIDATION_ERROR`.
- **Idempoten.** Aplikasi memanggil endpoint ini setiap kali FCM merotasi token,
  jadi tidak ada `X-Idempotency-Key` dan tidak ada penolakan duplikat.
- Perangkat yang sudah dicabut (`devices.revoked_at`) dijawab
  `403 AUTH_DEVICE_NOT_RECOGNIZED`, **bukan** `200` tanpa efek — 200 membuat
  aplikasi yakin tokennya tersimpan padahal tidak.

**Pengirimannya sekarang nyata.** Notifikasi dikirim ke FCM HTTP v1 begitu
`FCM_CREDENTIALS_FILE` diisi. Tiga hal yang perlu diketahui client:

- **Tanpa kredensial, tidak ada yang dikirim ke perangkat** — dan itu keadaan
  yang sah. Baris notifikasi tetap ditulis, jadi aplikasi tetap melihat pesannya
  lewat `GET /notifications` pada polling berikutnya. Di development isinya
  dicatat ke log; di lingkungan lain proses memberi peringatan saat boot.
- **Token yang ditolak FCM dibersihkan sendiri.** Hanya `UNREGISTERED` dan
  `INVALID_ARGUMENT` yang menghapus `devices.push_token`; gangguan sementara
  (`UNAVAILABLE`, kuota, jaringan) tidak — menghapus token karena FCM sedang
  bermasalah berarti nasabah berhenti menerima push sampai aplikasinya dibuka
  lagi. Aplikasi tidak perlu melakukan apa pun: pendaftaran token berikutnya
  memulihkannya.
- **Sakelar `push_notification_enabled` (`PUT /account/settings`) dihormati,
  kecuali untuk notifikasi `SECURITY`.** Ganti PIN, ganti kode akses, dan
  deteksi sesi mencurigakan tetap dikirim ke perangkat walau nasabah mematikan
  notifikasi — itu justru pesan yang paling ia butuhkan. Apa pun sakelarnya,
  baris in-app tetap ditulis dan tetap terbaca di layar Notifikasi.

Muatan yang dikirim: `notification.title`, `notification.body`, dan `data` berisi
`type` (lima nilai `NotificationTypes`) plus `deep_link` bila notifikasinya punya
tujuan (`bcamobile://transaction/{id}`).

## 4. Transactions / Mutations

### `GET /transactions/mutations`
> `account_id` wajib milik pemegang access token. Rekening milik orang lain
> dijawab `403 ACCOUNT_FORBIDDEN`.

**Auth:** Bearer Token
**Purpose:** Riwayat mutasi rekening — Screen: **Mutasi**
**Cache:** Redis 1 menit
**Pagination:** Cursor-based

**Nilai `period` yang diterima** — daftar tertutup. Nilai di luar daftar dijawab
`400 VALIDATION_ERROR` dengan `details.invalid_field = "period"` dan
`details.allowed_values`. Nilai tak dikenal **tidak** diabaikan: dulu nilai salah
tulis lolos sebagai "tanpa filter tanggal", jadi layar Mutasi menampilkan seluruh
riwayat rekening seolah-olah itu 7 hari terakhir.

| `period` | Rentang (tanggal WIB, kedua ujung inklusif) |
|---|---|
| `LAST_7_DAYS` | 6 hari lalu … hari ini |
| `LAST_30_DAYS` | 29 hari lalu … hari ini |
| `LAST_90_DAYS` | 89 hari lalu … hari ini |
| `THIS_MONTH` | tanggal 1 bulan ini … hari ini |
| `LAST_MONTH` | tanggal 1 bulan lalu … hari terakhir bulan lalu |
| `CUSTOM` | `from` … `to`, wajib keduanya |

Batas hari dihitung di `Asia/Jakarta`, bukan dengan `CURRENT_DATE`: pergantian
hari yang dilihat nasabah adalah tengah malam Jakarta.

`CUSTOM` menerima **`from`/`to`** maupun **`start_date`/`end_date`** (format
`YYYY-MM-DD`); `from`/`to` yang kanonik. Tanpa `period`, pasangan tanggal saja
tetap memfilter. `to` lebih awal dari `from`, format salah, atau `CUSTOM` tanpa
tanggal → `400 VALIDATION_ERROR`.

```json
// Request Query
GET /transactions/mutations?account_id=acc_001&period=LAST_7_DAYS&cursor=&limit=20

// Bulan lalu:
GET /transactions/mutations?account_id=acc_001&period=LAST_MONTH&cursor=&limit=20

// Atau custom date range:
GET /transactions/mutations?account_id=acc_001&period=CUSTOM&from=2026-08-01&to=2026-08-31&cursor=&limit=20

// Response 200
{
  "status": "success",
  "data": {
    "account": {
      "account_number": "1234567890",
      "account_label": "Tahapan BCA",
      "balance": "15750000.00"
    },
    "transactions": [
      {
        "id": "mut_001",
        "date": "2026-09-02",
        "time": "10:30:00",
        "description": "TRSF E-BANKING DB",
        "detail": "Transfer ke 0987654321 JOHN DOE",
        "amount": "-1500000.00",
        "balance_after": "15750000.00",
        "type": "DEBIT",
        "category": "TRANSFER",
        "icon": "ic_transfer",
        "reference_number": "REF2026090200001"
      },
      {
        "id": "mut_002",
        "date": "2026-09-01",
        "time": "15:45:00",
        "description": "TRSF E-BANKING CR",
        "detail": "Transfer dari 1111222233 JANE DOE",
        "amount": "5000000.00",
        "balance_after": "17250000.00",
        "type": "CREDIT",
        "category": "TRANSFER",
        "icon": "ic_transfer_in",
        "reference_number": "REF2026090100002"
      }
    ]
  },
  "pagination": {
    "cursor": "eyJkIjoiMjAyNi0wOS0wMSIsImMiOiIyMDI2LTA5LTAxVDE1OjQ1OjAwWiIsImkiOiJtdXRfMDAyIn0=",
    "has_more": true,
    "limit": 20
  }
}
```

### `GET /transactions/history`
**Auth:** Bearer Token
**Purpose:** Riwayat transaksi yang dilakukan user — Screen: **Riwayat**
**Berbeda dari mutasi:** Ini menampilkan transaksi yang di-initiate user (transfer, top-up, pembayaran), bukan semua mutasi rekening

**Filter periode:** sama persis dengan `GET /transactions/mutations` (§4) —
`period` menerima `LAST_7_DAYS`, `LAST_30_DAYS`, `LAST_90_DAYS`, `THIS_MONTH`,
`LAST_MONTH`, `CUSTOM`. `CUSTOM` wajib `from` + `to` (`YYYY-MM-DD`), dan
`start_date`/`end_date` tetap diterima sebagai nama lain. Nilai `period` yang
tidak dikenal dijawab `400 VALIDATION_ERROR` beserta `details.allowed_values` —
**tidak** diabaikan diam-diam. Batas hari dihitung di `Asia/Jakarta`; hari
terakhir rentang ikut terhitung penuh.

```json
// Request Query
GET /transactions/history?cursor=&limit=20&type=ALL&period=LAST_30_DAYS

// type: ALL, TRANSFER, EWALLET, PAYMENT, PULSA
// period: LAST_7_DAYS | LAST_30_DAYS | LAST_90_DAYS | THIS_MONTH | LAST_MONTH | CUSTOM
// from, to: wajib saat period=CUSTOM (YYYY-MM-DD, WIB, kedua ujung inklusif)

// Response 200
{
  "status": "success",
  "data": {
    "transactions": [
      {
        "id": "txn_001",
        "type": "EWALLET_TOPUP",
        "status": "SUCCESS",
        "amount": "100000.00",
        "admin_fee": "1000.00",
        "total": "101000.00",
        "description": "Top Up GoPay",
        "destination": "0812****5678",
        "destination_name": "NURHOLIS MAJID",
        "reference_number": "REF2026090200001",
        "created_at": "2026-09-02T10:30:00Z",
        "icon": "ic_gopay"
      }
    ]
  },
  "pagination": {
    "cursor": "...",
    "has_more": true,
    "limit": 20
  }
}
```

### `GET /transactions/{transaction_id}/receipt`
**Auth:** Bearer Token
**Purpose:** Detail bukti transaksi — Screen: **Bukti Transaksi**
**Cache:** Redis 24 jam (receipt immutable)

```json
// Response 200
{
  "status": "success",
  "data": {
    "transaction_id": "txn_001",
    "type": "EWALLET_TOPUP",
    "status": "SUCCESS",
    "date": "02 September 2026",
    "time": "10:30 WIB",
    "reference_number": "REF2026090200001",
    "source_account": "1234567890",
    "source_name": "NURHOLIS MAJID",
    "destination_number": "081234565678",
    "destination_name": "NURHOLIS MAJID",
    "provider": "GoPay",
    "amount": "100000.00",
    "admin_fee": "1000.00",
    "total": "101000.00",
    "currency": "IDR",
    "receipt_url": "https://api.bcamobile.id/v1/transactions/txn_001/receipt/pdf"
  }
}
```

### `GET /transactions/{transaction_id}/receipt/pdf`
**Auth:** Bearer Token
**Purpose:** Download PDF bukti transaksi
**Response:** `application/pdf` binary, dengan
`Content-Disposition: attachment; filename="bukti-transaksi-{reference_number}.pdf"`

Aturan kepemilikan sama dengan versi JSON: transaksi milik orang lain menjawab
`404 NOT_FOUND`, bukan PDF.

---

## 5. Transfer

### `GET /transfer/recent`
**Auth:** Bearer Token
**Purpose:** Daftar transfer terakhir — Screen: **Transfer**
**Cache:** Redis 5 menit

```json
// Response 200
{
  "status": "success",
  "data": {
    "recent_transfers": [
      {
        "id": "fav_001",
        "account_number": "0987654321",
        "account_name": "JOHN DOE",
        "bank": "BCA",
        "bank_code": "014",
        "type": "INTERNAL",
        "last_transfer_at": "2026-09-01T15:00:00Z",
        "transfer_count": 5
      }
    ]
  }
}
```

### `POST /transfer/inquiry`
**Auth:** Bearer Token
**Purpose:** Inquiry rekening tujuan — Screen: **Transfer Antar Rekening**
**Note:** Validasi rekening tujuan sebelum transfer

```json
// Request
{
  "destination_account": "0987654321",
  "bank_code": "014",
  "transfer_type": "INTERNAL"    // INTERNAL (BCA-BCA), EXTERNAL, VIRTUAL_ACCOUNT
}

// Response 200
{
  "status": "success",
  "data": {
    "account_number": "0987654321",
    "account_name": "JOHN DOE",
    "bank": "BCA",
    "bank_code": "014",
    "is_valid": true,
    "inquiry_id": "inq_abc123",
    "expires_at": "2026-09-02T10:35:00Z"
  }
}

// Response 404
{
  "status": "error",
  "error": {
    "code": "TRANSFER_ACCOUNT_NOT_FOUND",
    "message": "Nomor rekening tidak ditemukan."
  }
}
```

### `POST /transfer/execute`
> `source_account_id` wajib milik pemegang access token (`403 ACCOUNT_FORBIDDEN`
> kalau bukan). Kalau eksekusi gagal karena alasan bisnis — saldo kurang, limit
> terlampaui — `inquiry_id` dan `verification_token` **dikembalikan** dan masih
> bisa dipakai ulang sampai masa berlakunya habis, jadi nasabah tidak perlu
> mengulang dari layar tujuan dan prompt PIN. Sama berlaku untuk
> `POST /ewallet/topup` dan `POST /qris/pay`.

**Auth:** Bearer Token + PIN Verification
**Purpose:** Eksekusi transfer — Screen: **Transfer Antar Rekening**
**Idempotency:** Required (X-Idempotency-Key header)
**Note:** `destination_account`, `bank_code`, `transfer_type`, `amount` di-derive dari inquiry. Field di body hanya cross-check; mismatch → `422 INQUIRY_MISMATCH`.

```json
// Request
{
  "inquiry_id": "inq_abc123",
  "source_account_id": "acc_001",
  "destination_account": "0987654321",
  "bank_code": "014",
  "transfer_type": "INTERNAL",
  "amount": "1500000.00",
  "notes": "Bayar makan siang",
  "verification_token": "vtk_abc123"
}

// Response 201
{
  "status": "success",
  "data": {
    "transaction_id": "txn_002",
    "reference_number": "REF2026090200002",
    "status": "SUCCESS",
    "amount": "1500000.00",
    "admin_fee": "0.00",
    "total": "1500000.00",
    "source": {
      "account_number": "1234567890",
      "name": "NURHOLIS MAJID"
    },
    "destination": {
      "account_number": "0987654321",
      "name": "JOHN DOE",
      "bank": "BCA"
    },
    "notes": "Bayar makan siang",
    "created_at": "2026-09-02T10:30:00Z"
  }
}

// Response 422 — Saldo tidak cukup
{
  "status": "error",
  "error": {
    "code": "TRANSFER_INSUFFICIENT_BALANCE",
    "message": "Saldo rekening tidak mencukupi."
  }
}

// Response 422 — Limit terlampaui
{
  "status": "error",
  "error": {
    "code": "TRANSFER_LIMIT_EXCEEDED",
    "message": "Transfer melebihi limit harian. Sisa limit: Rp 48.500.000",
    "details": {
      "daily_limit": 50000000,
      "used_today": 1500000,
      "remaining": 48500000
    }
  }
}
```

---

## 6. E-Wallet

### `GET /ewallet/providers`
**Auth:** Bearer Token
**Purpose:** Daftar provider e-wallet — Screen: **E-Wallet Pilih**
**Cache:** Redis 1 jam

```json
// Response 200
{
  "status": "success",
  "data": {
    "providers": [
      {
        "id": "gopay",
        "name": "GoPay",
        "icon_url": "https://cdn.bca.co.id/ewallet/gopay.webp",
        "is_active": true,
        "min_amount": 10000,
        "max_amount": 2000000,
        "admin_fee": 1000,
        "preset_amounts": [50000, 100000, 200000, 500000, 1000000]
      },
      {
        "id": "ovo",
        "name": "OVO",
        "icon_url": "https://cdn.bca.co.id/ewallet/ovo.webp",
        "is_active": true,
        "min_amount": 10000,
        "max_amount": 2000000,
        "admin_fee": 1000,
        "preset_amounts": [50000, 100000, 200000, 500000, 1000000]
      },
      {
        "id": "dana",
        "name": "DANA",
        "icon_url": "https://cdn.bca.co.id/ewallet/dana.webp",
        "is_active": true,
        "min_amount": 10000,
        "max_amount": 2000000,
        "admin_fee": 1000,
        "preset_amounts": [50000, 100000, 200000, 500000, 1000000]
      },
      {
        "id": "shopeepay",
        "name": "ShopeePay",
        "icon_url": "https://cdn.bca.co.id/ewallet/shopeepay.webp",
        "is_active": true,
        "min_amount": 10000,
        "max_amount": 2000000,
        "admin_fee": 1000,
        "preset_amounts": [50000, 100000, 200000, 500000, 1000000]
      }
    ]
  }
}
```

### `POST /ewallet/inquiry`
**Auth:** Bearer Token
**Purpose:** Inquiry top-up e-wallet — Screen: **E-Wallet Konfirmasi**
**Note:** Validasi nomor tujuan + hitung biaya

```json
// Request
{
  "provider_id": "gopay",
  "phone_number": "081234565678",
  "amount": 100000,
  "source_account_id": "acc_001"
}

// Response 200
{
  "status": "success",
  "data": {
    "inquiry_id": "inq_ew_001",
    "provider": "GoPay",
    "destination_name": "NURHOLIS MAJID",
    "destination_phone": "0812****5678",
    "amount": "100000.00",
    "admin_fee": "1000.00",
    "total": "101000.00",
    "source_account": "1234567890",
    "source_balance": "15750000.00",
    "is_balance_sufficient": true,
    "expires_at": "2026-09-02T10:35:00Z"
  }
}

// Response 404
{
  "status": "error",
  "error": {
    "code": "EWALLET_ACCOUNT_NOT_FOUND",
    "message": "Nomor e-wallet tidak ditemukan."
  }
}
```

### `POST /ewallet/topup`
**Auth:** Bearer Token + PIN Verification
**Purpose:** Eksekusi top-up e-wallet
**Idempotency:** Required

```json
// Request
{
  "inquiry_id": "inq_ew_001",
  "verification_token": "vtk_abc123"
}

// Response 201
{
  "status": "success",
  "data": {
    "transaction_id": "txn_ew_001",
    "reference_number": "REF2026090200003",
    "status": "SUCCESS",
    "provider": "GoPay",
    "destination_phone": "0812****5678",
    "destination_name": "NURHOLIS MAJID",
    "amount": "100000.00",
    "admin_fee": "1000.00",
    "total": "101000.00",
    "source_account": "1234567890",
    "source_name": "NURHOLIS MAJID",
    "created_at": "2026-09-02T10:30:00Z"
  }
}

// Response 422
{
  "status": "error",
  "error": {
    "code": "EWALLET_INSUFFICIENT_BALANCE",
    "message": "Saldo rekening tidak mencukupi untuk nominal ini."
  }
}
```

---

## 7. Notifications

### `GET /notifications`
> Baris notifikasi kini benar-benar dibuat oleh server: transfer, top-up
> e-wallet, pembayaran QRIS (tipe `TRANSACTION`), serta perubahan kredensial,
> perubahan limit dan deteksi sesi mencurigakan (tipe `SECURITY`).

**Auth:** Bearer Token
**Purpose:** Daftar notifikasi — Screen: **Beranda** (bell icon)
**Cache:** Redis 1 menit — kunci cache memuat filter, jadi tab yang disaring
tidak pernah tersaji sebagai daftar "Semua"

**Filter jenis:** `type` menerima satu atau beberapa nilai dipisah koma dari
daftar tertutup `TRANSACTION`, `PROMO`, `SECURITY`, `SYSTEM`, `INFO` (daftar yang
sama dengan CHECK constraint `notifications.type`). Tanpa `type` = semua jenis.
Nilai tak dikenal dijawab `400 VALIDATION_ERROR` beserta
`details.allowed_values`. Urutan nilai tidak berpengaruh:
`?type=PROMO,INFO` dan `?type=INFO,PROMO` adalah permintaan yang sama.

```json
// Request Query
GET /notifications?cursor=&limit=20&type=PROMO,SECURITY

// Response 200
{
  "status": "success",
  "data": {
    "unread_count": 3,
    "notifications": [
      {
        "id": "notif_001",
        "type": "TRANSACTION",
        "title": "Transfer Berhasil",
        "body": "Transfer Rp 1.500.000 ke JOHN DOE berhasil.",
        "is_read": false,
        "deep_link": "bcamobile://transaction/txn_002",
        "created_at": "2026-09-02T10:30:00Z"
      },
      {
        "id": "notif_002",
        "type": "PROMO",
        "title": "Promo Cashback!",
        "body": "Dapatkan cashback 50% untuk top-up GoPay.",
        "is_read": false,
        "deep_link": "bcamobile://promo/001",
        "created_at": "2026-09-01T09:00:00Z"
      }
    ]
  },
  "pagination": {
    "cursor": "...",
    "has_more": true,
    "limit": 20
  }
}
```

### `PUT /notifications/{notification_id}/read`
**Auth:** Bearer Token
**Purpose:** Tandai notifikasi sudah dibaca

```json
// Response 200
{
  "status": "success",
  "data": {
    "message": "Notifikasi ditandai sudah dibaca"
  }
}
```

### `PUT /notifications/read-all`
**Auth:** Bearer Token
**Purpose:** Tandai semua notifikasi sudah dibaca

---

## 8. QRIS (Tambahan — sesuai FAB di bottom nav)

### `POST /qris/decode`
**Auth:** Bearer Token
**Purpose:** Decode QR code yang di-scan

```json
// Request
{
  "qr_data": "00020101021226610014ID.CO.BCA..."
}

// Response 200
{
  "status": "success",
  "data": {
    "merchant_name": "TOKO SEJAHTERA",
    "merchant_city": "JAKARTA",
    "amount": "50000.00",
    "is_amount_fixed": true,
    "qris_id": "qr_abc123",
    "expires_at": "2026-09-02T10:35:00Z"
  }
}
```

### `POST /qris/pay`
**Auth:** Bearer Token + PIN Verification
**Purpose:** Bayar via QRIS
**Idempotency:** Required

```json
// Request
{
  "qris_id": "qr_abc123",
  "source_account_id": "acc_001",
  "amount": "50000.00",
  "verification_token": "vtk_abc123"
}

// Response 201 (sama format seperti transaksi lain)
```

---

## 9. Registrasi (Buka Rekening) — **USANG**

> **Keluarga endpoint ini usang. Pakai `/v1/onboarding/*`
> (`docs/06-BUKA-REKENING-API-SPEC.md`).**
>
> Kedua keluarga menggambarkan fitur yang sama dengan dua kontrak berbeda:
> `/v1/registration/*` memakai Registration Token, `/v1/onboarding/*` memakai
> `session_id`. Aplikasi Android mengimplementasikan yang kedua, dan itu yang
> dipelihara.
>
> `/v1/registration/*` **masih berjalan** untuk build lama dan tidak akan
> dihapus diam-diam. Setiap responsnya membawa penanda, jadi integrasi baru bisa
> melihatnya tanpa membaca dua spec:
>
> ```
> Deprecation: true
> Link: </v1/onboarding/*>; rel="successor-version"
> X-API-Deprecation-Info: docs/01-API-SPECIFICATION.md#9-registrasi-buka-rekening
> ```
>
> Header `Sunset` **tidak** dikirim: belum ada tanggal penghapusan yang
> disepakati, dan mengarangnya di sini adalah janji yang tidak bisa ditepati
> repo ini. Integrasi baru: jangan pakai keluarga ini.


### `POST /registration/initiate`
> **Dev only:** saat `APP_ENV=development`, response memuat `otp_debug` berisi
> kode OTP, karena SMS gateway di lingkungan itu hanya menulis log. Field ini
> tidak pernah muncul di environment lain. Hal yang sama berlaku untuk
> `POST /v1/onboarding/personal-data` dan `POST /v1/onboarding/resend-otp`.

**Auth:** None
**Purpose:** Mulai proses buka rekening — Screen: **Buka Rekening**

```json
// Request
{
  "full_name": "NURHOLIS MAJID",
  "nik": "3201****0001",
  "phone_number": "081234565678",
  "email": "nurholis@gmail.com"
}

// Response 201
{
  "status": "success",
  "data": {
    "registration_id": "reg_abc123",
    "status": "OTP_PENDING",
    "otp_destination": "0812****5678"
  }
}
```

**`503 OTP_DELIVERY_FAILED`:** provider SMS menolak kiriman. Registrasi tetap
tersimpan di cache dan kodenya tetap sah, tapi `201 OTP_PENDING` untuk SMS yang
tidak pernah terkirim adalah jawaban yang salah — keluarga endpoint ini tidak
punya `resend-otp`, jadi nasabah tidak punya jalan keluar selain mengulang.

### `POST /registration/verify-otp`
**Auth:** None

```json
// Request
{
  "registration_id": "reg_abc123",
  "otp_code": "123456"
}
```

### `POST /registration/upload-document`
**Auth:** Registration Token
**Purpose:** Upload KTP, selfie untuk KYC

```json
// Request: multipart/form-data
// Fields:
//   registration_id: "reg_abc123"
//   document_type: "KTP" | "SELFIE" | "KTP_SELFIE"
//   file: <binary>
```

### `POST /registration/complete`
**Auth:** Registration Token
**Purpose:** Finalisasi pendaftaran, set PIN awal

---

## 10. Konten Statis

Dua halaman terakhir di seksi BANTUAN & INFORMASI layar Profil Saya.

**Keduanya TANPA `Authorization`.** Tidak ada data nasabah di dalamnya, dan
nasabah yang terkunci di luar aplikasi justru yang paling butuh nomor CS.
Keduanya membawa `Cache-Control: public, max-age=300` dan di-cache Redis 24 jam
(docs/03-REDIS-STRATEGY.md). Tetap kena rate limit per IP: 60 permintaan/menit.

Sumber datanya tabel `content_help_center` dan `content_contact_cs`
(migrasi `000023`) — bukan konstanta di kode, supaya nomor CS dan jawaban FAQ
bisa diperbaiki tanpa rilis.

### `GET /content/help-center`
**Auth:** — **Purpose:** Pusat Bantuan (FAQ)

```json
// Response 200
{
  "status": "success",
  "data": {
    "categories": [
      {
        "key": "CARD",
        "title": "Kartu Paspor",
        "items": [
          {
            "question": "Bagaimana cara memblokir kartu yang hilang?",
            "answer": "Buka Profil Saya, pilih kartu yang hilang, lalu tekan Blokir Kartu…"
          }
        ]
      }
    ]
  }
}
```

- `key` yang saat ini terbit: `CARD`, `TRANSACTION`, `SECURITY`, `ACCOUNT`.
  Daftar ini **bisa bertambah** tanpa perubahan kode; `title` selalu ikut
  dikirim supaya client yang belum mengenal sebuah key tetap punya teks untuk
  ditampilkan, bukan kunci mentah.
- Urutan kategori dan item ditentukan server. Tampilkan apa adanya.
- Tabel kosong dijawab `200` dengan `{"categories": []}` — bukan `404`.

### `GET /content/contact-cs`
**Auth:** — **Purpose:** Kontak Halo BCA

```json
// Response 200
{
  "status": "success",
  "data": {
    "phone": "1500888",
    "phone_free": "+62 21 23588000",
    "whatsapp": "+62 811 1500 998",
    "email": "halobca@bca.co.id",
    "chat_url": "https://www.bca.co.id/halobca",
    "hours": "24 jam setiap hari"
  }
}
```

- Semua field string siap tampil. `phone` untuk panggilan dalam negeri,
  `phone_free` untuk dari luar negeri.

---

## 11. Endpoint Operator / CS (`/internal/v1`)

Jalur petugas Halo BCA, bukan jalur nasabah. Di bawah `/internal/v1`, **bukan** `/v1`:
rate limit, body limit, dan CORS jalur nasabah tidak berlaku untuk jalur operator.

Rujukan paling dalam untuk sisi klien ada di
`.claude/skills/cs-desktop-api-integration/SKILL.md`.

### Autentikasi — tiga lapis

```
X-Internal-API-Key: <INTERNAL_API_KEY>     # sistem mana yang memanggil
X-Agent-Employee-ID: CS-1042               # petugas mana yang bertindak
X-Agent-API-Key: <kunci petugas>           # buktinya (Argon2id ke tabel cs_agents)
```

Kunci petugas diverifikasi ke `cs_agents`, dan **cakupan** (`scopes`) menentukan boleh
melakukan apa. Cakupannya diminta di titik pasang rute, jadi sebuah endpoint operator
tidak bisa terpasang tanpa menyatakan kewenangan yang dituntutnya.

| Cakupan | Membuka |
|---|---|
| `VIDEO_CALL` | ambil panggilan, submit hasil, antrean, daftar sesi onboarding |
| `CUSTOMER_PII` | detail sesi berisi data pribadi, pencarian & profil nasabah |
| `CARD_ADMIN` | administrasi katalog kartu Paspor |
| `TICKET` | tiket layanan |

`CUSTOMER_PII` **dipisah** dari `VIDEO_CALL` meski aplikasi desktop yang sama memakai
keduanya: melayani panggilan menampilkan nasabah yang *sedang* bicara, sementara membuka
data pribadi menjangkau nasabah mana pun yang pernah mendaftar.

Kunci sistem salah **dan** kewenangan kurang keduanya dijawab `403 FORBIDDEN` dengan
pesan identik — disengaja, supaya penyerang tidak bisa menebak kunci mana yang sudah
benar. Pakai `meta.request_id` dan log server untuk membedakannya.

### `GET /internal/v1/onboarding/sessions`

Scope `VIDEO_CALL`. Daftar sesi onboarding untuk layar pemantauan. **Tidak memuat PII** —
tidak ada nama, NIK, nomor HP, maupun `device_id`.

Query: `step`, `stalled_for_seconds`, `include_expired` (default `false`), `limit`
(1–100, default 20), `cursor`.

```json
{
  "status": "success",
  "data": {
    "sessions": [
      { "session_id": "onb_9f8e7d6c5b4a", "product_type": "TAHAPAN_BCA",
        "current_step": "BIOMETRIC", "card_type": "GPN",
        "steps_completed": { "tnc_accepted": true, "ocr_verified": true },
        "created_at": "2026-10-05T11:30:00+07:00",
        "updated_at": "2026-10-05T11:32:00+07:00",
        "expires_at": "2026-10-06T11:30:00+07:00",
        "expired": false, "stalled_seconds": 2479 }
    ]
  },
  "pagination": { "cursor": "eyJj…", "has_more": true, "limit": 20 }
}
```

- `stalled_seconds` adalah lama sesi diam di langkahnya sekarang. Inilah angka yang
  dicari petugas: sesi yang tidak bergerak 40 menit di `BIOMETRIC` adalah nasabah yang
  kemungkinan besar sedang gagal.
- `expired` dihitung di aplikasi; tidak ada proses yang menandai sesi kedaluwarsa.
- `step` yang tidak dikenal dijawab `400 VALIDATION_ERROR`, bukan daftar kosong.

### `GET /internal/v1/onboarding/sessions/{session_id}`

Scope **`CUSTOMER_PII`**. Satu sesi berikut data pribadinya yang sudah disamarkan. Setiap
pemanggilan yang berhasil menulis `CS_SESSION_VIEWED` ke jejak audit sesi.

```json
{
  "session_id": "onb_9f8e…", "current_step": "REVIEW", "expired": false,
  "stalled_seconds": 1269074,
  "personal_data": {
    "nama_lengkap": "MUHAMMAD ARDAN PRAYOGI",
    "nik_masked": "3174**********01",
    "tempat_lahir": "Jakarta", "tanggal_lahir": "1995-04-21",
    "jenis_kelamin": "LAKI_LAKI",
    "alamat_ktp": { "alamat_lengkap": "…", "kelurahan": "…", "provinsi": "…" },
    "pekerjaan": "KARYAWAN_SWASTA", "penghasilan_per_bulan": "10_20_JUTA",
    "nomor_hp_masked": "0812***1575", "email_masked": "e******9@example.com"
  },
  "video_call": { "queue_id": "q_…", "status": "COMPLETED", "result": "APPROVED" }
}
```

**Kebijakan penyamaran**, dan alasannya:

| Field | Perlakuan | Kenapa |
|---|---|---|
| `nama_lengkap` | **utuh** | Mencocokkan orang dengan namanya adalah inti pekerjaan petugas |
| `nik_masked` | 4 depan + 2 belakang | Pengenal terkuat dan berlaku seumur hidup; petugas membacanya dari kartu fisik saat panggilan, bukan dari layar ini |
| `nomor_hp_masked`, `email_masked` | disamarkan | Cukup mencocokkan apa yang nasabah sebutkan, tidak cukup menghubunginya di luar jalur resmi |
| alamat, lahir, pekerjaan, penghasilan | utuh | Dibutuhkan verifikasi dan tidak bisa dipakai menyamar sebagai nasabah |

`personal_data: null` berarti nasabah belum sampai langkah `PERSONAL_DATA` — keadaan
normal, bukan kegagalan.

### `GET /internal/v1/customers?q=`

Scope `CUSTOMER_PII`. Pencarian nasabah **cocok persis**: nomor rekening (10 digit) atau
nomor HP. Tidak ada pencarian nama dan tidak ada pencocokan sebagian — pencocokan
sebagian mengubah endpoint ini menjadi alat ekspor daftar nasabah.

```json
{ "status": "success",
  "data": { "count": 1,
            "customers": [ { "user_id": "95dd…", "full_name": "MUHAMMAD ARDAN PRAYOGI",
                             "tier": "REGULER", "status": "ACTIVE",
                             "phone_masked": "0812***1575" } ] } }
```

- Nomor HP diterima dalam bentuk apa pun: `08123421575`, `+628123421575`,
  `628123421575`, `0812-342-1575`.
- Tidak ditemukan dijawab **`200` dengan daftar kosong, bukan `404`**: 404 memberi tahu
  pemanggil bahwa kata kuncinya bukan nomor terdaftar, dan itu bisa dipakai menyapu ruang
  nomor rekening satu per satu.
- `q` yang bukan nomor rekening maupun nomor HP dijawab `400 VALIDATION_ERROR`.
- Setiap pencarian menulis `cs_access_logs` — **termasuk yang tidak menemukan apa pun**,
  karena pola pencarian yang gagal justru yang paling perlu terlihat saat memeriksa
  penyalahgunaan. Yang dicatat hanya **jenis** kata kuncinya (`ACCOUNT_NUMBER` / `PHONE`),
  bukan nilainya.

### `GET /internal/v1/customers/{user_id}`

Scope `CUSTOMER_PII`. Profil nasabah untuk petugas. Menulis `CUSTOMER_VIEWED` ke
`cs_access_logs`.

```json
{
  "user_id": "95dd…", "full_name": "MUHAMMAD ARDAN PRAYOGI", "display_name": "Muhammad",
  "nik_masked": "3174**********01", "phone_masked": "0812***1575",
  "email_masked": "e******9@example.com",
  "tier": "REGULER", "status": "ACTIVE",
  "biometric_enabled": false, "push_notification_enabled": true,
  "last_login_at": "2026-09-20T19:08:12+07:00", "created_at": "2026-09-20T19:07:50+07:00",
  "accounts": [
    { "account_number_masked": "****4654", "account_type": "TAHAPAN",
      "account_label": "Tahapan BCA", "currency": "IDR",
      "is_primary": true, "status": "ACTIVE", "opened_at": "…" }
  ]
}
```

**SALDO TIDAK ADA DI SINI, dan itu keputusan sadar.** Nasabah bisa melihat saldonya
sendiri di aplikasi; petugas tidak butuh angkanya untuk menyelesaikan keluhan; dan daftar
saldo seluruh nasabah adalah hal paling berharga yang bisa diambil dari kredensial petugas
yang bocor. Kalau suatu saat memang dibutuhkan, ia endpoint tersendiri dengan cakupan
tersendiri — bukan field tambahan di sini.

`locked_until` terisi hanya saat akun sedang terkunci karena PIN salah berulang; itu
jawaban langsung untuk "kenapa saya tidak bisa masuk".

### Tiket layanan — `/internal/v1/tickets`

Scope `TICKET`. Seluruhnya ditulis petugas; tidak ada endpoint nasabah yang menyentuhnya.

Dialamatkan lewat **`ticket_number`** (`TKT-20261005-000123`), bukan UUID — itu yang
tampil di layar petugas dan yang disebutkan nasabah lewat telepon. Pola yang sama dengan
`session_id` onboarding dan `queue_id` video call.

| Method | Path | Guna |
|---|---|---|
| POST | `/tickets` | Buat tiket |
| GET | `/tickets` | Daftar (filter `status`, `category`, `assigned_to`, `user_id`; cursor) |
| GET | `/tickets/{ticket_number}` | Detail berikut catatan |
| PATCH | `/tickets/{ticket_number}` | Ubah status / prioritas / kategori / penugasan |
| POST | `/tickets/{ticket_number}/notes` | Tambah catatan tindak lanjut |

```json
POST /internal/v1/tickets
{ "subject": "Transfer gagal tapi saldo terpotong",
  "category": "TRANSAKSI", "priority": "URGENT",
  "user_id": "95dd…", "session_id": "onb_…", "description": "…" }
```

```json
{ "ticket_number": "TKT-20261005-000002", "category": "TRANSAKSI",
  "priority": "URGENT", "status": "OPEN",
  "subject": "Transfer gagal tapi saldo terpotong",
  "created_by_agent": "SPV-3001", "created_at": "…", "updated_at": "…" }
```

- `created_by_agent` dan `author` catatan datang dari **kredensial**, bukan dari body.
  Tidak ada field body yang bisa mengakuinya.
- `category`: `KARTU`, `TRANSAKSI`, `AKUN`, `BUKA_REKENING`, `APLIKASI`, `LAINNYA`
  (default `LAINNYA`). `priority`: `LOW`, `NORMAL`, `HIGH`, `URGENT` (default `NORMAL`).
- `user_id` dan `session_id` **keduanya opsional dan boleh terisi sekaligus**: penelepon
  yang belum punya rekening hanya punya `session_id`, nasabah lama hanya punya `user_id`,
  dan yang gagal di tengah pembukaan rekening punya dua-duanya.
- `assigned_to=me` pada daftar diterjemahkan server dari kredensial — client tidak perlu
  tahu `employee_id`-nya sendiri.
- `PATCH` memakai pointer: field yang **tidak dikirim** dibiarkan apa adanya, sementara
  `"assigned_to_agent": ""` berarti melepas penugasan.
- `RESOLVED` mengisi `resolved_at`; `CLOSED` mengisi keduanya. Tiket yang sempat
  `RESOLVED` lalu ditutup **mempertahankan** `resolved_at` yang pertama.
- `CLOSED` adalah akhir: membukanya kembali dijawab `422 TICKET_INVALID_TRANSITION`.
  Keluhan yang muncul lagi layak jadi tiket baru.

### `GET /internal/v1/cards` — administrasi katalog

Scope **`CARD_ADMIN`**. Sebelumnya kunci sistem saja sudah cukup, yang berarti setiap
petugas video call bisa mengubah biaya dan limit kartu untuk seluruh nasabah. Lihat
`docs/08-PILIH-KARTU-API-SPEC.md` §3 untuk kontraknya.

> **Perubahan yang memutus client lama:** pemanggil `/internal/v1/cards` yang hanya
> mengirim `X-Internal-API-Key` sekarang dijawab `403`. Tambahkan kedua header petugas,
> dan pastikan barisnya di `cs_agents` punya scope `CARD_ADMIN`.

---

## 12. Penjadwalan ulang video call (sisi nasabah)

Melayani tombol **"Jadwalkan Panggilan Nanti"** di aplikasi Android, yang selama ini
dimatikan karena tidak ada endpoint yang menerimanya.

Endpoint **nasabah**, bukan CS: penjaganya `X-Device-ID` lewat `deviceOwnsSession`, sama
dengan `/video-call/queue`.

### `POST /v1/onboarding/video-call/schedule`

```json
{ "session_id": "onb_9f8e…", "scheduled_at": "2026-10-06T14:00:00+07:00" }
```

```json
{ "schedule_id": "vcs_28312175-c0cc-4a", "session_id": "onb_9f8e…",
  "scheduled_at": "2026-10-06T07:00:00Z", "status": "SCHEDULED",
  "operating_hours": { "start": "06:00", "end": "22:00", "timezone": "Asia/Jakarta" } }
```

- `scheduled_at` **wajib membawa offset zona** (RFC 3339). Tanpa offset dijawab
  `400 VALIDATION_ERROR`: "14:00" bisa berarti dua jam berbeda, dan yang salah tafsir
  adalah janji dengan nasabah.
- Jam divalidasi dalam **Asia/Jakarta**, bukan UTC maupun zona server.
- Batas: minimal 15 menit dari sekarang, maksimal 7 hari ke depan, dan di dalam
  06:00–22:00 WIB. Di luar itu `422 VIDEO_CALL_SCHEDULE_INVALID`.
- Hanya sesi yang sedang di langkah `VIDEO_CALL`. Dari langkah lain dijawab
  `422 ONBOARDING_INCOMPLETE`.
- **Satu jadwal aktif per sesi.** Yang kedua dijawab `422 VIDEO_CALL_ALREADY_SCHEDULED`.
- **Menjadwalkan TIDAK memasukkan nasabah ke antrean.** Ia janji, bukan tempat — nasabah
  tetap memanggil `/video-call/queue` saat waktunya datang.

### `GET /v1/onboarding/video-call/schedule?session_id=`

`200` dengan `schedule: null` kalau tidak ada jadwal aktif — **bukan `404`**. "Belum
menjadwalkan" adalah keadaan normal bagi hampir semua sesi.

### `DELETE /v1/onboarding/video-call/schedule?session_id=`

Membatalkan jadwal aktif, lalu nasabah boleh menjadwalkan ulang. Tanpa jadwal aktif
dijawab `404 VIDEO_CALL_SCHEDULE_NOT_FOUND`.

---

## Error Code Reference

| Code | HTTP | Description |
|------|------|-------------|
| `AUTH_INVALID_PIN` | 401 | PIN/kode akses salah |
| `AUTH_ACCOUNT_LOCKED` | 423 | Terlalu banyak percobaan |
| `AUTH_TOKEN_EXPIRED` | 401 | Access token expired |
| `AUTH_TOKEN_INVALID` | 401 | Token tidak valid |
| `AUTH_BIOMETRIC_NOT_REGISTERED` | 401 | Biometrik belum terdaftar |
| `AUTH_OLD_PIN_MISMATCH` | 422 | PIN lama salah saat ganti PIN |
| `AUTH_PIN_KEY_UNKNOWN` | 422 | `encryption_key_id` bukan kunci PIN yang aktif. `details.expected_key_id` menyebut yang benar — ambil ulang dari `GET /auth/pin/public-key` |
| `AUTH_BIOMETRIC_KEY_UNSUPPORTED` | 422 | `public_key` bukan EC P-256 saat register biometrik |
| `AUTH_DEVICE_NOT_RECOGNIZED` | 403 | Device tidak dikenal |
| `AUTH_SESSION_REVOKED` | 401 | Sesi sudah di-logout/dicabut — access token tidak berlaku lagi meski belum expired |
| `ACCOUNT_FORBIDDEN` | 403 | `account_id` / `source_account_id` bukan milik pemegang token |
| `ACCOUNT_NOT_FOUND` | 404 | Rekening tidak ditemukan |
| `TRANSFER_ACCOUNT_NOT_FOUND` | 404 | Rekening tujuan tidak valid |
| `TRANSFER_INSUFFICIENT_BALANCE` | 422 | Saldo tidak cukup |
| `TRANSFER_LIMIT_EXCEEDED` | 422 | Melebihi limit harian |
| `TRANSFER_SELF_TRANSFER` | 422 | Transfer ke rekening sendiri |
| `EWALLET_ACCOUNT_NOT_FOUND` | 404 | Nomor e-wallet tidak valid |
| `EWALLET_INSUFFICIENT_BALANCE` | 422 | Saldo tidak cukup |
| `EWALLET_PROVIDER_DOWN` | 503 | Provider sedang gangguan |
| `INQUIRY_EXPIRED` | 422 | Sesi transaksi sudah kedaluwarsa |
| `INQUIRY_MISMATCH` | 422 | Data body tidak sesuai dengan inquiry |
| `VERIFICATION_TOKEN_INVALID` | 401 | Token verifikasi tidak valid atau sudah dipakai |
| `OTP_INVALID` | 422 | Kode OTP salah |
| `OTP_EXPIRED` | 422 | OTP kedaluwarsa atau belum diminta |
| `OTP_BLOCKED` | 429 | Terlalu banyak percobaan OTP |
| `ONBOARDING_DEVICE_MISMATCH` | 403 | `X-Device-ID` bukan perangkat pembuat sesi onboarding |
| `CARD_NOT_FOUND` | 404 | Kartu tidak ditemukan **atau** bukan milik pemegang token — dua hal itu sengaja dijawab sama |
| `CARD_BLOCKED` | 409 | Sakelar kanal tidak bisa diubah pada kartu terblokir |
| `CARD_REPLACEMENT_IN_PROGRESS` | 409 | Sudah ada permintaan penggantian yang belum selesai untuk kartu ini |
| `CARD_DELIVERY_UNAVAILABLE` | 422 | `delivery_method` tidak dilayani untuk jenis kartu ini |
| `PROVIDER_NOT_CONFIGURED` | 503 | Integrasi eksternal (OCR, Dukcapil, biometrik, core banking) belum dipasang di environment ini |
| `OTP_DELIVERY_FAILED` | 503 | Kode OTP terbit dan tersimpan, tapi provider SMS menolak kiriman. Kodenya tetap sah — tawarkan kirim ulang |
| `OTP_CHANNEL_NOT_ALLOWED` | 400 | `channel` yang diminta tidak tersedia di deployment ini (atau tidak dikenal). Tidak ada yang dikirim dan tidak ada kuota terpakai — ulangi dengan `sms`. Lihat `docs/06-BUKA-REKENING-API-SPEC.md` §Channel pengiriman OTP |
| `RATE_LIMIT_EXCEEDED` | 429 | Terlalu banyak request |
| `MAINTENANCE_MODE` | 503 | Sedang maintenance |
| `IDEMPOTENCY_CONFLICT` | 409 | Transaksi sudah diproses |
| `VALIDATION_ERROR` | 400 | Input tidak valid |
| `FORBIDDEN` | 403 | Jalur `/internal/v1`: kunci sistem salah, kredensial petugas salah, atau cakupan kewenangannya kurang. Ketiganya dijawab sama — bedakan lewat `meta.request_id` dan log server |
| `AGENT_AUTH_UNAVAILABLE` | 503 | Verifikasi petugas tidak bisa dilakukan (Postgres tersendat). Bukan penolakan — boleh dicoba lagi dengan backoff |
| `TICKET_INVALID_TRANSITION` | 422 | Tiket `CLOSED` tidak bisa dibuka kembali |
| `VIDEO_CALL_ALREADY_SCHEDULED` | 422 | Sesi sudah punya jadwal video call aktif. Batalkan dulu untuk menjadwalkan ulang |
| `VIDEO_CALL_SCHEDULE_INVALID` | 422 | Waktu di luar 06:00–22:00 WIB, kurang dari 15 menit dari sekarang, atau lebih dari 7 hari ke depan |
| `VIDEO_CALL_SCHEDULE_NOT_FOUND` | 404 | Tidak ada jadwal aktif yang bisa dibatalkan |
| `INTERNAL_ERROR` | 500 | Kesalahan internal server |