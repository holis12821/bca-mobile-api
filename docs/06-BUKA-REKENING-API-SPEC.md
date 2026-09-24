# API Specification — Buka Rekening (KYC Flow)

> Endpoint lengkap untuk flow pembukaan rekening digital BCA.
> Mengikuti konvensi di `01-API-SPECIFICATION.md` (envelope, auth header, error codes).

---

## Session Lifecycle

```
GET  /onboarding/products/{product_type}/cards ← katalog kartu Paspor (publik, cacheable)
POST /onboarding/sessions          ← init session
PUT  /onboarding/sessions/{id}/card ← pilih/ganti kartu pada sesi berjalan
GET  /onboarding/ocr/{session_id}  ← hasil OCR terakhir
POST /onboarding/ocr               ← upload foto KTP
POST /onboarding/personal-data     ← simpan data pribadi
POST /onboarding/verify-otp        ← verifikasi OTP
POST /onboarding/resend-otp        ← kirim ulang OTP
POST /onboarding/biometric         ← upload face + liveness
POST /onboarding/video-call/queue  ← join antrean video call
WS   /onboarding/video-call/signal ← WebRTC signaling
POST /onboarding/video-call/result ← CS submit hasil verifikasi (internal)
POST /onboarding/video-call/agent-token ← token signaling sisi agent (internal)
GET  /onboarding/credentials/public-key ← RSA public key untuk enkripsi kredensial
POST /onboarding/credentials       ← simpan kode akses + PIN
POST /onboarding/submit            ← final submit, buat rekening
GET  /onboarding/sessions/{id}     ← resume draft / cek status
GET  /onboarding/sessions/{id}/audit ← audit trail session (internal)
GET  /onboarding/monitoring        ← status antrean & alert (internal)
DELETE /onboarding/sessions/{id}   ← batalkan & hapus data
```

Katalog kartu juga punya endpoint admin, di luar `/v1` karena bukan jalur nasabah:

```
GET /internal/v1/cards                                     ← baca katalog, termasuk kartu nonaktif
PUT /internal/v1/cards/{card_type}                         ← biaya, limit, pengiriman, is_active
PUT /internal/v1/products/{product_type}/cards/{card_type} ← urutan, default, badge, stok per wilayah
```

Ketiganya dijaga `X-Internal-API-Key`, menaikkan `catalog_version` tepat sekali per
penulisan, dan menulis nilai lama + baru ke `card_catalog_audit_log`. Header
`X-Admin-Actor` hanya DICATAT, tidak diverifikasi — model otorisasi di atas API key belum
diputuskan (`docs/08-PILIH-KARTU-API-SPEC.md` §17 butir 7). Menonaktifkan kartu yang sedang
menjadi default ditolak `422` kecuali `new_default_card_type` disertakan pada permintaan
yang sama.

Endpoint bertanda **(internal)** memerlukan header `X-Internal-API-Key` dan
tidak pernah dipanggil dari aplikasi mobile. Tanpa `INTERNAL_API_KEY` di
environment, semua endpoint itu menolak permintaan (403) — tidak ada nilai
default.

**Masa berlaku session: 24 jam sejak dibuat.** Perpindahan step tidak
memperpanjangnya; setelah lewat, semua endpoint menjawab
`ONBOARDING_SESSION_EXPIRED`.

---

## 1. Init Session

Dipanggil saat nasabah memilih jenis rekening dan setuju S&K.

```
POST /v1/onboarding/sessions
```

### Request
```json
{
  "product_type": "TAHAPAN_BCA",
  "device_id": "d_abc123",
  "accepted_tnc_version": "2026-09-01",
  "card_type": "PASPOR_BLUE",
  "card_catalog_version": "2026-09-23.1"
}
```

`card_type` dan `card_catalog_version` **opsional**; keduanya hanya berarti ketika sisipan
pilih kartu menyala (`FEATURE_CARD_SELECTION`). Query `?region_code=` dan header
`X-App-Version` ikut dibaca — wilayah menentukan ketersediaan kartu, versi aplikasi memicu
fallback client lama (kontrak lengkap: `docs/08-PILIH-KARTU-API-SPEC.md` §7).

| Kondisi | `current_step` hasil | `card` pada respons |
|---|---|---|
| Sisipan mati | `OCR` | tidak ada |
| `card_type` valid & tersedia | `OCR` | ada |
| `card_type` kosong | `CARD_SELECTION` | tidak ada |
| `card_type` tidak dikenal | tolak `422 CARD_TYPE_INVALID` | — |
| `card_type` tidak tersedia | tolak `409 CARD_TYPE_UNAVAILABLE` | — |

