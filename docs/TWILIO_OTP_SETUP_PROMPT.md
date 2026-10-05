# Prompt untuk AI Agent: Setup Twilio Verify (SMS + Voice Call) untuk OTP Nomor HP

> **Repo:** `bca-mobile-api` (Go, chi, pgx/PostgreSQL, Redis, modular monolith)
> **Package utama:** `internal/pkg/sms` (`sms.go`, `twilio.go`, `phone.go`, `twilio_test.go`, `phone_test.go`)
> **Tujuan:** Menyiapkan konfigurasi Twilio untuk memverifikasi nomor HP nasabah. OTP dikirim lewat **Twilio Verify** dengan channel **`sms`** dan **`call`**, lalu kode yang diketik nasabah dicek.

---

## 0. Aturan Kerja (WAJIB dibaca dulu)

1. **Review dulu, jangan langsung coding.** Baca semua file di `internal/pkg/sms`, lalu cari pemakaian `sms.NewProvider`, `sms.NewVerifier`, `sms.IsVerifierProvider`, `ProviderConfig`, dan semua env `SMS_*` di `internal/config` dan `internal/router`. Jangan berasumsi struktur config.
2. **Laporkan rencana dulu.** Sebelum mengubah kode, kirim ringkasan: file yang akan diubah, signature baru, dan risiko. **Tunggu konfirmasi.** Ini wajib untuk setiap perubahan *logic*, bukan hanya perubahan config.
3. **Jangan perbaiki bug lain di luar scope tanpa izin.** Catat temuannya, lalu tanyakan.
4. **Tidak boleh ada secret di repo, log, error, test fixture, atau commit.** Lihat §2.
5. Kerjakan acceptance criteria (§7) **satu per satu**, lalu verifikasi masing-masing. Jangan dicentang sekaligus.

---

## 1. Konteks Arsitektur yang Sudah Ada

Package ini sudah punya dua transport Twilio. **Jangan tulis ulang client dari nol.**

| Interface | Implementasi | Siapa yang membuat kode OTP | Env `SMS_PROVIDER` |
|---|---|---|---|
| `Gateway` (`SendOTP(ctx, phone, otp)`) | `TwilioGateway` — Messages API `api.twilio.com/2010-04-01/Accounts/{SID}/Messages.json` | Aplikasi kita (hash disimpan di Redis/DB) | `twilio` |
| `Verifier` (`StartVerification`, `CheckVerification`) | `TwilioVerifier` — Verify API `verify.twilio.com/v2/Services/{VA…}/Verifications` dan `/VerificationCheck` | Twilio | `twilio_verify` |

- Pemilihan transport dilakukan **sekali saat boot** di `internal/router` melalui `IsVerifierProvider` → `NewVerifier` atau `NewProvider`. Konfigurasi yang salah **harus menggagalkan boot**. Jangan pernah diam-diam fallback ke mock.
- **Scope tugas ini adalah `twilio_verify`.** Akun Twilio masih **trial**, dan di akun trial Messages API menolak body custom (error `572006`) serta hanya bisa mengirim ke *verified caller ID*. `TwilioGateway` tetap dipertahankan apa adanya.
- Yang tetap menjadi tanggung jawab kita meskipun memakai Verify: validasi session dan step, device binding, failure counter + lockout 30 menit, kuota resend per session, batas per nomor per jam, dan audit trail.
- Kontrak error yang sudah ada dan **tidak boleh berubah**:
  - Kode salah → `(false, nil)`, bukan error.
  - HTTP 404 dari Verify → `ErrVerifyExpired` → `OTP_EXPIRED`.
  - HTTP 429 / code `60202` / `60203` → `ErrVerifyRateLimited`.
  - Pesan error dari provider selalu melewati `scrubNumbers` (nomor HP di-redact, error code tetap terbaca).
  - Log hanya memuat `phone_suffix` (4 digit terakhir) dan `operator`. Nomor lengkap dan kode OTP **tidak pernah** dilog.

