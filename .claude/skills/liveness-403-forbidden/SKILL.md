---
name: liveness-403-forbidden
description: Menelusuri kegagalan verifikasi wajah (liveness) buka rekening yang ditolak server — terutama 403 LIVENESS_INTEGRITY_FAILED dan 403 ONBOARDING_DEVICE_MISMATCH pada POST /v1/onboarding/biometric, plus 422/429/503 yang menyusul. Memuat urutan tujuh gerbang ProcessBiometric, pohon triase dari kode error ke berkas penyebab, cara membaca alasan sebenarnya dari liveness_attempts (alasan sengaja tidak pernah dikirim ke client), matriks konfigurasi per lingkungan, dan prasyarat sisi Android. Gunakan saat "biometric face id selalu gagal", "403 saat deteksi wajah", "forbidden di endpoint biometric", "liveness selalu ditolak", "kena cooldown terus", atau saat mengubah kebijakan Play Integrity, nonce, atau ambang liveness. Trigger juga pada "LIVENESS_INTEGRITY_FAILED", "LIVENESS_PROVIDER_UNAVAILABLE", "LIVENESS_CHALLENGE_INVALID", "LIVENESS_SIGNATURE_INVALID", "LIVENESS_COOLDOWN", "LIVENESS_BLOCKED", "LIVENESS_ESCALATED_TO_VIDEO_CALL", "ONBOARDING_DEVICE_MISMATCH", "BIO_LIVENESS_FAILED", "BIO_FACE_NOT_MATCH", "integrity_token", "PLAY_INTEGRITY_CLOUD_PROJECT", "liveness_attempts", "integrity_ok", dan "duplicate_frame". JANGAN gunakan untuk membangun fitur liveness dari nol atau endpoint onboarding lain (itu `buka-rekening-backend`), antrean dan WebSocket video call (itu `buka-rekening-video-call-backend`), OTP onboarding (itu `buka-rekening-otp`), atau tampilan scanner dan kalibrasi sumbu ML Kit di Android (project terpisah).
---

# Liveness ditolak server — triase 403 dan kawan-kawannya

Satu kalimat yang menghemat waktu paling banyak: **403 di endpoint biometrik
hampir selalu berarti wajahnya belum pernah dinilai.** Gerbang integritas
perangkat dan gerbang binding perangkat berjalan **sebelum** provider liveness
dipanggil, jadi kualitas deteksi wajah di Android sama sekali tidak relevan
untuk 403. Jangan mulai dari kamera.

Berkas inti: `internal/domain/onboarding/biometric_service.go`,
`biometric_engine.go`, `liveness_challenge_service.go`,
`internal/pkg/apperr/apperr.go`, `internal/config/config.go`.
Keputusan di baliknya: `BcaMobile/docs/bca-face-liveness-decisions.md` (Q3, Q6).

---

## 1. Urutan gerbang `ProcessBiometric`

Urutannya mengikat — kode error yang dibalas memberi tahu gerbang **mana** yang
menolak, dan itu satu-satunya petunjuk yang dikirim ke client.

| # | Gerbang | Berkas | Gagal → |
|---|---|---|---|
| 0 | `X-Device-ID` vs pemilik sesi (handler) | `onboarding_handler.go:998` | `403 ONBOARDING_DEVICE_MISMATCH` |
| 0b | `challenge_id`/`nonce`/`signature` wajib ada | `onboarding_handler.go:655` | `400 VALIDATION_ERROR` |
| 1 | `current_step == BIOMETRIC` | `biometric_service.go:99` | `422 ONBOARDING_INVALID_STEP` |
| 2 | `device_id` tidak kosong **dan** sama dengan sesi | `biometric_service.go:103` | `403 ONBOARDING_DEVICE_MISMATCH` |
| 3 | Cooldown / blokir belum berlaku | `biometric_service.go:110` | `429 LIVENESS_COOLDOWN`, `422 LIVENESS_BLOCKED` |
| 4 | Nonce ada, belum kedaluwarsa, belum terpakai (`GETDEL`) | `biometric_service.go:117` | `422 LIVENESS_CHALLENGE_INVALID` |
| 5 | Tantangan terikat sesi + device + nonce ini | `biometric_service.go:130` | `422 LIVENESS_CHALLENGE_INVALID` |
| 6 | Tanda tangan ECDSA atas payload kanonis | `biometric_service.go:152` | `422 LIVENESS_SIGNATURE_INVALID` |
| **7** | **Play Integrity** | `biometric_service.go:159` | **`403 LIVENESS_INTEGRITY_FAILED`** |
| 8 | Provider liveness menilai frame | `biometric_service.go:169` | `503 LIVENESS_PROVIDER_UNAVAILABLE`, `422 BIO_*` |

