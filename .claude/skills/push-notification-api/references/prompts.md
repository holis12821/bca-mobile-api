# Prompt Implementasi — Transport FCM Sebenarnya

Dipakai berurutan. Setiap fase berhenti di titik yang bisa diuji sebelum lanjut.
Kontrak yang dirujuk: `SKILL.md` skill ini dan `docs/01-API-SPECIFICATION.md` §3, §7.

ATURAN #1 `CLAUDE.md` berlaku di setiap fase: sebut path file baru, tunggu jawaban.
Hanya Fase 1 yang butuh file baru; sisanya mengedit yang sudah ada.

---

## Fase 0 — Putuskan dulu, jangan menebak

Tiga pertanyaan yang jawabannya mengubah bentuk kodenya. Tanyakan ke pemilik
produk / infra sebelum menulis apa pun:

1. **Kredensial FCM masuk lewat apa?** Path ke service account JSON
   (`GOOGLE_APPLICATION_CREDENTIALS`) atau isi JSON-nya sebagai satu env var?
   Repo ini sudah punya preseden kunci berkas di `keys/` — tapi kredensial
   produksi tidak boleh ikut commit, dan `.env.example` hanya boleh memuat
   nilai contoh.
2. **Apakah notifikasi `SECURITY` menembus `push_notification_enabled`?**
   Ganti kode akses dan deteksi sesi mencurigakan adalah pesan yang justru
   dibutuhkan nasabah yang mematikan notifikasi. Ini keputusan produk.
3. **Push dikirim sinkron di dalam request, atau lewat worker?** Sekarang
   sinkron di `Notifier.Write`. FCM menambah latensi jaringan ke jalur transfer
   yang sudah commit; `postCtx` melindunginya dari koneksi klien yang putus,
   tapi tidak dari FCM yang lambat.

Catat jawaban 2 dan 3 ke `SKILL.md` §3.2 dan §3.1 sebelum lanjut.

---

## Fase 1 — `FCMPusher` di `internal/pkg/push`

File baru: `internal/pkg/push/fcm.go` (paket yang sama — `push.Pusher` sudah
tinggal di situ, dan implementasi ketiga bukan konteks baru).

Yang harus terjadi:

- `NewFCMPusher(tokens TokenStore, cfg Config) (*FCMPusher, error)` — gagal
  **di boot** kalau kredensial tidak sah, bukan pada push pertama.
- `Push` memakai `TokenStore.ListPushTokens` yang sudah ada. Nol token = bukan
  error; `return nil` tanpa memanggil FCM.
- Kirim ke FCM HTTP v1 per token, `data` diteruskan apa adanya (`type`,
  `deep_link` sudah disiapkan `Notifier`).
- Satu token gagal tidak membatalkan token lain. Kumpulkan hasilnya, jangan
  `return` pada kegagalan pertama.
- Timeout per panggilan, dari config, bukan angka telanjang di kode.

Selesai bila: `go build ./...` lolos dan `FCMPusher` memenuhi `notify.Pusher`
tanpa satu pun perubahan di `internal/pkg/notify`. Kalau `notify` harus diubah,
antarmukanya salah.

**Jangan** mengubah `NoopPusher` agar mengirim sesuatu. Ia ada supaya proses yang
tidak dikonfigurasi mengatakannya terang-terangan.

---

## Fase 2 — Config dan pemilihan di router

Edit `internal/config/config.go`, `.env.example`, dan
`internal/router/router.go:97`.

- Tambah blok config push sesuai keputusan Fase 0 (kredensial, `project_id`,
  timeout). Ikuti pola blok config yang sudah ada.
- `.env.example` memuat **nama** variabelnya dengan nilai contoh yang jelas
  palsu. Tidak ada kredensial sungguhan di file yang ter-commit.
- Urutan pemilihan di router, satu-satunya tempat: kredensial ada → `FCMPusher`;
  tidak ada dan `devMode` → `LoggingPusher`; tidak ada dan bukan dev →
  `NoopPusher` **plus satu `slog.Warn` saat boot** bahwa produksi berjalan tanpa
  pengiriman push. Diam di sini adalah kegagalan yang tidak terlihat.
- Kalau kredensial ada tapi tidak sah: server **gagal boot**. Jangan diam-diam
  turun ke `NoopPusher` — itu tepat kasus yang §3.1 ingin dihindari.

Selesai bila: tanpa env baru, perilaku sama persis seperti sekarang (dev
log, prod noop + warning); dengan env, `FCMPusher` terpilih.

---

## Fase 3 — Bersihkan token mati

Edit `internal/pkg/push/fcm.go` dan `internal/repository/postgres/device_repo.go`.

- `DeviceRepo` dapat `ClearPushToken(ctx, token string) error` →
  `UPDATE devices SET push_token = NULL, updated_at = NOW() WHERE push_token = $1`.
  Di-scope ke nilai tokennya, bukan ke `user_id`: token yang sama bisa berpindah
  perangkat, dan yang FCM tolak adalah tokennya.
- Perluas `TokenStore` — atau antarmuka kedua di sebelahnya — agar `FCMPusher`
  bisa memanggilnya. Handler tidak boleh mengimpor paket postgres.
- Hanya `UNREGISTERED` dan `INVALID_ARGUMENT` yang menghapus. Error jaringan,
  `UNAVAILABLE`, dan kuota **tidak** — menghapus token karena FCM sedang
  bermasalah berarti nasabah berhenti menerima push sampai aplikasi dibuka lagi.

Selesai bila: push ke token karangan menghapusnya dari baris `devices`, dan FCM
yang mati total tidak menghapus apa pun.

---

## Fase 4 — Hormati sakelar nasabah

Edit `internal/repository/postgres/device_repo.go` sesuai keputusan Fase 0 butir 2.

`ListPushTokens` mendapat `JOIN users u ON u.id = d.user_id` dengan
`u.push_notification_enabled` pada `WHERE`. Redamannya di lapisan token, bukan di
`Notifier`: baris in-app tetap ditulis apa pun sakelarnya.

Kalau `SECURITY` dikecualikan (jawaban Fase 0), `Push` butuh tipe notifikasinya.
Ia sudah ada di `data["type"]` yang dikirim `Notifier` — pakai itu, jangan
menambah parameter ke antarmuka `Pusher`.

Selesai bila: nasabah dengan sakelar mati tidak menerima push tapi tetap melihat
notifikasinya di `GET /v1/notifications`.

---

## Fase 5 — Test dan dokumen

- `internal/pkg/push/fcm_test.go` (file baru — konfirmasi dulu): klien FCM
  ditiru; uji nol token, satu token sukses, satu dari dua token `UNREGISTERED`
  (yang gagal terhapus, yang sukses tidak), dan FCM `UNAVAILABLE` (tidak ada
  yang terhapus).
- `internal/handler/device_handler_test.go` (file baru — konfirmasi dulu): 200,
  `400` token kosong dan > 512 karakter, `403` perangkat dicabut.
- `docs/01-API-SPECIFICATION.md` §3 push-token: sebut bahwa pengiriman kini
  nyata, dan bahwa token dibersihkan ketika FCM menolaknya.
- `docs/07-SETUP-RUNBOOK.md`: cara menyiapkan kredensial FCM lokal.
- `SKILL.md` §3.1 dan §3.2 diperbarui — celahnya tertutup, catatannya jangan
  ditinggalkan sebagai "belum ada" yang keliru.
- `CLAUDE.md` repo ini kalau ada env atau dependency baru.

Selesai bila: `make test` dan `go build ./...` lolos, dan tidak ada satu pun
kalimat di dokumen yang masih menyebut push notification sebagai placeholder.