### Response `201 Created`
```json
{
  "status": "success",
  "data": {
    "session_id": "onb_9f8e7d6c5b4a",
    "product": {
      "type": "TAHAPAN_BCA",
      "name": "Tahapan BCA",
      "currency": "IDR",
      "min_initial_deposit": 500000,
      "features": ["Paspor BCA Mastercard Debit", "m-BCA", "KlikBCA"]
    },
    "current_step": "OCR",
    "expires_at": "2026-09-19T10:30:00Z",
    "card": {
      "card_type": "PASPOR_BLUE",
      "name": "Blue Mastercard",
      "style": "BLUE",
      "fees": { "monthly_admin": 14000, "card_issuance": 0, "card_replacement": 15000 },
      "limits": { "cash_withdrawal": 10000000, "transfer_bca": 50000000,
                  "transfer_interbank": 15000000, "debit_purchase": 50000000 },
      "selected_at": "2026-09-18T10:30:00Z"
    }
  },
  "meta": { "request_id": "...", "timestamp": "...", "catalog_outdated": true }
}
```

`card` hadir hanya bila kartu memang terpilih. `meta.catalog_outdated` muncul hanya bernilai
`true`, yaitu ketika `card_catalog_version` yang dikirim bukan versi terkini — client
menyegarkan tampilan biaya sebelum layar Ringkasan.

### Error Codes
| Code | Keterangan |
|------|-----------|
| `ONBOARDING_DUPLICATE_NIK` | NIK sudah terdaftar sebagai nasabah |
| `ONBOARDING_PRODUCT_UNAVAILABLE` | Produk sedang maintenance |
| `ONBOARDING_SESSION_LIMIT` | Maks 3 session aktif per device |
| `CARD_TYPE_INVALID` | `card_type` tidak ada di katalog produk ini |
| `CARD_TYPE_UNAVAILABLE` | Kartu sedang habis atau dinonaktifkan |
| `CARD_NOT_ELIGIBLE` | Syarat kartu belum terpenuhi; lihat `details.reason_key` |

---

## 1b. Pilih / Ganti Kartu pada Sesi Berjalan

```
PUT /v1/onboarding/sessions/{session_id}/card
```

Dipakai saat nasabah melanjutkan draf yang berhenti di `CARD_SELECTION`, menekan Back dari
S&K lalu ganti kartu, atau mengubah kartu dari layar Ringkasan. Batas laju 10 per sesi per jam.

### Request
```json
{ "card_type": "PASPOR_GOLD", "card_catalog_version": "2026-09-23.1" }
```

`session_id` diambil dari path; nilai yang sama di body diabaikan.

### Response `200 OK`
```json
{
  "status": "success",
  "data": {
    "card": { "card_type": "PASPOR_GOLD", "name": "Gold Mastercard", "style": "GOLD",
              "fees": { "monthly_admin": 16000, "card_issuance": 0, "card_replacement": 15000 },
              "limits": { "cash_withdrawal": 10000000, "transfer_bca": 75000000,
                          "transfer_interbank": 25000000, "debit_purchase": 75000000 },
              "selected_at": "2026-09-18T11:00:00Z" },
    "current_step": "OCR",
    "steps_completed": { "tnc_accepted": true, "card_selected": true, "ocr_verified": false }
  }
}
```

`current_step` adalah langkah sesi yang **sebenarnya**, bukan selalu `OCR`: sesi yang berada
di `REVIEW` tetap di `REVIEW`. Client menavigasi mengikuti nilai ini. Boleh dipanggil selama
`steps_completed.submitted == false`.

### Error Codes
| Code | HTTP | Keterangan |
|------|------|-----------|
| `CARD_CATALOG_EMPTY` | 404 | Sisipan pilih kartu sedang mati |
| `ONBOARDING_NOT_FOUND` | 404 | Sesi tidak dikenal |
| `ONBOARDING_SESSION_EXPIRED` | 422 | Sesi kedaluwarsa |
| `CARD_TYPE_INVALID` | 422 | `card_type` tidak ada di katalog produk sesi ini |
| `CARD_TYPE_UNAVAILABLE` | 409 | Stok habis atau kartu dinonaktifkan |
| `CARD_NOT_ELIGIBLE` | 422 | Syarat belum terpenuhi; lihat `details.reason_key` |
| `CARD_LOCKED` | 409 | Pengajuan sudah disubmit, kartu terkunci |

---

## 2. OCR — Upload Foto KTP

Upload foto e-KTP, server extract data via OCR engine, validasi ke Dukcapil.

```
POST /v1/onboarding/ocr
Content-Type: multipart/form-data
```

