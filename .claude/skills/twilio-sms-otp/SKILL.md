---
name: twilio-sms-otp
description: Setup dan integrasi provider SMS Twilio untuk pengiriman OTP di BCA Mobile API — variabel SMS_PROVIDER/SMS_ACCOUNT_SID/SMS_AUTH_TOKEN/SMS_SENDER/SMS_BASE_URL/SMS_TIMEOUT/SMS_VERIFY_SERVICE_SID, dua model provider (twilio lewat Messages API dan twilio_verify lewat Twilio Verify yang memiliki kodenya sendiri), paket internal/pkg/sms (TwilioGateway, TwilioVerifier, MockGateway, UnconfiguredGateway, NewProvider, NewVerifier), normalisasi nomor ke E.164 dan deteksi operator Telkomsel/Indosat/Tri/XL/Axis/Smartfren, gerbang pemilihan gateway di router.New yang gagal boot saat kredensial salah, kebijakan retry, dan error OTP_DELIVERY_FAILED. Gunakan saat memasang kredensial Twilio, menyiapkan Messaging Service, membuka Geo Permissions Indonesia, menelusuri OTP yang tidak sampai ke HP, mengganti atau menambah provider SMS lain, atau mengubah teks pesan OTP. Trigger juga pada "Twilio", "SMS_PROVIDER", "SMS_SENDER", "Twilio Verify", "twilio_verify", "SMS_VERIFY_SERVICE_SID", "VA…", "572006", "akun trial", "predefined SMS templates", "Messaging Service", "MG…", "Account SID", "Auth Token", "Geo Permissions", "OTP_DELIVERY_FAILED", "OTP tidak sampai", "SMS tidak terkirim", "nomor E.164", "sender id", dan "aggregator SMS". JANGAN dipakai untuk kebijakan OTP onboarding — penerbitan, blokir, kuota kirim ulang, step OTP_VERIFY (itu `buka-rekening-otp`), push notification FCM (itu `push-notification-api`), OTP atau PIN login nasabah lama (itu `bca-mobile-backend`), atau UI Android (project terpisah).
---

# Twilio — Provider SMS untuk OTP

Skill ini soal **jalan keluarnya SMS**, bukan soal kebijakan OTP-nya. Penerbitan
kode, TTL, penghitung gagal, blokir, dan kuota kirim ulang ada di
`buka-rekening-otp`. Di sini: kredensial, pemilihan gateway, normalisasi nomor,
perilaku error, dan apa yang harus diperbarui ketika provider berubah.

**Baca `CLAUDE.md` repo ini lebih dulu.** ATURAN #1 berlaku penuh: setiap file
baru disebutkan path-nya dan ditunggu persetujuannya sebelum dibuat.

Satu kalimat yang menjelaskan seluruh desain paket ini: **proses yang kelihatan
sehat sambil tidak mengirim satu pun OTP adalah kegagalan terburuk di sini**,
karena setiap dashboard tetap hijau — kode terbit, tersimpan, teraudit, step
maju — sementara tidak ada satu nasabah pun bisa lewat `OTP_VERIFY`.

---

## 0. Yang sudah ada, jangan dibuat ulang

| Sudah ada | Melayani |
|---|---|
| `internal/pkg/sms/sms.go` | `Gateway`, `NewProvider`, `MockGateway`, `UnconfiguredGateway`, `OTPMessage` |
| `internal/pkg/sms/twilio.go` | `TwilioGateway` — transport sungguhan ke Messages API |
| `internal/pkg/sms/phone.go` | `NormalizePhone` (→ E.164), `OperatorOf`, `ErrInvalidPhone` |
| `config.SMS` (`internal/config/config.go`) | Enam variabel `SMS_*` + validasi boot |
| Switch di `router.New` (`internal/router/router.go`) | Satu-satunya tempat gateway dipilih |
| `apperr.OTPDeliveryFailed` | `503 OTP_DELIVERY_FAILED` |
| `internal/pkg/sms/twilio_test.go`, `phone_test.go` | Test tanpa jaringan (`httptest`), tanpa Docker |