Dua catatan yang menjelaskan gejala aneh:

- **Gerbang 0 lolos saat header `X-Device-ID` kosong**, karena
  `assertSessionDevice` balas nil untuk device kosong
  (`session_service.go:510-513`). Gerbang 2 yang menolaknya. Jadi header yang
  hilang tetap berakhir 403, cuma lebih dalam.
- **Setiap kegagalan di gerbang 4–8 memanggil `recordFailure`**
  (`biometric_service.go:324`). Jadi 403 yang berulang ikut membakar kuota:
  percobaan ke-3 balas `429` dengan `details.retry_after_seconds: 300`, dan 6
  kegagalan dalam 24 jam → `LIVENESS_ESCALATED_TO_VIDEO_CALL` / `LIVENESS_BLOCKED`.
  Nasabah terkunci oleh gerbang yang tidak pernah bisa dia lewati.

---

## 2. Penyebab paling sering: integritas belum dikonfigurasi, dan defaultnya menolak

Ini kondisi bawaan repo, bukan kerusakan. Rantainya:

1. Android: `PLAY_INTEGRITY_CLOUD_PROJECT = 0L` di **keempat** flavor
   (`app/build.gradle.kts`) → `DeviceIntegrityProvider` balas `NotConfigured` →
   `integrity_token` terkirim **kosong**.
2. Server: `PLAY_INTEGRITY_CREDENTIALS_FILE` kosong → `NewPlayIntegrityVerifier`
   balas verifier **noop** (`biometric_engine.go:480-484`), yang selalu melaporkan
   `Verdicts: ["NOT_CONFIGURED"]` → `Acceptable()` false.
3. `LIVENESS_INTEGRITY_LOG_ONLY` **envDefault `"false"`** (`config.go:106`) →
   `checkIntegrity` balas `Allowed=false` (`biometric_service.go:257`).
4. → `403 LIVENESS_INTEGRITY_FAILED` di **setiap** percobaan.

### Kenapa ini tidak ketahuan saat boot

`Validate()` punya tepat pemeriksaan yang akan menolak kombinasi ini
(`config.go:478-486`), tapi **hanya berjalan di luar `APP_ENV=development`**.
Di dev server boot tenang dengan flow liveness yang mustahil lulus. Yang
terlihat hanya `slog.Warn` dari `ProvidersWith`, yang mudah terlewat.

Jangan tertukar: `PLAY_INTEGRITY_LOG_ONLY` (BuildConfig Android, `true` di dev)
dan `LIVENESS_INTEGRITY_LOG_ONLY` (server, default `false`) adalah **dua
sakelar berbeda**. Yang menentukan 403 adalah yang **server**.

---

## 3. Pohon triase

Mulai dari `error.code` di body response, bukan dari status HTTP.

