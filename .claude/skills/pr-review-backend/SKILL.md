---
name: pr-review-backend
description: Rulebook review PR untuk bca-mobile-api — dipakai reviewer AI di CI (.github/workflows/claude-code-review.yml) dan bisa dijalankan lokal sebelum push. Memuat apa yang harus diperiksa pada setiap perubahan backend perbankan ini: logika yang salah, hang & kehabisan sumber daya (padanan ANR di sisi server — `ctx` yang tidak diteruskan, query tanpa `LIMIT`, N+1, pool koneksi habis, retry tanpa batas), konstanta & umur token/TTL yang menyimpang dari dokumen, kecocokan validasi aplikasi dengan batasan database (`DECIMAL(18,2)`, `VARCHAR(n)`, `CHECK`, `NOT NULL`, indeks unik), dan pemeriksaan Redis (fail-open vs fail-closed, TTL hilang, `redis.Nil`, pola `cachever:`, dua instance, PII di key). Juga memuat kontrak verdict ALLOW/BLOCK yang dipakai gerbang auto-merge. Gunakan saat me-review PR atau diff di repo ini, saat diminta "cek PR ini", "review kode ini", "apakah ini aman di-merge", "apa yang bisa bikin bug nanti", "cek validasi database", atau "verifikasi Redis". Trigger juga pada "review PR", "code review", "ANR", "hang", "request lambat", "pool habis", "N+1", "TTL", "cachever", "fail-open", "fail-closed", "auto-merge", dan "verdict". JANGAN dipakai untuk memilih bentuk branch atau isi pesan commit (itu `git-branch-workflow`), merancang endpoint baru (itu skill domain seperti `bca-mobile-backend`), atau konfigurasi runner CI.
---

# Rulebook Review PR — bca-mobile-api

Repo ini backend perbankan: Go 1.26, `chi`, `pgx/v5` (PostgreSQL 16), `go-redis/v9`,
melayani aplikasi Android. Yang lolos review di sini menyentuh uang dan identitas
nasabah sungguhan.

**Semua temuan ditulis dalam bahasa Indonesia.** Pembacanya reviewer dan pengembang
di repo ini, dan pesan error yang dikutip memang berbahasa Indonesia.

## Cara memakai

Satu temuan harus menyebut tiga hal, dan tanpa ketiganya jangan dilaporkan:

1. **Berkas dan baris** yang menyebabkannya.
2. **Skenario konkret**: masukan atau keadaan apa → akibat apa. "Bisa bermasalah"
   bukan temuan; "`amount` dengan tiga angka desimal → `INSERT` ditolak, nasabah
   dapat 500, bukan 422" adalah temuan.
3. **Apakah sudah ada di kode ini** atau hanya mungkin terjadi nanti. Keduanya boleh
   dilaporkan, tapi jangan dicampur jadi satu nada.

Jangan melaporkan selera: penamaan yang Anda lebih suka, komentar yang Anda akan tulis
berbeda, atau pembagian berkas yang berbeda. Repo ini punya konvensi; yang menyimpang
dari konvensinya layak dilaporkan, yang menyimpang dari selera Anda tidak.

Diff-nya yang di-review, bukan seluruh repo. Masalah lama yang tidak disentuh PR ini
bukan temuan PR ini — kecuali PR ini membuatnya jauh lebih mudah terjadi.

---

## §1 Hang & kehabisan sumber daya — padanan ANR di sisi server

Di Android, ANR berarti satu layar membeku. Di sini akibatnya lebih luas: satu
permintaan yang menggantung memegang satu koneksi database, dan koneksi yang habis
membuat **seluruh API** melambat, bukan satu endpoint. Jadi bagian ini yang paling
mahal kalau terlewat.

**`middleware.Timeout` tidak menghentikan apa pun.** `internal/middleware/timeout.go`
membungkus `chimiddleware.Timeout`, dipasang di `internal/router/router.go` dengan
`RequestTimeout`. Yang dilakukannya hanya **membatalkan `ctx`** — goroutine handler-nya
tetap berjalan sampai selesai. Jadi kode yang mengabaikan `ctx` tidak ikut berhenti; ia
hanya kehilangan pembacanya, sementara koneksi database dan memorinya masih dipegang.