### Yang TIDAK termasuk scope
- **Email OTP** (`comms.twilio.com/v1/Emails` atau Verify `Channel=email`) tidak dikerjakan.
- **Voice call dengan TwiML `voice_speech_recognition`** (`/Calls.json`) **bukan** mekanisme pengiriman OTP. Template itu memakai `<Gather input="speech">` untuk *mendengarkan* ucapan penerima. OTP lewat suara dikirim memakai **Verify `Channel=call`**, dan Twilio yang membacakan kodenya. Jangan pakai Calls API untuk OTP.

---

## 2. Credential & Environment Variable

> ⛔ **JANGAN** menulis Account SID, Auth Token, atau Service SID asli di kode, test, `.env.example`, README, commit message, atau output log agent. Ambil nilainya dari environment / secret manager saat runtime saja.
> Credential yang sudah pernah ditempel di chat atau dokumen harus dianggap bocor dan **di-rotate** di Twilio Console (Account → API keys & tokens → buat secondary token → promote).

| Env | Wajib | Format / validasi | Keterangan |
|---|---|---|---|
| `SMS_PROVIDER` | ✅ | `twilio_verify` | Memilih `NewVerifier` |
| `SMS_ACCOUNT_SID` | ✅ | `^AC[0-9a-fA-F]{32}$` | Basic-auth user |
| `SMS_AUTH_TOKEN` | ✅ | non-empty | Basic-auth password. Tidak pernah dilog atau di-echo |
| `SMS_VERIFY_SERVICE_SID` | ✅ | `^VA[0-9a-fA-F]{32}$` | Verify Service ("Try It Out Verify Service" di trial) |
| `SMS_VERIFY_CHANNELS` | ⬜ | subset dari `sms,call`; default `sms` | **Baru.** Channel yang diizinkan |
| `SMS_VERIFY_LOCALE` | ⬜ | default `id` | **Baru.** Bahasa SMS/suara Verify |
| `SMS_VERIFY_CODE_TTL` | ⬜ | durasi Go; default `10m` | **Baru.** Harus sama dengan setting Verify Service di Console |
| `SMS_VERIFY_BASE_URL` | ⬜ | URL; default `https://verify.twilio.com` | **Baru.** Hanya untuk test (httptest) |
| `SMS_BASE_URL` | ⬜ | URL; default `https://api.twilio.com` | **Hanya** untuk Messages API |
| `SMS_SENDER` | ⬜ | E.164 `+…` atau `MG…` | Hanya untuk `twilio` (Messages). Tidak dipakai di scope ini |
| `SMS_TIMEOUT` | ⬜ | durasi Go; default `10s` | Timeout satu HTTP call |

**`.env.example`** (placeholder saja):

```dotenv
SMS_PROVIDER=twilio_verify
SMS_ACCOUNT_SID=ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
SMS_AUTH_TOKEN=__set_in_secret_manager__
SMS_VERIFY_SERVICE_SID=VAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
SMS_VERIFY_CHANNELS=sms,call
SMS_VERIFY_LOCALE=id
SMS_VERIFY_CODE_TTL=10m
SMS_TIMEOUT=10s
# SMS_VERIFY_BASE_URL=   # test only
```

### Checklist Twilio Console (dikerjakan manusia, bukan agent)
- [ ] Verify Service sudah ada. Code length = 6. Catat SID `VA…` ke secret manager.
- [ ] **Messaging → Settings → Geo Permissions**: Indonesia (+62) diaktifkan.
- [ ] **Voice → Settings → Geo Permissions**: Indonesia (+62) diaktifkan (untuk `Channel=call`).
- [ ] Trial: nomor tujuan uji sudah terdaftar sebagai *Verified Caller ID*.
- [ ] Code expiry di Verify Service sama dengan `SMS_VERIFY_CODE_TTL`.
- [ ] Auth Token lama sudah di-rotate.