### Request (multipart)
| Field | Type | Keterangan |
|-------|------|-----------|
| `session_id` | string | ID session aktif |
| `ktp_photo` | file (JPEG/PNG) | Foto e-KTP, maks 10MB |
| `flash_used` | `"true"`/`"false"` | Flash aktif saat capture |
| `auto_captured` | `"true"`/`"false"` | Capture otomatis oleh SDK |
| `resolution` | string | Mis. `1920x1080`. Di bawah 640x480 ditolak sebagai buram |
| `sharpness_score` | float 0-100 (opsional) | Skor ketajaman dari capture SDK. < 60 → `OCR_PHOTO_BLURRY` |
| `glare_score` | float 0-100 (opsional) | Skor pantulan cahaya. ≥ 50 → `OCR_GLARE_DETECTED` |
| `corners_detected` | int 0-4 (opsional) | Jumlah sudut KTP terdeteksi. < 4 → `OCR_CORNERS_MISSING` |

Tiga field terakhir bersifat opsional: bila client tidak mengirimnya, sinyal itu
dianggap "tidak dilaporkan" dan tidak menggugurkan capture. Yang tetap dinilai
server adalah resolusi dan confidence dari OCR engine (< 75 → `OCR_PHOTO_BLURRY`).

### Response `200 OK`
```json
{
  "status": "success",
  "data": {
    "ocr_id": "ocr_a1b2c3d4",
    "accuracy_percent": 99.4,
    "extracted": {
      "nik": "3174082104950001",
      "nama_lengkap": "MUHAMMAD ARDAN PRAYOGI",
      "tempat_lahir": "Jakarta",
      "tanggal_lahir": "1995-04-21",
      "jenis_kelamin": "LAKI_LAKI",
      "alamat": "Jl. Sudirman Kav. 45 No. 12B",
      "rt_rw": "003/005",
      "kelurahan": "Senayan",
      "kecamatan": "Kebayoran Baru",
      "kota": "Jakarta Selatan",
      "provinsi": "DKI Jakarta",
      "agama": "Islam",
      "status_perkawinan": "Belum Kawin"
    },
    "dukcapil_match": true,
    "photo_quality": {
      "sharpness": "HIGH",
      "glare_detected": false,
      "all_corners_visible": true
    },
    "current_step": "PERSONAL_DATA"
  }
}
```

### Error Codes
| Code | Keterangan |
|------|-----------|
| `OCR_PHOTO_BLURRY` | Foto terlalu buram, minta ambil ulang |
| `OCR_GLARE_DETECTED` | Pantulan cahaya terdeteksi |
| `OCR_CORNERS_MISSING` | Sudut KTP terpotong |
| `OCR_NOT_KTP` | Dokumen bukan e-KTP |
| `OCR_EXPIRED_KTP` | KTP sudah tidak berlaku |
| `OCR_DUKCAPIL_MISMATCH` | Data tidak cocok dengan Dukcapil |
| `OCR_DUKCAPIL_TIMEOUT` | Koneksi ke Dukcapil timeout, boleh retry |
| `OCR_DUKCAPIL_UNAVAILABLE` | Dukcapil error (bukan timeout) — gangguan upstream |

Foto KTP baru diunggah ke object storage setelah semua validasi di atas lolos,
sehingga dokumen yang ditolak tidak meninggalkan file di bucket.

---

## 3. Simpan Data Pribadi

Nasabah konfirmasi/edit data OCR + isi data tambahan (pekerjaan, penghasilan).

```
> **Dev only:** saat `APP_ENV=development`, response `personal-data` dan
> `resend-otp` memuat `otp_debug` berisi kode OTP-nya, karena SMS gateway di
> lingkungan itu hanya menulis log. Field ini tidak pernah muncul di
> environment lain.
>
> **Integrasi eksternal:** OCR, Dukcapil, biometrik, object storage dan core
> banking hanya punya implementasi mock. Di luar `APP_ENV=development`
> semuanya menolak dengan `503 PROVIDER_NOT_CONFIGURED` — tidak lagi
> mengarang identitas terverifikasi. `POST /v1/onboarding/submit` juga menolak
> kalau `AES_KEY` atau `LOOKUP_HMAC_SECRET` tidak diset, karena tanpa itu user
> m-BCA tidak bisa dibuat dan nasabah tidak akan pernah bisa login.

POST /v1/onboarding/personal-data
```

