---
name: push-notification-api
description: Notifikasi push dan notifikasi dalam aplikasi di backend BCA Mobile API — POST /v1/account/device/push-token, paket internal/pkg/notify sebagai satu-satunya penulis baris notifications, paket internal/pkg/push (FCMPusher untuk FCM HTTP v1, LoggingPusher, NoopPusher), filter type pada GET /v1/notifications, dan invalidasi cache cachever:notif. Gunakan saat mendaftarkan atau mencabut FCM token perangkat, menambah pemicu notifikasi baru dari sebuah flow, menyambungkan provider FCM sebenarnya, memperbaiki badge unread, atau menelusuri notifikasi yang tidak sampai ke perangkat. Trigger juga pada "push token", "push-token", "FCM", "Firebase", "NoopPusher", "LoggingPusher", "notify.Notifier", "devices.push_token", "AUTH_DEVICE_NOT_RECOGNIZED", "unread_count", "badge notifikasi kosong", "notifikasi tidak muncul", dan "push_notification_enabled". JANGAN dipakai untuk endpoint saldo, mutasi, transfer, ledger, atau desain Redis umum (itu `bca-mobile-backend`), kartu nasabah dan PUT /account/settings (itu `profil-saya-kartu-api`), OTP onboarding (itu `buka-rekening-otp`), atau integrasi sisi Android (itu skill di repo `bca_mobile`).
---

# Push Notification & Notifikasi Dalam Aplikasi

Dua hal yang sering dikira satu: **baris notifikasi di database** (dibaca
`GET /v1/notifications`, sumber badge unread) dan **pengiriman ke perangkat**
(FCM). Keduanya sudah jalan penuh, tapi tetap terpisah — dan itu inti skill ini:
nasabah tetap melihat pesan lewat polling aplikasi meski tidak ada satu pun push
terkirim, entah karena kredensial belum dipasang atau karena FCM sedang mati.

**Baca `CLAUDE.md` repo ini lebih dulu.** ATURAN #1 berlaku penuh: setiap file
baru — migrasi, paket, handler, test — disebutkan path-nya dan ditunggu
persetujuannya sebelum dibuat.

---

## 0. Yang sudah ada, jangan dibuat ulang

| Sudah ada | Melayani |
|---|---|
| `devices.push_token TEXT` (migrasi `000001`) | Penyimpanan token. **Tidak butuh migrasi baru.** |
| `notifications` + 2 indeks (migrasi `000005`) | Baris notifikasi, `CHECK` 5 tipe |
| `internal/pkg/notify` | Satu-satunya penulis baris `notifications` |
| `internal/pkg/push` | `Pusher` + `FCMPusher` (FCM HTTP v1), `LoggingPusher`, `NoopPusher` |
| `config.Push` (`FCM_CREDENTIALS_FILE`, `FCM_TIMEOUT`) | Kredensial + timeout, dipilih di `router.New` |
| `DeviceRepo.ClearPushToken` | Menghapus token yang ditolak FCM secara permanen |
| `internal/handler/device_handler.go` | `POST /account/device/push-token` |
| `internal/handler/notification_handler.go` | List, read, read-all, parser filter `type` |
| `internal/repository/postgres/device_repo.go` | `RegisterPushToken`, `ListPushTokens` |
| `internal/repository/postgres/notification_repo.go` | `Insert` (implements `notify.Store`), list keyset, unread, mark read |
| `internal/repository/redis/notification_cache.go` | `cache:notif:{user}:v{n}:{cursor_hash}`, TTL `account.NotificationCacheTTL` |
| `users.push_notification_enabled` (migrasi `000018`) | Sakelar di `PUT /account/settings` — dibaca di `ListPushTokens`, `SECURITY` menembusnya (§3.2) |

Sebelum menambah apa pun: baris notifikasi baru **hampir selalu** cukup dengan
memanggil `notify.Notifier` dari service yang sudah memegangnya. Itu bukan file baru.

---

## 1. Kontrak endpoint

### 1.1 `POST /v1/account/device/push-token`