---

## 3. Referensi API (bentuk request; credential lewat env)

```bash
# Kirim OTP via SMS
curl -X POST "https://verify.twilio.com/v2/Services/$SMS_VERIFY_SERVICE_SID/Verifications" \
  --data-urlencode "To=+62812xxxxxxx" \
  --data-urlencode "Channel=sms" \
  --data-urlencode "Locale=id" \
  -u "$SMS_ACCOUNT_SID:$SMS_AUTH_TOKEN"

# Kirim OTP via Voice Call (Twilio membacakan kode)
curl -X POST "https://verify.twilio.com/v2/Services/$SMS_VERIFY_SERVICE_SID/Verifications" \
  --data-urlencode "To=+62812xxxxxxx" \
  --data-urlencode "Channel=call" \
  --data-urlencode "Locale=id" \
  -u "$SMS_ACCOUNT_SID:$SMS_AUTH_TOKEN"

# Cek kode
curl -X POST "https://verify.twilio.com/v2/Services/$SMS_VERIFY_SERVICE_SID/VerificationCheck" \
  --data-urlencode "To=+62812xxxxxxx" \
  --data-urlencode "Code=123456" \
  -u "$SMS_ACCOUNT_SID:$SMS_AUTH_TOKEN"
```

Respons penting: `status` = `pending` | `approved` | `canceled`. Verify **menghapus** verification yang sudah expired atau approved, sehingga check berikutnya mendapat 404.

> Sebelum mengimplementasikan `Locale=id`, agent wajib mengecek dokumentasi Twilio Verify (daftar *supported languages*) apakah `id` didukung untuk **sms dan call**. Kalau tidak didukung di salah satunya, laporkan dan jangan kirim `Locale` untuk channel tersebut.

---

## 4. Task Implementasi

### Task A — Wiring config (`internal/config` → `sms.ProviderConfig` → `internal/router`)
1. Tambahkan field baru di `ProviderConfig`: `VerifyBaseURL string`, `VerifyChannels []string`, `VerifyLocale string`, `VerifyCodeTTL time.Duration`.
2. Parse env §2 di `internal/config`, mengikuti pola parsing yang sudah ada (cek dulu apakah memakai `envconfig`, `os.Getenv` manual, atau yang lain).
3. Pastikan router memanggil `NewVerifier` saat `IsVerifierProvider(SMS_PROVIDER)`, dan **error dari constructor menggagalkan boot**.

### Task B — Fix temuan #2: `BaseURL` dipakai bersama Messages dan Verify (bug)
- **Masalah:** `NewVerifier` meneruskan `cfg.BaseURL` (milik Messages API). Kalau `SMS_BASE_URL=https://api.twilio.com` di-set, Verify mengirim ke host yang salah dan mendapat 404. Di `TwilioVerifier.post`, 404 dipetakan ke `ErrVerifyExpired`, sehingga nasabah melihat **OTP_EXPIRED** padahal penyebabnya salah konfigurasi. Bug ini senyap.
- **Fix:** `NewVerifier` memakai `cfg.VerifyBaseURL` saja, dan default-nya `twilioVerifyBaseURL`. `SMS_BASE_URL` tidak boleh lagi memengaruhi Verify.
- **Regression test wajib:** T9.

### Task C — Fix temuan #3: TTL tidak sinkron
- **Masalah:** `OTPMessage` menulis "Berlaku 5 menit" (TTL milik Gateway), sedangkan Verify memakai TTL-nya sendiri (default 10 menit, konstanta `twilioVerifyCodeTTL`). Kalau `expires_in` di response API onboarding/registrasi tetap 300 detik saat mode `twilio_verify`, app akan menampilkan countdown yang salah.
- **Fix (usulan, wajib dikonfirmasi):**
  - Tambahkan method `CodeTTL() time.Duration` ke interface `Verifier`. `TwilioVerifier` mengembalikan `cfg.VerifyCodeTTL` (default 10m).
  - Cari semua tempat `expires_in` atau TTL OTP dihitung di flow registrasi/onboarding. Saat Verifier aktif, nilainya diambil dari `verifier.CodeTTL()`.
  - Fungsi publik `TwilioVerifyCodeTTL()` boleh dipertahankan untuk kompatibilitas, atau dihapus setelah semua pemakainya dicek. Laporkan pilihannya.

