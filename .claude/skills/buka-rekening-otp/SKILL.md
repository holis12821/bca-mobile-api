---
name: buka-rekening-otp
description: Verifikasi OTP SMS pada flow buka rekening BCA — endpoint POST /v1/onboarding/verify-otp dan POST /v1/onboarding/resend-otp, step OTP_VERIFY di antara PERSONAL_DATA dan BIOMETRIC, penerbitan OTP 6 digit saat personal-data tersimpan, hash OTP dengan TTL, penghitung percobaan gagal, blokir OTP_BLOCKED beserta retry_after_seconds, rate limit kirim ulang, SMS gateway, dan audit OTP_SENT/OTP_VERIFIED/OTP_FAILED. Gunakan saat membuat atau memodifikasi penerbitan, verifikasi, atau kirim ulang OTP onboarding, kebijakan blokir dan rate limit OTP, atau adaptor SMS gateway. Trigger juga pada "verify-otp", "resend-otp", "OTP_INVALID", "OTP_EXPIRED", "OTP_BLOCKED", "otp_sent_to", "otp_expires_at", "kirim ulang OTP", dan "blokir OTP". JANGAN gunakan untuk OCR e-KTP, biometrik, video call, atau submit akhir (itu skill `buka-rekening-onboarding`), katalog kartu Paspor (itu `buka-rekening-kartu`), OTP atau PIN login nasabah lama (itu `auth`), atau UI Android (project terpisah).
---

# Buka Rekening — Verifikasi OTP
   File reference prompt merujuk ke .claude/skills/buka-rekening-otp/references/prompts.md

OTP di sini **bukan** OTP login. Ini penerbitan dan verifikasi kode sekali pakai
untuk calon nasabah yang **belum punya akun**, di tengah flow pembukaan rekening.
Identitasnya adalah `session_id` onboarding, bukan token bearer.

Kontrak yang mengikat: `06-BUKA-REKENING-API-SPEC.md` §3a, §3b, §3c.
Skill ini memperdalam bagian itu; kalau keduanya berbeda, **spec yang menang**.

---

## 1. Posisi di mesin step

```
TNC → OCR → PERSONAL_DATA → OTP_VERIFY → BIOMETRIC → VIDEO_CALL → CREDENTIALS → REVIEW → COMPLETED
```

OTP menempati satu step penuh di antara data pribadi dan biometrik. Konsekuensinya:

- `POST /personal-data` **yang menerbitkan** OTP pertama, bukan endpoint tersendiri.
  Balasannya membawa `otp_sent_to`, `otp_expires_at`, dan `current_step: "OTP_VERIFY"`.
- `POST /verify-otp` hanya sah bila `current_step == "OTP_VERIFY"`. Di luar itu balas
  `ONBOARDING_INVALID_STEP` — client akan menarik ulang sesi dan menyesuaikan posisinya.
- Sukses memajukan `current_step` ke `BIOMETRIC`. Client **tidak** menebak langkah
  berikutnya; ia mengikuti `current_step` dari response.

---

## 2. Endpoint

Semua response memakai envelope standar (`status` / `data` / `error` / `meta`).
Base path: `/v1/onboarding`. Tanpa header `Authorization` — flow ini pra-akun.

### 2a. `POST /v1/onboarding/verify-otp`

| Field request | Tipe | Catatan |
|---|---|---|
| `session_id` | string | Wajib. |
| `otp_code` | string | Wajib. Tepat 6 digit angka. |

Header `X-Device-ID` **opsional**: bila dikirim, harus cocok dengan device
pembuat sesi. Belum wajib karena tidak ada endpoint onboarding lain yang
membawanya — mewajibkannya sekarang memutus build Android yang sudah beredar.

Sukses `200 OK` → `data.current_step = "BIOMETRIC"`.