### Request
```json
{
  "session_id": "onb_9f8e7d6c5b4a",
  "ocr_id": "ocr_a1b2c3d4",
  "personal_data": {
    "nik": "3174082104950001",
    "nama_lengkap": "MUHAMMAD ARDAN PRAYOGI",
    "tempat_lahir": "Jakarta",
    "tanggal_lahir": "1995-04-21",
    "jenis_kelamin": "LAKI_LAKI",
    "alamat_ktp": {
      "alamat_lengkap": "Jl. Sudirman Kav. 45 No. 12B",
      "rt_rw": "003/005",
      "kode_pos": "12190",
      "kelurahan": "Senayan",
      "kecamatan": "Kebayoran Baru",
      "kota": "Jakarta Selatan",
      "provinsi": "DKI Jakarta"
    },
    "alamat_domisili_sama": true,
    "pekerjaan": "KARYAWAN_SWASTA",
    "penghasilan_per_bulan": "10_20_JUTA",
    "sumber_dana_utama": "GAJI",
    "nomor_hp": "081234568889",
    "email": "m.ardan@example.com"
  }
}
```

### Response `200 OK`
```json
{
  "status": "success",
  "data": {
    "personal_data_id": "pd_x1y2z3",
    "otp_sent_to": "0812****8889",
    "otp_expires_at": "2026-09-18T10:35:00Z",
    "current_step": "OTP_VERIFY"
  }
}
```

### 3b. Verifikasi OTP

```
POST /v1/onboarding/verify-otp
X-Device-ID: d_abc123        ← opsional; bila dikirim harus cocok dengan device pembuat session
```

```json
{
  "session_id": "onb_9f8e7d6c5b4a",
  "otp_code": "847291"
}
```

`otp_code` wajib tepat 6 digit angka. Bentuk lain dijawab `VALIDATION_ERROR`
(400) dan **tidak** memotong jatah percobaan.

Response: `200 OK` → `current_step: "BIOMETRIC"`

#### Error Codes

| Code | HTTP | Keterangan |
|------|------|-----------|
| `VALIDATION_ERROR` | 400 | `otp_code` bukan 6 digit angka, atau `session_id` kosong |
| `ONBOARDING_NOT_FOUND` | 404 | Session tidak dikenal, **atau** `X-Device-ID` bukan device pemilik session |
| `OTP_INVALID` | 422 | Kode salah, percobaan masih tersisa |
| `OTP_EXPIRED` | 422 | Lewat `otp_expires_at`, atau OTP sudah diganti karena 3 kegagalan beruntun |
| `ONBOARDING_INVALID_STEP` | 422 | `current_step` bukan `OTP_VERIFY` |
| `ONBOARDING_SESSION_EXPIRED` | 422 | Session kedaluwarsa (24 jam) |
| `OTP_BLOCKED` | 429 | 5 kegagalan; membawa `details.retry_after_seconds` |
| `OTP_DELIVERY_FAILED` | 503 | Kode terbit dan tersimpan, tapi SMS gateway menolak. Kode tetap sah — nasabah bisa kirim ulang |

#### Kebijakan penghitung

Tiga hal ini pernah saling bertabrakan di dokumen. Nilainya sekarang tetap:

1. **Blokir 5-kegagalan adalah satu-satunya pembatas `verify-otp`.** Tidak ada
   jendela "5 per 5 menit" yang terpisah — kegagalan kelima memblokir session
   30 menit, yang sudah lebih ketat. Percobaan yang ditolak karena session
   sedang terblokir **tidak** menambah penghitung, jadi membanjiri endpoint
   tidak memperpanjang blokir.
2. **Regenerasi otomatis setelah 3 kegagalan tidak memotong kuota kirim
   ulang.** Nasabah tidak meminta SMS itu.
3. **Kirim ulang tidak mereset penghitung kegagalan.** Kalau mereset, 3 kirim
   ulang berarti 12 tebakan tanpa pernah kena blokir.

Penghitung kegagalan dan kuota kirim ulang sama-sama dinolkan saat
`POST /personal-data` menerbitkan OTP pembuka sebuah step.

---

## 4. Biometric — Face Liveness

Upload foto wajah + liveness proof (frame sequence dari challenge-response).

```
POST /v1/onboarding/biometric
Content-Type: multipart/form-data
```

### Request (multipart)
| Field | Type | Keterangan |
|-------|------|-----------|
| `session_id` | string | |
| `face_photo` | file (JPEG) | Foto wajah utama |
| `liveness_frames` | file[] (JPEG) | 3-5 frame challenge (kedip, gerak kepala) |
| `liveness_meta` | JSON string | `{"challenge_type": "BLINK", "completed_actions": 3, "precision_score": 98.2}` |

### Response `200 OK`
```json
{
  "status": "success",
  "data": {
    "biometric_id": "bio_m1n2o3",
    "liveness_verified": true,
    "liveness_score": 98.2,
    "face_match_with_ktp": true,
    "face_match_score": 96.7,
    "iso_30107_compliant": true,
    "current_step": "VIDEO_CALL"
  }
}
```