Menambah provider **bukan** berarti menambah antarmuka baru: `Gateway` sudah ada
dan tiga domain (`onboarding`, `registration`, `account`) sudah mendefinisikan
`SMSGateway` masing-masing dengan bentuk yang sama.

---

## 1. Siapa memanggil siapa

```
onboarding.PersonalDataService  ─┐   SavePersonalData / ResendOTP / regenerateOTP
registration.Service            ─┼─→ SMSGateway (interface domain)
account.Service                 ─┘   RequestProfileOTP
                                        │
                                        ↓  router.New memilih SATU implementasi
                      sms.Gateway ──→ TwilioGateway | MockGateway | UnconfiguredGateway
```

Domain tidak tahu Twilio ada. Itu bukan kerapian kosmetik: itu yang membuat
pergantian provider tidak menyentuh satu baris pun logika bisnis.

**Normalisasi nomor terjadi di dalam gateway**, bukan di pemanggil. Tiga call
site tidak perlu masing-masing ingat mengubah `0812…` jadi `+62812…`.

---

## 2. Variabel environment

Enam variabel, semuanya di blok `# === SMS OTP (provider pengiriman) ===`
pada `.env.example`.

| Variabel | Wajib | Isi | Salah yang sering terjadi |
|---|---|---|---|
| `SMS_VERIFY_SERVICE_SID` | saat `SMS_PROVIDER=twilio_verify` | `VA` + 32 digit hex | Diabaikan saat provider `twilio`. Bentuk salah → gagal boot. Akun trial sudah punya satu: "Try It Out Verify Service" |
| `SMS_PROVIDER` | ya, di luar `development` | `twilio` | Dikosongkan di staging → **boot ditolak** `Config.Validate()`. Typo (`twillio`) → boot ditolak juga, bukan diam-diam tidak mengirim |
| `SMS_ACCOUNT_SID` | saat provider terisi | `AC` + 32 digit hex (34 karakter) | Bentuknya dicek **persis**, bukan hanya prefiksnya: API Key SID `SK…`, tempelan terpotong, atau huruf non-hex → ditolak saat boot. Account SID ada di halaman depan console |
| `SMS_AUTH_TOKEN` | saat provider terisi | Auth Token akun | Ini password. Tempatnya di `.env` (ter-gitignore) atau secret manager — **bukan** di `.env.example`/`.env.prod.example`, yang dua-duanya ikut commit. Jangan di-log, jangan masuk Postman yang di-share |
| `SMS_SENDER` | saat provider terisi | `MG…` (Messaging Service SID) **atau** nomor Twilio E.164 `+1555…` | Diisi nama alfanumerik (`BCA`) → ditolak saat boot. Lihat §3c |
| `SMS_BASE_URL` | tidak | Kosongkan | **Host Messages saja.** Hanya untuk test (`httptest`) dan aggregator on-premise nanti. Dulu Verify ikut membacanya — lihat baris `SMS_VERIFY_BASE_URL` |
| `SMS_TIMEOUT` | tidak | `10s` | Terlalu besar menahan handler sementara nasabah menatap spinner |
| `SMS_VERIFY_CHANNELS` | tidak | `sms,call` | Channel yang boleh dipakai Verify. Kosong = `sms` saja. Nama yang tidak dikenal **menggagalkan boot**: `voice` adalah typo yang masuk akal (itu nama produknya, nama channel-nya `call`), dan menyaringnya diam-diam membuat operator yakin voice aktif. `call` butuh **Voice** → Geo Permissions Indonesia, bukan hanya Messaging |
| `SMS_VERIFY_LOCALE` | tidak | `id` | Dipatuhi untuk `sms`. Template **suara** Verify tidak mencakup bahasa Indonesia, jadi pada `call` kode dibacakan dalam bahasa Inggris dan parameter `Locale` sengaja tidak dikirim — Twilio tidak menolak locale yang tidak didukung, ia diam-diam fallback ke Inggris, jadi mengirimnya hanya menyamarkan faktanya |
| `SMS_VERIFY_CODE_TTL` | tidak | `10m` | **Harus sama** dengan code expiry di Verify Service pada console. Tidak ditegakkan di sini: ini yang dilaporkan sebagai `otp_expires_at`. Selisih = hitung mundur yang berbeda dari kode di tangan nasabah |
| `SMS_VERIFY_BASE_URL` | tidak | Kosongkan | Host **Verify**, untuk test. Sengaja terpisah dari `SMS_BASE_URL`: dua transport ini hidup di host berbeda (`api.twilio.com` dan `verify.twilio.com`). Verify dulu membaca `SMS_BASE_URL`, jadi mengisinya mengirim setiap verifikasi ke host Messages → `404` → dan `404` dari Verify berarti "tidak ada verifikasi aktif", sehingga nasabah melihat `OTP_EXPIRED` padahal kredensialnya benar. Senyap total |