### Task D — Channel `call` di Verify
- **Masalah:** `StartVerification` hardcode `Channel=sms`.
- **Fix (usulan signature, wajib dikonfirmasi sebelum mengubah interface):**
  ```go
  type Channel string
  const (
      ChannelSMS  Channel = "sms"
      ChannelCall Channel = "call"
  )
  // Verifier
  StartVerification(ctx context.Context, phone string, ch Channel) error
  ```
  - Validasi `ch` terhadap `VerifyChannels` dari config. Channel di luar allowlist → error sentinel baru `ErrChannelNotAllowed` (map ke 400 di domain), tanpa memanggil Twilio.
  - Channel kosong → `ChannelSMS`.
  - Kirim `Locale` sesuai §3.
  - Tambahkan field `"channel"` di log `verification sent` / `verify provider refused to send`.
  - **Tidak ada retry**, sama seperti sekarang: Verify punya counter per nomor sendiri.
  - Semua pemanggil `StartVerification` di domain registrasi/onboarding diperbarui. Endpoint request menerima `channel` opsional (`sms` | `call`). Perubahan kontrak API harus dilaporkan dulu.
  - `CheckVerification` **tidak berubah**: kode dari SMS maupun call dicek di endpoint yang sama.

### Task E — Minor: pesan error `NewProvider`
- Pesan `unknown SMS_PROVIDER %q (supported: twilio, twilio_verify)` menyesatkan karena `NewProvider` tidak menangani `twilio_verify`. Ubah menjadi `(supported here: twilio; use NewVerifier for twilio_verify)`.

### (Opsional, catat saja) Error Verify lain yang layak dipetakan
- `60200` invalid parameter → 400. `60205` SMS tidak didukung untuk landline (sarankan `call`). `60410` diblokir Fraud Guard.
- **Jangan diimplementasikan tanpa konfirmasi.** Cukup dilaporkan.

---

## 5. Aturan Keamanan (non-negotiable)

- OTP, nomor HP lengkap, Auth Token, dan Account SID tidak boleh muncul di log, error, metric label, maupun panic message.
- Test memakai `httptest.Server` + `VerifyBaseURL`. **Unit test tidak boleh memanggil Twilio sungguhan.**
- Fixture test memakai SID dummy yang lolos regex (mis. `AC` + 32×`0`) dan nomor dummy (`+6281200000000`).
- Sebelum commit, jalankan: `git diff --cached | grep -E "AC[0-9a-f]{32}|VA[0-9a-f]{32}"`. Hasilnya harus kosong selain fixture dummy.

---

## 6. Test Plan

Unit test di `twilio_test.go` / `sms_test.go`, ditambah test config/router bila ada.