Di dalam `r.Group` ber-`middleware.Auth` (`internal/router/router.go:691`).
Envelope, `X-Device-ID`, dan `X-Request-ID` mengikuti aturan yang sudah berlaku.

```json
// Request
{ "push_token": "fcm_token_dari_firebase" }

// Response 200
{ "status": "success", "data": { "message": "Token notifikasi berhasil didaftarkan." } }
```

| Kondisi | Jawaban |
|---|---|
| `push_token` kosong atau > 512 karakter | `400 VALIDATION_ERROR` |
| Body bukan JSON | `400 VALIDATION_ERROR` |
| Klaim `sub` atau `did` tidak terbaca | `401 AUTH_TOKEN_INVALID` |
| Tidak ada `devices` aktif untuk (`device_id` dari `did`, `user_id`) | `403 AUTH_DEVICE_NOT_RECOGNIZED` |

**`device_id` diambil dari klaim `did`, bukan dari body.** Klien tidak boleh
menempelkan token ke perangkat yang bukan sedang dipakainya. `UPDATE` di
`DeviceRepo.RegisterPushToken` juga di-scope ke `user_id` **dan**
`revoked_at IS NULL`, jadi token yang menyebut perangkat orang lain tidak
menimpa apa pun — `RowsAffected() == 0` → `apperr.DeviceNotRecognized`.
Jangan melonggarkan salah satu predikat itu agar "lebih mudah dites".

Endpoint ini **idempoten**: memanggilnya ulang dengan token sama hanya menulis
nilai yang sama. Aplikasi memanggilnya setiap kali FCM merotasi token, jadi
jangan menambahkan `X-Idempotency-Key` atau penolakan duplikat.

### 1.2 `GET /v1/notifications` — filter `type`

Menerima satu atau beberapa nilai dipisah koma dari daftar tertutup
`account.NotificationTypes` (`internal/domain/account/entity.go:251`), sama
dengan `CHECK` constraint tabel. Tanpa `type` = semua jenis.

- Nilai tak dikenal → `400 VALIDATION_ERROR` + `details.allowed_values`.
  **Bukan** filter yang diabaikan diam-diam — kekeliruan itu pernah membuat
  `period` menjawab 200 dengan seluruh riwayat rekening.
- Urutan tidak berpengaruh: `?type=PROMO,INFO` dan `?type=INFO,PROMO` satu
  permintaan yang sama, dan `cursorToHash` mengurutkan nilainya supaya keduanya
  berbagi satu entri cache.
- SQL memakai `type = ANY($n)`, bukan daftar `IN` yang dirakit manual.
  Nilainya datang dari query string; daftar rakitan adalah tempat injeksi masuk.
- Filter **wajib** masuk kunci cache. Tanpa itu halaman pertama tab PROMO
  tersaji sebagai halaman pertama daftar "Semua".

`PUT /notifications/{id}/read` dan `/read-all` menaikkan
`cachever:notif:{user_id}` **dan** `cachever:dashboard:{user_id}` — badge di
Beranda ikut basi kalau yang kedua dilupakan.

---

## 2. Cara menambah pemicu notifikasi

`notify.Notifier` sudah disuntikkan ke `transaction`, `ewallet`, `qris`, `auth`,
dan `account` (`ServiceConfig.Notifier`). Menambah pemicu = memanggilnya.

```go
// Transaksi: judul, badan, deep link, metadata JSONB
s.notifier.Transaction(postCtx, userID, "Transfer berhasil",
    fmt.Sprintf("Transfer %s ke %s berhasil. Ref: %s", formatIDR(total), destName, txn.ReferenceNumber),
    "bcamobile://transaction/"+txn.ID.String(),
    map[string]any{"transaction_id": txn.ID.String(), "reference_number": txn.ReferenceNumber})

// Keamanan: tanpa deep link, tanpa metadata
s.notifier.Security(ctx, userID, "Kode akses berhasil diubah", "…segera hubungi Halo BCA.")
```

Aturan yang tidak boleh dilanggar:

1. **Selalu `if s.notifier != nil`.** Test menyuntik `nil`.
2. **Best-effort, dan itu disengaja.** `Notifier.Write` menelan error setelah
   mencatatnya. Notifikasi yang gagal ditulis tidak boleh menggagalkan transfer
   yang sudah commit. Jangan mengembalikan error dari jalur ini.
3. **Panggil setelah commit, dengan `postCtx`.** Lihat
   `service_execute.go:317` — dipanggil setelah `releaseIdem = false`, memakai
   context yang tidak mati saat klien memutus koneksi.
4. **Tipe hanya dari konstanta `notify.Type*`.** Tipe di luar lima nilai itu
   ditolak `CHECK` constraint, dan errornya hanya muncul di log.
5. **PII tidak masuk `title`/`body`/`metadata`.** Nomor rekening dan nomor HP
   dimasked lebih dulu (pola `maskedPhone` di `ewallet/service.go:370`).
   Badan notifikasi berakhir di notification tray perangkat, di layar terkunci.
6. Deep link memakai skema `bcamobile://` yang sudah dipakai
   (`bcamobile://transaction/{id}`). Skema baru berarti kesepakatan dengan
   client lebih dulu, bukan diputuskan di backend.

Pemicu yang sudah terpasang: transfer (`transaction/service_execute.go:317`),
top-up e-wallet (`ewallet/service.go:370`), QRIS (`qris/service.go:246`),
ganti PIN dan ganti kode akses (`auth/service.go:598`, `:733`), deteksi reuse
refresh token (`auth/session_service.go:258`), ubah limit
(`account/service.go:337`), ubah email (`account/service.go:471`).

---

## 3. Keadaan sebenarnya

Butir 3.1, 3.2, dan pembersihan token di 3.4 sudah selesai. Yang masih terbuka
tinggal 3.3.

### 3.1 Transport FCM — SELESAI

`internal/pkg/push/fcm.go` mengirim ke **FCM HTTP v1**. Access token ditebus dari
service account (assertion RS256 → `oauth2.googleapis.com/token`) dengan stdlib
saja; tidak ada dependency baru.

Pemilihannya di satu tempat, `router.New`:

| `FCM_CREDENTIALS_FILE` | `APP_ENV` | Pusher |
|---|---|---|
| kosong | `development` | `LoggingPusher` |
| kosong | selain itu | `NoopPusher` + **`slog.Warn` saat boot** |
| terisi | apa pun | `FCMPusher` |

**Terisi tapi tidak sah → `router.New` mengembalikan error dan server gagal
boot.** Itulah sebabnya `New` sekarang `(http.Handler, error)`. Turun diam-diam ke
`NoopPusher` adalah tepat kegagalan yang bagian ini dibuat untuk menghilangkan.

`project_id` dibaca dari dalam berkas kredensial — **jangan** menambah
`FCM_PROJECT_ID`: variabel kedua bisa tidak cocok, dan gejalanya "pesan diterima
FCM, tidak ada yang sampai".

Yang tetap berlaku: `NoopPusher` dan `LoggingPusher` **tidak** mengirim apa pun.
Jangan mengubahnya. Keduanya ada supaya proses yang tidak dikonfigurasi
mengatakannya terang-terangan.

Pengiriman **sinkron** di dalam `Notifier.Write`, dengan timeout per panggilan
dari `FCM_TIMEOUT` (default 10s) — keputusan yang diambil sadar: push jalan
setelah uang commit, jadi FCM yang lambat dipotong, bukan dibiarkan menahan
handler. Kalau latensinya nanti jadi masalah, pindahkan ke worker; antarmuka
`Pusher` tidak perlu berubah untuk itu.

### 3.2 Sakelar `push_notification_enabled` — SELESAI

Dibaca di `DeviceRepo.ListPushTokens` lewat `JOIN users`, bukan di `Notifier`:
baris in-app harus tetap ditulis apa pun sakelarnya, karena itu yang dibaca layar
Notifikasi dan badge unread. Yang diredam hanya pengiriman ke perangkat.