### Error Codes
| Code | Keterangan |
|------|-----------|
| `BIO_LIVENESS_FAILED` | Gagal deteksi keaktifan, minta ulang |
| `BIO_FACE_NOT_MATCH` | Wajah tidak cocok dengan foto KTP |
| `BIO_MULTIPLE_FACES` | Lebih dari satu wajah terdeteksi |
| `BIO_LOW_QUALITY` | Pencahayaan/resolusi tidak memadai |
| `BIO_SPOOF_DETECTED` | Dugaan foto/layar/masker terdeteksi |

---

## 5. Video Call — Queue Management

### 5a. Join Antrean

```
POST /v1/onboarding/video-call/queue
```

```json
{
  "session_id": "onb_9f8e7d6c5b4a"
}
```

Response:
```json
{
  "status": "success",
  "data": {
    "queue_id": "q_abc123",
    "queue_number": "A-042",
    "position": 2,
    "estimated_wait_seconds": 180,
    "operating_hours": {
      "start": "06:00",
      "end": "22:00",
      "timezone": "Asia/Jakarta"
    },
    "signaling_url": "wss://signal.bcamobile.id/v1/onboarding/video-call/signal?token=eyJ..."
  }
}
```

### 5b. WebSocket Signaling Protocol

```
WS wss://signal.bcamobile.id/v1/onboarding/video-call/signal?token=<jwt>
```

Token diambil dari field `signaling_url` pada response join antrean (sisi
nasabah) atau dari `POST /v1/onboarding/video-call/agent-token` (sisi agent,
internal). **Role ada di dalam token, bukan di query string** — server menolak
koneksi yang tokennya tidak memuat role `nasabah`/`agent`, sehingga nasabah
tidak bisa menyambung sebagai agent.

Untuk klien browser, `Origin` harus terdaftar di `CORS_ALLOWED_ORIGINS`;
aplikasi native tidak mengirim `Origin` dan tidak terpengaruh.

#### 5b-1. Agent Token (internal)

```
POST /v1/onboarding/video-call/agent-token
X-Internal-API-Key: <key>

{ "queue_id": "q_abc123" }
```

Response `200 OK`:
```json
{
  "status": "success",
  "data": {
    "queue_id": "q_abc123",
    "session_id": "onb_9f8e7d6c5b4a",
    "queue_number": "A-014",
    "signaling_url": "wss://signal.bcamobile.id/v1/onboarding/video-call/signal?token=...",
    "expires_at": "2026-09-20T11:00:00Z"
  }
}
```

Antrean yang sudah `COMPLETED`/`CANCELLED` tidak lagi menerbitkan token
(`VIDEO_CALL_NOT_ACTIVE`).

#### Client → Server Messages
```jsonc
// Join room (setelah connect)
{"type": "join", "session_id": "onb_...", "queue_id": "q_abc123"}

// WebRTC offer (SDP)
{"type": "offer", "sdp": "v=0\r\no=- ..."}

// ICE candidate
{"type": "ice_candidate", "candidate": {"candidate": "...", "sdpMid": "0", "sdpMLineIndex": 0}}

// Mute/unmute audio
{"type": "media_control", "action": "mute_audio"}
{"type": "media_control", "action": "unmute_audio"}

// Switch camera
{"type": "media_control", "action": "switch_camera"}
```

#### Server → Client Messages
```jsonc
// Queue update (periodic)
{"type": "queue_update", "position": 1, "estimated_wait_seconds": 60}

// Agent assigned
{"type": "agent_assigned", "agent": {"name": "Sarah Adisti", "employee_id": "CS-1042", "photo_url": "..."}}

// WebRTC answer (SDP)
{"type": "answer", "sdp": "v=0\r\no=- ..."}

// ICE candidate dari agent
{"type": "ice_candidate", "candidate": {...}}

// Agent instruction (instruksi selama call)
{"type": "instruction", "text": "Mohon tunjukkan e-KTP asli Anda ke kamera"}

// Call ended by agent
{"type": "call_ended", "result": "APPROVED", "agent_name": "Sarah Adisti", "duration_seconds": 195}
```

### 5c. Submit Hasil Video Call (dipanggil oleh CS backend, bukan mobile)

```
POST /v1/onboarding/video-call/result
X-Internal-Service-Key: <cs-backend-key>
```

```json
{
  "session_id": "onb_9f8e7d6c5b4a",
  "queue_id": "q_abc123",
  "agent_employee_id": "CS-1042",
  "result": "APPROVED",
  "ktp_shown_live": true,
  "identity_confirmed": true,
  "notes": "Nasabah kooperatif, KTP asli terverifikasi.",
  "call_duration_seconds": 195,
  "recording_id": "rec_xyz789"
}
```

---

## 6. Simpan Kredensial

Kode akses dan PIN dikirim terenkripsi (RSA public key dari server).