| Error code | HTTP | Arti |
|---|---|---|
| `VALIDATION_ERROR` | 400 | `otp_code` bukan 6 digit angka. Tidak memotong jatah percobaan. |
| `ONBOARDING_NOT_FOUND` | 404 | Sesi tidak dikenal, atau `X-Device-ID` bukan pemiliknya. |
| `OTP_INVALID` | 422 | Kode salah, percobaan masih tersisa. Juga pada kegagalan ke-3 saat batas kirim per nomor sudah habis: regenerasi dilewati, kode lama tetap sah. |
| `OTP_EXPIRED` | 422 | Lewat `otp_expires_at`, atau OTP sudah diganti karena percobaan gagal beruntun **dan SMS penggantinya terkirim**. |
| `ONBOARDING_INVALID_STEP` | 422 | `current_step` bukan `OTP_VERIFY`. |
| `ONBOARDING_SESSION_EXPIRED` | 422 | Sesi onboarding kedaluwarsa. |
| `OTP_BLOCKED` | 429 | Percobaan gagal melewati ambang. **Wajib** menyertakan `details.retry_after_seconds`. |
| `OTP_DELIVERY_FAILED` | 503 | Kode terbit dan tersimpan, tapi SMS gateway menolak. Termasuk saat regenerasi otomatis gagal dikirim — lihat §penghitung butir 2. |

> Status di atas mengikuti `06-...-API-SPEC.md`, yang menempatkan seluruh
> keluarga onboarding di 422. Skill ini sebelumnya menulis 400/409/410;
> spec yang menang.

### 2b. `POST /v1/onboarding/resend-otp`

| Field request | Tipe | Catatan |
|---|---|---|
| `session_id` | string | Wajib. |
| `channel` | string | Opsional. `"sms"` (default) atau `"call"`. Lihat §2b-ter. |

Sukses `200 OK` → `data.otp_sent_to`, `data.otp_expires_at`. Menerima
`X-Device-ID` opsional dengan aturan yang sama seperti `verify-otp`.

Melebihi kuota kirim ulang (3/jam per sesi) balas `RATE_LIMIT_EXCEEDED` (429)
dengan `details.retry_after_seconds` berisi sisa jendela. Batas kedua, 10 SMS/jam
**per nomor tujuan** dan lintas sesi, balas error yang sama — lihat §penghitung
butir 4. Sesi yang sedang diblokir balas `OTP_BLOCKED` (429) — kirim ulang bukan
jalan memutar blokir.

### 2b-bis. Siapa yang memiliki kodenya

`SMS_PROVIDER` memilih satu dari dua model, dan satu-satunya yang terlihat dari
luar adalah `otp_debug` dan `otp_expires_at`:

- `twilio` — kita membuat kode, menyimpan hash-nya di Redis (TTL 5 menit), dan
  memeriksanya sendiri. `otp_debug` ada di development.
- `twilio_verify` — Twilio Verify membuat, menyimpan, dan memeriksa kodenya.
  `otp_expires_at` memakai TTL provider — `SMS_VERIFY_CODE_TTL`, default 10 menit,
  **ditanyakan ke provider lewat `CodeTTL()`** dan bukan konstanta di paket domain.
  Dulu ada dua salinan angka 10 menit (satu di `sms`, satu di `onboarding`); yang
  kedua tidak ikut berubah saat expiry diubah di console, jadi aplikasi memasang
  hitung mundur yang berbeda dari kode di tangan nasabah. **`otp_debug` selalu
  kosong** karena kodenya tidak pernah lewat server ini. Wajib dipakai selama akun
  Twilio masih trial (`572006`).

### 2b-ter. Channel pengiriman (`sms` | `call`)

Berlaku di `POST /personal-data` dan `POST /resend-otp`, keduanya lewat field
`channel` yang opsional. Kosong = `sms`, jadi build Android lama tidak terpengaruh.

`"call"` membuat provider menelepon dan membacakan kodenya. Dijawab
`400 OTP_CHANNEL_NOT_ALLOWED` kecuali **dua** syarat terpenuhi: provider pemilik
kode sedang aktif (`twilio_verify` — jalur `Gateway` tidak punya transport suara
sama sekali), dan `call` ada di `SMS_VERIFY_CHANNELS`.