Yang sudah ada dan tetap relevan:

- `APP_ENV` — menentukan fallback saat `SMS_PROVIDER` kosong, dan apakah
  `otp_debug` muncul. Dengan provider terisi, SMS **tetap dikirim sungguhan di
  `development`** — ini disengaja, supaya integrasi bisa diuji tanpa pura-pura
  jadi produksi.
- `TRUSTED_PROXIES` — tidak ada hubungannya dengan Twilio, tapi tanpa itu semua
  tester lewat tunnel berbagi satu jatah rate limit dan `resend-otp` habis
  duluan. Lihat `CLAUDE.md` bagian tunnel.

Yang **belum** ada dan sengaja tidak dibuat: delivery receipt. Twilio bisa
memanggil balik `StatusCallback` saat SMS benar-benar sampai, tapi itu butuh URL
publik, endpoint baru, verifikasi tanda tangan `X-Twilio-Signature`, dan satu
variabel `SMS_STATUS_CALLBACK_URL`. Status yang dilaporkan sekarang adalah
status *penerimaan oleh Twilio* (`queued`/`accepted`), bukan *sampai di HP*.
Kalau nanti dibutuhkan, itu pekerjaan tersendiri — minta persetujuan dulu.

---

## 3. Setup di console Twilio

### 3a. Ambil kredensial

Console → halaman depan: **Account SID** (`AC…`) dan **Auth Token**. Masukkan ke
`.env`. Jangan pakai API Key/Secret (`SK…`) — `NewTwilioGateway` menolaknya di
boot karena basic-auth-nya memakai Account SID.

### 3b. Buka Geo Permissions untuk Indonesia

Messaging → Settings → **Geo Permissions**, centang **Indonesia**.

Ini jebakan nomor satu dan tidak kelihatan dari kode: akun baru hanya boleh
mengirim ke sebagian negara. Tanpa langkah ini setiap kiriman dijawab
`21408 Permission to send an SMS has not been enabled for the region`, dan dari
sisi nasabah gejalanya identik dengan "provider mati".

### 3c. Pilih pengirim

Buat **Messaging Service** (Messaging → Services), salin SID-nya (`MG…`) ke
`SMS_SENDER`. Lebih baik daripada nomor telanjang: ia yang memilih rute dan
sender id per operator tujuan, dan nomor bisa ditambah/diganti tanpa deploy.

Gateway membaca prefiks `SMS_SENDER` untuk memutuskan parameter mana yang
dikirim — `MessagingServiceSid` atau `From`. Mengirim yang salah adalah `400`,
jadi ini bukan detail yang bisa dikira-kira.

**Sender ID alfanumerik (`BCA`) tidak dilayani untuk Indonesia.** Operator
Indonesia mewajibkan pendaftaran sender id lewat jalur resmi mereka. Untuk
tester, pakai nomor Twilio biasa; nomor pengirim akan tampil sebagai nomor
internasional, dan itu wajar.

