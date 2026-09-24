---
name: frontend-otp-verification
description: Integrasi sisi client (Android) untuk verifikasi kode OTP pada flow buka rekening BCA — POST /v1/onboarding/verify-otp, POST /v1/onboarding/resend-otp, dan penerbitan OTP dari POST /v1/onboarding/personal-data. Gunakan saat membuat atau memperbaiki layar input OTP: bentuk request/response, pemetaan error code ke perilaku UI dan copy Bahasa Indonesia, hitung mundur otp_expires_at, tombol kirim ulang, penanganan 429 lewat details.retry_after_seconds, header X-Device-ID, dan otp_debug di development. Trigger juga pada "layar OTP", "input OTP", "pin view OTP", "countdown OTP", "tombol kirim ulang", "OTP_INVALID", "OTP_EXPIRED", "OTP_BLOCKED", "RATE_LIMIT_EXCEEDED", "retry_after_seconds", dan "otp_sent_to". JANGAN gunakan untuk implementasi backend OTP (hash, Redis, SMS gateway, audit — itu skill `buka-rekening-otp`), OTP registrasi/login nasabah lama (`/v1/registration/verify-otp`), OCR/biometrik/video call, atau katalog kartu.
---

# Frontend — Verifikasi Kode OTP (Buka Rekening)

Panduan client untuk satu layar: input OTP 6 digit di tengah flow buka rekening.

Identitas di flow ini adalah `session_id` onboarding, **bukan** bearer token —
nasabah belum punya akun. Tidak ada header `Authorization` di ketiga endpoint di
bawah.

Kontrak yang mengikat: `docs/06-BUKA-REKENING-API-SPEC.md` §3, §3b, §10a. Sisi
server ada di skill `buka-rekening-otp`. Kalau dokumen ini berbeda dengan spec,
**spec yang menang** — laporkan selisihnya, jangan diam-diam ikut dokumen ini.

---

## 1. Posisi layar OTP

```
TNC → OCR → PERSONAL_DATA → [OTP_VERIFY] → BIOMETRIC → VIDEO_CALL → CREDENTIALS → REVIEW → COMPLETED
```

- Layar OTP **tidak punya endpoint "kirim OTP"** sendiri. OTP pertama terbit
  sebagai efek samping `POST /v1/onboarding/personal-data`. Response-nya sudah
  memuat semua yang dibutuhkan layar ini.
- Navigasi selalu mengikuti `current_step` dari response, **jangan** hardcode
  layar berikutnya. Sukses verifikasi mengembalikan `current_step: "BIOMETRIC"`;
  itu yang menentukan tujuan navigasi.
- Kalau `verify-otp` menjawab `ONBOARDING_INVALID_STEP`, artinya posisi client
  sudah tidak sinkron. Tarik ulang `GET /v1/onboarding/sessions/{session_id}`
  dan pindah ke layar sesuai `current_step` yang dikembalikan.

---

## 2. Envelope response

Semua endpoint memakai satu envelope. `details` **selalu ada** pada error (bisa
`null`), jadi modelkan sebagai nullable, bukan optional-yang-hilang.

```json
{
  "status": "success",
  "data": { },
  "meta": { "request_id": "…", "timestamp": "2026-09-24T10:35:00Z" }
}
```

```json
{
  "status": "error",
  "error": { "code": "OTP_INVALID", "message": "Kode OTP tidak valid.", "details": null },
  "meta": { "request_id": "…", "timestamp": "2026-09-24T10:35:00Z" }
}
```

`meta.request_id` adalah satu-satunya pegangan saat melaporkan keluhan ke
backend — simpan di log client (kode OTP-nya jangan).

---

## 3. Tiga panggilan

Base path `/v1/onboarding`. Header opsional `X-Device-ID`: **kalau dikirim di
satu panggilan, kirim di semua panggilan sesi itu dengan nilai yang sama.**
Nilai yang berbeda dari device pembuat sesi dijawab `ONBOARDING_NOT_FOUND`
(404), bukan 403 — server sengaja tidak membocorkan bahwa sesinya ada.

### 3a. OTP pertama — dari `personal-data`

`POST /v1/onboarding/personal-data` → `200 OK`:

```json
{
  "status": "success",
  "data": {
    "personal_data_id": "pd_x1y2z3",
    "otp_sent_to": "0812****8889",
    "otp_expires_at": "2026-09-24T10:35:00Z",
    "current_step": "OTP_VERIFY"
  }
}
```