Penolakannya **sebelum** apa pun terkirim: tidak ada kode terbit, step tidak maju.
Satu pengecualian yang diterima sadar — allowlist provider hanya diketahui di
transport, jadi channel yang lolos `resolveChannel` lalu ditolak di sana sudah
memotong satu jatah dari batas 10 SMS/jam per nomor. Itu berarti client meminta
channel yang deployment ini memang tidak pernah aktifkan: salah build atau salah
config, bukan sesuatu yang bisa dialami nasabah biasa.

Yang **tidak** dilakukan: menurunkan `call` menjadi SMS diam-diam. Nasabah yang
meminta telepon lalu menerima SMS akan menunggu panggilan yang tidak pernah datang.

`verify-otp` **tidak berubah**: kode dari SMS maupun telepon diperiksa di endpoint
yang sama, karena cara pengiriman tidak mengubah cara pemeriksaan.

Regenerasi otomatis setelah 3 kegagalan verifikasi **selalu `sms`** — kiriman itu
dipicu server, bukan diminta nasabah, dan telepon yang tidak diminta lebih
mengagetkan daripada SMS yang tidak diminta.

Di dalam kode, percabangannya hanya ada di dua fungsi — `issueCode` dan
`checkCode` di `personal_data_service.go`. Semua kebijakan di §penghitung tetap
milik kita pada kedua model, dan `TestVerifierPath_OurPolicyStillApplies`
memakunya. Kalau menambah provider lagi, tambahkan di dua fungsi itu, jangan
menyebar `if` ke seluruh flow.

## 2c. Bentuk `details` pada 429

Client menampilkan hitung mundur dari field ini. Namanya tidak boleh berubah:

```json
{ "error": { "code": "OTP_BLOCKED", "details": { "retry_after_seconds": 1800 } } }
```

---

## 3. Aturan yang wajib dipegang

1. **OTP tidak pernah disimpan apa adanya.** Yang tersimpan hash-nya. Perbandingan
   memakai fungsi waktu-tetap supaya tidak bocor lewat selisih waktu respons.
2. **OTP tidak pernah masuk log, response, atau audit trail.** Yang boleh tampil
   hanya `otp_sent_to` dalam bentuk tersamar (`0812****8889`).
3. **Penyamaran nomor dilakukan server.** Client menampilkan apa adanya; jangan
   pernah mengirim nomor HP utuh di response OTP.
4. **Satu OTP aktif per sesi.** Menerbitkan OTP baru (kirim ulang, atau regenerasi
   setelah gagal beruntun) membatalkan yang lama seketika.
5. **Kedaluwarsa ditegakkan server**, bukan dihitung dari `otp_expires_at` di client.
   Field itu hanya untuk tampilan hitung mundur.
6. **Blokir terikat sesi**, bukan nomor HP — supaya satu nomor tidak bisa dipakai
   memblokir sesi orang lain.
7. **Nomor HP adalah PII.** Ikuti tabel enkripsi di spec §Encryption Requirements:
   terenkripsi saat transit dan saat disimpan.

---

## 4. Penyimpanan sementara

OTP bersifat sementara dan tidak perlu masuk basis data relasional.

| Kunci | Isi | Umur |
|---|---|---|
| Hash OTP aktif per sesi | hash kode + waktu terbit | selaras `otp_expires_at` |
| Penghitung percobaan gagal per sesi | bilangan | direset saat OTP baru terbit |
| Penanda blokir per sesi | waktu blokir berakhir | selama masa blokir |
| Penghitung kirim ulang per sesi | bilangan | jendela kuota kirim ulang |

Yang masuk basis data hanya **fakta** verifikasi: `otp_verified` pada rangkuman
step sesi, plus baris audit. Bukan kodenya.

---

## 5. Rate limit dan ambang

Nilai yang sudah tertulis di dokumen yang ada:

| Aturan | Nilai | Konstanta |
|---|---|---|
| Panjang OTP | 6 digit | `otpLength` |
| Umur OTP | 5 menit | `otpTTL` |
| Regenerasi otomatis | setelah 3 kali gagal | `otpRegenAt` |
| Blokir | 30 menit setelah 5 kali gagal | `otpMaxFail`, `otpBlockTime` |
| Kirim ulang per sesi | 3 per jam | `otpMaxResend` |

Semuanya di `internal/domain/onboarding/personal_data_service.go`.