### 3d. Verifikasi nomor tester (khusus akun trial)

Akun trial **hanya** bisa mengirim ke nomor yang sudah diverifikasi di console
(Phone Numbers → Verified Caller IDs). Nomor tester yang belum terdaftar dijawab
`21608`, dan gateway memperlakukannya sebagai kegagalan permanen — tidak
di-retry, jadi tidak ada tagihan ganda. Setiap pesan trial juga diawali
"Sent from your Twilio trial account".

---

## 4. Gerbang pemilihan gateway

Satu `switch` di `router.New`, dan urutannya mengikat:

| Keadaan | Hasil |
|---|---|
| `SMS_PROVIDER` terisi | Provider sungguhan. **Error apa pun → boot gagal** |
| Kosong + `APP_ENV=development` | `MockGateway` — kode hanya ke log, juga muncul sebagai `otp_debug` |
| Kosong + environment lain | Tidak tercapai: `Config.Validate()` sudah menolak boot. Cabang `UnconfiguredGateway` tetap ada untuk test yang membangun `Deps` kosong |

**Kredensial yang terisi tapi salah tidak boleh turun diam-diam ke "tidak
mengirim".** Ini aturan yang sama dengan `FCMPusher`, dan alasannya sama:
fallback diam-diam adalah bagaimana konfigurasi rusak sampai ke produksi tanpa
ada yang tahu. Karena itu `router.New` mengembalikan `(http.Handler, error)`.

Jangan pernah menyusun gateway tiruan sendiri di paket domain. Pernah ada
`MockSMSGateway` yatim di `internal/domain/onboarding/` yang mencatat OTP ke log
tanpa gerbang environment apa pun.

---

## 5. Normalisasi nomor dan operator

`NormalizePhone` menerima apa pun yang diketik nasabah dan mengembalikan E.164:

| Masukan | Hasil |
|---|---|
| `081234567890`, `6281234567890`, `+62 812-3456-7890`, `81234567890` | `+6281234567890` |
| `0215551234` (telepon rumah), `08012345678`, 9 digit, 14 digit | `ErrInvalidPhone` |

Penolakan terjadi **sebelum** panggilan HTTP: aggregator menagih per pesan yang
diterima, dan nomor salah ketik adalah pesan yang tidak mungkin sampai.

`OperatorOf` memetakan prefiks ke Telkomsel / Indosat / Tri / XL / Axis /
Smartfren dan ikut ke setiap baris log pengiriman. Itu yang mengubah keluhan
"OTP kadang tidak sampai" menjadi "OTP tidak sampai di nomor Indosat" — satu
pertanyaan yang bisa dijawab.

Prefiks yang tidak dikenal **bukan error**: regulator terus mengalokasikan blok
baru, dan menolak nomor karena tabel di `phone.go` ketinggalan sebulan berarti
memblokir nasabah sungguhan. Kirim tetap jalan, hanya log-nya bilang `unknown`.

---

## 5b. Dua model provider: siapa yang memiliki kodenya

`SMS_PROVIDER` memilih satu dari dua kontrak yang berbeda, bukan hanya dua
transport.

| | `twilio` (Messages) | `twilio_verify` (Verify) |
|---|---|---|
| Pembuat kode | Kita (`generateOTP`) | Provider |
| Penyimpan | Hash SHA-256 di Redis, TTL 5 menit | Provider |
| Pemeriksa | `subtle.ConstantTimeCompare` di `checkCode` | `POST .../VerificationCheck` |
| `otp_debug` | Ada di development | **Selalu kosong** |
| `otp_expires_at` | 5 menit (milik kita) | `SMS_VERIFY_CODE_TTL`, default 10 menit — ditanyakan lewat `Verifier.CodeTTL()` |
| Akun trial Twilio | **Tidak bisa** — `572006` | Bisa, dan terbukti sampai |
| Registrasi & OTP ubah profil | Terlayani | **Tidak** — keduanya butuh Messages |

