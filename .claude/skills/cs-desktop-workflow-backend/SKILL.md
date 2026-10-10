---
name: cs-desktop-workflow-backend
description: Pekerjaan BACKEND yang dibutuhkan alur kerja lengkap aplikasi desktop CS Halo BCA (project `cs-halo-bca`) — sembilan belas layar dari registrasi petugas, login, kesiapan terminal (otorisasi supervisor, healthcheck perangkat, pakta integritas PII, aktivasi terminal), command center/dashboard, antrean video call, ruang panggilan e-KYC, keputusan verifikasi, penyelesaian eskalasi Tier 2, sampai logout dan penutupan loket. Skill ini adalah ANALISIS SELISIH, dan selisihnya hampir tertutup: tujuh belas dari sembilan belas layar bisa disambungkan hari ini lewat tiga puluh empat endpoint CS yang terbit. Ia mencatat apa yang SUDAH DIBANGUN beserta keputusan yang membentuknya — identitas petugas berbasis NPP+kata sandi dengan sesi bertenggat, siklus hidup terminal (REGISTERED/READY/ONLINE/OFFLINE) yang dipegang server, otorisasi dual-control supervisor, tiga gerbang kesiapan berumur 8 jam, KPI dashboard yang hanya memuat yang terukur, keputusan NEED_REVIEW beserta penutupan perkaranya oleh Tier 2 dengan four-eyes, cakupan AUDIT_READ dan ESCALATION_REVIEW, perubahan kewenangan petugas lewat PATCH /agents/{npp}, dan tiga belas peristiwa audit — serta apa yang MASIH kosong: hanya pratinjau dokumen/OCR sisi CS, pengukur SLA/CSAT, dan OIDC. Gunakan saat membangun endpoint baru untuk aplikasi desktop CS, saat menilai apakah sebuah layar bisa disambungkan hari ini, saat mengubah tabel terminal/supervisor/sesi petugas/gerbang kesiapan/eskalasi, atau saat menjawab "apa yang kurang di backend supaya alur CS bisa jalan". Untuk bentuk request/response endpoint yang sudah terbit, pakai `cs-desktop-api-integration` — bukan skill ini. Trigger juga pada "workflow CS desktop", "SCR-001".."SCR-019", "terminal readiness", "aktivasi terminal", "otorisasi supervisor", "dual-control", "four-eyes", "pakta integritas", "healthcheck perangkat", "command center", "KPI petugas", "NEED_REVIEW", "eskalasi Tier 2", "tutup eskalasi", "ESCALATION_REVIEW", "AUDIT_READ", "ESCALATION_SELF_RESOLVE", "AGENT_SELF_UPDATE", "cabut hak petugas", "queue reservation", "RESERVED", "terminal ONLINE", "logout petugas", dan "cs-halo-bca". JANGAN dipakai untuk kontrak endpoint CS yang SUDAH terbit (itu `cs-desktop-api-integration`), server video call itu sendiri (itu `buka-rekening-video-call-backend`), atau OCR/biometrik/kredensial/submit sisi nasabah (itu `buka-rekening-backend`).
---

# Backend untuk alur kerja desktop CS Halo BCA

Spesifikasi alurnya ada di project frontend:
`cs-halo-bca/docs/halo-bca-ekyc-desktop-workflow.md` — 19 layar, state machine,
7 aturan bisnis, acceptance criteria.

Skill ini **bukan** salinan dokumen itu. Ia menjawab satu pertanyaan yang tidak
dijawab dokumen itu: **apa yang harus ada di backend ini supaya alurnya nyata,
dan apa yang sudah ada sehingga tidak perlu dibangun ulang.**

---

## ATURAN #0 — Tiga puluh empat endpoint CS sudah terbit. Jangan bangun ulang.

Sebelum menulis satu baris pun, baca `.claude/skills/cs-desktop-api-integration/SKILL.md`.
Itu kontrak yang **berlaku** untuk yang sudah ada, dan daftarnya sudah jauh lebih panjang
daripada saat skill ini pertama ditulis:

| Kelompok | Endpoint | Penjaga |
|---|---|---|
| Panggilan video | `queued`, `agent-token`, `signal?token=`, `result` | kunci sistem · + petugas `VIDEO_CALL` · token sekali pakai |
| Jejak & pemantauan | `GET /v1/onboarding/sessions/{id}/audit`, `GET /v1/onboarding/monitoring` | kunci sistem |
| Identitas & sesi petugas | `auth/login`, `auth/password`, `auth/me`, `auth/logout` | kunci sistem · + identitas petugas · + `session_token` |
| Pendaftaran & kewenangan | `POST /agents`, `PATCH /agents/{npp}`, `GET /hris/employees/{npp}` | + identitas petugas |
| Terminal & 3 gerbang | `POST /terminals`, `GET /terminals/{id}`, `readiness`, `healthcheck`, `pii-ack`, `activate`, `deactivate` | kunci sistem · + identitas petugas · + `session_token` |
| Supervisor | `GET /supervisors`, `POST /supervisors/authorize` | + `session_token` |
| Beranda petugas | `GET /cs/dashboard` | + identitas petugas |
| Jejak audit petugas | `GET /cs/audit-events` | + petugas `AUDIT_READ` |
| Eskalasi Tier 2 | `GET /escalations`, `PATCH /escalations/{id}` | + petugas `ESCALATION_REVIEW` |
| Sesi onboarding | `GET /internal/v1/onboarding/sessions`, `/sessions/{id}` | + petugas `VIDEO_CALL` · `CUSTOMER_PII` |
| Nasabah, tiket, kartu | `/customers*`, `/tickets*`, `/cards*` | + petugas `CUSTOMER_PII` · `TICKET` · `CARD_ADMIN` |

Yang sudah ada dan **sering dikira belum**:

- **Reservasi antrean (Rule 5) SUDAH berjalan.** `agent-token` memindahkan
  panggilan `QUEUED → ACTIVE`, mengeluarkannya dari sorted set Redis, dan
  mengirim `agent_assigned` ke nasabah. Panggilan yang sudah diambil **hilang
  dari daftar** petugas lain. Tidak perlu status `RESERVED` terpisah; yang perlu
  diputuskan hanya apakah "RESERVED" di dokumen alur berarti sesuatu yang lain.
- **`call_ended` sudah dikirim ke KEDUA sisi** saat hasil disubmit dan saat
  panggilan dilepas.
- **Jejak audit sesi onboarding sudah ada** (`GET /sessions/{id}/audit`), begitu
  juga jejak akses PII petugas (`cs_access_logs`, migrasi 000029) dan jejak
  tindakan petugas/terminal (`cs_audit_events`, migrasi 000037).