### Keputusan yang sudah diambil

Tiga penghitung di atas dulu bertabrakan di dokumen. Jawabannya sekarang tetap,
dan ikut tertulis di `06-...-API-SPEC.md` §3b:

1. **Blokir 5-kegagalan adalah satu-satunya pembatas `verify-otp`.** Tidak ada
   jendela "5 per 5 menit" yang terpisah — angkanya sama dan blokirnya sudah
   lebih ketat, jadi dua pembatas hanya membuat alasan sebuah 429 jadi kabur.
   Percobaan yang ditolak karena sesi sedang terblokir **tidak** menambah
   penghitung: membanjiri endpoint tidak memperpanjang blokir.
2. **Regenerasi otomatis tidak memotong kuota kirim ulang.** Nasabah tidak
   meminta SMS itu; menagihkannya berarti tiga tebakan salah diam-diam
   menghabiskan satu hak kirim ulang. Terjadi **tepat sekali**, pada kegagalan
   ke-3 (`attempts == otpRegenAt`, bukan `>=`): dulu kegagalan ke-4 menerbitkan
   satu lagi, jadi tiga tebakan salah memakan dua SMS. Tapi regenerasi yang **gagal dikirim**
   dijawab `OTP_DELIVERY_FAILED` (503), bukan `OTP_EXPIRED`: kode lama sudah
   ditimpa, jadi "tunggu kode baru" menyuruh nasabah menunggu SMS yang tidak
   pernah berangkat.
3. **Kirim ulang tidak mereset penghitung gagal.** Kalau mereset, 3 kirim ulang
   = 12 tebakan tanpa pernah menyentuh blokir.
4. **Batas keempat mengikuti NOMOR, bukan session: 10 SMS/jam per nomor tujuan**
   (`otpMaxSendPerPhone`). Kuota kirim ulang di butir 2 dikunci per `session_id`,
   dan session gratis dibuat — jadi dulu satu alamat bisa memanen SMS dengan
   membuang session dan membuat yang baru, sebatas rate limit per IP. Tagihan
   providernya dan HP yang kebanjiran tetap milik kita. Habis →
   `429 RATE_LIMIT_EXCEEDED` + `retry_after_seconds`, di `personal-data` maupun
   `resend-otp`. **Tidak** dinolkan oleh session baru — itu justru gunanya.

   Pada regenerasi otomatis batas ini tidak menimpa apa pun: regenerasinya
   dilewati, kode yang dipegang nasabah tetap sah, jawabannya tetap
   `OTP_INVALID`. Kuncinya nomor yang sudah dinormalkan ke E.164, jadi menulis
   nomor yang sama dalam format berbeda tidak memberi jatah baru.

Penghitung gagal dan kuota kirim ulang dinolkan bersama saat `personal-data`
menerbitkan OTP pembuka sebuah step — bukan saat kirim ulang.

### Batas yang ditegakkan di handler

`otp_code` harus tepat 6 digit angka. Bentuk lain dijawab `VALIDATION_ERROR`
(400) sebelum menyentuh service, supaya salah ketik tidak memakan jatah
percobaan.

---

## 6. SMS gateway

Pengiriman SMS berada di balik satu antarmuka agar bisa ditukar per lingkungan.
**Setup provider-nya ada di skill `twilio-sms-otp`** — kredensial, Geo
Permissions, kode error Twilio, dan cara menelusuri OTP yang tidak sampai. Di
sini hanya yang mengikat kebijakan OTP.

Pilihannya dibuat sekali di `router.New`, dari `SMS_PROVIDER` dan `APP_ENV`.
Jangan pernah menyusun gateway tiruan sendiri di paket domain — pernah ada
`MockSMSGateway` yatim di `internal/domain/onboarding/` yang mencatat OTP ke log
tanpa gerbang apa pun.

| Keadaan | Gateway |
|---|---|
| `SMS_PROVIDER` terisi | Provider sungguhan (`TwilioGateway`). Kredensial salah → **boot gagal**, bukan diam-diam tidak mengirim |
| Kosong + `APP_ENV=development` | `MockGateway` — kode hanya ke log, juga muncul sebagai `otp_debug` |
| Kosong + environment lain | `Config.Validate()` menolak boot |