| # | Skenario | Input | Ekspektasi |
|---|---|---|---|
| T1 | SID kosong / `SK…` / terpotong / huruf kecil `ac…` | `NewVerifier` | Error, boot gagal, pesan jelas |
| T2 | Service SID kosong / salah format | `NewVerifier` | Error |
| T3 | Wiring provider | `SMS_PROVIDER=twilio_verify` | Router memanggil `NewVerifier`, bukan `NewProvider` |
| T4 | Start SMS sukses | httptest 201 `{"status":"pending"}` | `nil`. Form berisi `To` (E.164), `Channel=sms`, `Locale`. Basic-auth benar. Log tanpa nomor penuh |
| T5 | Start call sukses | `ChannelCall` | Form berisi `Channel=call` |
| T6 | Channel di luar allowlist | `VerifyChannels=[sms]`, kirim `call` | `ErrChannelNotAllowed`, **0 request** ke server |
| T7 | Channel kosong | `""` | Diperlakukan sebagai `sms` |
| T8 | Check | `approved` / `pending` / `canceled` | `(true,nil)` / `(false,nil)` / `(false,nil)` |
| T9 | **Regresi BaseURL** | `BaseURL=http://messages-mock`, `VerifyBaseURL` kosong | URL Verify = `https://verify.twilio.com/v2/Services/VA…/…` |
| T10 | Check 404 | httptest 404 | `errors.Is(err, ErrVerifyExpired)` |
| T11 | Rate limit | 429 / code 60202 / 60203 | `errors.Is(err, ErrVerifyRateLimited)` |
| T12 | Error berisi nomor | 400 code 60200, message berisi `+6281234567890` | Error berisi `[number redacted]` dan `60200` |
| T13 | Nomor tidak valid | `"12345"` | `ErrInvalidPhone`, 0 request |
| T14 | Timeout | Server tidur > `Timeout` | Error, tidak hang |
| T15 | TTL | `SMS_VERIFY_CODE_TTL` kosong / `5m` | `CodeTTL()` = 10m / 5m. `expires_in` di response API mengikuti nilai ini |
| T16 | Start tanpa retry | httptest 503 | Tepat **1** request |
| T17 | Pesan error `NewProvider` | `Provider="foo"` | Pesan memuat petunjuk `NewVerifier` |
| T18 | Secret scan | Seluruh repo dan output test | Tidak ada SID/token asli |

**Smoke test manual (oleh manusia, akun trial, nomor *verified*):**
- S1: Start `sms` → SMS diterima (cek bahasa sesuai `Locale`) → check kode benar → `approved`.
- S2: Start `call` → telepon masuk, kode dibacakan → check → `approved`.
- S3: Check kode salah 1× → `(false,nil)`, failure counter kita naik.
- S4: Tunggu > TTL → check → `OTP_EXPIRED`.
- S5: Matikan Geo Permission Voice ID → start `call` → error terbaca, nomor ter-redact di log.

Perintah verifikasi:

```bash
go vet ./... && go test ./internal/pkg/sms/... -race -count=1 && go test ./... -count=1
```

---

## 7. Acceptance Criteria (cek satu per satu, laporkan bukti per item)

- [ ] AC1 — Boot gagal dengan pesan jelas untuk setiap env wajib yang kosong atau salah format (T1, T2).
- [ ] AC2 — `SMS_PROVIDER=twilio_verify` men-wire `Verifier` (T3).
- [ ] AC3 — OTP terkirim lewat `sms` dan `call`. Channel di luar allowlist ditolak tanpa memanggil Twilio (T4–T7).
- [ ] AC4 — `SMS_BASE_URL` tidak memengaruhi Verify (T9).
- [ ] AC5 — `expires_in` di response API = TTL Verifier saat mode Verify (T15).
- [ ] AC6 — Kontrak error lama tetap: wrong code, expired, rate limit, redaction (T8, T10–T12).
- [ ] AC7 — Tidak ada secret atau PII di repo, log, maupun error (T18, §5).
- [ ] AC8 — `go vet` dan seluruh test hijau.
- [ ] AC9 — `.env.example` dan README/config docs diperbarui (placeholder saja).

## 8. Format Laporan Akhir dari Agent
1. Ringkasan perubahan per file (path + alasan).
2. Perubahan kontrak publik: interface `Verifier`, field request API, error sentinel baru.
3. Hasil test (output ringkas) dan status tiap AC beserta buktinya.
4. Temuan di luar scope yang **tidak** dikerjakan.
5. Langkah manual yang masih perlu dilakukan manusia (Console checklist §2, smoke test §6).