- **Rule 3 dan Rule 4 sudah DITEGAKKAN, bukan didokumentasikan.** Status terminal
  hidup di server (`cs_terminals`, migrasi 000032), `activate` menolak kalau salah
  satu dari tiga gerbang belum lolos (`422 TERMINAL_NOT_READY`, menyebut gerbang
  mana), dan `agent-token` menolak terminal yang bukan `ONLINE`
  (`422 TERMINAL_NOT_ONLINE`).
- **Sesi petugas bertenggat sudah ada** (`cs_agent_sessions`, migrasi 000035).
  "Session Started 08:02 WIB" di §19 dokumen alur adalah baris di tabel itu.
- **`NEED_REVIEW` sudah hidup** beserta `rejection_reason` ber-enum dan baris
  eskalasi (`video_call_escalations`, migrasi 000038).
- **`GET /auth/me` sudah ada**, jadi aplikasi desktop tidak perlu lagi menyimpulkan
  cakupannya dari `403` yang pernah diterima.
- **Perkara `NEED_REVIEW` sekarang BISA DITUTUP** lewat `GET /escalations` +
  `PATCH /escalations/{id}` (migrasi 000042). Sebelumnya barisnya lahir tapi tidak ada
  satu pun jalur yang menutupnya, jadi nasabah yang dieskalasi tertahan dari antrean
  selamanya. Ia dijaga cakupan `ESCALATION_REVIEW` **dan** four-eyes: pengaju perkara
  tidak boleh menyentuh ajuannya sendiri.
- **Cakupan petugas bisa diubah dan dicabut** lewat `PATCH /agents/{npp}`, dengan
  otorisasi supervisor di body dan peristiwa `AGENT_UPDATED`. Dulu hanya lewat SQL.
- **`GET /cs/audit-events` SUDAH punya pembatas kewenangan** — cakupan `AUDIT_READ`
  (migrasi 000041). Yang masih mengiranya terbuka untuk setiap petugas membaca catatan
  yang sudah kedaluwarsa.

---

## 1. Selisih per layar

`ADA` = bisa disambungkan hari ini. `SEBAGIAN` = ada tapi kurang field/semantik.
`BELUM` = tidak ada apa pun di backend.