Periksa pada setiap kode baru:

- **`ctx` permintaan diteruskan sampai ke query.** Setiap `pool.Query`, `pool.Exec`,
  dan panggilan Redis menerima `ctx` yang berasal dari `r.Context()`, bukan yang dibuat
  sendiri. `context.Background()` atau `context.TODO()` di `internal/domain/`,
  `internal/handler/`, atau `internal/repository/` adalah temuan — ia memutus
  pembatalan dan membuat permintaan yang sudah ditinggalkan klien tetap bekerja.
  Dua tempat **sudah** begitu dan sedang dibiarkan (`internal/domain/auth/service.go`
  dan `internal/repository/redis/nonce.go`); keduanya bukan izin menambah yang ketiga,
  dan PR yang menyalin polanya harus menyebutkan alasannya.
- **Pekerjaan latar yang sengaja hidup lebih lama dari permintaan** (audit, notifikasi)
  boleh punya `ctx` sendiri, tapi **wajib berbatas waktu** —
  `context.WithTimeout(..., n)` dengan `n` yang disebut, bukan tanpa deadline. Pola yang
  benar ada di `internal/domain/auth/audit.go`.
- **Tidak ada `SELECT` daftar tanpa `LIMIT`.** Endpoint yang mengembalikan banyak baris
  wajib memakai `internal/pkg/pagination`. Query yang hari ini mengembalikan 50 baris
  di database dev akan mengembalikan 500 ribu di produksi, dan yang pertama kali
  merasakannya adalah pool koneksi.
- **Tidak ada query di dalam loop (N+1).** Satu query per baris hasil berarti seratus
  perjalanan ke database untuk satu layar. Gabungkan dengan `JOIN`, `WHERE … = ANY($1)`,
  atau satu query agregat.
- **Tidak ada panggilan jaringan di dalam transaksi database.** HTTP ke penyedia OCR,
  Dukcapil, SMS, FCM, atau Redis **di dalam** `BEGIN…COMMIT` memegang baris yang
  terkunci selama penyedia itu lambat. Kumpulkan datanya lebih dulu, baru buka
  transaksinya.
- **Retry berbatas.** Jalur uang memakai `SERIALIZABLE` + retry pada `40001`. Loop
  retry wajib punya **jumlah percobaan maksimum** dan memeriksa `ctx.Err()` tiap
  putaran. Retry tanpa batas pada database yang sedang tertekan adalah cara memastikan
  tekanannya tidak pernah reda.
- **Transaksi selalu punya jalan keluar.** Setiap `Begin` punya `defer Rollback`, dan
  tidak ada `return` di antaranya yang melewatkannya.
- **Goroutine punya jalan keluar.** `internal/websocket/` memegang satu goroutine per
  koneksi; yang baru wajib berhenti saat `ctx` selesai atau kanalnya ditutup, dan
  tidak menulis ke kanal tanpa `select` berpenjaga — satu penulis yang menggantung
  menahan satu koneksi selamanya.
- **Kunci diambil dengan urutan tetap.** Jalur uang mengunci rekening **urut UUID**.
  Dua transfer yang mengunci dua rekening dengan urutan berbeda saling menunggu; itu
  deadlock, dan di jalur uang ia muncul sebagai transfer yang menggantung.

---

## §2 Konstanta, umur token & TTL

Yang dicari di sini: angka yang **ada di dua tempat** dan baru satu yang diperbarui.
Bug seperti ini tidak pernah muncul di test; ia muncul berminggu-minggu kemudian
sebagai "sesi kok kadang habis lebih cepat".

- **Setiap TTL Redis cocok dengan `docs/03-REDIS-STRATEGY.md`.** Dokumen itu menyebut
  TTL per key. TTL yang diubah di kode tanpa mengubah dokumen (atau sebaliknya) adalah
  temuan, bukan kelalaian kecil.
- **Umur token saling masuk akal.** Access token tidak boleh hidup lebih lama dari
  sesinya di Redis — kalau lebih lama, token yang masih sah menunjuk sesi yang sudah
  hilang, dan `middleware.Auth` akan menolaknya dengan kode yang membingungkan.
  Refresh token lebih lama dari access token. Nonce dan `verification_token` lebih
  pendek dari keduanya. Token signaling **sekali pakai** dan tidak diperpanjang.
