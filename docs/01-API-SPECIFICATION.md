# API Specification — BCA Mobile Backend

> Semua endpoint, request/response contracts, error codes

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
**Cache:** Redis 5 menit

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

### `POST /auth/login/pin`
**Auth:** None (public)
**Purpose:** Login dengan kode akses (PIN) — Screen: **Kode Akses**
**Rate Limit:** 5 attempts / 15 menit per device, lockout 30 menit setelah 5x gagal

```json
// Request
{
  "device_id": "d4e5f6a7-...",
  "pin_encrypted": "base64_encrypted_pin_with_server_public_key",
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

### `GET /auth/biometric/challenge`
**Auth:** None
**Purpose:** Request challenge untuk biometric authentication
**Note:** Challenge berlaku 60 detik, single-use

```json
// Request Query
GET /auth/biometric/challenge?device_id=d4e5f6a7-...

// Response 200
{
  "status": "success",
  "data": {
    "challenge_id": "ch_abc123",
    "challenge": "random_32_byte_base64_string",
    "expires_at": "2026-09-02T10:31:00Z"
  }
}
```

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
    "biometric_id": "bio_abc123",
    "registered_at": "2026-09-02T10:30:00Z"
  }
}
```

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

### `POST /auth/pin/verify`
**Auth:** Bearer Token
**Purpose:** Verifikasi PIN untuk otorisasi transaksi (E-Wallet PIN, Transfer PIN)

```json
// Request
{
  "pin_encrypted": "base64_encrypted",
  "transaction_id": "txn_abc123",
  "purpose": "EWALLET_TOPUP"    // TRANSFER, EWALLET_TOPUP, PAYMENT
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
    "last_login": "2026-09-02T10:00:00Z"
  }
}
```

### `PUT /account/profile`
**Auth:** Bearer Token
**Purpose:** Update profil (email, phone) — memerlukan OTP