```
403 ONBOARDING_DEVICE_MISMATCH
  → X-Device-ID tidak dikirim, atau beda dari device pembuat sesi.
    Reinstall aplikasi mengubah ANDROID_ID: sesi lama memang tidak bisa
    dilanjutkan (session_service.go:505-509). Mulai sesi baru.
    Cek: SELECT device_id FROM onboarding_sessions WHERE id = '<sesi>';

403 LIVENESS_INTEGRITY_FAILED
  → §2. Tiga kemungkinan, dibedakan dari log:
    a. tidak ada log "play integrity verification failed" dan tidak ada
       "verdict rejected"  → verifier noop, kredensial belum diisi.
    b. ada "play integrity verdict rejected" → verdict sungguhan tidak lulus:
       emulator, perangkat root, atau PLAY_INTEGRITY_CERT_SHA256 salah.
    c. ada "play integrity verification failed" → Google tidak terjangkau
       atau service account salah scope.

422 LIVENESS_CHALLENGE_INVALID
  → nonce tidak dikenal / kedaluwarsa (TTL 60s) / sudah terpakai, ATAU
    tantangan itu milik sesi atau device lain. Kirim ulang
    POST /liveness/challenge; jangan pakai ulang nonce lama.

422 LIVENESS_SIGNATURE_INVALID
  → payload kanonis kedua sisi menyimpang. core/liveness/LivenessPayload.kt
    (Android) dan livenessSignedPayload (Go) adalah PASANGAN TERIKAT;
    mengubah satu tanpa yang lain memecah SEMUA pengiriman. Juga muncul bila
    urutan frame tidak diurutkan menurut step index.

422 LIVENESS_DEVICE_KEY_INVALID
  → signature_algorithm bukan yang didukung server. Cek
    livenessSignatureAlgorithm di handler.

503 LIVENESS_PROVIDER_UNAVAILABLE
  → BUKAN kegagalan nasabah. LIVENESS_PROVIDER=internal sementara lapisan ML
    belum punya model → fail-closed by design (reason ml_layer_unavailable).
    Di dev pakai LIVENESS_PROVIDER=stub.

422 BIO_LIVENESS_FAILED / BIO_FACE_NOT_MATCH / BIO_MULTIPLE_FACES
  → provider BENAR-BENAR menilai frame dan menolak. Hanya di sini kualitas
    deteksi wajah di Android relevan. Baca kolom reason (§4).

429 LIVENESS_COOLDOWN
  → akibat, bukan sebab. Cari kegagalan SEBELUMNYA di liveness_attempts.
    Reset di dev: hapus kunci cooldown di Redis, atau pakai sesi baru.
```

---

## 4. Membaca alasan sebenarnya

Alasan **tidak pernah** dikirim ke client — menyebut gerbang yang menolak
memberi tahu penyerang gerbang mana yang harus diakali berikutnya. Jadi satu-satunya
tempat yang jujur adalah tabel audit:

```sql
SELECT created_at, outcome, reason, integrity_ok,
       liveness_score, face_match_score, frame_count, risk_signals
FROM liveness_attempts
WHERE session_id = '<session_id>'
ORDER BY created_at DESC
LIMIT 20;
```

`reason` yang mungkin muncul, dan artinya:

| `reason` | Gerbang | Artinya |
|---|---|---|
| `challenge_unknown_or_spent` | 4 | replay, kedaluwarsa, atau `challenge_id` palsu |
| `challenge_binding_mismatch` | 5 | tantangan milik sesi/device/nonce lain |
| `challenge_expired` | 5 | lewat 60 detik |
| `signature_invalid` | 6 | payload kanonis menyimpang |
| `integrity_failed` | 7 | §2 — periksa `integrity_ok` di baris yang sama |
| `ml_layer_unavailable` | 8 | provider `internal` tanpa model → fail-closed |
| `provider_error`, `provider_no_verdict`, `provider_not_configured` | 8 | provider tidak menghakimi |
| `frame_count_mismatch`, `step_index_mismatch`, `step_action_mismatch` | 8 | bukti tidak sesuai tantangan |
| `duplicate_frame`, `frame_too_similar_to_neutral` | 8 | satu foto diam dipakai untuk beberapa langkah |
| `timestamp_outside_window` | 8 | jam perangkat meleset > `LIVENESS_MAX_CLOCK_SKEW` (bawaan 2m) |
| `missing_neutral_frame`, `empty_step_frame`, `neutral_frame_undecodable` | 8 | bukti tidak lengkap/rusak |
| `no_enrolled_reference` | 8 | foto KTP tidak termuat — face match mustahil |
| `pad_below_threshold`, `face_match_below_threshold` | 8 | penilaian ML sungguhan |
| `step_frame_face_count`, `neutral_frame_face_count`, `person_changed_mid_challenge` | 8 | butuh lapisan ML |
| `stub_provider` | 8 | lulus lewat stub — **bukan** bukti apa pun soal anti-spoofing |