- `otp_sent_to` **sudah tersamar oleh server**. Tampilkan apa adanya; jangan
  menyusun ulang penyamaran dari nomor yang diisi nasabah di form.
- `otp_expires_at` adalah UTC RFC3339. Umur OTP 5 menit.
- Di `APP_ENV=development` response ini juga memuat `otp_debug` berisi kodenya,
  karena SMS gateway lokal hanya menulis log. Lihat §7.

### 3b. Verifikasi — `POST /v1/onboarding/verify-otp`

```json
{ "session_id": "onb_9f8e7d6c5b4a", "otp_code": "847291" }
```

`200 OK`:

```json
{ "status": "success", "data": { "verified": true, "current_step": "BIOMETRIC" } }
```

### 3c. Kirim ulang — `POST /v1/onboarding/resend-otp`

```json
{ "session_id": "onb_9f8e7d6c5b4a" }
```

`200 OK`:

```json
{
  "status": "success",
  "data": { "otp_sent_to": "0812****8889", "otp_expires_at": "2026-09-24T10:41:00Z" }
}
```

Setiap kirim ulang membatalkan OTP sebelumnya seketika — kode lama langsung
ditolak. Reset field input dan ganti hitung mundur dengan `otp_expires_at` yang
baru.

Kuota: **3 kirim ulang per jam per sesi.**

---

## 4. Peta error → aksi UI

Server sudah mengirim `message` dalam Bahasa Indonesia. **Tampilkan
`error.message`**; kolom copy di bawah hanya cadangan saat pesan server kosong
atau code tidak dikenal.

| Code | HTTP | Aksi UI | Copy cadangan |
|---|---|---|---|
| `VALIDATION_ERROR` | 400 | Bug client (lihat §5) — jangan tampilkan sebagai kesalahan nasabah. **Tidak** memotong jatah percobaan. | "Terjadi kesalahan. Coba lagi." |
| `ONBOARDING_NOT_FOUND` | 404 | Sesi/device tidak dikenal. Hentikan flow, kembali ke awal buka rekening. | "Sesi tidak ditemukan. Mulai ulang pendaftaran." |
| `OTP_INVALID` | 422 | Kosongkan input, fokuskan digit pertama, tampilkan pesan inline. Hitung mundur **tetap jalan**. | "Kode OTP tidak valid." |
| `OTP_EXPIRED` | 422 | Kode lama mati **dan OTP baru sudah dikirim server** (lihat §6.1). Kosongkan input, mulai hitung mundur 5 menit baru. Jangan panggil `resend-otp`. | "Kode OTP sudah kedaluwarsa. OTP baru telah dikirim." |
| `ONBOARDING_INVALID_STEP` | 422 | Tarik ulang sesi, navigasi ke `current_step` sebenarnya. | "Sesi sudah berpindah langkah." |
| `ONBOARDING_SESSION_EXPIRED` | 422 | Sesi 24 jam habis. Tidak bisa diselamatkan — mulai ulang. | "Sesi pendaftaran sudah kedaluwarsa (24 jam)." |
| `OTP_BLOCKED` | 429 | Matikan input **dan** tombol kirim ulang. Hitung mundur dari `details.retry_after_seconds`. | "Terlalu banyak percobaan OTP. Coba lagi dalam 30 menit." |
| `RATE_LIMIT_EXCEEDED` | 429 | Matikan tombol kirim ulang selama `details.retry_after_seconds`. Input tetap aktif — kode terakhir masih sah. | "Kirim ulang OTP sudah mencapai batas. Silakan coba lagi nanti." |
| `OTP_DELIVERY_FAILED` | 503 | SMS gagal berangkat, tapi **kode tetap terbit dan sah**. Tawarkan kirim ulang, jangan mulai ulang flow. | "Kode OTP gagal dikirim. Silakan coba kirim ulang." |

Code yang tidak ada di tabel ini: tampilkan `error.message`, biarkan nasabah
mencoba lagi. Jangan pernah memetakan code asing ke "mulai ulang pendaftaran".

### Dua sumber `RATE_LIMIT_EXCEEDED`

Code-nya sama untuk dua hal berbeda:

1. Kuota kirim ulang 3/jam per sesi (dari service).
2. Batas grup onboarding **60 request per 5 menit per IP** (dari middleware) —
   kena kalau client memanggil berlebihan, misalnya polling sesi tiap detik.

Keduanya membawa `details.retry_after_seconds`. Perlakukan sama: hormati angka
itu. Bedanya: yang dari middleware juga mengirim header `Retry-After` dan
`X-RateLimit-*`; yang dari kuota kirim ulang **hanya** di body. Karena itu
**baca `details.retry_after_seconds` dari body**, jangan bergantung pada header.