```json
// Request
{
  "email": "newemail@gmail.com",
  "otp_code": "123456"
}

// Response 200
{
  "status": "success",
  "data": {
    "message": "Profil berhasil diperbarui"
  }
}
```

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
        "balance": 15750000.00,
        "currency": "IDR",
        "available_balance": 15250000.00,
        "hold_amount": 500000.00
      }
    ],
    "total_balance": 15750000.00
  }
}
```

### `GET /account/dashboard`
**Auth:** Bearer Token
**Purpose:** Aggregated data untuk Home screen — Screen: **Beranda**
**Cache:** Redis 1 menit
**Note:** Menggabungkan balance + promo + notif count untuk mengurangi round-trip

```json
// Response 200
{
  "status": "success",
  "data": {
    "user": {
      "display_name": "NURHOLIS",
      "masked_account": "****4567"
    },
    "balance": {
      "total": 15750000.00,
      "currency": "IDR",
      "primary_account": "1234567890"
    },
    "unread_notifications": 3,
    "promotions": [
      {
        "id": "promo_001",
        "title": "Cashback 50% Top Up GoPay",
        "image_url": "https://cdn.bca.co.id/promo/001.webp",
        "deep_link": "bcamobile://promo/001",
        "valid_until": "2026-09-30"
      }
    ],
    "quick_actions": [
      {
        "id": "M_INFO",
        "label": "m-Info",
        "icon": "ic_info",
        "enabled": true
      },
      {
        "id": "TRANSFER",
        "label": "Transfer",
        "icon": "ic_transfer",
        "enabled": true
      },
      {
        "id": "E_WALLET",
        "label": "e-Wallet",
        "icon": "ic_ewallet",
        "enabled": true
      }
    ]
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

### `PUT /account/transaction-limit`
**Auth:** Bearer Token + PIN Verification
**Purpose:** Atur limit transaksi — Screen: **Akun → Atur Limit**

```json
// Request
{
  "verification_token": "vtk_abc123",
  "limits": {
    "transfer_internal_daily": 50000000,
    "transfer_external_daily": 25000000,
    "ewallet_daily": 10000000
  }
}

// Response 200
{
  "status": "success",
  "data": {
    "limits": {
      "transfer_internal_daily": 50000000,
      "transfer_external_daily": 25000000,
      "ewallet_daily": 10000000,
      "transfer_internal_used_today": 0,
      "transfer_external_used_today": 0,
      "ewallet_used_today": 0
    }
  }
}
```

---

## 4. Transactions / Mutations

### `GET /transactions/mutations`
**Auth:** Bearer Token
**Purpose:** Riwayat mutasi rekening — Screen: **Mutasi**
**Cache:** Redis 1 menit
**Pagination:** Cursor-based

```json
// Request Query
GET /transactions/mutations?account_id=acc_001&period=LAST_7_DAYS&cursor=&limit=20

// Atau custom date range:
GET /transactions/mutations?account_id=acc_001&period=CUSTOM&start_date=2026-08-01&end_date=2026-08-31&cursor=&limit=20

// Response 200
{
  "status": "success",
  "data": {
    "account": {
      "account_number": "1234567890",
      "account_label": "Tahapan BCA",
      "balance": 15750000.00
    },
    "transactions": [
      {
        "id": "mut_001",
        "date": "2026-09-02",
        "time": "10:30:00",
        "description": "TRSF E-BANKING DB",
        "detail": "Transfer ke 0987654321 JOHN DOE",
        "amount": -1500000.00,
        "balance_after": 15750000.00,
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
        "amount": 5000000.00,
        "balance_after": 17250000.00,
        "type": "CREDIT",
        "category": "TRANSFER",
        "icon": "ic_transfer_in",
        "reference_number": "REF2026090100002"
      }
    ]
  },
  "pagination": {
    "cursor": "eyJpZCI6Im11dF8wMDIiLCJkYXRlIjoiMjAyNi0wOS0wMSJ9",
    "has_more": true,
    "limit": 20
  }
}
```

### `GET /transactions/history`
**Auth:** Bearer Token
**Purpose:** Riwayat transaksi yang dilakukan user — Screen: **Riwayat**
**Berbeda dari mutasi:** Ini menampilkan transaksi yang di-initiate user (transfer, top-up, pembayaran), bukan semua mutasi rekening

```json
// Request Query
GET /transactions/history?cursor=&limit=20&type=ALL

// type: ALL, TRANSFER, EWALLET, PAYMENT, PULSA

// Response 200
{
  "status": "success",
  "data": {
    "transactions": [
      {
        "id": "txn_001",
        "type": "EWALLET_TOPUP",
        "status": "SUCCESS",
        "amount": 100000.00,
        "admin_fee": 1000.00,
        "total": 101000.00,
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
    "amount": 100000.00,
    "admin_fee": 1000.00,
    "total": 101000.00,
    "currency": "IDR",
    "receipt_url": "https://api.bcamobile.id/v1/transactions/txn_001/receipt/pdf"
  }
}
```

### `GET /transactions/{transaction_id}/receipt/pdf`
**Auth:** Bearer Token
**Purpose:** Download PDF bukti transaksi
**Response:** `application/pdf` binary

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
**Auth:** Bearer Token + PIN Verification
**Purpose:** Eksekusi transfer — Screen: **Transfer Antar Rekening**
**Idempotency:** Required (X-Idempotency-Key header)

```json
// Request
{
  "idempotency_key": "idk_unique_uuid",
  "inquiry_id": "inq_abc123",
  "source_account_id": "acc_001",
  "destination_account": "0987654321",
  "bank_code": "014",
  "transfer_type": "INTERNAL",
  "amount": 1500000.00,
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
    "amount": 1500000.00,
    "admin_fee": 0,
    "total": 1500000.00,
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
    "amount": 100000.00,
    "admin_fee": 1000.00,
    "total": 101000.00,
    "source_account": "1234567890",
    "source_balance": 15750000.00,
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
  "idempotency_key": "idk_unique_uuid",
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
    "amount": 100000.00,
    "admin_fee": 1000.00,
    "total": 101000.00,
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
**Auth:** Bearer Token
**Purpose:** Daftar notifikasi — Screen: **Beranda** (bell icon)
**Cache:** Redis 1 menit

```json
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
    "amount": 50000.00,
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
  "idempotency_key": "idk_unique_uuid",
  "qris_id": "qr_abc123",
  "source_account_id": "acc_001",
  "amount": 50000.00,
  "verification_token": "vtk_abc123"
}

// Response 201 (sama format seperti transaksi lain)
```

---

## 9. Registrasi (Buka Rekening)

### `POST /registration/initiate`
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

## Error Code Reference

| Code | HTTP | Description |
|------|------|-------------|
| `AUTH_INVALID_PIN` | 401 | PIN/kode akses salah |
| `AUTH_ACCOUNT_LOCKED` | 423 | Terlalu banyak percobaan |
| `AUTH_TOKEN_EXPIRED` | 401 | Access token expired |
| `AUTH_TOKEN_INVALID` | 401 | Token tidak valid |
| `AUTH_BIOMETRIC_NOT_REGISTERED` | 401 | Biometrik belum terdaftar |
| `AUTH_OLD_PIN_MISMATCH` | 422 | PIN lama salah saat ganti PIN |
| `AUTH_DEVICE_NOT_RECOGNIZED` | 403 | Device tidak dikenal |
| `ACCOUNT_NOT_FOUND` | 404 | Rekening tidak ditemukan |
| `TRANSFER_ACCOUNT_NOT_FOUND` | 404 | Rekening tujuan tidak valid |
| `TRANSFER_INSUFFICIENT_BALANCE` | 422 | Saldo tidak cukup |
| `TRANSFER_LIMIT_EXCEEDED` | 422 | Melebihi limit harian |
| `TRANSFER_SELF_TRANSFER` | 422 | Transfer ke rekening sendiri |
| `EWALLET_ACCOUNT_NOT_FOUND` | 404 | Nomor e-wallet tidak valid |
| `EWALLET_INSUFFICIENT_BALANCE` | 422 | Saldo tidak cukup |
| `EWALLET_PROVIDER_DOWN` | 503 | Provider sedang gangguan |
| `RATE_LIMIT_EXCEEDED` | 429 | Terlalu banyak request |
| `MAINTENANCE_MODE` | 503 | Sedang maintenance |
| `IDEMPOTENCY_CONFLICT` | 409 | Transaksi sudah diproses |
| `VALIDATION_ERROR` | 400 | Input tidak valid |
| `INTERNAL_ERROR` | 500 | Kesalahan internal server |