# API Specification — Buka Rekening (KYC Flow)

> Endpoint lengkap untuk flow pembukaan rekening digital BCA.
> Mengikuti konvensi di `01-API-SPECIFICATION.md` (envelope, auth header, error codes).

---

## Session Lifecycle

```
POST /onboarding/sessions          ← init session
POST /onboarding/ocr               ← upload foto KTP
POST /onboarding/personal-data     ← simpan data pribadi
POST /onboarding/biometric         ← upload face + liveness
POST /onboarding/video-call/queue  ← join antrean video call
WS   /onboarding/video-call/signal ← WebRTC signaling
POST /onboarding/video-call/result ← CS submit hasil verifikasi
POST /onboarding/credentials       ← simpan kode akses + PIN
POST /onboarding/submit            ← final submit, buat rekening
GET  /onboarding/sessions/{id}     ← resume draft / cek status
DELETE /onboarding/sessions/{id}   ← batalkan & hapus data
```

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
  "accepted_tnc_version": "2026-09-01"
}
```

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
    "expires_at": "2026-09-19T10:30:00Z"
  }
}
```

### Error Codes
| Code | Keterangan |
|------|-----------|
| `ONBOARDING_DUPLICATE_NIK` | NIK sudah terdaftar sebagai nasabah |
| `ONBOARDING_PRODUCT_UNAVAILABLE` | Produk sedang maintenance |
| `ONBOARDING_SESSION_LIMIT` | Maks 3 session aktif per device |

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
| `device_capture_meta` | JSON string | `{"flash_used": true, "auto_captured": true, "resolution": "1920x1080"}` |

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
| `OCR_DUKCAPIL_TIMEOUT` | Koneksi ke Dukcapil timeout, retry |

---

## 3. Simpan Data Pribadi

Nasabah konfirmasi/edit data OCR + isi data tambahan (pekerjaan, penghasilan).

```
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
```

```json
{
  "session_id": "onb_9f8e7d6c5b4a",
  "otp_code": "847291"
}
```

Response: `200 OK` → `current_step: "BIOMETRIC"`

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
      "user_id": "mbca_s1t2u3",
      "access_code_set": true,
      "pin_set": true
    },
    "created_at": "2026-09-18T10:45:00Z"
  }
}
```

### Error Codes
| Code | Keterangan |
|------|-----------|
| `ONBOARDING_INCOMPLETE` | Belum semua step selesai |
| `ONBOARDING_SESSION_EXPIRED` | Session sudah expired (24 jam) |
| `ONBOARDING_VERIFICATION_FAILED` | Salah satu verifikasi gagal |
| `ACCOUNT_CREATION_FAILED` | Gagal buat rekening di core banking |

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
      "ocr_verified": true,
      "personal_data_saved": true,
      "otp_verified": true,
      "biometric_verified": true,
      "video_call_verified": false,
      "credentials_set": false,
      "submitted": false
    },
    "created_at": "2026-09-18T09:00:00Z",
    "expires_at": "2026-09-19T09:00:00Z"
  }
}
```

---

## 9. Batalkan Session

```
DELETE /v1/onboarding/sessions/{session_id}
```

Menghapus semua data terkait (foto, OCR, biometric) sesuai regulasi data minimization.

Response: `200 OK` → `{"status": "success", "data": {"deleted": true}}`

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
| `POST /onboarding/sessions` | 3/jam per device |
| `POST /onboarding/ocr` | 10/jam per session |
| `POST /onboarding/biometric` | 5/jam per session |
| `POST /onboarding/verify-otp` | 5/5menit per session |
| `POST /onboarding/submit` | 1/session (idempotent) |

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