```
POST /v1/onboarding/credentials
```

### Request
```json
{
  "session_id": "onb_9f8e7d6c5b4a",
  "access_code_encrypted": "BASE64_RSA_OAEP_ENCRYPTED...",
  "pin_encrypted": "BASE64_RSA_OAEP_ENCRYPTED...",
  "encryption_key_id": "key_2026Q3_001"
}
```

### Response `200 OK`
```json
{
  "status": "success",
  "data": {
    "credential_id": "cred_p1q2r3",
    "biometric_login_available": true,
    "current_step": "REVIEW"
  }
}
```

Semua field di atas **wajib**, termasuk `encryption_key_id` (kolomnya NOT NULL
di database). Bila kosong, server menjawab `VALIDATION_ERROR` (400), bukan 500.

Aturan "berurutan" berlaku untuk tiga karakter beruntun ke arah mana pun, di
posisi mana pun: `Abc123`, `k3lmn9`, dan `654321` semuanya ditolak.

### Error Codes
| Code | Keterangan |
|------|-----------|
| `CRED_WEAK_ACCESS_CODE` | Kode akses terlalu lemah (berurutan/berulang) |
| `CRED_WEAK_PIN` | PIN terlalu lemah |
| `CRED_SAME_AS_ACCESS_CODE` | PIN sama dengan kode akses |
| `CRED_DECRYPTION_FAILED` | Gagal decrypt, key mismatch |

---

## 7. Final Submit — Buka Rekening

```
POST /v1/onboarding/submit
X-Idempotency-Key: <uuid>
```

### Request
```json
{
  "session_id": "onb_9f8e7d6c5b4a",
  "agreement_accepted": true,
  "agreement_version": "2026-09-01"
}
```

### Response `201 Created`
```json
{
  "status": "success",
  "data": {
    "account": {
      "account_number": "5420891234",
      "account_type": "TAHAPAN_BCA",
      "account_holder": "MUHAMMAD ARDAN PRAYOGI",
      "branch": "KCU Jakarta Thamrin",
      "branch_code": "0539",
      "currency": "IDR",
      "status": "ACTIVE",
      "min_initial_deposit": 500000,
      "initial_deposit_deadline": "2026-10-18T23:59:59Z"
    },
    "m_bca": {
      "user_id": "9f1b7a6e-2c44-4c9e-9a1f-2f0d5b7c9e31",
      "access_code_set": true,
      "pin_set": true
    },
    "created_at": "2026-09-18T10:45:00Z",
    "card": {
      "card_type": "PASPOR_GOLD",
      "name": "Gold Mastercard",
      "masked_number": "•••• 5678",
      "status": "REQUESTED",
      "delivery": {
        "method": "COURIER",
        "estimated_arrival_from": "2026-09-25",
        "estimated_arrival_to": "2026-09-29",
        "tracking_number": null
      }
    }
  }
}
```

`card` bernilai `null` bila sesi tidak memilih kartu (sesi lama, atau sisipan pilih kartu
mati). Tanggalnya `YYYY-MM-DD`, dihitung dari `delivery_days_min/max` katalog — bukan
timestamp, karena yang dijanjikan ke nasabah adalah HARI. Kartu tanpa jendela pengiriman
yang dikonfigurasi mengirim `null`, bukan tanggal yang ditebak.

`status` bernilai `REQUESTED`, `PRINTING`, atau `SHIPPED`. **Penerbitan kartu yang gagal
tetap dilaporkan `REQUESTED`**, bukan gagal: rekeningnya sudah `ACTIVE`, permintaan cetaknya
masuk antrean retry, dan menggagalkan seluruh submit karena kartu akan menukar satu kartu
yang terlambat dengan satu rekening yang hilang. Nomor kartu tidak pernah disimpan utuh —
hanya empat digit terakhir.

Submit ulang tidak menghasilkan dua permintaan cetak: `X-Idempotency-Key` menjaganya di
Redis, dan `UNIQUE(session_id)` di tabel antrean menjaganya lagi bila slot Redis sudah
kedaluwarsa.

Submit yang berhasil benar-benar membuat baris `users`, `devices`, `accounts`,
dan `transaction_limits` dalam satu transaksi — `m_bca.user_id` adalah id user
tersebut. Nasabah bisa langsung login lewat `POST /v1/auth/login/pin` dengan PIN
yang baru saja diset, dari device yang dipakai onboarding.

Provisioning membutuhkan `AES_KEY` dan `LOOKUP_HMAC_SECRET`. Bila keduanya tidak
diset (hanya mungkin di development), pembuatan user dilewati dan `user_id`
berisi placeholder `mbca_...`; service mencatat peringatan saat startup.