`retry_after_seconds` bisa bernilai `0` kalau Redis gagal dibaca saat menghitung
sisa waktu. Pakai fallback (mis. 60 detik) alih-alih langsung mengaktifkan
tombol.

---

## 5. Validasi di client

`otp_code` harus **tepat 6 digit angka**. Bentuk lain dijawab
`VALIDATION_ERROR` (400) sebelum menyentuh service, dan **tidak** memotong jatah
percobaan.

Konsekuensi untuk UI: jangan pernah biarkan request berangkat dengan input yang
belum 6 digit. Kirim otomatis saat digit keenam terisi, bukan lewat tombol yang
bisa ditekan saat kosong. Filter input ke `0-9` saja (papan ketik bisa mengirim
spasi, dan autofill SMS bisa membawa teks lain).

Sama untuk `session_id` kosong → `VALIDATION_ERROR`. Kalau layar OTP terbuka
tanpa `session_id`, itu bug navigasi; jangan kirim request.

---

## 6. Jebakan nyata

### 6.1 Kegagalan ke-3 menjawab `OTP_EXPIRED`, bukan `OTP_INVALID`

Server meregenerasi OTP otomatis setelah **3 kali** salah dan mengirim SMS baru.
Client menerima `OTP_EXPIRED` walaupun kodenya baru saja salah.

Artinya:

- Jangan menyimpulkan "kode kedaluwarsa karena waktu habis" dari code ini. Copy
  yang benar menyebut OTP baru sudah dikirim — itu sudah ada di `message` server.
- Jangan memanggil `resend-otp` setelah menerimanya. SMS-nya sudah berangkat;
  regenerasi otomatis **tidak** memotong kuota kirim ulang, tapi kirim ulang
  manual memotongnya.
- Mulai hitung mundur baru 5 menit. Response ini **tidak** membawa
  `otp_expires_at` — pakai konstanta 5 menit dari saat response diterima.

### 6.2 `otp_expires_at` tidak bisa diambil ulang dari server

`GET /v1/onboarding/sessions/{session_id}` mengembalikan `current_step`,
`steps_completed`, dan `expires_at` **sesi**, tetapi tidak `otp_expires_at`.

Jadi kalau proses aplikasi mati saat layar OTP terbuka, deadline OTP hilang.
Simpan `otp_expires_at` di penyimpanan yang bertahan (`SavedStateHandle` +
DataStore), dan saat resume tanpa nilai itu: jangan tampilkan hitung mundur
palsu — tampilkan tombol kirim ulang aktif.

### 6.3 Blokir 5 kegagalan adalah satu-satunya pembatas verifikasi

Kegagalan ke-5 memblokir sesi 30 menit. Selama terblokir:

- `verify-otp` menjawab `OTP_BLOCKED` dan percobaan itu **tidak** menambah
  penghitung — membanjiri endpoint tidak memperpanjang blokir. Tetap saja,
  matikan input: retry otomatis hanya memakan jatah rate limit per-IP.
- `resend-otp` juga menjawab `OTP_BLOCKED`. Kirim ulang bukan jalan memutar
  blokir; jangan tawarkan sebagai solusi di layar terblokir.
- Kirim ulang **tidak** mereset penghitung gagal. Jangan menjanjikan ke nasabah
  bahwa minta kode baru memulihkan jatah percobaan.

Penghitung gagal dan kuota kirim ulang hanya dinolkan saat `personal-data`
menerbitkan OTP pembuka step.

### 6.4 Kode OTP tidak boleh meninggalkan layar

- Jangan pernah menulis kode ke log, crash report, atau analytics.
- Jangan menyimpannya di `SavedStateHandle`, DataStore, atau cache apa pun.
- Bersihkan state input saat layar ditinggalkan.
- Jangan pernah ikut sertakan kode di laporan bug — kirim `meta.request_id`.

### 6.5 `otp_debug` hanya ada di development

Response `personal-data` dan `resend-otp` memuat `otp_debug` **hanya** saat
server berjalan dengan `APP_ENV=development`. Di environment lain field itu tidak
pernah muncul.

Boleh dipakai untuk mempercepat QA lokal, dengan dua syarat: field-nya nullable
(build release menghadapi server yang tidak mengirimnya), dan pembacaannya
dipagari flag debug build sehingga tidak ada jalur kode di release yang bisa
menampilkannya.