Interface-nya berbeda, dan itu disengaja: `sms.Gateway` menerima kode yang kita
buat, `sms.Verifier` tidak bisa diberi kode. `NewProvider` menolak
`twilio_verify` dan `NewVerifier` menolak `twilio`, supaya config yang tertukar
gagal saat boot alih-alih menghasilkan proses yang tidak pernah bisa memeriksa
kode.

**Yang tetap milik kita di kedua model** — ini alasan percabangannya dikurung di
`issueCode` dan `checkCode` saja: validasi step dan device, penghitung kegagalan
dan blokir 30 menit, kuota kirim ulang per sesi, batas 10 SMS/jam per nomor, dan
audit trail. Ada test yang memaku keempatnya di jalur verifier
(`TestVerifierPath_OurPolicyStillApplies`).

### Kenapa akun trial tidak bisa `twilio`

Dibuktikan lewat API, bukan dugaan:

```
POST /Messages.json   → 400  572006  Invalid template name.
                               Trial accounts can only use predefined SMS templates.
GET  content.twilio.com/v1/Content
                      → 401  20003   This feature is not available on a Trial account.
```

Jadi body kustom ditolak, dan template yang diizinkan tidak bisa dibuat. Messaging
Service **tidak** menolong: batasannya pada isi pesan, bukan pada pengirim.

### Pemetaan error Verify

| Keadaan | Dari provider | Jadi |
|---|---|---|
| Kode benar | `status: approved` | lanjut ke `BIOMETRIC` |
| Kode salah | `status: pending` | `422 OTP_INVALID` — **bukan** error |
| Tidak ada verifikasi aktif | `404` | `ErrVerifyExpired` → `422 OTP_EXPIRED` |
| Provider membatasi | `429`, `60202`, `60203` | `ErrVerifyRateLimited` |

Verify **tidak** di-retry, berbeda dari Messages: ia punya penghitung kirim per
nomor sendiri, dan retry buta menghabiskan jatah nasabah dua kali untuk satu
permintaan.

### Channel: `sms` dan `call`

`StartVerification(ctx, phone, ch)` menerima `sms.ChannelSMS` atau
`sms.ChannelCall`; kosong berarti SMS. Channel di luar `SMS_VERIFY_CHANNELS`
dijawab `ErrChannelNotAllowed` **tanpa satu pun panggilan HTTP** — setiap channel
berbiaya dan punya checkbox Geo Permissions sendiri, jadi "channel apa yang boleh
dipakai deployment ini" adalah keputusan deployment, bukan parameter request.

Di API, field `channel` opsional pada `personal-data` dan `resend-otp`, dipetakan
ke `400 OTP_CHANNEL_NOT_ALLOWED`. Detailnya di skill `buka-rekening-otp` §2b-ter.

`CheckVerification` **tidak** menerima channel: kode dari SMS maupun telepon
diperiksa di endpoint yang sama.

**Jangan pakai Calls API (`/Calls.json`) untuk OTP.** Template
`voice_speech_recognition` memakai `<Gather input="speech">` untuk *mendengarkan*
ucapan penerima — itu bukan mekanisme pengiriman. OTP lewat suara dikirim dengan
Verify `Channel=call`, dan Twilio yang membacakan kodenya.

---

## 6. Perilaku error dan retry

- **Retry hanya dua percobaan**, dan hanya untuk `5xx` dan `429` — keduanya
  tidak pernah sampai ke operator. Jeda 500 ms, **kecuali** kalau Twilio
  mengirim `Retry-After`: nilainya dipakai selama ≤ 2 detik, dan kalau lebih
  panjang percobaan kedua dibatalkan. Mengabaikan `Retry-After` membuat akun
  yang sedang dibatasi semakin dibatasi; menungguinya sampai habis menahan
  handler yang nasabahnya sedang melihat spinner. Dua-duanya salah, jadi batasnya
  tegas: patuhi selama masih muat anggaran, menyerah kalau tidak.