- **Angka ajaib jadi `const` bernama**, di paket yang memilikinya. `3`, `5*time.Minute`,
  atau `0.95` yang ditulis langsung di tengah logika tidak bisa dicari saat harus
  diubah.
- **Batas percobaan tidak ditulis ulang di handler.** `MaxFailedLogins`,
  `MaxUnlockAttempts`, dan kawan-kawannya hidup di `internal/domain/`. Handler yang
  menuliskan angkanya sendiri akan menyimpang dari service pada perubahan berikutnya,
  dan yang menyimpang adalah batas keamanan.
- **Versi cache yang hilang berarti 0, dan itu punya akibat.**
  `internal/repository/redis/version_counter.go` mengembalikan versi `0` saat key-nya
  tidak ada — pilihan yang benar untuk cache dingin. Tapi artinya: setelah Redis
  di-restart, **semua** pengguna kembali membaca key versi `0`. Kode baru yang menulis
  ke key berversi harus tetap benar dalam keadaan itu, dan tidak boleh menganggap versi
  naik sebagai satu-satunya cara datanya berubah.
- **Konstanta yang juga ada di `docs/` atau Postman** diperbarui bersama di PR yang
  sama. Ini termasuk kode galat, batas rate limit, dan nilai enum.
- **`APP_ENV` tetap jadi gerbang.** Integrasi eksternal (OCR, Dukcapil, biometrik,
  object storage, core banking, SMS) hanya punya mock: `development` → mock, selain itu
  → `503 PROVIDER_NOT_CONFIGURED`. Mock tanpa gerbang environment adalah temuan berat.
  Push notification satu-satunya pengecualian dan punya aturannya sendiri. `otp_debug`
  di response **hanya** saat `APP_ENV=development`.

---

## §3 Validasi database vs data yang masuk

Pertanyaannya satu: **apakah setiap nilai yang bisa lolos validasi aplikasi pasti
diterima database, dan sebaliknya?** Selisih di antara keduanya selalu muncul sebagai
`500` di tempat yang seharusnya `422` — atau lebih buruk, sebagai data yang diterima
padahal tidak sah.

- **Uang: `DECIMAL(18,2)` ↔ `decimal.Decimal`.** Jangan pernah `float64` untuk uang.
  Dan karena skalanya 2, nilai dengan tiga angka desimal harus **ditolak di aplikasi**
  dengan `422`; membiarkannya sampai ke `INSERT` menghasilkan pembulatan diam-diam atau
  galat driver. Periksa juga `18` digit total pada perkalian/akumulasi.
- **`VARCHAR(n)` ↔ validasi panjang.** Setiap kolom teks berbatas punya pemeriksaan
  panjang di handler atau service. Tanpa itu, masukan 300 karakter menjadi galat driver
  `value too long`, yang keluar sebagai `500`.
- **`CHECK (… IN (…))` ↔ konstanta Go.** Setiap daftar nilai di `CHECK` punya pasangan
  `const` di `internal/domain/`, dan **keduanya harus persis sama**. Menambah satu
  nilai status, alasan, atau jenis peristiwa **wajib** disertai migrasi yang memperluas
  `CHECK`-nya — kalau tidak, barisnya ditolak saat dijalankan, bukan saat dikompilasi.
  Contoh hidup: `cs_audit_events_type_valid` dan `cs_terminals_status_valid`.
- **`NOT NULL` ↔ field wajib.** Kolom `NOT NULL` tanpa `DEFAULT` harus selalu diisi
  kode; field Go bertipe pointer yang menulis ke kolom seperti itu adalah temuan.
  Sebaliknya, kolom nullable yang dibaca ke tipe non-pointer akan gagal men-scan
  `NULL`.
- **Dua kolom yang saling mengikat punya `CHECK`,** bukan hanya disiplin kode. Pola yang
  dipakai repo ini: `(desk_pin_hash IS NULL) = (desk_pin_set_at IS NULL)`. Nilai dan
  stempelnya ditulis bersama; `CHECK`-nya yang menjamin tidak ada separuh.
