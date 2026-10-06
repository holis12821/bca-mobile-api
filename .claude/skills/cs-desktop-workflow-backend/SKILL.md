---
name: cs-desktop-workflow-backend
description: Pekerjaan BACKEND yang dibutuhkan alur kerja lengkap aplikasi desktop CS Halo BCA (project `cs-halo-bca`) — sembilan belas layar dari registrasi petugas, login, kesiapan terminal (otorisasi supervisor, healthcheck perangkat, pakta integritas PII, aktivasi terminal), command center/dashboard, antrean video call, ruang panggilan e-KYC, keputusan verifikasi, sampai logout dan penutupan loket. Skill ini adalah ANALISIS SELISIH: ia menyebut apa yang SUDAH ADA di repo ini (empat belas endpoint CS yang terbit) dan apa yang BELUM ADA beserta kontrak yang perlu dibangun — identitas petugas berbasis NPP+kata sandi, siklus hidup terminal (REGISTERED/READY/ONLINE/BUSY/OFFLINE), otorisasi dual-control supervisor, gerbang kesiapan, KPI dashboard, keputusan NEED_REVIEW dan eskalasi, serta dua belas peristiwa audit. Gunakan saat membangun endpoint baru untuk aplikasi desktop CS, saat menilai apakah sebuah layar bisa disambungkan hari ini, saat merancang tabel terminal/supervisor/sesi petugas, atau saat menjawab "apa yang kurang di backend supaya alur CS bisa jalan". Trigger juga pada "workflow CS desktop", "SCR-001".."SCR-019", "terminal readiness", "aktivasi terminal", "otorisasi supervisor", "dual-control", "pakta integritas", "healthcheck perangkat", "command center", "KPI petugas", "NEED_REVIEW", "eskalasi Tier 2", "queue reservation", "RESERVED", "terminal ONLINE", "logout petugas", dan "cs-halo-bca". JANGAN dipakai untuk kontrak endpoint CS yang SUDAH terbit (itu `cs-desktop-api-integration`), server video call itu sendiri (itu `buka-rekening-video-call-backend`), atau OCR/biometrik/kredensial/submit sisi nasabah (itu `buka-rekening-backend`).
---

# Backend untuk alur kerja desktop CS Halo BCA

Spesifikasi alurnya ada di project frontend:
`cs-halo-bca/docs/halo-bca-ekyc-desktop-workflow.md` — 19 layar, state machine,
7 aturan bisnis, acceptance criteria.

Skill ini **bukan** salinan dokumen itu. Ia menjawab satu pertanyaan yang tidak
dijawab dokumen itu: **apa yang harus ada di backend ini supaya alurnya nyata,
dan apa yang sudah ada sehingga tidak perlu dibangun ulang.**

---

## ATURAN #0 — Empat belas endpoint CS sudah terbit. Jangan bangun ulang.

Sebelum menulis satu baris pun, baca `.claude/skills/cs-desktop-api-integration/SKILL.md`.
Itu kontrak yang **berlaku** untuk yang sudah ada:

| # | Endpoint | Penjaga |
|---|---|---|
| 1 | `GET /v1/onboarding/video-call/queued` | kunci sistem |
| 2 | `POST /v1/onboarding/video-call/agent-token` | + petugas `VIDEO_CALL` |
| 3 | `GET /v1/onboarding/video-call/signal?token=` | token sekali pakai |
| 4 | `POST /v1/onboarding/video-call/result` | + petugas `VIDEO_CALL` |
| 5 | `GET /v1/onboarding/sessions/{id}/audit` | kunci sistem |
| 6 | `GET /v1/onboarding/monitoring` | kunci sistem |
| 7 | `GET/PUT /internal/v1/cards*` | + petugas `CARD_ADMIN` |
| 8 | `GET /internal/v1/onboarding/sessions` | + petugas `VIDEO_CALL` |
| 9 | `GET /internal/v1/onboarding/sessions/{id}` | + petugas `CUSTOMER_PII` |
| 10–11 | `GET /internal/v1/customers*` | + petugas `CUSTOMER_PII` |
| 12–14 | `/internal/v1/tickets*` | + petugas `TICKET` |

Yang sudah ada dan **sering dikira belum**:

- **Reservasi antrean (Rule 5) SUDAH berjalan.** `agent-token` memindahkan
  panggilan `QUEUED → ACTIVE`, mengeluarkannya dari sorted set Redis, dan
  mengirim `agent_assigned` ke nasabah. Panggilan yang sudah diambil **hilang
  dari daftar** petugas lain. Tidak perlu status `RESERVED` terpisah; yang perlu
  diputuskan hanya apakah "RESERVED" di dokumen alur berarti sesuatu yang lain.
- **`call_ended` sudah dikirim ke KEDUA sisi** saat hasil disubmit dan saat
  panggilan dilepas.
- **Jejak audit sesi onboarding sudah ada** (`GET /sessions/{id}/audit`), begitu
  juga jejak akses PII petugas (`cs_access_logs`, migrasi 000029).

---

## 1. Selisih per layar

`ADA` = bisa disambungkan hari ini. `SEBAGIAN` = ada tapi kurang field/semantik.
`BELUM` = tidak ada apa pun di backend.

| Layar | Yang dibutuhkan server | Status |
|---|---|---|
| SCR-001 Registration | Lookup NPP ke HRIS, daftar supervisor, verifikasi token supervisor, info terminal, pendaftaran terminal | **BELUM** |
| SCR-002 Register Success | — (tanda terima dari #1) | — |
| SCR-003/004 Login | Login petugas NPP + kata sandi → sesi bertenggat | **BELUM** |
| SCR-005 Terminal Readiness | Keadaan kesiapan terminal (3 gerbang) | **BELUM** |
| SCR-006 Supervisor Auth | Verifikasi dual-control + penandatanganan | **BELUM** |
| SCR-007 Device Healthcheck | Pencatatan hasil (probe-nya di klien) | **BELUM** |
| SCR-008 PII Confirmation | Pencatatan pakta integritas | **BELUM** |
| SCR-009 Terminal Activation | Transisi terminal → `ONLINE` | **BELUM** |
| SCR-010 Dashboard | KPI (SLA, panggilan hari ini, target shift, CSAT), status terminal, telemetri | **BELUM** — `monitoring` ada tapi isinya berbeda |
| SCR-011 Queue | Daftar antrean | **SEBAGIAN** — tanpa `priority` dan `service` |
| SCR-012 Call Connecting | `agent-token` + signaling | **ADA** |
| SCR-013 Video Call Room | Signaling, data nasabah tersamar | **SEBAGIAN** — pratinjau dokumen/OCR sisi CS belum ada |
| SCR-014 Verification Form | Keputusan + alasan + eskalasi | **SEBAGIAN** — hanya `APPROVED`/`REJECTED` |
| SCR-015 Processing | — | — |
| SCR-016 Result | `current_step` | **ADA** |
| SCR-017/018 Complete & Next | Antrean berikutnya | **ADA** (pakai `queued`) |
| SCR-019 Logout | Tutup sesi + terminal → `OFFLINE` | **BELUM** |

**Kesimpulannya:** jantung operasionalnya — antrean, panggilan, signaling, hasil
verifikasi — **sudah jalan**. Yang belum ada adalah seluruh lapisan di
sekelilingnya: identitas petugas, terminal, dan gerbang kesiapan.

---

## 2. Keputusan yang harus diambil SEBELUM menulis kode

Tiga hal ini mengubah arsitektur, dan menebaknya akan mahal dibereskan.

### 2.1 Kredensial petugas: kunci statis atau kata sandi?

**Sekarang:** `cs_agents` menyimpan `api_key_hash` (Argon2id). Petugas memakai
`X-Agent-Employee-ID` + `X-Agent-API-Key` di setiap permintaan. Kuncinya statis,
didistribusikan manual, tidak bertenggat.

**Yang diminta alur:** SCR-003 login dengan NPP + kata sandi, menghasilkan sesi
(§12: "Creating CS session"), dan §19 "Session Started 08:02 WIB".

Keduanya **tidak bisa hidup berdampingan tanpa keputusan**: kalau sesi jadi
pembawa identitas, `X-Agent-API-Key` berhenti jadi sumber kebenaran `actor` di
audit, dan seluruh 11 endpoint ber-`AgentAuth` harus menerima kedua bentuk
selama masa transisi.

Pilihan yang masuk akal:

1. **Sesi di atas kunci yang ada** — `POST /internal/v1/auth/login` menerima NPP
   + kunci petugas (bukan kata sandi baru), mengembalikan token sesi bertenggat.
   `AgentAuth` menerima token sesi ATAU pasangan header lama. Paling murah,
   tidak menyentuh `cs_agents`.
2. **Kata sandi terpisah** — kolom `password_hash` baru di `cs_agents`, kunci API
   tetap untuk sistem-ke-sistem. Lebih dekat ke dokumen alur, menuntut jalur
   penyetelan kata sandi awal yang belum ada.
3. **OIDC** — sudah tercatat sebagai pekerjaan yang belum ada. Paling benar,
   paling lama.

**Jangan memilih diam-diam.** Tanyakan sebelum membangun.

### 2.2 Status terminal: apakah server yang memegangnya?

Rule 4 berbunyi "hanya petugas `ONLINE` yang boleh mengambil antrean". Hari ini
**server tidak tahu apa pun tentang terminal** — `agent-token` hanya memeriksa
kredensial dan cakupan. Rule 4 **tidak bisa ditegakkan** sampai status terminal
hidup di server.

Kalau status terminal hanya di memory frontend, Rule 4 adalah hiasan: petugas
yang melewati layar kesiapan dengan menyunting state klien tetap bisa mengambil
panggilan. Untuk aturan yang menyangkut siapa boleh melayani nasabah, itu tidak
memadai.

Maka: **status terminal harus di server**, dan `agent-token` harus menolak
terminal yang bukan `ONLINE`.

### 2.3 `NEED_REVIEW` memindahkan langkah nasabah ke mana?

`VideoCallResult` hari ini hanya `APPROVED` dan `REJECTED`, dan keduanya punya
akibat yang jelas: `APPROVED` → `CREDENTIALS`, `REJECTED` → tetap di
`VIDEO_CALL`. Dokumen alur menambah keputusan ketiga.

Yang harus diputuskan: nasabah yang `NEED_REVIEW` **menunggu di langkah mana**,
siapa yang menyelesaikannya, dan apa yang dilihat nasabah di aplikasinya selama
menunggu. Tanpa jawaban itu, `NEED_REVIEW` hanya membuat sesi menggantung tanpa
jalan keluar — lebih buruk daripada menolak.

---

## 3. Endpoint yang perlu dibangun

Semua di `/internal/v1` — jalur operator tidak berbagi rate limit, body limit,
dan CORS dengan jalur nasabah. Semua di belakang `InternalAPIKey`.

### 3.1 Identitas & sesi petugas

```
POST   /internal/v1/auth/login          { employee_id, credential } → sesi bertenggat
POST   /internal/v1/auth/logout         menutup sesi
GET    /internal/v1/auth/me             identitas + cakupan + sesi aktif
```

`GET /auth/me` menjawab masalah nyata yang sudah tercatat di
`cs-desktop-api-integration` §1.2: **aplikasi desktop tidak bisa menanyakan
cakupannya**, jadi ia menyembunyikan menu berdasarkan `403` yang pernah diterima.
Satu endpoint ini menghapus seluruh tebak-tebakan itu.

### 3.2 Pendaftaran petugas & terminal

```
GET    /internal/v1/hris/employees/{npp}      lookup NPP
POST   /internal/v1/agents                    daftarkan petugas          [SUPERVISOR]
GET    /internal/v1/supervisors               daftar supervisor
POST   /internal/v1/supervisors/authorize     { supervisor_id, token } → tanda tangan
GET    /internal/v1/terminals/current         info terminal pemanggil
POST   /internal/v1/terminals                 daftarkan terminal + allowlist
```

**HRIS adalah sistem luar.** Jangan menyalin tabel pegawai ke sini. Kalau
integrasinya belum ada, endpoint `hris/employees/{npp}` menjawab
`503 HRIS_UNAVAILABLE` — bukan data karangan, dan bukan 404 yang terbaca seperti
"NPP tidak terdaftar".

**Tiga jawaban berbeda** yang harus dibedakan dan jangan disatukan: NPP
ditemukan & aktif, NPP ditemukan tapi nonaktif, NPP tidak ditemukan. Menyamakan
dua yang terakhir membuat pegawai yang statusnya dicabut mengira ia salah ketik.

### 3.3 Kesiapan & siklus hidup terminal

```
GET    /internal/v1/terminals/{id}/readiness     keadaan 3 gerbang
POST   /internal/v1/terminals/{id}/healthcheck   catat hasil probe klien
POST   /internal/v1/terminals/{id}/pii-ack       catat pakta integritas
POST   /internal/v1/terminals/{id}/activate      → ONLINE  (menuntut 3 gerbang PASS)
POST   /internal/v1/terminals/{id}/deactivate    → OFFLINE
```

**Healthcheck diprobe KLIEN, dicatat SERVER.** Server tidak bisa mengukur kamera
atau mikrofon di meja petugas; yang bisa dilakukannya adalah mencatat pernyataan
klien beserta waktunya, lalu memakainya sebagai gerbang. Perlakukan isinya
sebagai **pernyataan**, bukan pengukuran — dan jangan menampilkannya di laporan
kepatuhan seolah server yang mengukurnya.

`activate` **menolak** kalau salah satu gerbang belum `PASS` (Rule 3). Gerbang
yang kedaluwarsa (mis. healthcheck kemarin) dihitung belum lolos — tentukan
umurnya, jangan dibiarkan selamanya.

### 3.4 Dashboard

```
GET    /internal/v1/cs/dashboard     KPI + status terminal + telemetri + puncak antrean
```

Satu endpoint, bukan empat: layar ini dimuat tiap petugas membuka beranda, dan
empat permintaan untuk satu layar adalah empat kali penjaga Argon2id.

**Peringatan isi:** `SLA 98.4%`, `CSAT 96.8%`, `Target Shift 50` di dokumen alur
adalah **angka demo** (§75). Tidak ada sumbernya hari ini, dan tidak ada
mekanisme yang mengukur CSAT. Bangun hanya yang benar-benar terukur —
panggilan hari ini, rata-rata durasi, panjang antrean — dan **hilangkan sisanya
dari kontrak** sampai ada yang mengukurnya. KPI karangan di layar operasional
akan dipakai menilai orang.

### 3.5 Antrean & verifikasi: menambal yang sudah ada

```
GET  /v1/onboarding/video-call/queued    + field `priority`, `service`
POST /v1/onboarding/video-call/result    + `NEED_REVIEW`, `rejection_reason`, `escalation_queue`
```

Keduanya **perubahan pada endpoint yang sudah dipakai**. Tambahkan field, jangan
mengubah bentuk yang ada — aplikasi desktop yang sudah berjalan membacanya.

`rejection_reason` dibatasi enum (`IDENTITY_MISMATCH`, `INVALID_DOCUMENT`,
`FACE_MISMATCH`, `SUSPICIOUS_ACTIVITY`, `INCOMPLETE_INFORMATION`, `OTHER`), bukan
teks bebas: alasan penolakan verifikasi identitas adalah hal yang akan
dilaporkan dan dihitung.

### 3.6 Audit (Rule 7)

```
POST   /internal/v1/cs/audit-events      (internal, dipanggil service lain)
GET    /internal/v1/cs/audit-events      pencarian untuk pengawas
```

Dua belas tindakan di Rule 7 harus menghasilkan peristiwa. **Enam di antaranya
sudah tercatat** lewat jejak onboarding dan `cs_access_logs`; enam sisanya
(registrasi, login, otorisasi supervisor, healthcheck, pakta PII, aktivasi,
logout) belum punya tempat.

Peristiwa audit **ditulis di dalam transaksi tindakannya**, bukan sesudahnya.
Audit yang ditulis terpisah akan hilang persis pada kegagalan yang paling perlu
ditelusuri.

---

## 4. Tabel baru

Migrasi berikutnya mulai dari **`000032`** (terakhir: `000031_video_call_schedules`).
Penomoran berurutan — periksa ulang sebelum menulis, dan jangan menabrak nomor
yang sedang dikerjakan orang lain.

| Migrasi | Tabel | Isi |
|---|---|---|
| `000032` | `cs_terminals` | `terminal_id`, `workstation`, `location`, `status`, `registered_by`, `last_seen_at` |
| `000033` | `cs_terminal_readiness` | gerbang per sesi: supervisor/healthcheck/pii, `passed_at`, `expires_at` |
| `000034` | `cs_supervisors` | supervisor + `token_hash` (Argon2id, sama seperti `cs_agents`) |
| `000035` | `cs_agent_sessions` | sesi petugas bertenggat, `terminal_id`, `started_at`, `ended_at` |
| `000036` | `cs_audit_events` | dua belas tindakan Rule 7 |

Dua catatan yang menentukan bentuknya:

- **`cs_terminals.status`** memakai `CHECK` constraint, bukan enum Postgres:
  menambah status ke enum menuntut migrasi yang mengunci tabel, dan daftar status
  di dokumen alur (`REGISTERED/READY/ONLINE/BUSY/CALL_ACTIVE/PROCESSING/OFFLINE/ERROR`)
  masih akan berubah.
- **`cs_supervisors.token_hash`** — token supervisor adalah kredensial dual-control.
  Ia di-hash seperti `cs_agents.api_key_hash`, tidak pernah disimpan atau
  dikembalikan sebagai teks, dan `#BCA-AUTH-9942` di dokumen alur adalah
  **rujukan tanda terima**, bukan tokennya. Jangan menyimpan token di kolom yang
  ikut terbaca di `GET`.

---

## 5. Yang di dokumen alur TAPI jangan dibangun sebagai fakta

§75 dokumen itu menyatakan dirinya mock/demo. Yang berikut adalah nilai demo,
dan membangunnya seolah nyata akan menanamkan kebohongan di sistem perbankan:

| Di dokumen | Kenapa jangan |
|---|---|
| `Jabra Headset Detected`, `Logitech C925e` | Merek perangkat tertentu. Klien melaporkan apa yang ada, server tidak mengarang |
| `CSAT 96.8%` | Tidak ada mekanisme pengukuran kepuasan nasabah |
| `SLA 98.4%`, `Target Shift 50` | Tidak ada definisi SLA maupun target yang disepakati |
| `Face Match ✓ 98.7%` | **Tidak ada pencocokan wajah di sisi CS.** Menampilkannya membuat petugas berhenti memeriksa sendiri |
| `Noise Suppression 98%` | Angka tanpa satuan dan tanpa pengukur |
| `Latency 12 ms` | Boleh — ini terukur dari klien. Tapi catat sebagai laporan klien |
| `#WKS-SMG-0842` | Format id terminal, bukan id sungguhan |

Aturan umumnya: **kalau tidak ada yang mengukurnya, jangan bikin kolomnya.**
Kolom yang ada akan diisi, dan yang terisi akan dipercaya.

---

## 6. Urutan pengerjaan

Diurutkan supaya tiap tahap menghasilkan sesuatu yang bisa dipakai, bukan
supaya terlihat rapi di diagram.

1. **`GET /auth/me`** — paling kecil, langsung menghapus tebak-tebakan cakupan di
   desktop. Tidak butuh tabel baru.
2. **`cs_terminals` + aktivasi/deaktivasi + penjaga di `agent-token`** — ini yang
   membuat Rule 3 dan Rule 4 bisa ditegakkan. Tanpa ini, seluruh layar kesiapan
   hanyalah animasi.
3. **Sesi petugas** (`/auth/login`, `/auth/logout`) — sesudah keputusan §2.1.
4. **Supervisor + gerbang kesiapan** — menuntut #2 dan #3 lebih dulu.
5. **Dashboard** — hanya metrik yang terukur.
6. **`NEED_REVIEW` + alasan penolakan** — sesudah keputusan §2.3.
7. **Pendaftaran petugas & HRIS** — terakhir, karena bergantung sistem luar dan
   paling jarang dipakai (sekali per petugas).

Layar desktop yang sudah ada hari ini (antrean, panggilan, hasil) **tidak boleh
rusak** di tahap mana pun. Tambahkan field, jangan mengubah yang terbit.

---

## 7. Rujukan

| Isi | Tempat |
|---|---|
| Spesifikasi alur 19 layar | `cs-halo-bca/docs/halo-bca-ekyc-desktop-workflow.md` |
| Kontrak endpoint CS yang SUDAH terbit | `.claude/skills/cs-desktop-api-integration/SKILL.md` |
| Perilaku server video call | `.claude/skills/buka-rekening-video-call-backend/SKILL.md` |
| Penjaga auth & cakupan | `internal/middleware/internal_api_key.go` |
| Perakitan rute operator | `internal/router/router.go` (blok `/internal/v1`) |
| Jejak akses petugas | `migrations/000029_cs_access_logs.up.sql`, `internal/domain/cs/` |
| Aplikasi desktop yang mengonsumsinya | project `cs-halo-bca` |