- **Setiap `4xx` lain tidak di-retry.** Itu penolakan atas pesan ini: nomor
  salah, nomor trial belum diverifikasi, region ditutup. Mengirim ulang hanya
  memakan sisa TTL 5 menit nasabah dan bisa menagih dua kali.
- **`201` dengan `status: "failed"` atau `"undelivered"` diperlakukan sebagai
  error.** Twilio bisa menerima request lalu menolak pesannya; menganggap itu
  sukses persis sama dengan "OTP tidak sampai tapi log hijau".
- Kegagalan dibalas `503 OTP_DELIVERY_FAILED`. **Kodenya tetap terbit dan tetap
  sah** — hanya response-nya yang jujur, supaya client menawarkan kirim ulang
  alih-alih memasang hitung mundur untuk SMS yang tidak pernah berangkat.
- Kode OTP **tidak pernah** masuk log, error, atau metrik. Yang boleh dicatat:
  `phone_suffix` (4 digit terakhir), `operator`, `message_sid`, `message_status`.
- **Teks error dari Twilio dibersihkan sebelum di-log.** Twilio mengutip nomor
  tujuan di dalam pesannya sendiri — `21211` dan `21408` dua-duanya begitu, dan
  `21408` justru yang pertama ditemui setiap deployment baru (itu checkbox Geo
  Permissions). Pesannya tetap ikut karena itu yang membuat sebuah `400` bisa
  didiagnosis, tapi deretan ≥ 7 digit diganti `[number redacted]`. Kode error
  Twilio (4–5 digit) sengaja lolos: itu yang dicari di dokumentasi.

Tabel kode Twilio yang paling sering muncul ada di
`.claude/skills/twilio-sms-otp/references/troubleshooting.md`.

---

## 7. Mengganti atau menambah provider

Seam-nya sudah ada. Untuk aggregator lokal (atau A2P langsung ke operator kalau
kontraknya sudah ada), yang berubah hanya:

1. Satu file baru `internal/pkg/sms/<provider>.go` — **minta persetujuan dulu**.
2. Satu `case` baru di `NewProvider` (`sms.go`).
3. Field tambahan di `ProviderConfig` + `config.SMS` **bila** provider itu butuh
   parameter yang belum ada. Pakai ulang `SMS_ACCOUNT_SID`/`SMS_AUTH_TOKEN`
   kalau bentuknya sama; variabel kembar untuk hal yang sama adalah dua sumber
   kebenaran yang bisa berselisih.
4. Test `httptest` sendiri.
5. `.env.example` + tabel env di `README.md`.

Yang **tidak** berubah: antarmuka `Gateway`, tiga `SMSGateway` di domain,
`NormalizePhone`, teks `OTPMessage`, dan seluruh kebijakan OTP.

Catatan yang sering disalahpahami: **Telkomsel dan Indosat adalah operator
pemilik nomor, bukan API**. Twilio (atau aggregator mana pun) yang menyerahkan
pesan ke operator tujuan sesuai prefiks nomor. Adaptor "provider Indosat" baru
masuk akal kalau sudah ada kontrak A2P dan dokumen API dari mereka.

---

## 8. Yang harus diperbarui setiap provider/kredensial berubah

- [ ] `.env` (lokal, tidak di-commit) dan `.env.example` (tanpa nilai rahasia).
- [ ] Tabel variabel environment di `README.md`.
- [ ] Tabel *Environment-gated behaviour* di `README.md` bila perilakunya bergeser.
- [ ] `docs/01-API-SPECIFICATION.md` bila ada error code baru.
- [ ] `docs/06-BUKA-REKENING-API-SPEC.md` bila perilaku `personal-data`/`resend-otp` bergeser.
- [ ] Skill `buka-rekening-otp` §6 — di situlah pembaca OTP mencarinya lebih dulu.
- [ ] Secret manager / variabel environment di deployment, bukan hanya di laptop.

---

## 9. Jebakan