- **Indeks unik ↔ cara menulis.** `INSERT` ke tabel dengan indeks unik (termasuk
  **parsial**) harus menangani tabrakan: `ON CONFLICT`, atau galat yang diterjemahkan
  jadi kode `apperr` yang pantas — bukan `500`. Dan predikat indeks parsial harus cocok
  dengan `WHERE` query yang mengandalkannya, atau indeksnya tidak terpakai.
- **Setiap kolom yang ditulis kode ada di `migrations/`.** Pernah ada dua endpoint yang
  `500` permanen karena menulis kolom yang tidak pernah dibuat. Verifikasinya dengan
  menjalankan migrasi, bukan membaca sekilas.
- **Migrasi berpasangan dan `down`-nya benar.** `.up.sql` + `.down.sql`, nomor berurutan
  tanpa menabrak nomor orang lain, dan `down` yang sudah diuji `down 1` lalu `up` lagi.
  `CHECK` yang dipersempit di `down` harus didahului pemindahan baris yang melanggarnya.
- **Waktu: `TIMESTAMPTZ`, dan tanggal WIB dihitung di aplikasi.** Jangan `CURRENT_DATE`
  untuk batas harian — batasnya berganti tengah malam Jakarta, bukan tengah malam UTC.
- **Kepemilikan diperiksa di query, bukan setelahnya.** `source_account_id` dan
  `account_id` datang dari request. Gunakan `accounts.FindOwnedByID(ctx, userID,
  accountID)` sebelum menyentuh saldo; `WHERE id = $1` tanpa `user_id` pada jalur uang
  adalah temuan paling berat di repo ini — ia pernah membolehkan satu user mendebet
  rekening user lain.

---

## §4 Verifikasi Redis

Redis di repo ini **dua instance**: sesi dan cache (`make infra-up` menjalankan
keduanya). Keduanya tidak boleh tertukar — data sesi di instance cache akan hilang saat
cache dibersihkan, dan itu terbaca sebagai logout acak.

- **Fail-open vs fail-closed disebut sadar.** `middleware.RateLimit(…, failClosed bool)`:
  endpoint auth/PIN **wajib** `true` (Redis mati → tolak), endpoint baca-saja `false`
  (Redis mati → izinkan). Endpoint autentikasi baru yang lewat `false` membuat rate
  limit dan lockout **hilang diam-diam** begitu Redis bermasalah — itu temuan berat,
  dan bentuknya tidak kelihatan dari nama fungsinya.
- **Setiap `Set` punya TTL.** Key tanpa TTL tidak pernah hilang; ribuan di antaranya
  adalah kebocoran memori yang baru terasa berbulan-bulan kemudian. Satu-satunya
  pengecualian adalah key yang memang dikelola seumur hidup proses, dan itu harus
  ditulis alasannya.
- **`redis.Nil` bukan galat.** `err == goredis.Nil` berarti **tidak ada** (cache miss)
  dan harus dibedakan dari galat sungguhan. Menyamakan keduanya membuat Redis yang
  bermasalah terbaca sebagai data kosong — dan data kosong di jalur saldo atau limit
  adalah jawaban yang salah, bukan jawaban yang lambat.
- **Galat cache tidak boleh menggagalkan permintaan** pada jalur baca, dan **tidak boleh
  diabaikan** pada jalur tulis/invalidasi. Cache yang gagal diinvalidasi harus terlihat
  di log dengan jelas; kalau tidak, nasabah melihat saldo lama dan tidak ada yang tahu.
- **Penamaan key ikut `docs/03-REDIS-STRATEGY.md` §Konvensi Penamaan Key** — `cache:`,
  `cachever:`, `session:`, dan seterusnya. Key baru didaftarkan di dokumen itu di PR
  yang sama, termasuk TTL-nya.
- **Invalidasi lewat `INCR cachever:…`, bukan `SCAN` atau `DEL` massal.** Pola repo ini
  menaruh versi di dalam key (`cache:balance:{user}:v{n}`), jadi invalidasi cukup
  menaikkan penghitungnya. `SCAN` di jalur permintaan memblokir Redis; `FLUSHDB` tidak
  pernah boleh ada di kode aplikasi.
