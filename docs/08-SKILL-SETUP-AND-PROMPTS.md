# 08 — Pemasangan Skill & Katalog Prompt

> Cara menaruh skill `bca-mobile-backend` ke repo, memverifikasinya, dan prompt siap pakai untuk tiap fase di `07-SETUP-RUNBOOK.md`.

---

## Daftar Isi

1. [Dua Tempat Skill Bisa Hidup](#1-dua-tempat-skill-bisa-hidup)
2. [Langkah Pemasangan](#2-langkah-pemasangan)
3. [Verifikasi](#3-verifikasi)
4. [Struktur Repo Setelah Terpasang](#4-struktur-repo-setelah-terpasang)
5. [Cara Skill Terpakai](#5-cara-skill-terpakai)
6. [Katalog Prompt per Fase](#6-katalog-prompt-per-fase)
7. [Pola Prompt yang Bekerja](#7-pola-prompt-yang-bekerja)
8. [Troubleshooting](#8-troubleshooting)

---

## 1. Dua Tempat Skill Bisa Hidup

| | Personal | Project |
|---|---|---|
| Lokasi | `~/.claude/skills/bca-mobile-backend/SKILL.md` | `<repo>/.claude/skills/bca-mobile-backend/SKILL.md` |
| Ikut git | Tidak | Ya |
| Berlaku untuk | Hanya mesin Anda | Siapa pun yang clone repo |
| Tersedia di sesi cloud | Hanya kalau di-enable untuk akun claude.ai | Ya, karena ikut ter-commit |

**Pakai yang project.** Alasannya bukan sekadar rapi: aturan di skill ini (uang `int64`, ledger tiga-leg, idempotency per-user) adalah aturan *repo ini*, bukan preferensi pribadi. Kalau skill-nya hanya ada di mesin Anda, orang lain — atau Anda sendiri di mesin lain — akan menulis kode yang melanggarnya tanpa peringatan apa pun.

Skill ini juga sudah tersimpan di akun Claude Anda, jadi otomatis tersedia di sesi web/desktop. Salinan di repo tidak menggantikannya; keduanya bisa hidup bersama.

> **Nama folder menentukan slash command-nya**, bukan field `name:` di frontmatter. Folder `bca-mobile-backend/` → `/bca-mobile-backend`. Kalau foldernya di-rename, command-nya ikut berubah.

---

## 2. Langkah Pemasangan

### 2.1 Ekstrak

Zip-nya sudah berisi jalur lengkap `.claude/skills/bca-mobile-backend/SKILL.md`, jadi ekstrak dari **root repo**, bukan dari dalam `.claude/`:

```bash
cd /path/ke/bca-mobile-api          # root repo, bukan subfolder

unzip ~/Downloads/bca-mobile-backend-skill.zip

# Hasil yang benar:
find .claude -type f
# .claude/skills/bca-mobile-backend/SKILL.md
```

Kalau salah ekstrak dan hasilnya jadi `.claude/skills/.claude/skills/...` atau `SKILL.md` tergeletak langsung di `.claude/skills/`, perbaiki manual:

```bash
mkdir -p .claude/skills/bca-mobile-backend
mv <lokasi SKILL.md yang salah> .claude/skills/bca-mobile-backend/SKILL.md
find .claude -type d -empty -delete
```

`SKILL.md` **harus** berada di dalam foldernya sendiri. File yang tergeletak langsung di `.claude/skills/` tidak akan terbaca.

Kalau repo Anda sudah punya `.claude/` (misalnya `settings.json`), `unzip` akan menggabungkan, bukan menimpa — isinya yang lain aman.

### 2.2 Cek frontmatter

Baris pertama file harus persis `---`. Bukan baris kosong, bukan BOM, bukan komentar.

```bash
head -c 3 .claude/skills/bca-mobile-backend/SKILL.md | xxd | head -1
# harus: 2d2d 2d   ("---")
```

YAML frontmatter yang rusak **tidak memunculkan error** — skill-nya tetap termuat tapi tanpa description, sehingga Claude tidak pernah memilihnya sendiri. Gejalanya: skill "ada" tapi tidak pernah aktif. Karena itu langkah 2.3 penting.

### 2.3 Validasi

```bash
claude plugin validate .claude/skills
```

Perintah ini menangkap error parsing YAML yang tidak terlihat di mata.

### 2.4 Taruh dokumen pendukung

Skill merujuk `docs/06-LEDGER-AND-DEVICE-BINDING.md` dan `01-API-SPECIFICATION.md`. Dokumen itu **tidak** ikut termuat otomatis — file pendukung hanya terbaca kalau Claude membukanya. Jadi taruh semuanya di repo supaya bisa dibuka saat dibutuhkan:

```bash
mkdir -p docs
# salin 00-ARCHITECTURE-OVERVIEW.md .. 05-PROJECT-SETUP.md ke docs/
# lalu 06, 07, 08 dari deliverable sebelumnya
ls docs/
# 00-ARCHITECTURE-OVERVIEW.md  01-API-SPECIFICATION.md  02-DATABASE-SCHEMA.md
# 03-REDIS-STRATEGY.md  04-SECURITY.md  05-PROJECT-SETUP.md
# 06-LEDGER-AND-DEVICE-BINDING.md  07-SETUP-RUNBOOK.md  08-SKILL-SETUP-AND-PROMPTS.md
```

### 2.5 Commit

```bash
git add .claude/skills docs/
git commit -m "chore: add bca-mobile-backend skill and architecture docs

Skill mengunci konvensi repo: uang int64 satuan minor, ledger tiga-leg
untuk rail keluar, idempotency di-scope per user, device_id sebagai
identitas login, dan tanggal WIB disuplai aplikasi."
```

Pastikan `.gitignore` **tidak** memblokir `.claude/`. Sebagian template Go mengabaikan seluruh dotfolder. Cek:

```bash
git check-ignore -v .claude/skills/bca-mobile-backend/SKILL.md
# tidak ada output = aman
```

---

## 3. Verifikasi

Jalankan Claude Code dari root repo, lalu:

```
/skills
```

Menu harus memuat `bca-mobile-backend`. Kalau tidak muncul, langsung ke [Troubleshooting](#8-troubleshooting).

Lalu uji bahwa skill benar-benar dipakai, bukan sekadar terdaftar. Prompt uji:

```
Kalau saya menambahkan endpoint POST /qris/pay, berapa leg mutasi yang
harus ditulis dan dari mana akun lawannya diambil?
```

Jawaban yang benar menyebut **tiga leg** (DEBIT nasabah `amount + fee`, CREDIT shard QRIS `amount`, CREDIT fee-income `fee`) dan fungsi `settlement_account_id('QRIS', transaction_id)`. Kalau jawabannya "dua mutasi" atau tidak menyebut fee-income, skill-nya tidak terpakai.

Uji kedua, untuk memastikan skill tidak over-trigger:

```
Bagaimana cara membuat custom view di Android dengan Jetpack Compose?
```

Skill ini tidak boleh aktif untuk pertanyaan itu.

---

## 4. Struktur Repo Setelah Terpasang

```
bca-mobile-api/
├── .claude/
│   └── skills/
│       └── bca-mobile-backend/
│           └── SKILL.md              ← skill, ikut git
├── cmd/server/
├── internal/
├── migrations/
│   ├── 000001_create_users.{up,down}.sql
│   ├── ...
│   └── 000009_ledger_and_device_binding.{up,down}.sql
├── scripts/
│   ├── 000009_precheck_devices.sql
│   ├── 000009_verify.sql
│   ├── demo.sh
│   └── seed/main.go
├── deployments/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   ├── docker-compose.prod.yml
│   └── nginx/
├── docs/
│   └── 00 .. 08 *.md
├── keys/                             ← gitignored
├── .env                              ← gitignored
├── .env.example
├── .gitignore
├── .air.toml
├── .golangci.yml
├── Makefile
├── go.mod
└── README.md
```

---

## 5. Cara Skill Terpakai

**Otomatis.** Claude memilih skill sendiri berdasarkan `description`-nya. Prompt seperti "implement transfer execute" biasanya sudah cukup memicunya.

**Eksplisit.** Ketik `/bca-mobile-backend` diikuti instruksi, untuk memaksa skill termuat:

```
/bca-mobile-backend implement POST /transfer/inquiry
```

Gunakan bentuk eksplisit ketika prompt-nya pendek atau ambigu — misalnya "perbaiki bug di handler ini" — karena di situ pemicu otomatisnya lemah.

---

## 6. Katalog Prompt per Fase

Semua prompt di bawah mengikuti pola yang sama: **minta scope dan test plan dulu, baru kode setelah dikonfirmasi.** Ini bukan formalitas — pada kode yang memegang uang, membaca rencana 30 detik jauh lebih murah daripada mereview 400 baris diff yang arahnya sudah salah sejak awal.

### Prompt pembuka sesi

Jalankan sekali di awal setiap sesi baru:

```
Baca docs/07-SETUP-RUNBOOK.md dan docs/06-LEDGER-AND-DEVICE-BINDING.md.

Konteks: kita membangun backend ini dari nol mengikuti runbook tersebut,
fase demi fase. Skill bca-mobile-backend memuat semua konvensi yang berlaku.

Aturan kerja untuk sesi ini:
- Sebelum menulis kode, tunjukkan scope (file yang disentuh) dan test plan.
  Tunggu konfirmasi saya.
- Jangan lanjut ke fase berikutnya sebelum DoD fase sekarang hijau.
- Kalau instruksi saya bertentangan dengan aturan di skill, katakan, jangan
  diam-diam ikuti saya.

Sekarang: fase mana yang belum selesai? Cek kondisi repo dulu.
```

Baris ketiga dari aturan itu yang paling berguna. Saya lebih sering salah daripada yang saya kira, dan asisten yang menurut saja pada instruksi keliru tidak menolong siapa pun.

---

### Fase 0 — Bootstrap

```
Fase 0 runbook: bootstrap project.

Kerjakan:
1. go mod init github.com/<user>/bca-mobile-api
2. Buat struktur folder sesuai §1 skill
3. .gitignore — keys/, .env, bin/, tmp/, coverage.* SEBELUM commit pertama
4. go get semua dependency di §0.3 runbook, termasuk golang.org/x/sync
   dan testcontainers-go
5. deployments/docker-compose.yml dengan DUA instance Redis (session
   noeviction, cache allkeys-lru)
6. .env.example lengkap dengan REDIS_SESSION_* dan REDIS_CACHE_*
7. Makefile dari deliverable

Belum ada kode Go selain go.mod. Tunjukkan daftar file yang akan dibuat
dulu.
```

Setelah selesai:

```
Taruh migrasi 000001-000008 dari docs/02-DATABASE-SCHEMA.md ke migrations/,
satu file .up.sql dan .down.sql per nomor. File down harus benar-benar
membalik file up-nya, bukan placeholder kosong.

Lalu jalankan: make infra-up && make migrate-up && make verify-009

Laporkan hasilnya. DoD fase 0: 64 akun internal ada, verify-009 nol FAIL.
```

---

### Fase 1 — Fondasi

```
Fase 1 runbook: fondasi.

Urutan di §1.1. Yang saya mau ditegaskan:

- internal/pkg/apperr: satu tempat pemetaan domain error ke
  (httpStatus, code, message). Satu entry untuk SETIAP code di tabel §2 skill.
  INTERNAL_ERROR tidak pernah membawa pesan error asli ke client.
- /health dipecah dua: liveness tidak di-cache (ping DB + Redis, timeout 1s),
  config di-cache 5 menit.
- Dua client Redis terpisah, dua Ping saat start.

Tunjukkan scope + test plan dulu.
```

Verifikasi:

```
Jalankan DoD fase 1 di runbook, satu per satu, dan laporkan hasil tiap
perintah. Jangan diborong jadi satu ringkasan.
```

---

### Fase 2 — Autentikasi

Fase ini pecah jadi enam prompt. Jangan digabung — ini bagian dengan jebakan terbanyak.

**2.1 Kripto**

```
Fase 2.1: internal/pkg/crypto.

Lima file sesuai tabel §2.1 runbook. Yang wajib benar:
- Argon2id dibungkus semaphore.Weighted(min(NumCPU, 8))
- RSA OAEP-SHA256, BUKAN PKCS#1 v1.5
- Payload PIN {"pin","nonce","ts"}; tolak skew >60s atau nonce terpakai
- JWT verify WAJIB cek claim typ
- HMAC-SHA256 untuk lookup hash, lowercase+trim dulu

Tulis unit test bersamaan dengan implementasinya, bukan setelahnya.
Semua fungsi murni, jadi tidak ada alasan menunda.
```

**2.2 Rate limit**

```
Fase 2.2: rate limiting sliding window + account lockout.

Tiga layer wajib: login per device (5/15m), login PER IP (20/15m),
PIN verify per user (5/15m berbagi counter lockout).

Layer per-IP itu bukan opsional — karena login me-resolve user dari
device_id, tanpa itu penyerang bisa mengunci akun orang lain dengan
menebak device_id. Jelaskan di komentar kode kenapa ada.

Redis mati: auth endpoint fail-closed, read-only fail-open.
```

**2.3 Login PIN**

```
Fase 2.3: POST /auth/login/pin.

Ikuti tujuh langkah §3 skill persis. Tiga yang paling sering salah:
- Langkah 5: UPDATE ... RETURNING failed_pin_attempts, bercabang pada nilai
  yang DIKEMBALIKAN. Bukan user.FailedPINAttempts+1 dari memori.
- Langkah 6: device tak dikenal harus punya bentuk DAN waktu respons yang
  sebanding — verifikasi Argon2 terhadap hash dummy.
- Langkah 2: tidak ada baris device aktif → 403 AUTH_DEVICE_NOT_RECOGNIZED.

Scope + test plan dulu. Test plan harus memuat kasus race pada counter.
```

**2.4 Sesi & rotation**

```
Fase 2.4: sesi + refresh token rotation dengan reuse detection.

- Sesi di PostgreSQL dan Redis; sessions:user:{id} sebagai Set untuk
  logout-all, JANGAN pakai SCAN.
- Rotation: tandai hash lama refresh:revoked:{hash} dengan TTL sisa umur.
  Jangan hanya dihapus — key hilang tidak bisa dibedakan dari expired,
  dan reuse detection jadi mustahil.
- Hash ditemukan di revoked → cabut SEMUA sesi user + audit
  SECURITY_SUSPICIOUS_LOGIN + push.

Test plan wajib memuat skenario reuse.
```

**2.5 Biometrik**

```
Fase 2.5: challenge biometrik + login biometrik + registrasi kunci.

Challenge dikonsumsi dengan GETDEL (atomik). Challenge yang dibaca dulu
lalu dihapus belakangan bisa diputar ulang.

Verifikasi key_id milik baris device yang sama dengan challenge dan request.

Test: challenge dipakai dua kali → yang kedua ditolak.
```

**2.6 Audit**

```
Fase 2.6: audit service.

Buffered channel + worker pool tetap, BUKAN go func() per entry.
Fallback ke slog kalau insert DB gagal ATAU buffer penuh.
Audit gagal tidak menggagalkan transaksi; audit hilang diam-diam juga tidak
boleh terjadi.

Pasang audit di semua endpoint auth yang sudah ada.
```

---

### Fase 3 — Account & Dashboard

```
Fase 3 runbook: account & dashboard.

Urutan §3.1. Tiga aturan yang gampang dilanggar:
- Cursor WAJIB masuk cache key untuk endpoint paginated
- Invalidasi pakai version counter cachever:*, bukan SCAN pattern
- Setiap cache ditulis bersama jalur invalidasinya dalam commit yang sama

Dan: setiap query yang lookup berdasarkan NOMOR REKENING harus filter
owner_type = 'CUSTOMER'. Tanpa itu, 9902000000 (shard settlement) jadi
tujuan transfer yang valid.

Scope + test plan dulu.
```

---

### Fase 4 — Transaksi

**4.1–4.3 dulu, execute belakangan.**

```
Fase 4, langkah 1-4: mutations, history, transfer/recent, transfer/inquiry.

Keyset pagination: tuple cursor harus persis sama dengan ORDER BY, dengan
id sebagai tiebreaker terakhir. Cursor {id, date} akan menjatuhkan baris
ketika beberapa mutasi berbagi tanggal.

Ambil LIMIT+1 untuk menghitung has_more.

Test: halaman 2 tidak boleh sama dengan halaman 1, dan tidak boleh ada
baris yang hilang di perbatasan halaman.
```

```
Fase 4 langkah 5: internal/pkg/idempotency.

SETNX dulu, JANGAN GET dulu. Handler di 04-SECURITY.md mengembalikan
literal string "PROCESSING" sebagai body 200 saat replay konkuren — itu bug,
jangan ditiru.

Key selalu idem:{user_id}:{key}.
Pada jalur gagal apa pun: DEL key supaya client bisa retry.
Setelah TTL 24 jam dan replay menabrak uq_txn_user_idempotency: ambil
transaksi yang sudah ada dan kembalikan responsnya, BUKAN 409.
```

**4.4 — yang paling hati-hati**

```
Fase 4 langkah 6: POST /transfer/execute.

Ini endpoint paling kritis di aplikasi. Ikuti resep 17 langkah §5 skill
persis, dan sebelum menulis kode tunjukkan:
1. Daftar file yang disentuh
2. Urutan langkah yang akan diimplementasikan
3. Test plan lengkap — happy path, idempotency, inquiry binding,
   verification token, saldo & limit, konkurensi, ledger

Yang saya akan cek di review:
- destination di-derive dari inquiry, body hanya cross-check
- semua akun terdampak di-lock ORDER BY id
- retry 40001, 3x, backoff berjitter
- tiga leg untuk rail keluar, dua untuk internal tanpa fee
- tanggal WIB dari aplikasi, bukan CURRENT_DATE
- uang int64 satuan minor

Jangan tulis kode sampai saya konfirmasi test plan-nya.
```

**Test konkurensi**

```
Tulis integration test dengan testcontainers untuk:

1. 20 goroutine transfer dari satu rekening, total melebihi saldo
   → saldo tidak pernah negatif, hanya subset yang mampu yang sukses
2. Transfer A→B dan B→A konkuren → keduanya selesai, tidak ada deadlock
3. 40001 yang dipaksa → di-retry transparan, tidak ada 500 yang bocor

Lalu jalankan: go test -race -count=20 -run Concurrent ./...
Flaky di sini berarti bug nyata, bukan test yang rewel. Laporkan apa
adanya kalau ada yang gagal.
```

---

### Fase 5 — E-Wallet & QRIS

```
Fase 5 runbook.

Perhatikan: POST /ewallet/topup di spec hanya menerima idempotency_key +
inquiry_id + verification_token. Itu bentuk yang BENAR. Ikuti pola ini,
jangan menirukan transfer/execute yang menduplikasi field destination.

Stub provider: satu interface EWalletProvider dengan implementasi fake.
Sertakan jalur error yang bisa dipicu (nomor tertentu → EWALLET_PROVIDER_DOWN).
Stub yang selalu sukses tidak berguna untuk demo.

Untuk QRIS decode: dukung field wajib EMVCo, tolak sisanya dengan jelas.
Jangan pura-pura mendukung spec penuh.
```

---

### Fase 6 — Registrasi

```
Fase 6 runbook: registrasi & KYC stub.

Registration token belum didefinisikan di spec. Buat JWT ber-scope pendek:
typ "registration", TTL 30 menit, hanya diterima endpoint /registration/*.

Upload dokumen: validasi content-type dari ISI file, bukan header.
Batasi ukuran. Simpan ke disk/MinIO, jangan ke database.

Catatan: contoh NIK di spec ditulis tersamar (3201****0001). Itu keliru
untuk KYC — input harus NIK lengkap, penyamaran hanya saat menampilkan.
```

---

### Fase 7 — Hardening & Rilis

```
Fase 7 runbook, bagian test.

Audit coverage yang ada sekarang. Target: domain/service >= 80%.
Untuk tiap paket di bawah target, sebutkan cabang mana yang belum
tersentuh — jangan tulis test asal menaikkan angka.

Prioritaskan cabang error di service layer. Getter yang tidak diuji
tidak apa-apa.
```

```
Fase 7: scripts/seed/main.go.

3 user dengan PIN yang saya tahu, masing-masing 1-2 rekening bersaldo,
riwayat mutasi 30 hari yang terlihat wajar, beberapa promo, notifikasi,
favorit transfer.

Mutasi harus konsisten dengan ledger: balance_before/balance_after
berurutan, dan v_ledger_reconciliation tetap kosong setelah seed.
```

```
Fase 7: scripts/demo.sh.

14 langkah sesuai §"Demo Script End-to-End" di runbook. Bash, pakai curl
dan jq, berhenti di error pertama, cetak PASS/FAIL per langkah.

Langkah 9 (replay idempotency), 13 (view rekonsiliasi kosong), dan
14 (lockout) tidak boleh dilewat — itu yang membedakan demo ini dari CRUD.
```

```
Fase 7: README.md.

Lima bagian sesuai §7.7 runbook. Yang paling penting bagian 4 dan 5:
- Keputusan desain DAN alasannya: ledger tiga-leg, device binding,
  idempotency per-user, sharding settlement
- Apa yang sengaja TIDAK dikerjakan: HSM, pentest, compliance, HA

Bagian 5 ditulis eksplisit, bukan disembunyikan. Tahu apa yang belum aman
itu bagian dari nilai portfolio-nya.

Dan bagian 1 wajib memuat disclaimer bahwa ini latihan arsitektur tanpa
afiliasi dengan bank mana pun.
```

---

### Prompt review & QA

Berguna kapan saja, tidak terikat fase.

```
Review diff yang belum saya commit terhadap aturan di skill
bca-mobile-backend. Fokus ke §0 Non-negotiables.

Untuk tiap pelanggaran: sebutkan file:baris, aturan mana yang dilanggar,
dan kenapa itu masalah pada sistem yang memegang uang. Kalau tidak ada
pelanggaran, katakan begitu — jangan cari-cari temuan.
```

```
Buatkan test plan untuk <fitur>. Format tabel evidence-convention tim:
ID / deskripsi-langkah / expected result / evidence.

Tunjukkan scope dan logika yang diuji dulu. Jangan langsung eksekusi,
dan jangan borong acceptance criteria — verifikasi satu per satu.
```

```
Jalankan make ledger-check. Kalau ada baris yang keluar dari salah satu
view, telusuri transaksi mana yang menyebabkannya dan jelaskan leg mana
yang hilang.
```

---

## 7. Pola Prompt yang Bekerja

| Kurang efektif | Lebih efektif | Kenapa |
|---|---|---|
| "Buatkan transfer" | "Fase 4 langkah 6: POST /transfer/execute, ikuti resep 17 langkah §5 skill" | Menyebut fase dan bagian skill membuat aturannya termuat, bukan disimpulkan |
| "Perbaiki bug ini" | "/bca-mobile-backend perbaiki bug ini" | Prompt pendek lemah memicu skill otomatis; bentuk eksplisit memaksanya |
| "Buatkan semua endpoint transaksi" | Empat prompt terpisah per endpoint | Batch besar menghasilkan diff yang terlalu besar untuk direview jujur |
| "Test yang lengkap ya" | Daftar kasus konkret yang harus ada | "Lengkap" tidak punya definisi; daftar punya |
| "Sudah benar?" | "Review terhadap §0 skill, sebutkan file:baris" | Pertanyaan tertutup mengundang jawaban yang menyenangkan, bukan yang akurat |

Satu kebiasaan yang paling berpengaruh: **selalu minta scope dan test plan sebelum kode.** Bukan karena rencananya pasti benar, tapi karena rencana yang salah terlihat dalam 30 detik sementara implementasi yang salah butuh setengah jam untuk ketahuan.

---

## 8. Troubleshooting

### Skill tidak muncul di `/skills`

```bash
# 1. Lokasi benar?
ls -la .claude/skills/bca-mobile-backend/SKILL.md

# 2. Claude Code dijalankan dari root repo?
pwd

# 3. Ter-ignore git / tidak ada di working tree?
git check-ignore -v .claude/skills/bca-mobile-backend/SKILL.md

# 4. Frontmatter valid?
claude plugin validate .claude/skills
head -1 .claude/skills/bca-mobile-backend/SKILL.md   # harus persis: ---
```

Penyebab paling umum: `SKILL.md` tergeletak di `.claude/skills/` tanpa folder pembungkus, atau Claude Code dijalankan dari subfolder.

### Skill terdaftar tapi tidak pernah aktif sendiri

Kemungkinan besar frontmatter-nya rusak — skill termuat tanpa description, jadi tidak pernah cocok otomatis. Jalankan `claude plugin validate .claude/skills`.

Kemungkinan kedua: terlalu banyak skill terpasang. Description dipotong agar muat dalam anggaran listing, dan kata kunci pemicunya bisa ikut terpotong. Sementara itu, pakai `/bca-mobile-backend` eksplisit.

### Skill aktif tapi jawabannya melanggar aturannya sendiri

Terjadi, terutama di sesi panjang. Yang efektif:

```
Cek jawaban terakhirmu terhadap §0 Non-negotiables di skill
bca-mobile-backend. Ada yang dilanggar?
```

Kalau berulang di area yang sama, itu sinyal bahwa bagian skill-nya kurang tegas — bukan sekadar kelalaian. Catat, lalu perbaiki skill-nya.

### Sesudah mengedit SKILL.md

Perubahan teks pada `SKILL.md` terbaca dalam sesi berjalan, tidak perlu restart. Kalau ragu, mulai sesi baru.

### Mengedit skill vs mengedit dokumen

Aturan yang berlaku untuk **semua** pekerjaan di repo → `SKILL.md`.
Penjelasan, alasan, dan riwayat keputusan → `docs/`.

Skill dibaca setiap kali; docs dibaca saat dibutuhkan. Memasukkan seluruh isi docs ke skill akan membuatnya terlalu panjang untuk dipatuhi dengan konsisten.