| Layar | Yang dibutuhkan server | Status |
|---|---|---|
| SCR-001 Registration | Lookup NPP ke HRIS, daftar supervisor, verifikasi token supervisor, info terminal, pendaftaran terminal | **ADA** — `GET /hris/employees/{npp}`, `GET /supervisors`, `POST /agents`, `POST /terminals`, `GET /terminals/{id}` |
| SCR-002 Register Success | — (tanda terima dari #1) | — `api_key` dari response `POST /agents`, **sekali tampil** |
| SCR-003/004 Login | Login petugas NPP + kata sandi → sesi bertenggat | **ADA** — `POST /auth/password` lalu `POST /auth/login` |
| SCR-005 Terminal Readiness | Keadaan kesiapan terminal (3 gerbang) | **ADA** — `GET /terminals/readiness`, `can_activate` dihitung server |
| SCR-006 Supervisor Auth | Verifikasi dual-control + penandatanganan | **ADA** — `POST /supervisors/authorize` → `authorization_ref` |
| SCR-007 Device Healthcheck | Pencatatan hasil (probe-nya di klien) | **ADA** — `POST /terminals/healthcheck` |
| SCR-008 PII Confirmation | Pencatatan pakta integritas | **ADA** — `POST /terminals/pii-ack` + `pact_version` |
| SCR-009 Terminal Activation | Transisi terminal → `ONLINE` | **ADA** — `POST /terminals/activate`, menolak gerbang tak lengkap |
| SCR-010 Dashboard | KPI (SLA, panggilan hari ini, target shift, CSAT), status terminal, telemetri | **SEBAGIAN** — `GET /cs/dashboard` ada dan lengkap untuk yang **terukur**; `sla_percent`, `csat_percent`, `shift_target` selalu `null` karena tidak ada yang mengukurnya |
| SCR-011 Queue | Daftar antrean | **ADA** — `priority` (diturunkan dari waktu tunggu) dan `service` sudah ada |
| SCR-012 Call Connecting | `agent-token` + signaling | **ADA** |
| SCR-013 Video Call Room | Signaling, data nasabah tersamar | **SEBAGIAN** — pratinjau dokumen/OCR sisi CS masih belum ada |
| SCR-014 Verification Form | Keputusan + alasan + eskalasi | **ADA** — `NEED_REVIEW`, `rejection_reason` ber-enum, `escalation_queue`; penyelesaiannya di `PATCH /escalations/{id}` |
| SCR-015 Processing | — | — |
| SCR-016 Result | `current_step` | **ADA** |
| SCR-017/018 Complete & Next | Antrean berikutnya | **ADA** (pakai `queued`) |
| SCR-019 Logout | Tutup sesi + terminal → `OFFLINE` | **ADA** — `POST /auth/logout`; istirahat pakai `POST /terminals/deactivate` |

**Kesimpulannya, dan ini berbeda dari saat skill ini pertama ditulis:** tujuh belas dari
sembilan belas layar bisa disambungkan hari ini. Dua yang masih `SEBAGIAN` tidak terhalang
pekerjaan backend yang belum dikerjakan, melainkan oleh dua hal berbeda:

- **SCR-010** kekurangan *pengukur*, bukan endpoint. SLA, CSAT, dan target shift belum
  didefinisikan siapa pun; kontraknya sudah menyediakan tempatnya dan `measured`
  menyebut mana yang nyata.
- **SCR-013** kekurangan *keputusan tentang PII*: menampilkan citra e-KTP dan selfie di
  layar petugas adalah pembukaan data yang lebih dalam daripada field tersamar, dan
  belum ada kebijakan retensi, penyamaran, maupun jejaknya.

---

## 2. Tiga keputusan arsitektural — SUDAH DIAMBIL

Ketiganya dulu tercatat di sini sebagai "jangan menebak, tanyakan dulu". Ketiganya sudah
dijawab dan sudah terbangun. Yang tertulis di bawah adalah **jawabannya beserta
alasannya**, supaya tidak dibuka ulang tanpa alasan baru.

### 2.1 Kredensial petugas → pilihan 2: kata sandi terpisah, berdampingan dengan kunci API

**Yang dibangun** (migrasi `000033_cs_agent_passwords`): kolom `password_hash` +
`password_set_at` di `cs_agents`, dan **dua kredensial dengan dua guna berbeda yang hidup
berdampingan**:

| Kredensial | Untuk | Sifat |
|---|---|---|
| `api_key_hash` | sistem-ke-sistem, header tiap permintaan | statis, tidak bertenggat |
| `password_hash` | login petugas (SCR-003) → sesi bertenggat | milik orang, bisa diganti sendiri |

Pilihan 1 (sesi di atas kunci yang ada) ditolak karena ia mencampur dua hal: kunci API
adalah kredensial *sistem* yang didistribusikan ke mesin, kata sandi adalah kredensial
*orang*. Menjadikan satu kunci melayani keduanya berarti petugas yang kata sandinya perlu
diganti juga memutus integrasi yang memakai kuncinya.

Konsekuensi yang **tidak** terjadi, dan itu disengaja: kunci API **tetap** jadi sumber
kebenaran `actor` di audit, jadi sebelas endpoint ber-`AgentAuth` **tidak berubah sama
sekali**. Token sesi tidak menggantikan apa pun — ia menjawab pertanyaan tambahan
("sedang bertugas di loket mana"), jadi ia dituntut sebagai lapis **keempat** di endpoint
yang memang menanyakan itu. Tidak ada masa transisi, tidak ada endpoint yang harus
menerima dua bentuk.

Jalur penyetelan kata sandi awal yang dulu disebut "belum ada" sekarang ada:
`POST /internal/v1/auth/password`, dijaga identitas petugas (**bukan** token sesi —
petugas yang belum punya kata sandi tidak bisa punya sesi). Minimal 12 karakter,
`422 AGENT_PASSWORD_WEAK` di bawah itu.

**Yang masih belum:** OIDC/SSO dan rotasi kunci API. Keduanya tetap pilihan 3, tetap
paling benar, tetap paling lama.

### 2.2 Status terminal → server yang memegangnya, dan Rule 4 ditegakkan di satu tempat

**Yang dibangun** (migrasi `000032_cs_terminals`): status terminal hidup di server, dan
`agent-token` menolak terminal yang bukan `ONLINE` dengan `422 TERMINAL_NOT_ONLINE`.

Alasannya tetap seperti yang tercatat dulu: kalau status terminal hanya di memory
frontend, Rule 4 adalah hiasan — petugas yang melewati layar kesiapan dengan menyunting
state klien tetap bisa mengambil panggilan. Untuk aturan yang menyangkut siapa boleh
melayani nasabah, itu tidak memadai.

Empat hal yang menentukan bentuknya, dan masing-masing punya alasan:

- **Hanya empat status yang DISIMPAN:** `REGISTERED` · `READY` · `ONLINE` · `OFFLINE`
  (CHECK `cs_terminals_status_valid`). `BUSY`, `CALL_ACTIVE`, dan `PROCESSING` di dokumen
  alur **tidak disimpan** — ketiganya diturunkan dari panggilan aktif petugasnya. Status
  tersimpan tanpa satu sumber kebenaran akan melenceng dari tabel panggilan, dan yang
  melenceng akan dipercaya.
- **`VARCHAR` + `CHECK`, bukan enum Postgres.** Menambah nilai ke enum menuntut migrasi
  yang mengunci tabel, dan daftar status di dokumen alur masih akan berubah.
- **`ONLINE` wajib punya pemegang** (CHECK `cs_terminals_online_has_agent`:
  `status <> 'ONLINE' OR (active_agent_id IS NOT NULL AND activated_at IS NOT NULL)`).
  Terminal `ONLINE` tanpa petugas adalah loket yang akan meluluskan Rule 4 tanpa ada
  orangnya.
- **Rule 4 ditegakkan di SATU tempat** — di `agent-token`, **sebelum** barisnya dibaca.
  Diperiksa lebih dulu supaya petugas yang terminalnya belum aktif tidak bisa
  menyimpulkan `queue_id` mana yang sah dari selisih pesan error.

**Gerbang kesiapan disimpan per SESI, bukan per terminal** (migrasi
`000036_cs_terminal_readiness`): gerbangnya menyatakan "orang INI, di loket INI, pada
giliran INI sudah diperiksa". Per terminal akan membuat petugas giliran berikutnya
mewarisi pakta integritas yang ditandatangani orang lain.

Dua umur yang berbeda, dan bedanya disengaja: **gerbang 8 jam** (`cs.GateTTL`) = satu
giliran kerja, sementara **sesi 9 jam** (`cs.SessionTTL`) = giliran plus margin serah
terima. Sesi yang lebih panjang berarti terminal yang ditinggalkan tetap `ONLINE` sampai
besok.

### 2.3 `NEED_REVIEW` → nasabah TETAP di `VIDEO_CALL`; yang menahannya adalah baris eskalasi

**Yang dibangun** (migrasi `000038_video_call_escalations`): keputusan ketiga ada, dan
nasabahnya **tidak dipindahkan ke langkah baru**.

Itu keputusan yang paling menentukan di sini: **tidak ada nilai baru di enum
`onboarding_step`**, jadi aplikasi Android tidak pernah menemui `current_step` yang tidak
dikenalnya. Langkah baru akan memaksa rilis APK sebelum backend boleh menyalakan fitur.

Yang menahan nasabah supaya tidak mengantre lagi adalah **baris eskalasinya**, bukan
langkahnya: `JoinQueue` menjawab `422 VIDEO_CALL_UNDER_REVIEW` selama ada eskalasi
`PENDING` — diperiksa **sebelum** jam operasional, karena nasabah yang sedang ditinjau
perlu diberi tahu bahwa perkaranya ditangani, bukan soal jam layanan.

Tiga jawaban atas tiga pertanyaan yang dulu terbuka:

| Pertanyaan dulu | Jawabannya |
|---|---|
| Menunggu di langkah mana? | `VIDEO_CALL`, sama seperti `REJECTED`. Dibedakan lewat objek `escalation` di response, **bukan** lewat `current_step` |
| Siapa yang menyelesaikannya? | Antrean eskalasi: `TIER_2_VERIFICATION` (bawaan) · `FRAUD_REVIEW` · `COMPLIANCE_REVIEW` |
| Apa yang dilihat nasabah? | "Verifikasi Anda sedang ditinjau petugas. Mohon tunggu, kami akan menghubungi Anda." — pesan `VIDEO_CALL_UNDER_REVIEW` |

Dua penjaga yang mencegah sesi menggantung, yang dulu jadi alasan menunda fase ini:

- **`notes` WAJIB** pada `NEED_REVIEW`. Eskalasi tanpa keterangan memaksa Tier 2
  mengulang seluruh panggilan untuk tahu apa yang mengganjal.
- **Repo eskalasi nil → `NEED_REVIEW` DITOLAK** (`503 PROVIDER_NOT_CONFIGURED`), bukan
  disimpan tanpa baris eskalasi. Sesi yang menggantung tanpa jalan keluar memang lebih
  buruk daripada permintaan yang ditolak — persis seperti yang tercatat dulu.

**Penyelesaiannya sekarang ADA**, dan ini yang paling penting berubah sejak versi
sebelumnya skill ini: `GET /escalations` + `PATCH /escalations/{id}` (§3.7, migrasi
`000042`). Dulu barisnya lahir dan `FindOpenBySessionID` membacanya, tapi menutupnya hanya
bisa lewat SQL langsung — artinya nasabah yang dieskalasi ditolak
`422 VIDEO_CALL_UNDER_REVIEW` **selamanya**, dan pertolongannya adalah seseorang yang
mengetik `UPDATE` di database produksi.

Empat keputusan yang membentuk penyelesaiannya, dan masing-masing punya alasan:

- **Cakupan `ESCALATION_REVIEW`, bukan `VIDEO_CALL`.** Sebuah `APPROVED` di jalur ini
  memindahkan nasabah ke `CREDENTIALS` — keputusan yang sama besar dengan keputusan
  panggilannya sendiri. Memakai cakupan yang sama dengan panggilan berarti memberikan
  tepat itu kepada setiap petugas Tier 1.
- **Four-eyes: pengaju tidak boleh menyentuh ajuannya sendiri**, dan berlaku pada `CLAIM`
  **maupun** `RESOLVE` (`403 ESCALATION_SELF_RESOLVE`). `NEED_REVIEW` adalah pernyataan
  bahwa petugas itu tidak sanggup memutuskan; membiarkannya memutus sendiri sesudahnya
  mengubah eskalasi jadi jalan memutar tanpa pemeriksaan. Melarangnya hanya di `RESOLVE`
  berarti ia boleh memegang perkaranya lebih dulu lalu ditolak di langkah terakhir —
  perkara yang kemudian tertahan atas namanya sampai seseorang menyadarinya.
- **Yang membebaskan nasabah adalah TERTUTUPNYA perkara, bukan hasilnya.** Setelah
  `REJECTED` ia boleh mengantre lagi ke Tier 1: hasil akhir yang tidak bisa diperbaiki
  sama sekali bukan hasil verifikasi, ia pemblokiran permanen tanpa jalan banding.
- **Tidak ada `action: "CANCEL"`**, meski `CANCELLED` ada di CHECK migrasi 000038. Tier 2
  yang tidak sanggup memutuskan menolak dengan `INCOMPLETE_INFORMATION` — yang sudah
  menahan nasabah di `VIDEO_CALL` sekaligus membebaskannya mengantre lagi. Keluaran ketiga
  hanya akan menambah status yang tidak punya kolom pelaku maupun waktu, yaitu penutupan
  perkara tanpa pertanggungjawaban.

---

## 3. Endpoint yang sudah dibangun

Semua di `/internal/v1` — jalur operator tidak berbagi rate limit, body limit,
dan CORS dengan jalur nasabah. Semua di belakang `InternalAPIKey`.

Bentuk request/response lengkapnya di `docs/01-API-SPECIFICATION.md` §11 dan
`.claude/skills/cs-desktop-api-integration/SKILL.md`. Yang dicatat di bawah hanya
**penjaga dan alasannya** — itu yang hilang kalau seseorang menambah endpoint baru
di sini tanpa membaca apa pun.

### 3.1 Identitas & sesi petugas

```
POST   /internal/v1/auth/login     kunci sistem SAJA      → session_token bertenggat
POST   /internal/v1/auth/password  + identitas petugas     penyetelan pertama / ganti
GET    /internal/v1/auth/me        + identitas petugas     identitas + cakupan + sesi
POST   /internal/v1/auth/logout    + session_token         tutup sesi, terminal OFFLINE
```

**Tiga penjaga berbeda di satu grup rute, dan bedanya disengaja.** `login` hanya
berpenjaga kunci sistem karena di sanalah petugas MEMBUKTIKAN dirinya; menuntut
kredensial petugas akan membuatnya harus sudah masuk untuk bisa masuk. `me` dan
`password` berpenjaga identitas petugas tanpa cakupan: `me` justru yang MEMBERI TAHU
cakupan, jadi menuntut cakupan di sana memutar balik, dan `password` adalah jalur
penyetelan PERTAMA, jadi pemanggilnya belum bisa punya sesi. `logout` berpenjaga sesi
karena yang ditutup adalah sesi yang **tokennya dikirim**, bukan sesi yang disebut di
body — tanpa itu token siapa pun bisa menutup giliran orang lain.

`GET /auth/me` menjawab masalah nyata yang tercatat di `cs-desktop-api-integration`
§1.2: aplikasi desktop dulu **tidak bisa menanyakan cakupannya**, jadi ia menyembunyikan
menu berdasarkan `403` yang pernah diterima. Satu endpoint ini menghapus seluruh
tebak-tebakan itu — dan menghapus satu verifikasi Argon2id (64 MB × 4 thread) per menu
yang dicoba.

`next_step: "TERMINAL_READINESS"` di jawaban `login` dikirim server, bukan disimpulkan
klien: aturan "sesi baru wajib lewat layar kesiapan" tidak boleh hidup hanya di routing
klien, yang bisa disunting.

### 3.2 Pendaftaran petugas & terminal

```
GET    /internal/v1/hris/employees/{npp}   + identitas petugas   lookup NPP
POST   /internal/v1/agents                 + identitas petugas   daftarkan petugas
PATCH  /internal/v1/agents/{npp}           + identitas petugas   ubah cakupan / cabut hak
POST   /internal/v1/terminals              + identitas petugas   daftarkan terminal
GET    /internal/v1/terminals/{id}         kunci sistem SAJA     baca terminal
```

**Tidak menuntut cakupan tertentu**, dan itu bukan kelalaian: mendaftarkan petugas,
mengubah kewenangannya, atau memasang terminal adalah pekerjaan supervisor/teknisi, dan
tidak satu pun dari enam cakupan yang ada menggambarkannya. Penjaganya identitas petugas
supaya pendaftar bisa disebut namanya di jejak audit — dan otorisasi supervisor di body
yang jadi gerbang sesungguhnya. Lihat §3.7 untuk bentuk `PATCH`-nya.

Otorisasi dual-control diminta di **BODY** `POST /agents` (`supervisor_id` + `token`),
bukan sebagai gerbang sesi: yang perlu ditandatangani adalah pemberian kewenangan itu,
bukan kesiapan loket si pendaftar.

`GET /terminals/{id}` berpenjaga kunci sistem saja karena klien perlu tahu terminalnya
terdaftar **sebelum** ada petugas yang masuk. Jawabannya **tidak** menyertakan siapa yang
sedang memegang terminalnya — nama petugas giliran sebelumnya bukan hal yang perlu
dibaca layar masuk.

**HRIS adalah sistem luar**, dan tidak ada tabel pegawai di repo ini. Di
`APP_ENV=development` dilayani `cs.MockHRISDirectory`; di environment lain
`503 HRIS_UNAVAILABLE`. Pola yang sama dengan OCR, Dukcapil, biometrik, dan core
banking.

**Tiga jawaban yang dibedakan**, dan jangan disatukan: NPP ada & aktif (`200`,
`active: true`), NPP ada tapi nonaktif (`200`, `active: false` — **bukan** error), NPP
tidak ditemukan (`404 EMPLOYEE_NOT_FOUND`). Menyamakan dua yang terakhir membuat pegawai
yang statusnya dicabut mengira ia salah ketik, lalu mencobanya berkali-kali. Dan
`503` untuk direktori yang mati, bukan `404`: `404` akan terbaca sebagai "NPP tidak
terdaftar", dan petugas yang mendapatkannya saat HRIS mati akan menyimpulkan hal yang
salah tentang rekannya.

`api_key` **tidak diterima dari pemanggil** — diterbitkan server, dan hanya ada di
response `201` itu. Kunci pilihan klien adalah kunci yang bisa dipilih lemah, dipakai
ulang dari sistem lain, atau sudah pernah bocor, dan tidak ada cara memeriksanya dari
sini. Nama, jabatan, dan cabang datang dari **HRIS**, bukan dari body: baris `cs_agents`
tidak boleh menyebut orang yang berbeda dari yang ada di direktori pegawai.

### 3.3 Kesiapan & siklus hidup terminal

```
GET    /internal/v1/supervisors?location=      + session_token   daftar supervisor
POST   /internal/v1/supervisors/authorize      + session_token   gerbang 1
POST   /internal/v1/terminals/healthcheck      + session_token   gerbang 2
POST   /internal/v1/terminals/pii-ack          + session_token   gerbang 3
GET    /internal/v1/terminals/readiness        + session_token   keadaan 3 gerbang
POST   /internal/v1/terminals/activate         + session_token   → ONLINE
POST   /internal/v1/terminals/deactivate       + session_token   → OFFLINE
```

**Tidak ada `{terminal_id}` di path**, dan itu keputusan keamanan, bukan gaya:
terminalnya ditentukan SESI. Petugas tidak bisa menyatakan kesiapan atas loket yang
bukan tempat ia masuk, dan tidak bisa menanyakan kesiapan loket orang lain. Kalau
`terminal_id` datang dari body, satu nilai yang disunting klien sudah cukup untuk
keduanya.

**Healthcheck diprobe KLIEN, dicatat SERVER.** Server tidak bisa mengukur kamera atau
mikrofon di meja petugas; yang bisa dilakukannya adalah mencatat pernyataan klien
beserta waktunya, lalu memakainya sebagai gerbang. Perlakukan isi `details` sebagai
**pernyataan**, bukan pengukuran — dan jangan menampilkannya di laporan kepatuhan seolah
server yang mengukurnya. `details` sengaja bebas bentuk: klien melaporkan apa yang ada
di mejanya, dan daftar bidangnya akan berubah tanpa rilis server.

`activate` **menolak** kalau salah satu gerbang belum `PASS` (Rule 3) dengan
`422 TERMINAL_NOT_READY`, dan pesannya **menyebut gerbang mana** yang kurang — tanpa itu
petugas hanya melihat "belum siap" dan harus menebak layar mana yang harus diulang.

Gerbang berumur **8 jam** (`cs.GateTTL`). Yang kedaluwarsa dihitung **belum lolos**, tapi
dibedakan di response: `expired: true` berarti pernah lolos lalu lewat tenggat,
`passed: false` berarti belum pernah. Yang pertama diselesaikan dengan mengulang probe;
yang kedua mungkin berarti satu layar dilewati.

`deactivate` **tidak menutup sesi**: petugas yang istirahat menonaktifkan loketnya tanpa
mengakhiri gilirannya. Yang pulang memanggil `auth/logout`, yang melakukan keduanya.
Menyamakan keduanya memaksa petugas mengulang ketiga gerbang sepulang istirahat.

Token supervisor **tidak pernah** ikut terbaca di `GET /supervisors` — jawabannya hanya
nama dan lokasi. Yang dikembalikan `authorize` adalah `authorization_ref`, **rujukan
tanda terima**; `#BCA-AUTH-9942` di dokumen alur adalah itu, bukan tokennya.

### 3.4 Dashboard

```
GET    /internal/v1/cs/dashboard    + identitas petugas
```

**Satu endpoint, bukan empat**: layar ini dimuat tiap petugas membuka beranda, dan empat
permintaan untuk satu layar adalah empat kali penjaga Argon2id.

**Hanya yang terukur yang diisi.** `calls_handled`, `approved`, `rejected`,
`need_review`, `avg_duration_seconds`, panjang antrean, waktu tunggu terlama, dan status
terminal — semuanya nyata. `sla_percent`, `csat_percent`, dan `shift_target` **selalu
`null`**: ketiganya angka demo di §75 dokumen alur, tidak ada definisi SLA yang
disepakati, tidak ada mekanisme pengukuran kepuasan nasabah, dan tidak ada target shift
yang ditetapkan siapa pun.

Ketiganya tetap ada di kontrak supaya aplikasi desktop tidak perlu berubah bentuk saat
pengukurnya nanti ada, dan map **`measured`** menyebut mana yang nyata. Klien yang
menemukan `false` **menyembunyikan kartunya**, bukan menampilkan `null` sebagai `0%`.
Yang juga tidak dilakukan: mengirim `0` sebagai ganti `null` — nol terbaca sebagai
"SLA-nya nol", dan itu kebohongan yang berbeda dari ketiadaan data. KPI karangan di
layar operasional akan dipakai menilai orang.

`can_take_calls` adalah **Rule 4 yang sudah dihitung server**, supaya tombol "Ambil
Panggilan" tidak perlu menyimpulkannya dari string status — dan supaya klien tidak bisa
menyimpulkan sebaliknya.

Batas "hari ini" adalah **tengah malam WIB yang dihitung aplikasi**, bukan
`CURRENT_DATE`: server bisa berjalan di UTC, dan hari kerja petugas berganti tengah
malam Jakarta. Yang dihitung adalah panggilan yang **berakhir** hari ini, dan rentangnya
setengah terbuka supaya panggilan tepat di tengah malam tidak terhitung dua kali.

### 3.5 Antrean & verifikasi: tambalan pada yang sudah dipakai

```
GET  /v1/onboarding/video-call/queued    + priority, service
POST /v1/onboarding/video-call/result    + NEED_REVIEW, rejection_reason, escalation_queue
```

Keduanya **perubahan pada endpoint yang sudah dipakai**, jadi field ditambahkan dan
bentuk yang ada tidak diubah — aplikasi desktop yang sudah berjalan membacanya.

`priority` **diturunkan dari `waited_seconds`**, tidak disimpan: `HIGH` setelah 10 menit,
`NORMAL` sebelumnya. Tidak ada sumber prioritas lain di sistem ini — nasabah yang belum
punya rekening belum punya tier apa pun — jadi kolom prioritas akan berisi nilai yang
sama di semua baris, dan kolom yang ada akan diisi lalu dipercaya. Ambang 10 menit juga
bukan selera: `estimated_wait_seconds` yang dikirim ke nasabah dihitung dari rata-rata
panggilan, jadi menunggu lebih lama dari dua kali itu berarti antreannya tidak bergerak
sebagaimana ia diberi tahu.

`service` selalu `EKYC_ONBOARDING` — ada di kontrak supaya layar petugas tidak perlu
berubah bentuk saat layanan kedua menyusul.

`rejection_reason` dibatasi enum (`IDENTITY_MISMATCH`, `INVALID_DOCUMENT`,
`FACE_MISMATCH`, `SUSPICIOUS_ACTIVITY`, `INCOMPLETE_INFORMATION`, `OTHER`), bukan teks
bebas: alasan penolakan verifikasi identitas adalah hal yang akan dilaporkan dan
dihitung, dan teks bebas membuat "KTP tidak jelas", "ktp blur", dan "dokumen tidak
terbaca" jadi tiga kategori berbeda. WAJIB saat `REJECTED`.

### 3.6 Audit (Rule 7)

```
GET    /internal/v1/cs/audit-events    + petugas AUDIT_READ   pencarian untuk pengawas
```

Dua belas tindakan Rule 7 menghasilkan peristiwa, dan **enam di antaranya sudah tercatat
di tempat lain** — tabel `cs_audit_events` (migrasi `000037`) **tidak menduplikasinya**:

| Tindakan | Tercatat di |
|---|---|
| reservasi antrean, panggilan dimulai, panggilan berakhir, keputusan verifikasi | `onboarding_audit_logs` (ber-kunci `session_id` nasabah) |
| pembukaan PII petugas | `cs_access_logs` (migrasi `000029`) |
| registrasi, login, login gagal, logout, penyetelan kata sandi, otorisasi supervisor, otorisasi gagal, healthcheck, pakta PII, pendaftaran/aktivasi/deaktivasi terminal | `cs_audit_events` (migrasi `000037`) |

Menuliskannya dua kali akan membuat setiap pemeriksaan harus memutuskan sumber mana yang
benar.

**Tidak ada `POST /cs/audit-events`**, dan itu disengaja — berbeda dari rancangan awal
yang menyebutnya. Peristiwa audit **ditulis di dalam transaksi tindakannya** oleh service
yang bersangkutan, bukan lewat HTTP: audit yang ditulis terpisah akan hilang persis pada
kegagalan yang paling perlu ditelusuri, dan endpoint penulis audit adalah endpoint yang
bisa dipakai mengarang jejak.

Jenis peristiwa yang tidak dikenal **ditolak** `400 VALIDATION_ERROR`, bukan dibiarkan
menghasilkan daftar kosong: nol baris karena salah ketik tidak bisa dibedakan dari nol
baris karena memang belum ada yang terjadi — dan yang kedua adalah kesimpulan
pemeriksaan. `from` yang lebih akhir dari `to` ditolak dengan alasan yang sama.

> **Penjaganya sudah diketatkan.** `GET /cs/audit-events` dulu hanya menuntut identitas
> petugas — satu-satunya pilihan selama CHECK `cs_agents_scopes_valid` terkunci ke empat
> nilai — jadi setiap petugas terautentikasi bisa membaca jejak rekannya. Sejak migrasi
> `000041` ia menuntut cakupan `AUDIT_READ`, dan migrasinya **tidak memberikannya kepada
> siapa pun**: endpoint ini menjawab `403` sampai seseorang diberi cakupan itu lewat
> `PATCH /agents/{npp}`.
>
> `GET /cs/dashboard` **tetap** hanya menuntut identitas petugas, dan bedanya disengaja:
> beranda menjawab tentang pekerjaan petugas sendiri, jejak audit menjawab tentang
> pekerjaan orang lain.

### 3.7 Eskalasi Tier 2 dan perubahan kewenangan petugas

```
GET    /internal/v1/escalations                   + petugas ESCALATION_REVIEW
PATCH  /internal/v1/escalations/{escalation_id}    + petugas ESCALATION_REVIEW
PATCH  /internal/v1/agents/{employee_id}          + identitas petugas
```

Tiga endpoint yang menutup tiga dari empat selisih yang dulu tercatat di bagian ini.
Keputusan di baliknya ada di §2.3 (eskalasi); yang dicatat di sini hanya penjaganya.

**Daftar eskalasi TERLAMA DULU** (`raised_at ASC`), berbeda dari setiap daftar lain di
repo ini yang terbaru dulu. Ia antrean kerja, bukan lini masa: perkara yang paling lama
menunggu adalah nasabah yang paling lama tidak bisa mengantre lagi. Tanpa `?status=`,
yang dijawab hanya yang TERBUKA — dan daftar status "terbuka" itu sama persis dengan
predikat `idx_vc_escalations_one_open` serta dengan `FindOpenBySessionID`. Tiga daftar
yang boleh berbeda akan berbeda, dan yang berbeda di sini berarti perkara yang menahan
nasabah tidak muncul di layar siapa pun.

`waited_seconds` **diturunkan**, tidak disimpan. Tidak ada label `priority` seperti di
daftar antrean panggilan: tinjauan Tier 2 memang berjam-jam, dan ambang sepuluh menit
milik antrean akan menandai SEMUA perkara `HIGH` sejak menit kesebelas — label yang
selalu menyala bukan label.

**Satu endpoint ber-`action`, bukan `/claim` dan `/resolve` terpisah.** Keduanya menulis
baris yang sama dan berbagi seluruh pemeriksaan di depan — perkaranya ada, masih terbuka,
bukan milik orang lain, bukan ajuan si pemanggil. Dua sub-path berarti dua salinan
pemeriksaan itu, dan salinan kedua adalah tempat four-eyes nanti terlupakan.

**Lima keadaan yang dibedakan**, dan tidak satu pun boleh disatukan: `404` perkaranya
tidak ada · `409 ESCALATION_ALREADY_CLOSED` sudah ditutup (muat ulang daftarnya) ·
`409 ESCALATION_CLAIMED_BY_OTHER` dipegang orang lain (`details.claimed_by_agent`
menyebut siapa) · `403 ESCALATION_SELF_RESOLVE` four-eyes · `422` bentuk keputusannya
kurang. `CLAIM` oleh pemegangnya sendiri dijawab `200` apa adanya — layar yang dibuka
ulang akan memanggilnya lagi.

Penulisannya lewat **UPDATE berkondisi**, bukan SELECT-lalu-UPDATE: statusnya dan
pemegangnya ada DI DALAM `WHERE`, jadi dua peninjau yang menekan tombolnya pada detik
yang sama tidak bisa sama-sama menang. Pemeriksaan di service tetap ada — tapi hanya
supaya penolakannya bisa menyebut sebab yang tepat.

`PATCH /agents/{npp}` berpenjaga **identitas petugas tanpa cakupan**, sama dengan
`POST /agents` dan untuk alasan yang sama: tidak satu pun dari enam cakupan
menggambarkan "boleh memberi kewenangan". Otorisasi dual-control diminta di **body**;
yang ditandatangani adalah perubahan kewenangan itu sendiri. `scopes` **mengganti**
seluruh daftar — hanya bentuk itu yang bisa dipakai mencabut — dan `is_active` sebuah
pointer, karena `false` dan "tidak disebut" adalah dua permintaan berbeda.
`403 AGENT_SELF_UPDATE` menolak petugas yang mengubah kewenangannya sendiri, diperiksa
sebelum apa pun yang mahal.

### 3.8 Yang MASIH harus dibangun

Dua hal, dan tidak satu pun menghalangi tujuh belas layar yang sudah bisa disambungkan:

| Kebutuhan | Kontrak yang perlu dibangun | Kenapa belum |
|---|---|---|
| Pratinjau dokumen/OCR sisi CS (SCR-013) | Belum berbentuk | Menampilkan citra e-KTP dan selfie di layar petugas adalah pembukaan data yang lebih dalam daripada field tersamar. **Butuh keputusan kebijakan lebih dulu**: retensi, penyamaran, jejak akses, dan siapa yang boleh. Jangan bangun sebelum itu dijawab |
| OIDC/SSO dan rotasi kunci API petugas | Belum berbentuk | Tetap pilihan 3 di §2.1 — tetap paling benar, tetap paling lama. Kata sandi per petugas dan sesi bertenggat sudah ada; yang belum adalah identitas dari penyedia luar |

Yang **tidak** akan dibangun, dan alasannya ada di §5: pengukur SLA, CSAT, dan target
shift. Ketiganya menunggu definisi, bukan kode.

Tiga yang dulu ada di tabel ini **sudah selesai** — jangan bangun ulang: cakupan
`AUDIT_READ`, penyelesaian eskalasi Tier 2, dan `PATCH /agents/{npp}`.

---

## 4. Tabel yang sudah ada

Sembilan migrasi: **`000032`–`000038`**, lalu **`000041`–`000042`**. Urutannya berbeda
dari rancangan awal di versi skill ini — yang berlaku adalah yang di bawah, bukan yang
pernah direncanakan.

| Migrasi | Tabel | Isi |
|---|---|---|
| `000032` | `cs_terminals` | `terminal_id`, `workstation`, `location`, `status`, `active_agent_id`, `activated_at`, `registered_by` |
| `000033` | `cs_agents` (+2 kolom) | `password_hash`, `password_set_at` — kata sandi petugas, **bukan** tabel baru |
| `000034` | `cs_supervisors` | supervisor + `token_hash` (Argon2id, sama seperti `cs_agents`) |
| `000035` | `cs_agent_sessions` | sesi petugas bertenggat, `terminal_id`, `started_at`, `ended_at`, `expires_at` |
| `000036` | `cs_terminal_readiness` | gerbang per SESI: supervisor/healthcheck/pii, `passed_at`, `expires_at` |
| `000037` | `cs_audit_events` | enam dari dua belas tindakan Rule 7 — enam sisanya di tabel lain, lihat §3.6 |
| `000038` | `onboarding_video_call_escalations` | perkara `NEED_REVIEW`, `escalation_queue`, `status` |
| `000041` | `cs_agents` + `cs_audit_events` (CHECK) | cakupan `AUDIT_READ` & `ESCALATION_REVIEW`, peristiwa `AGENT_UPDATED` |
| `000042` | `onboarding_video_call_escalations` (+4 kolom) | `claimed_by_agent`, `claimed_at`, `resolution_reason`, `resolution_notes` + CHECK penyelesaian |

**Migrasi berikutnya mulai dari `000043`** (terakhir:
`000042_video_call_escalation_resolution`; `000039` katalog produk dan `000040` liveness
bukan milik jalur CS). Penomoran berurutan — `ls migrations/` dulu, jangan menebak, dan
jangan menabrak nomor yang sedang dikerjakan orang lain.

Dua catatan tentang `000041`, karena keduanya menentukan cara merilisnya:

- **Tidak ada baris yang diberi cakupan baru oleh migrasinya**, dan itu disengaja:
  migrasi yang diam-diam memberi kewenangan pengawas kepada setiap petugas yang sudah ada
  akan melakukan persis hal yang pemisahan itu ada untuk mencegahnya. Akibatnya
  `GET /cs/audit-events` menjawab **403 untuk semua petugas** sampai `AUDIT_READ`
  diberikan lewat `PATCH /agents/{npp}`. Itu pengetatan yang memutus pemanggil lama,
  dengan sengaja.
- **`down`-nya mencabut cakupan itu lalu menonaktifkan** petugas yang cakupannya habis
  (`cardinality(scopes) > 0` akan menolaknya), dan ia melepas lalu memasang ulang trigger
  append-only `cs_audit_events` untuk membuang baris `AGENT_UPDATED`. Itu satu-satunya
  jalan sah membuang baris audit di repo ini, dan ia hanya ada di sana karena CHECK tidak
  bisa dipersempit sementara barisnya masih ada.

Empat catatan yang menentukan bentuknya:

- **`cs_terminals.status`** memakai `CHECK` constraint, bukan enum Postgres: menambah
  status ke enum menuntut migrasi yang mengunci tabel, dan daftar status di dokumen alur
  masih akan berubah. Yang **disimpan** hanya empat —
  `REGISTERED/READY/ONLINE/OFFLINE`. `BUSY`, `CALL_ACTIVE`, dan `PROCESSING` diturunkan
  dari panggilan aktif petugasnya; status tersimpan tanpa satu sumber kebenaran akan
  melenceng dari tabel panggilan.
- **`cs_terminals_online_has_agent`** memaksa `ONLINE` punya `active_agent_id` dan
  `activated_at`. Terminal `ONLINE` tanpa petugas adalah loket yang meluluskan Rule 4
  tanpa ada orangnya.
- **`cs_supervisors.token_hash`** — token supervisor adalah kredensial dual-control.
  Ia di-hash seperti `cs_agents.api_key_hash`, tidak pernah disimpan atau
  dikembalikan sebagai teks, dan `#BCA-AUTH-9942` di dokumen alur adalah
  **rujukan tanda terima**, bukan tokennya. Jangan menyimpan token di kolom yang
  ikut terbaca di `GET`.
- **Kata sandi jadi dua kolom di `cs_agents`, bukan tabel tersendiri** (`000033`).
  Kredensialnya milik petugas yang barisnya sudah ada; tabel terpisah hanya menambah
  join pada setiap login tanpa menjawab apa pun yang berbeda. Kunci API **tidak**
  diganti — lihat §2.1.

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

**Bagaimana aturan itu akhirnya dijalankan**, karena jawabannya ternyata bukan "hilangkan
dari kontrak":

- `sla_percent`, `csat_percent`, `shift_target` **ada di response `cs/dashboard` dan
  selalu `null`**, ditemani map `measured` yang menyebut mana yang nyata. Dipertahankan
  di kontrak supaya aplikasi desktop tidak perlu berubah bentuk saat pengukurnya nanti
  ada; `null` + `measured: false` membuat klien **menyembunyikan kartunya**. Yang tidak
  dilakukan: mengirim `0` — nol terbaca sebagai "SLA-nya nol", dan itu kebohongan yang
  berbeda dari ketiadaan data.
- Merek perangkat dan `Latency 12 ms` masuk ke `details` bebas bentuk pada
  `POST /terminals/healthcheck`, dan **dicatat sebagai pernyataan klien** — bukan diukur
  server, bukan divalidasi terhadap daftar merek. Tidak ada kolom `camera_brand` di mana
  pun.
- `Face Match 98.7%` **tidak dibangun sama sekali.** Tidak ada field untuknya di
  `video-call/result` maupun di detail sesi sisi CS.
- `#WKS-SMG-0842` jadi format `terminal_id` yang dipakai seeder development, dan
  `cs_terminals` tidak pernah mengarang barisnya — terminal lahir dari
  `POST /terminals`.

---

## 6. Urutan pengerjaan — sudah dijalankan

Tujuh tahap di bawah **sudah selesai semuanya**, dalam urutan ini. Dicatat bukan sebagai
rencana melainkan sebagai riwayat: siapa pun yang menambah lapisan berikutnya di atasnya
perlu tahu apa yang bergantung pada apa.

1. **`GET /auth/me`** — paling kecil, langsung menghapus tebak-tebakan cakupan di
   desktop. Tidak butuh tabel baru. ✅
2. **`cs_terminals` + aktivasi/deaktivasi + penjaga di `agent-token`** — ini yang
   membuat Rule 3 dan Rule 4 bisa ditegakkan. Tanpa ini, seluruh layar kesiapan
   hanyalah animasi. ✅
3. **Sesi petugas** (`/auth/login`, `/auth/logout`) — sesudah keputusan §2.1. ✅
4. **Supervisor + gerbang kesiapan** — menuntut #2 dan #3 lebih dulu. ✅
5. **Dashboard** — hanya metrik yang terukur. ✅
6. **`NEED_REVIEW` + alasan penolakan** — sesudah keputusan §2.3. ✅
7. **Pendaftaran petugas & HRIS** — terakhir, karena bergantung sistem luar dan
   paling jarang dipakai (sekali per petugas). ✅

Urutan itu terbukti benar pada satu hal khususnya: **#2 sebelum #3**. Status terminal
harus sudah hidup di server sebelum sesi petugas lahir, karena sesi membawa
`terminal_id` dan `auth/login` menolak terminal yang tidak terdaftar
(`404 TERMINAL_NOT_FOUND`). Dibalik urutannya, sesi akan lahir menunjuk loket yang belum
punya baris.

**Tahap 8 juga sudah selesai**, dalam urutan yang disarankan versi sebelumnya skill
ini: penyelesaian eskalasi Tier 2 lebih dulu (§2.3, §3.7) — nasabah yang eskalasinya
menggantung tidak bisa mengantre lagi, jadi itu yang paling mendesak — lalu cakupan
`AUDIT_READ`, lalu `PATCH /agents/{npp}`. Ketiganya satu rilis, dan urutan ITU juga
terbukti benar pada satu hal: `AUDIT_READ` sengaja tidak diberikan kepada siapa pun oleh
migrasinya, jadi tanpa `PATCH /agents/{npp}` di rilis yang sama tidak akan ada satu pun
petugas yang bisa membaca jejak audit lagi.

**Yang tersisa ada di §3.8**, dan keduanya menunggu hal di luar kode: pratinjau dokumen
sisi CS menunggu keputusan kebijakan PII, OIDC menunggu penyedia identitas.

Layar desktop yang sudah ada **tidak boleh rusak** di tahap mana pun. Tambahkan field,
jangan mengubah yang terbit — itu berlaku untuk ketiga puluh empat endpoint sekarang,
bukan hanya untuk antrean, panggilan, dan hasil.

**Satu pengecualian yang sudah diambil, dan harus diketahui:** pengetatan
`GET /cs/audit-events` ke `AUDIT_READ` MEMUTUS pemanggil lama. Itu dipilih dengan sadar —
jejak pengawasan yang terbuka bagi semua yang diawasinya bukan pembatas kewenangan, dan
mempertahankan kompatibilitasnya berarti mempertahankan lubangnya.

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
| Terminal, sesi petugas, gerbang kesiapan | `migrations/000032`, `000035`, `000036`; `internal/domain/cs/terminal.go`, `terminal_service.go`, `agent_session.go`, `agent_session_service.go` |
| Kata sandi & supervisor | `migrations/000033`, `000034`; `cs.MinPasswordLen`, `cs.MaxFailedLogins`, `cs.LoginLockout` |
| Peristiwa audit petugas & terminal | `migrations/000037_cs_audit_events.up.sql` |
| Eskalasi `NEED_REVIEW` | `migrations/000038_video_call_escalations.up.sql`; `onboarding.VideoCallEscalation`, `VideoCallEscalationRepository` |
| Penyelesaian eskalasi Tier 2 | `migrations/000042_video_call_escalation_resolution.up.sql`; `internal/domain/onboarding/escalation_service.go`, `escalation_service_test.go`; `internal/handler/cs_escalation_handler.go` |
| Cakupan `AUDIT_READ` & `ESCALATION_REVIEW` | `migrations/000041_cs_scope_audit_read.up.sql`; `cs.AllScopes`, `middleware.ScopeAuditRead`, `middleware.ScopeEscalationReview` |
| Perubahan kewenangan petugas | `cs.AgentUpdate`, `AgentSessionService.UpdateAgent`, `postgres.CSAgentRegistryRepo.Update` |
| Petugas seed untuk menguji four-eyes | `scripts/seed/main.go` — `SPV-3001` & `SPV-3002`, dua peninjau karena four-eyes tidak bisa diuji dengan satu orang |
| Umur sesi & gerbang | `cs.SessionTTL` (9 jam), `cs.GateTTL` (8 jam) |
| Direktori pegawai (mock) | `cs.MockHRISDirectory`, digerbangi `APP_ENV=development` |
| Aplikasi desktop yang mengonsumsinya | project `cs-halo-bca` |