- **Invalidasi SEMUA pihak terdampak.** Transfer menyentuh pengirim **dan** penerima;
  menaikkan versi hanya untuk pengirim membuat penerima melihat saldo lama. Lihat
  `transaction_cache.go` yang menaikkan `cachever:mutations:`, `cachever:history:`, dan
  `cachever:notif:` bersama-sama.
- **Cek kepemilikan dulu, baca cache kemudian.** Cache struk di-key per `transaction_id`
  saja; membacanya sebelum memeriksa pemiliknya membocorkan struk orang lain. Ini pernah
  terjadi.
- **Operasi multi-key yang harus utuh pakai pipeline atau Lua.** Beberapa `INCR` yang
  dikirim satu-satu bisa berhenti di tengah dan meninggalkan cache separuh terinvalidasi.
- **Tidak ada PII di dalam key.** Tanpa NIK, nomor rekening penuh, nomor kartu, nomor
  HP, atau nama di nama key — key terlihat di `MONITOR`, di metrik, dan di log galat.
- **Idempotency key tidak boleh bisa ditebak atau dipakai ulang antar pengguna**, dan
  TTL-nya harus lebih panjang dari jendela retry klien.

---

## §5 Yang sudah dijaga, dan tetap diperiksa

Ringkas, karena rinciannya ada di `CLAUDE.md` dan skill domain:

- **Uang** — kepemilikan sebelum saldo; saldo tersedia = `balance - hold_amount`; baris
  limit yang hilang berarti **NOL (tolak)**, bukan tak terbatas; `SERIALIZABLE` + kunci
  urut UUID + retry `40001`; ledger harus tetap berimbang (`make ledger-check`).
- **Auth** — access token dicek ke **sesi**, bukan cuma tanda tangan; kode akses ≠ PIN;
  operasi yang mengubah keamanan lewat `verification_token`/OTP asli **dan** mencabut
  sesi lain; tiap cek PIN kena rate limit + lockout + nonce.
- **Lapisan** — `domain/` tidak mengimpor `repository/` atau `handler/`; `handler/`
  tidak mengimpor `repository/`; interface repository didefinisikan di `domain/`.
- **Kebocoran** — tidak pernah mencatat PIN, OTP, token, kode akses, atau PII ke log,
  termasuk di dalam pesan galat yang dibungkus `fmt.Errorf`.
- **Kontrak** — perubahan bentuk response **wajib** disertai pembaruan
  `docs/01-API-SPECIFICATION.md` **dan** `docs/postman/` di PR yang sama; tanpa itu
  build Android pecah.
- **Test** — method baru di interface repository berarti semua mock di test ikut
  diperbarui. Kegagalan `rootless Docker not found` adalah lingkungan, bukan regresi —
  jangan menyuruh orang "memperbaiki" kode karenanya.

---

## §6 Verdict — dipakai gerbang auto-merge

Setelah seluruh temuan dilaporkan, tulis **satu kata** ke berkas
`review-verdict.txt` di akar workspace. Hanya kata itu, tanpa tanda baca atau
penjelasan:

```
BLOCK
```

**`BLOCK`** kalau ada **satu saja** dari ini:

- Temuan di jalur uang, auth, atau kebocoran PII/kredensial (§3 kepemilikan, §5).
- Bug kebenaran yang bisa Anda tunjukkan skenarionya — bukan dugaan.
- Kolom yang ditulis kode tapi tidak ada di `migrations/`, atau nilai baru yang tidak
  ada di `CHECK`.
- Perubahan bentuk response tanpa pembaruan `docs/01-API-SPECIFICATION.md` + Postman.
- Mock integrasi eksternal tanpa gerbang `APP_ENV`.
- Endpoint autentikasi baru dengan rate limit `failClosed: false`.
- Migrasi tanpa pasangan `.down.sql`, atau `down` yang jelas tidak bisa jalan.

**`ALLOW`** hanya kalau tidak ada satu pun di atas. Temuan ringan — angka ajaib,
komentar yang kurang, penyederhanaan — tetap dilaporkan tapi **tidak** memblokir.

Kalau Anda tidak yakin antara keduanya, tulis `BLOCK`. Gerbang ini memutuskan apakah
kode masuk `main` tanpa dibaca manusia; ragu-ragu di sini harganya satu orang membaca
PR, sementara salah di sisi `ALLOW` harganya uang nasabah.