- **Geo Permissions Indonesia belum dibuka** (§3b). Gejalanya identik dengan
  "provider mati". Periksa ini sebelum menyalahkan kode. Untuk `Channel=call`
  yang harus dibuka adalah **Voice** → Geo Permissions, bukan Messaging.
- **`SMS_BASE_URL` diisi saat memakai `twilio_verify`.** Tidak berpengaruh lagi
  sejak Verify memakai `SMS_VERIFY_BASE_URL`, tapi kalau menemui `OTP_EXPIRED`
  massal di kode lama, ini penyebabnya.
- **Kode dibacakan dalam bahasa Inggris pada `call`.** Bukan bug dan bukan
  `SMS_VERIFY_LOCALE` yang salah: template suara Verify tidak mencakup `id`.
- **Akun trial, nomor tester belum diverifikasi** (§3d) — `21608`.
- **Sender alfanumerik untuk Indonesia** tidak dilayani (§3c).
- **`APP_ENV=development` + provider terisi = SMS sungguhan terkirim.** Pulsa
  betulan terpakai saat menjalankan test manual. Kosongkan `SMS_PROVIDER` kalau
  sedang tidak menguji pengiriman.
- **`otp_debug` tetap hanya di `development`.** Kalau tester butuh kodenya di
  staging, jawabannya SMS yang benar-benar sampai, bukan membuka field itu.
- **Pesan >160 karakter jadi dua segmen**: dua kali tagihan dan bisa sampai
  tidak berurutan. `OTPMessage` sengaja di bawah batas itu — ukur ulang kalau
  teksnya diubah.
- **Carrier filtering** (`30007`). Operator Indonesia menyaring SMS massal yang
  mirip spam. Pesan OTP yang mengandung tautan jauh lebih sering disaring —
  jangan tambahkan URL ke `OTPMessage`.
- **Nomor terenkripsi**. `AES_KEY` yang berubah membuat `decryptField` gagal;
  sejak perbaikan terakhir itu dibalas `OTP_DELIVERY_FAILED`, bukan diam-diam
  mengirim ciphertext ke Twilio. Kalau error ini muncul massal, periksa kunci.

---

## 10. Daftar uji

Tanpa jaringan:

- [ ] `go test ./internal/pkg/sms/` hijau.
- [ ] `SMS_PROVIDER=twilio` dengan `SMS_ACCOUNT_SID` salah → `make run` **gagal boot** dengan pesan yang menyebut variabelnya. Uji ketiga bentuknya: `SK…`, `AC` + tempelan terpotong (`ACxxx`), dan 34 karakter yang mengandung non-hex. Dulu hanya yang pertama tertangkap; dua sisanya boot mulus lalu `404` di setiap kiriman.
- [ ] `SMS_PROVIDER=indosat` (belum ada) → gagal boot, bukan jalan tanpa SMS.
- [ ] `APP_ENV=production` tanpa `SMS_PROVIDER` → `Config.Validate()` menolak.

Dengan kredensial sungguhan:

- [ ] `POST /v1/onboarding/personal-data` dengan nomor tester → SMS sampai, isinya satu segmen, kodenya cocok dengan `otp_debug`.
- [ ] Log pengiriman memuat `operator` yang benar dan **tidak** memuat kode OTP.
- [ ] Matikan Geo Permissions Indonesia sebentar → `21408`, dan baris log-nya memuat penjelasan Twilio tapi **tidak** memuat nomor tujuan.
- [ ] `POST /v1/onboarding/resend-otp` → kode lama ditolak, kode baru sampai.
- [ ] Nomor telepon rumah (`021…`) → `OTP_DELIVERY_FAILED`, **nol** panggilan ke Twilio (cek log console Twilio).
- [ ] Auth token sengaja disalahkan saat runtime → `503`, bukan `200`, dan kodenya tetap tersimpan.
- [ ] Grep seluruh log satu kali lagi khusus mencari kebocoran kode OTP.