- Kegagalan kirim **tidak boleh** membatalkan penerbitan OTP secara diam-diam:
  kode tetap tersimpan dan sesi tetap maju ke `OTP_VERIFY`, tapi response-nya
  `OTP_DELIVERY_FAILED` (503) supaya client menawarkan kirim ulang, bukan `200`
  dengan hitung mundur untuk SMS yang tidak pernah berangkat. Aturan yang sama
  berlaku di `POST /registration/initiate` dan `POST /account/profile/otp`.
- Pengiriman dilakukan **setelah** OTP tersimpan, bukan sebelum — supaya tidak ada
  SMS untuk kode yang gagal disimpan.
- Nomor dinormalkan ke E.164 **di dalam gateway**, bukan di service. `0812…`,
  `62812…`, dan `+62 812…` sama-sama sampai; nomor yang tidak bisa dirutekan
  ditolak sebelum ada panggilan HTTP yang ditagih.
- Nomor yang gagal didekripsi berhenti di sini dengan `OTP_DELIVERY_FAILED`.
  Sebelumnya ciphertext-nya diteruskan ke gateway **dan** dimasking balik ke
  nasabah sebagai `otp_sent_to`.

---

## 7. Audit trail

Tiga kejadian wajib tercatat, tanpa pernah memuat kodenya:

| Event | Kapan | Yang dicatat |
|---|---|---|
| `OTP_SENT` | OTP terbit | session_id, `phone_masked`, waktu, `reason`, `delivered` |
| `OTP_VERIFIED` | verifikasi berhasil | session_id, waktu |
| `OTP_FAILED` | kode salah, kedaluwarsa, atau ditolak saat terblokir | session_id, waktu, `reason` |

`reason` pada `OTP_SENT`: `personal_data`, `resend`, `regenerated_after_failures`.
`reason` pada `OTP_FAILED`: `wrong_code` (plus `attempt`), `expired`, `blocked`.
`delivered` mencatat apakah SMS gateway menerimanya — inilah yang membedakan
"nasabah salah ketik" dari "SMS tidak pernah berangkat" saat menelusuri keluhan.

---

## 8. Daftar uji

- [ ] Kode benar dalam masa berlaku → `200`, `current_step` jadi `BIOMETRIC`.
- [ ] Kode salah → `OTP_INVALID` (400), penghitung gagal bertambah.
- [ ] Kode lewat masa berlaku → `OTP_EXPIRED` (422).
- [ ] Kirim ulang membatalkan OTP lama → kode lama ditolak.
- [ ] Gagal beruntun sampai ambang → `OTP_BLOCKED` (429) **dengan** `details.retry_after_seconds`.
- [ ] Verifikasi saat terblokir tetap `OTP_BLOCKED`, penghitung tidak bertambah lagi.
- [ ] Kirim ulang melebihi kuota → `RATE_LIMIT_EXCEEDED` (429) dengan `details.retry_after_seconds`.
- [ ] Sesi baru untuk nomor yang sama, diulang sampai 10 SMS dalam satu jam → sesi ke-11 ditolak `RATE_LIMIT_EXCEEDED` **tanpa** satu pun SMS terkirim, dan nomor lain tetap dilayani.
- [ ] `verify-otp` saat `current_step` bukan `OTP_VERIFY` → `ONBOARDING_INVALID_STEP`.
- [ ] `session_id` milik device lain → ditolak.
- [ ] Sesi kedaluwarsa → `ONBOARDING_SESSION_EXPIRED`.
- [ ] Tidak ada satu pun log, response, atau baris audit yang memuat kode OTP.
- [ ] `otp_sent_to` selalu tersamar.

---

## 9. Selesai berarti

1. Kedua endpoint memenuhi kontrak §2 dan lolos seluruh daftar uji §8.
2. Pertanyaan di §5 sudah dijawab dan nilainya tertulis, bukan tersirat di kode.
3. `06-BUKA-REKENING-API-SPEC.md` diperbarui bila ada nilai yang berubah.
4. Tidak ada kode OTP yang bisa ditemukan di log mana pun.