### Error Codes
| Code | Keterangan |
|------|-----------|
| `ONBOARDING_INCOMPLETE` | Belum semua step selesai. Bila sisipan pilih kartu menyala dan sesi belum memilih kartu, `details.missing_step` bernilai `"CARD_SELECTION"` — client mengarahkan nasabah ke `PUT /sessions/{id}/card` |
| `ONBOARDING_SESSION_EXPIRED` | Session sudah expired (24 jam) |
| `ONBOARDING_VERIFICATION_FAILED` | Salah satu verifikasi gagal |
| `ACCOUNT_CREATION_FAILED` | Gagal buat rekening di core banking / provisioning user |
| `IDEMPOTENCY_CONFLICT` | Submit dengan key yang sama masih diproses |

---

## 8. Resume Session / Cek Status

```
GET /v1/onboarding/sessions/{session_id}
```

### Response
```json
{
  "status": "success",
  "data": {
    "session_id": "onb_9f8e7d6c5b4a",
    "product_type": "TAHAPAN_BCA",
    "current_step": "VIDEO_CALL",
    "steps_completed": {
      "tnc_accepted": true,
      "card_selected": true,
      "ocr_verified": true,
      "personal_data_saved": true,
      "otp_verified": true,
      "biometric_verified": true,
      "video_call_verified": false,
      "credentials_set": false,
      "submitted": false
    },
    "created_at": "2026-09-18T09:00:00Z",
    "expires_at": "2026-09-19T09:00:00Z",
    "card": {
      "card_type": "PASPOR_GOLD",
      "name": "Gold Mastercard",
      "style": "GOLD",
      "fees": { "monthly_admin": 16000, "card_issuance": 0, "card_replacement": 15000 },
      "limits": { "cash_withdrawal": 10000000, "transfer_bca": 75000000,
                  "transfer_interbank": 25000000, "debit_purchase": 75000000 },
      "selected_at": "2026-09-18T09:05:00Z"
    }
  }
}
```

`card` bernilai `null` bila sesi berhenti sebelum kartu dipilih, atau bila sisipan pilih kartu
sedang mati. `steps_completed.card_selected` adalah field **baru** dan bernilai `false` pada
sesi yang dibuat sebelum sisipan ini ada — aman bagi client lama yang tidak membacanya.

---

## 9. Batalkan Session

```
DELETE /v1/onboarding/sessions/{session_id}
```

Menghapus semua data terkait (foto, OCR, biometric) sesuai regulasi data minimization.

Response: `200 OK` → `{"status": "success", "data": {"deleted": true}}`

---

## 10. Endpoint Pendukung

### 10a. Kirim Ulang OTP

```
POST /v1/onboarding/resend-otp

{ "session_id": "onb_9f8e7d6c5b4a" }
```

Response `200 OK`:
```json
{
  "status": "success",
  "data": {
    "otp_sent_to": "0812****8889",
    "otp_expires_at": "2026-09-20T10:35:00Z"
  }
}
```

Hanya berlaku saat session berada di step `OTP_VERIFY`. Bila session sedang
diblokir, jawabannya `OTP_BLOCKED` (429) beserta `retry_after_seconds` —
kirim ulang bukan jalan memutar blokir.

Menerima `X-Device-ID` opsional dengan aturan yang sama seperti `verify-otp`.

**Kuota: 3 kirim ulang per jam per session.** Jatah habis dijawab
`RATE_LIMIT_EXCEEDED` (429) dengan `details.retry_after_seconds` berisi sisa
jendela. Regenerasi otomatis setelah 3 kegagalan verifikasi tidak memotong
kuota ini. Kuota dinolkan saat `POST /personal-data` menerbitkan OTP pembuka.

OTP baru membatalkan yang lama seketika — kode sebelumnya langsung ditolak.

| Code | HTTP | Keterangan |
|------|------|-----------|
| `ONBOARDING_NOT_FOUND` | 404 | Session tidak dikenal, atau `X-Device-ID` bukan pemiliknya |
| `ONBOARDING_INVALID_STEP` | 422 | `current_step` bukan `OTP_VERIFY` |
| `OTP_BLOCKED` | 429 | Session sedang terblokir; `details.retry_after_seconds` |
| `RATE_LIMIT_EXCEEDED` | 429 | Kuota 3/jam habis; `details.retry_after_seconds` |
| `OTP_DELIVERY_FAILED` | 503 | SMS gateway menolak. Kuota tetap terpotong |

### 10b. Hasil OCR Tersimpan

```
GET /v1/onboarding/ocr/{session_id}
```

Mengembalikan hasil OCR terakhir dengan PII yang sudah didekripsi. Endpoint ini
hanya dilindungi `session_id`, sehingga tunduk pada rate limit per-IP grup
onboarding.