**Notifikasi `SECURITY` menembus sakelar** — keputusan produk, diambil bersama
pemilik produk. "Kode akses Anda diubah" justru pesan yang paling dibutuhkan
nasabah yang mematikan notifikasi. Tipenya dibaca dari `data["type"]` yang sudah
diisi `Notifier`, jadi antarmuka `Pusher` tidak berubah; parameternya bernama
`overrideMuted` pada `ListPushTokens`.

### 3.3 `unread_count` dijanjikan spec tapi tidak dikirim

`docs/01-API-SPECIFICATION.md:1462` menampilkan `data.unread_count` pada
`GET /notifications`, sedangkan `account.NotificationListResponse`
(`entity.go:268`) hanya punya `notifications`. Angka unread sekarang hanya
datang dari Beranda (`unread_notifications`, plus alias lama
`unread_notification_count`).

`NotificationRepo.CountUnread` sudah ada. Menambahkan field ke response adalah
perubahan aditif yang aman, tapi ia menambah satu `COUNT(*)` per halaman dan
harus masuk entri cache yang sama. Pilihannya dua, keduanya sah: tambahkan
field itu, **atau** perbaiki spec agar cocok dengan implementasi. Yang tidak
boleh: membiarkannya berbeda tanpa catatan.

### 3.4 Token basi dan lubang test

- `device_handler_test.go` **sudah ada** (200, `400` token kosong/`>512`/bukan
  JSON, batas 512 yang inklusif, `403` perangkat dicabut, `401` tanpa token,
  dan bukti `device_id` diambil dari klaim `did` bukan dari body).
  `notification_handler_test.go` masih belum ada — yang paling layak dites di
  sana `parseNotificationTypes` (nilai tak dikenal, duplikat, urutan berbeda,
  spasi).
- **Token mati sudah dibersihkan.** `FCMPusher` memanggil
  `DeviceRepo.ClearPushToken` hanya untuk `UNREGISTERED` dan `INVALID_ARGUMENT`.
  Gangguan sementara (`UNAVAILABLE`, `INTERNAL`, kuota, jaringan, dan masalah
  autentikasi kita sendiri) **tidak** menghapus apa pun — menghapus token karena
  FCM sedang bermasalah mengubah pemadaman lima menit jadi nasabah yang berhenti
  menerima push sampai aplikasinya dibuka lagi. Dijaga
  `internal/pkg/push/fcm_test.go`.
- `POST /account/device/push-token` tidak punya rate limit sendiri; ia hanya
  ikut `middleware.Auth`. Cukup untuk sekarang (butuh access token yang sah,
  dan tulisannya idempoten), tapi sebut ini kalau ada audit rate limit.

---

## 4. Verifikasi sebelum melapor selesai

```bash
make test                      # unit test
go build ./...                 # kompilasi
```

Manual, dengan `APP_ENV=development` supaya `LoggingPusher` aktif:

1. `POST /v1/account/device/push-token` token sah → 200, dan
   `SELECT push_token FROM devices WHERE device_id = …` terisi.
2. Ulangi dengan `X-Device-ID`/`did` perangkat yang sudah dicabut → `403
   AUTH_DEVICE_NOT_RECOGNIZED`, bukan 200 tanpa efek.
3. Jalankan satu transfer sampai `execute` → log `push (logging pusher)` muncul
   dengan `devices` > 0, dan `GET /v1/notifications` memuat barisnya.
4. `GET /v1/notifications?type=SECURITY` → hanya `SECURITY`;
   `?type=NGAWUR` → `400` dengan `details.allowed_values`.
5. `PUT /notifications/{id}/read` lalu `GET /notifications` → `is_read` berubah
   pada panggilan berikutnya (cache versioned, bukan TTL 60 detik).
6. Koleksi Postman `docs/postman/bca-mobile-api.postman_collection.json` sudah
   memuat keempat request ini — perbarui di commit yang sama kalau kontraknya
   berubah.

Perubahan kontrak **wajib** ikut memperbarui `docs/01-API-SPECIFICATION.md`
(§3 untuk push-token, §7 untuk notifications) di commit yang sama.