### 6.6 Integrasi eksternal hanya mock

Di luar `APP_ENV=development`, OCR/Dukcapil/biometrik/object storage/core banking
menjawab `503 PROVIDER_NOT_CONFIGURED`. Itu bukan error OTP, tapi layar
sebelum/sesudah OTP akan menemuinya di staging — jangan tangani sebagai kegagalan
OTP.

---

## 7. Kontrak Kotlin

Bentuk minimal yang cukup untuk layar ini.

```kotlin
// Envelope. `details` is nullable, not absent: the server always emits the key.
data class ApiEnvelope<T>(
    val status: String,
    val data: T? = null,
    val error: ApiError? = null,
    val meta: ApiMeta,
)

data class ApiError(
    val code: String,
    val message: String,
    val details: ApiErrorDetails? = null,
)

data class ApiErrorDetails(
    @SerializedName("retry_after_seconds") val retryAfterSeconds: Int? = null,
)

data class ApiMeta(
    @SerializedName("request_id") val requestId: String,
    val timestamp: String,
)

data class VerifyOtpRequest(
    @SerializedName("session_id") val sessionId: String,
    @SerializedName("otp_code") val otpCode: String,
)

data class VerifyOtpData(
    val verified: Boolean,
    @SerializedName("current_step") val currentStep: String,
)

data class ResendOtpRequest(
    @SerializedName("session_id") val sessionId: String,
)

data class ResendOtpData(
    @SerializedName("otp_sent_to") val otpSentTo: String,
    @SerializedName("otp_expires_at") val otpExpiresAt: Instant,
    // Development server only; never present in staging or production.
    @SerializedName("otp_debug") val otpDebug: String? = null,
)
```

```kotlin
interface OnboardingOtpApi {
    // X-Device-ID is optional, but once sent for a session it must stay
    // identical: a different value is answered 404, not 403.
    @POST("v1/onboarding/verify-otp")
    suspend fun verifyOtp(
        @Body body: VerifyOtpRequest,
        @Header("X-Device-ID") deviceId: String,
    ): Response<ApiEnvelope<VerifyOtpData>>

    @POST("v1/onboarding/resend-otp")
    suspend fun resendOtp(
        @Body body: ResendOtpRequest,
        @Header("X-Device-ID") deviceId: String,
    ): Response<ApiEnvelope<ResendOtpData>>
}
```

Pemetaan hasil. Perhatikan `OTP_EXPIRED`: ia membawa OTP baru, jadi bukan
kegagalan terminal.

```kotlin
sealed interface OtpResult {
    data class Verified(val nextStep: String) : OtpResult
    /** Wrong code, attempts remain. Countdown keeps running. */
    data class WrongCode(val message: String) : OtpResult
    /** Old code dead AND a fresh SMS already sent — do not call resend. */
    data class Regenerated(val message: String) : OtpResult
    /** Session locked, or resend quota spent. Honour [retryAfterSeconds]. */
    data class Throttled(val message: String, val retryAfterSeconds: Int, val inputDisabled: Boolean) : OtpResult
    /** Code is valid but the SMS never left; offer resend. */
    data class DeliveryFailed(val message: String) : OtpResult
    /** Client must re-read the session and navigate to the real step. */
    data class StepMismatch(val message: String) : OtpResult
    /** Unrecoverable: restart onboarding. */
    data class SessionGone(val message: String) : OtpResult
    data class Unknown(val message: String) : OtpResult
}

private const val FALLBACK_RETRY_SECONDS = 60

fun ApiError.toOtpResult(): OtpResult {
    // Redis read failures can make the server report 0 seconds left; a 0 would
    // re-enable the button instantly and earn another 429.
    val retry = details?.retryAfterSeconds?.takeIf { it > 0 } ?: FALLBACK_RETRY_SECONDS
    return when (code) {
        "OTP_INVALID" -> OtpResult.WrongCode(message)
        "OTP_EXPIRED" -> OtpResult.Regenerated(message)
        "OTP_BLOCKED" -> OtpResult.Throttled(message, retry, inputDisabled = true)
        "RATE_LIMIT_EXCEEDED" -> OtpResult.Throttled(message, retry, inputDisabled = false)
        "OTP_DELIVERY_FAILED" -> OtpResult.DeliveryFailed(message)
        "ONBOARDING_INVALID_STEP" -> OtpResult.StepMismatch(message)
        "ONBOARDING_NOT_FOUND", "ONBOARDING_SESSION_EXPIRED" -> OtpResult.SessionGone(message)
        else -> OtpResult.Unknown(message)
    }
}
```