Tabel ini **tidak pernah** menyimpan frame. Tidak ada kolom `bytea`, dan ada
test Postgres yang menolak kalau ada yang menambahkannya. Jangan persistensi
frame untuk "memudahkan debug" — tidak ada kebijakan retensi biometrik di
project ini.

---

## 5. Matriks konfigurasi

| Key | dev / SIT | production | Kalau salah |
|---|---|---|---|
| `LIVENESS_PROVIDER` | `stub` | vendor / `internal` + model | `internal` tanpa model → `503` setiap kali |
| `LIVENESS_INTEGRITY_LOG_ONLY` | `true` | `false` (dipaksa `Validate()`) | `false` di dev → **`403` setiap kali** |
| `PLAY_INTEGRITY_CREDENTIALS_FILE` | kosong | wajib, taruh di `keys/`, **jangan commit** | kosong di prod → boot ditolak |
| `PLAY_INTEGRITY_CERT_SHA256` | kosong | wajib | kosong → APK bungkus ulang diterima |
| `LIVENESS_CHALLENGE_TTL` | `60s` | `60s` | terlalu pendek → `CHALLENGE_INVALID` wajar |
| `LIVENESS_MAX_FAILURES` / `LIVENESS_COOLDOWN` | `3` / `5m` | sama | — |
| `LIVENESS_MAX_CLOCK_SKEW` | `2m` | pertimbangkan `5m` | jam perangkat salah → `timestamp_outside_window` di **setiap** percobaan, lalu terkunci |

`stub` dan `LIVENESS_INTEGRITY_LOG_ONLY=true` ditolak **dua kali** di luar
development: `resolveLivenessProvider` mengabaikan stub, dan `Validate()`
menolak boot. Jangan pernah melemahkan salah satu dari dua gerbang itu.

Stub **tetap menegakkan seluruh lapisan deterministik** — nonce, binding, tanda
tangan, jumlah & urutan frame, timestamp, kekhasan frame. Satu foto diam yang
diulang untuk setiap langkah tetap gagal (`duplicate_frame`).

---

## 6. Prasyarat sisi Android

Sebelum menyalahkan server, pastikan client memang mengirim ini:

- Header `X-Device-ID`, nilainya **sama** dengan `device_id` pembuat sesi.
- `POST /liveness/challenge` lebih dulu, dengan `device_key_id` +
  `device_public_key` (EC P-256). Urutan aksi datang dari **server**; client
  tidak pernah membuat atau mengurutkannya sendiri.
- Multipart `biometric`: `session_id`, `challenge_id`, `nonce`, `device_key_id`,
  `device_public_key`, `signature_algorithm`, `signature`, `neutral_frame`,
  frame per langkah + `step_meta` (jumlahnya **harus sama**, diurutkan menurut
  `index`), `integrity_token`, `risk_signals`.
- `PLAY_INTEGRITY_CLOUD_PROJECT` ≠ `0L` kalau ingin token sungguhan. Selama `0`,
  server **harus** dalam mode log-only atau semuanya 403.

---

## 7. Yang tidak boleh dikerjakan sambil memperbaiki 403

- **Jangan mengembalikan field hasil dari client.** Kontrak lama punya
  `liveness_meta.completed_actions` yang diisi konstanta oleh client dan
  dipercaya server. Itu dihapus. Tidak ada field di request yang menyatakan
  hasil; server men-deteksi ulang sendiri.
- **Jangan mengirim `reason` ke client** sebagai "supaya lebih informatif".
- **Jangan menaikkan 403 jadi 200** dengan melemahkan gerbang integritas di
  produksi. Yang benar: isi kredensialnya.
- **Jangan menyebut flow ini aman dari pemalsuan.** Tanpa lapisan PAD, tidak ada
  satu pun pemeriksaan yang menilai apakah di depan kamera itu kulit atau kertas.
- **Jangan mengklaim ISO/IEC 30107-3.** Field `iso_30107_compliant` sudah dihapus
  dari kontrak; jangan dihidupkan kembali.