### 10c. Public Key Enkripsi Kredensial

```
GET /v1/onboarding/credentials/public-key
```

Response `200 OK`:
```json
{
  "status": "success",
  "data": {
    "algorithm": "RSA-OAEP-SHA256",
    "key_id": "pin-key-v1",
    "public_key_pem": "-----BEGIN PUBLIC KEY-----\n..."
  }
}
```

`key_id` inilah yang dikirim balik sebagai `encryption_key_id` saat menyimpan
kredensial.

### 10d. Audit Trail Session (internal)

```
GET /v1/onboarding/sessions/{session_id}/audit
X-Internal-API-Key: <key>
```

Response `200 OK`:
```json
{
  "status": "success",
  "data": {
    "session_id": "onb_9f8e7d6c5b4a",
    "events": [
      {
        "event_type": "SESSION_CREATED",
        "actor": "nasabah:device-nurholis-001",
        "details": {"product_type": "TAHAPAN_BCA"},
        "ip_address": "203.0.113.50",
        "created_at": "2026-09-20T10:00:00Z"
      }
    ],
    "count": 1
  }
}
```

`actor` memuat `device_id` nasabah untuk setiap event yang dipicu nasabah, baik
session dibaca dari cache maupun dari database. `ip_address` diambil dari
resolver terpusat: header `X-Forwarded-For`/`X-Real-IP` hanya dipercaya bila
koneksi datang dari proxy yang terdaftar di `TRUSTED_PROXIES`.

### 10e. Monitoring (internal)

```
GET /v1/onboarding/monitoring
X-Internal-API-Key: <key>
```

Response `200 OK`:
```json
{
  "status": "success",
  "data": {
    "queue_length": 12,
    "alerts": [
      {
        "rule": "QUEUE_LENGTH_HIGH",
        "severity": "WARNING",
        "message": "Video call queue exceeds 10 people. Alert CS supervisor.",
        "triggered": true
      }
    ]
  }
}
```

---

## Enum Values

### product_type
```
TAHAPAN_BCA | TAHAPAN_XPRESI | TABUNGANKU
```

### current_step (urutan wajib)
```
TNC → OCR → PERSONAL_DATA → OTP_VERIFY → BIOMETRIC → VIDEO_CALL → CREDENTIALS → REVIEW → COMPLETED
```

### pekerjaan
```
KARYAWAN_SWASTA | PNS | TNI_POLRI | WIRASWASTA | PROFESIONAL | PELAJAR_MAHASISWA | IBU_RUMAH_TANGGA | LAINNYA
```

### penghasilan_per_bulan
```
DIBAWAH_5_JUTA | 5_10_JUTA | 10_20_JUTA | 20_50_JUTA | DIATAS_50_JUTA
```

### sumber_dana_utama
```
GAJI | USAHA | INVESTASI | WARISAN | LAINNYA
```

---

## Rate Limiting

| Endpoint | Limit |
|----------|-------|
| Semua `/v1/onboarding/*` | 60/5menit per IP |
| `POST /onboarding/sessions` | 3/jam per device |
| `POST /onboarding/ocr` | 10/jam per session |
| `POST /onboarding/biometric` | 5/jam per session |
| `POST /onboarding/verify-otp` | 5 kegagalan → blokir 30 menit per session (satu-satunya pembatas; lihat §3b) |
| `POST /onboarding/resend-otp` | 3/jam per session |
| `POST /onboarding/submit` | 1/session (idempotent) |

Batas per-IP berlaku untuk seluruh grup onboarding: endpoint-endpoint ini tidak
memakai token, dan `GET /onboarding/ocr/{session_id}` mengembalikan PII hanya
dengan modal `session_id`.

### Idempotensi Submit

`X-Idempotency-Key` di-namespace per `session_id` dan diklaim secara atomik
(SETNX). Dua permintaan paralel dengan key yang sama: satu diproses, satu
dijawab `IDEMPOTENCY_CONFLICT` (409). Key yang sama dari session berbeda tidak
pernah saling mengembalikan data.

---

## Encryption Requirements

| Data | At Transit | At Rest |
|------|-----------|---------|
| Foto KTP | TLS 1.3 | AES-256-GCM, auto-delete setelah 30 hari |
| Foto wajah | TLS 1.3 | AES-256-GCM, auto-delete setelah 7 hari |
| NIK | TLS 1.3 | AES-256-GCM (PII encryption) |
| Kode Akses | RSA-OAEP + TLS 1.3 | Argon2id hash |
| PIN | RSA-OAEP + TLS 1.3 | Argon2id hash |
| Video recording | DTLS-SRTP + TLS 1.3 | AES-256-GCM, retain 5 tahun (POJK) |