Hitung mundur dari `otp_expires_at`. Deadline ditegakkan server; angka ini hanya
tampilan, jadi selisih jam perangkat tidak boleh memblokir pengiriman.

```kotlin
// Server-side expiry is authoritative. The countdown reaching zero only
// changes what the screen offers — it never stops the user from submitting a
// code they just received on a device with a skewed clock.
fun otpCountdown(expiresAt: Instant): Flow<Duration> = flow {
    while (true) {
        val left = Duration.between(Instant.now(), expiresAt)
        emit(if (left.isNegative) Duration.ZERO else left)
        if (left.isNegative || left.isZero) return@flow
        delay(1_000)
    }
}

private val OTP_TTL: Duration = Duration.ofMinutes(5)

// OTP_EXPIRED carries no otp_expires_at, so the fresh window is measured from
// the moment the response arrived.
fun regeneratedDeadline(now: Instant = Instant.now()): Instant = now.plus(OTP_TTL)
```

---

## 8. State layar

| State | Input | Tombol kirim ulang | Pemicu keluar |
|---|---|---|---|
| Menunggu kode | aktif | nonaktif sampai hitung mundur habis | 6 digit terisi → kirim |
| Mengirim | nonaktif | nonaktif | response |
| Kode salah | aktif, dikosongkan | mengikuti hitung mundur | input baru |
| Kode diregenerasi | aktif, dikosongkan | nonaktif, hitung mundur 5 menit baru | input baru |
| Terblokir | nonaktif | nonaktif | hitung mundur `retry_after_seconds` habis |
| Kuota kirim ulang habis | aktif | nonaktif selama `retry_after_seconds` | verifikasi berhasil |
| Gagal kirim SMS | aktif | aktif | kirim ulang |
| Berhasil | nonaktif | nonaktif | navigasi ke `current_step` |

Dua aturan yang mudah terlewat: tombol kirim ulang **tidak** aktif sejak awal
(nasabah baru saja menerima SMS), dan state terblokir mematikan **keduanya**.

---

## 9. Daftar uji UI

- [ ] Kode benar → navigasi mengikuti `current_step` dari response, bukan rute hardcode.
- [ ] Kode salah → input kosong, fokus digit pertama, hitung mundur tetap jalan.
- [ ] Salah 3 kali → `OTP_EXPIRED`, hitung mundur mulai ulang 5 menit, `resend-otp` **tidak** dipanggil.
- [ ] Salah 5 kali → input dan tombol kirim ulang mati, hitung mundur dari `retry_after_seconds`.
- [ ] Kirim ulang → hitung mundur memakai `otp_expires_at` baru, input dikosongkan, kode lama ditolak.
- [ ] Kirim ulang ke-4 dalam sejam → tombol mati selama `retry_after_seconds`, input tetap bisa dipakai.
- [ ] `retry_after_seconds: 0` → fallback dipakai, tombol tidak langsung hidup.
- [ ] `OTP_DELIVERY_FAILED` → menawarkan kirim ulang, tidak mengusir nasabah ke awal flow.
- [ ] Input 5 digit atau berisi non-angka → tidak ada request yang berangkat.
- [ ] Proses aplikasi dimatikan lalu dibuka lagi → tidak ada hitung mundur palsu.
- [ ] Jam perangkat digeser maju → nasabah masih bisa mengirim kode yang valid.
- [ ] `ONBOARDING_INVALID_STEP` → sesi ditarik ulang, navigasi ke step sebenarnya.
- [ ] `X-Device-ID` konsisten di `personal-data`, `verify-otp`, dan `resend-otp`.
- [ ] Build release: tidak ada jalur kode yang menampilkan atau mencatat `otp_debug`.
- [ ] Logcat, crash report, dan analytics tidak memuat kode OTP.
- [ ] Setiap pesan error yang tampil diambil dari `error.message` server.

---

## 10. Selesai berarti

1. Ketiga panggilan di §3 terpakai dengan bentuk field yang persis, dan seluruh
   code di §4 punya perilaku UI yang jelas — tidak ada yang jatuh ke pesan
   generik.
2. Seluruh daftar uji §9 lolos, termasuk yang tentang `otp_debug` dan log.
3. Tidak ada kode OTP yang bisa ditemukan di luar layar itu.
4. Kalau ada selisih antara dokumen ini dan `docs/06-BUKA-REKENING-API-SPEC.md`,
   selisihnya dilaporkan — bukan diakali di client.
