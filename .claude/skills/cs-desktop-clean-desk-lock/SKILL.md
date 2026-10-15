---
name: cs-desktop-clean-desk-lock
description: Membangun API kunci meja petugas CS (clean desk policy / Fase 3 panduan implementasi `cs-halo-bca`) — satu-satunya celah backend yang tersisa untuk aplikasi desktop petugas, dan satu-satunya yang TIDAK tercatat di analisis selisih `cs-desktop-workflow-backend`. Memuat rancangan lengkap: PIN meja 6 digit per petugas (`desk_pin_hash` Argon2id di `cs_agents`, terpisah dari `password_hash` dan `api_key_hash`), status terminal `LOCKED` sebagai status kelima di samping REGISTERED/READY/ONLINE/OFFLINE, lima endpoint (`POST /internal/v1/auth/desk-pin`, `POST /terminals/lock`, `POST /terminals/unlock`, `GET /terminals/lock-state`, `POST /terminals/unlock-override`), pembekuan loket setelah tiga kali gagal dengan pembukaan dual-control supervisor, empat peristiwa audit baru (`DESK_PIN_SET`, `DESK_LOCKED`, `DESK_UNLOCKED`, `DESK_UNLOCK_FAILED`), serta aturan yang wajib ditegakkan service — panggilan video yang sedang berjalan TIDAK diputus oleh penguncian, antrean berhenti mengalir ke loket terkunci lewat `can_take_calls`, dan sesi petugas tetap hidup beserta tenggatnya. Gunakan saat membangun atau mengubah penguncian layar petugas, saat menambah status terminal, saat menambah kredensial petugas ketiga, atau saat menjawab "kenapa aplikasi desktop CS tidak bisa memverifikasi PIN petugas". Memuat juga koreksi atas `docs/panduan-workflow-implementasi-halo-bca-ekyc.md` di repo `cs-halo-bca`, yang menyebut Fase 5 (CRM nasabah dan tiket layanan) belum punya endpoint padahal `/internal/v1/customers`, `/internal/v1/tickets`, dan `/internal/v1/escalations` sudah terbit. Trigger juga pada "kunci layar petugas", "clean desk policy", "layar terkunci", "SCREEN_2", "Fase 3", "PIN petugas", "desk PIN", "desk_pin_hash", "terminal LOCKED", "bekukan loket", "DESK_LOCKED", "DESK_UNLOCK_FAILED", "unlock-override", "UU PDP", "petugas meninggalkan meja", dan "lock state petugas CS". JANGAN dipakai untuk kontrak 34 endpoint CS yang sudah terbit (itu `cs-desktop-api-integration`), analisis selisih 19 layar desktop (itu `cs-desktop-workflow-backend`), PIN nasabah di aplikasi Android (itu `bca-mobile-backend`), atau tirai lokal di sisi renderer Electron (itu repo `cs-halo-bca`, `src/features/lock/`).
---

# Kunci Meja Petugas CS — API Fase 3

Skill ini **merancang yang belum ada**, bukan mendokumentasikan yang sudah
jalan. Seluruh isinya dibaca dari kode repo ini — `migrations/`,
`internal/domain/cs/`, `internal/router/router.go`, `internal/pkg/apperr/` —
dan dari aplikasi desktop di repo `cs-halo-bca` yang akan memakainya.

Tunduk pada **ATURAN #1 `CLAUDE.md`**: setiap file baru di bawah ini
dikonfirmasi dulu ke user, satu per satu, sebelum dibuat.

---

## 0. Duduk perkara — apa yang sudah ada, apa yang belum

`docs/panduan-workflow-implementasi-halo-bca-ekyc.md` di repo `cs-halo-bca`
membagi pekerjaan jadi enam fase. Dibandingkan `internal/router/router.go`
hari ini:

| Fase | Kata panduan | Kenyataan di repo ini |
|---|---|---|
| 0 | Design system & app shell | Tidak menyentuh backend |
| 1 | Autentikasi & kesiapan loket | **ADA lengkap** — `auth/login`, `auth/me`, `auth/password`, `auth/logout`, `POST`/`PATCH /agents`, `hris/employees/{npp}`, `supervisors`, `supervisors/authorize`, `terminals` + tiga gerbang |
| 2 | Inti e-KYC | **ADA** — `video-call/queued`, `agent-token`, `signal` (WS), `result`, `monitoring`, `cs/dashboard` |
| 3 | **Kunci meja & clean desk** | **TIDAK ADA APA PUN.** Inilah isi skill ini |
| 4 | Audit & katalog kartu | **ADA** — `sessions/{id}/audit`, `cs/audit-events`, `/internal/v1/cards` |
| 5 | CRM nasabah & tiket — *"belum punya endpoint backend, tunggu rilis resmi"* | **Panduannya usang.** `/internal/v1/customers` + `/{user_id}`, `/internal/v1/tickets` (lima route termasuk `/notes`), `/internal/v1/escalations` (dua route) **sudah terbit**, lengkap dengan cakupan `CUSTOMER_PII`/`TICKET`/`ESCALATION_REVIEW` dan pencatatan `cs_access_logs` |

**Konsekuensi langsungnya: "membangun API Fase 3 sampai Fase 5" menyusut jadi
Fase 3 saja.** Fase 4 dan 5 tidak butuh satu endpoint baru pun — yang kurang
adalah *layar* di `cs-halo-bca`, bukan kontrak di sini. Siapa pun yang diminta
"membuat API Fase 5" harus menolak dan menunjuk ke `cs-desktop-api-integration`
lebih dulu; membangunnya ulang akan menduplikasi jalur yang sudah diaudit.

### 0.1 Kenapa ini tidak tercatat sebagai selisih

`cs-desktop-workflow-backend` §3.8 menyebut dua hal yang masih harus dibangun —
pratinjau dokumen/OCR sisi CS, dan OIDC/SSO. Kunci meja **tidak disebut sama
sekali**, di bagian mana pun. Ia bukan backlog yang ditunda, melainkan titik
buta: dokumen alur 19 layar memang tidak pernah memuat layar terkunci, karena
layar itu lahir belakangan dari desain Stitch `SCREEN_2`.

Saat skill ini selesai dikerjakan, **§3.8 skill itu ikut diperbarui** — satu
baris, agar selisihnya tidak hilang lagi dari catatan.

### 0.2 Yang sudah dibangun di sisi klien, dan kenapa itu tidak cukup

`cs-halo-bca` sudah punya tirai lokal di `src/features/lock/`: overlay
blur, PIN 6 digit yang dipilih di awal giliran, disimpan **di memori renderer
saja**, dibandingkan lokal. Tirai itu menahan **mata orang lewat**. Yang tidak
bisa dilakukannya, dan hanya bisa dijawab server:

1. **Antrean tetap mengalir ke loket yang ditinggalkan.** Server tidak tahu
   petugasnya tidak di tempat, jadi `can_take_calls` tetap `true` dan nasabah
   tetap diarahkan ke meja kosong.
2. **Tidak ada jejak.** Tidak ada baris audit yang bisa menjawab "jam berapa
   loket itu ditinggalkan, berapa lama, dan siapa yang membukanya kembali" —
   padahal justru itu yang ditanyakan pemeriksa saat kepatuhan UU PDP No. 27
   Tahun 2022 diperiksa.
3. **Hilang saat proses mati.** Renderer di-reload, Electron di-restart, PC
   mati — tirainya ikut hilang bersama PIN-nya, dan layar terbuka lagi.

---

## 1. Tiga keputusan yang membentuk kontrak ini

### 1.1 PIN meja adalah kredensial KETIGA, bukan kata sandi yang dipakai ulang

`cs_agents` sudah memegang dua kredensial dengan dua guna berbeda, dan
migrasi `000033` sudah menuliskan alasannya: `api_key_hash` untuk
sistem-ke-sistem, `password_hash` untuk membuka giliran. PIN meja adalah yang
ketiga, dan bedanya bukan kerapian:

- **Kata sandi dipakai sekali per giliran; PIN dipakai belasan kali per hari.**
  Memaksa mengetik kata sandi minimal 12 karakter setiap kembali dari toilet
  adalah cara paling pasti membuat petugas memilih kata sandi yang lemah, atau
  tidak mengunci layarnya sama sekali. Kenyamanan di sini adalah soal keamanan,
  bukan soal rasa.
- **Keduanya membuka hal yang berbeda.** Kata sandi membuka **giliran** — ia
  menerbitkan `session_token` dan memindahkan terminal ke `ONLINE`. PIN hanya
  mengangkat **tirai** atas giliran yang sudah terbuka. Kredensial yang
  membuka lebih sedikit boleh lebih murah.
- **Kebocorannya berbeda harganya.** PIN enam digit yang terlihat dari balik
  bahu membuka satu layar di satu meja selama giliran itu. Kata sandi yang
  bocor membuka giliran baru di loket mana pun.

PIN **tidak pernah** boleh jadi jalan masuk `POST /auth/login`. Ia hanya
diterima oleh endpoint di §3, dan hanya untuk terminal yang sesi pemanggilnya
sendiri sedang membukanya.

### 1.2 Yang dikunci adalah TERMINAL, bukan sesi

Dua model yang mungkin, dan pilihannya menentukan segalanya:

| Model | Akibatnya |
|---|---|
| Kunci = menutup sesi | Petugas harus login ulang penuh. Terminal jadi `OFFLINE`, gerbang kesiapan berumur 8 jam harus diulang, dan panggilan yang sedang berjalan putus |
| **Kunci = status terminal baru (dipilih)** | Sesi tetap hidup beserta tenggatnya, gerbang kesiapan tidak diulang, panggilan tidak putus, antrean berhenti mengalir |

Jadi `LOCKED` menjadi **status kelima** `cs_terminals`, di samping
`REGISTERED`, `READY`, `ONLINE`, `OFFLINE`. Hanya terminal `ONLINE` yang bisa
dikunci, dan membuka kunci mengembalikannya ke `ONLINE` — bukan ke `READY`,
karena gerbangnya tidak pernah batal.

Satu akibat yang menyenangkan dan tidak perlu kode tambahan:
`dashboard_service.go:127` menghitung `CanTakeCalls: t.Status == TerminalOnline`.
Begitu statusnya `LOCKED`, `can_take_calls` jadi `false` dengan sendirinya, dan
Rule 4 tetap dihitung di satu tempat.

### 1.3 Yang TIDAK dijanjikan kunci ini — tulis ini di response

Kunci ini **bukan pengamanan mesin**. Siapa pun yang memegang PC-nya bisa
menutup aplikasi, membuka devtools, atau membaca memori. Yang dijamin:

- Layar petugas tidak lagi menampilkan video, nama, dan NIK nasabah.
- Loket berhenti menerima panggilan baru.
- Ada baris audit yang mencatat kapan dan oleh siapa.

Yang **tidak** dijamin: kerahasiaan data terhadap orang yang menguasai
terminal fisiknya. Itu pekerjaan kebijakan perangkat (screen lock OS, disk
encryption), bukan API ini. Jangan menulis kalimat di response atau dokumen
yang terbaca sebagai janji lebih besar dari ini.

---

## 2. Migrasi

Nomor berikutnya **000043** (terakhir terpakai: `000042_video_call_escalation_resolution`).
Bikin lewat `make migrate-create NAME=cs_desk_lock` supaya pasangan
`.up.sql`/`.down.sql` lahir bersama.

### 2.1 `000043_cs_desk_lock.up.sql`

Tiga perubahan dalam satu migrasi — ketiganya satu keputusan dan tidak masuk
akal dipisah:

**(a) PIN meja di `cs_agents`.** Mengikuti pola `000033` persis:

```sql
ALTER TABLE cs_agents
    ADD COLUMN desk_pin_hash   TEXT,
    ADD COLUMN desk_pin_set_at TIMESTAMPTZ,
    ADD COLUMN failed_unlock_attempts INTEGER NOT NULL DEFAULT 0;

-- Stempel terikat nilainya, seperti cs_agents_password_consistent.
ALTER TABLE cs_agents
    ADD CONSTRAINT cs_agents_desk_pin_consistent CHECK (
        (desk_pin_hash IS NULL) = (desk_pin_set_at IS NULL)
    );
```

`desk_pin_hash` NULL = petugas belum pernah menyetel PIN. Itu keadaan sah bagi
setiap baris yang sudah ada, dan `POST /terminals/lock` menolaknya dengan kode
tersendiri — bukan "PIN salah".

**Tidak ada `desk_locked_until`.** Lockout berbasis waktu salah tempat di sini:
petugas yang lupa PIN-nya dan harus menunggu lima belas menit di depan meja
sendiri adalah loket yang mati lima belas menit. Setelah tiga kali gagal,
loketnya **beku sampai supervisor membukanya** (§3.5) — orang, bukan jam.

**(b) Status `LOCKED` di `cs_terminals`.** Perluas CHECK yang ada di `000032`:

```sql
ALTER TABLE cs_terminals DROP CONSTRAINT cs_terminals_status_valid;
ALTER TABLE cs_terminals ADD CONSTRAINT cs_terminals_status_valid CHECK (
    status IN ('REGISTERED', 'READY', 'ONLINE', 'OFFLINE', 'LOCKED')
);

ALTER TABLE cs_terminals
    ADD COLUMN locked_at     TIMESTAMPTZ,
    ADD COLUMN locked_reason VARCHAR(24);

ALTER TABLE cs_terminals
    ADD CONSTRAINT cs_terminals_locked_consistent CHECK (
        (status = 'LOCKED') = (locked_at IS NOT NULL)
    );
ALTER TABLE cs_terminals
    ADD CONSTRAINT cs_terminals_locked_reason_valid CHECK (
        locked_reason IS NULL OR locked_reason IN ('AGENT', 'IDLE', 'FAILED_UNLOCK')
    );
```

**Periksa dulu `cs_terminals_online_consistent`** (`000032` baris 49): ia
menuntut `active_agent_id IS NOT NULL` saat `ONLINE`. Terminal `LOCKED` **tetap
memegang `active_agent_id`** — petugasnya masih bertugas, hanya sedang tidak di
tempat — jadi constraint itu tidak perlu diubah. Jangan melepas
`active_agent_id` saat mengunci; melepasnya akan membuat `unlock` tidak tahu
siapa yang berhak membuka.

Perhatikan juga indeks parsial `idx_cs_terminals_status ... WHERE status IN
('READY','ONLINE')`. `LOCKED` sengaja **tidak** ditambahkan ke sana: loket
terkunci bukan kandidat penugasan, dan itulah inti perubahan ini.

**(c) Empat peristiwa audit** di CHECK `cs_audit_events` (`000037`):

```sql
ALTER TABLE cs_audit_events DROP CONSTRAINT cs_audit_events_type_valid;
ALTER TABLE cs_audit_events ADD CONSTRAINT cs_audit_events_type_valid CHECK (
    event_type IN (
        'AGENT_REGISTERED', 'AGENT_LOGIN', 'AGENT_LOGIN_FAILED', 'AGENT_LOGOUT',
        'AGENT_PASSWORD_SET', 'SUPERVISOR_AUTHORIZED', 'SUPERVISOR_AUTH_FAILED',
        'DEVICE_HEALTHCHECK', 'PII_ACKNOWLEDGED', 'TERMINAL_REGISTERED',
        'TERMINAL_ACTIVATED', 'TERMINAL_DEACTIVATED',
        'DESK_PIN_SET', 'DESK_LOCKED', 'DESK_UNLOCKED', 'DESK_UNLOCK_FAILED'
    )
);
```

### 2.2 `.down.sql`

Turunkan terbalik, dan **lepas `LOCKED` paling akhir setelah memindahkan
barisnya**, atau CHECK-nya gagal dipasang:

```sql
UPDATE cs_terminals SET status = 'ONLINE', locked_at = NULL, locked_reason = NULL
WHERE status = 'LOCKED';
```

Peristiwa audit `DESK_*` yang sudah telanjur ada **tidak bisa dihapus** —
tabelnya append-only lewat trigger. Jadi `down` untuk bagian (c) harus
menghapus barisnya lebih dulu dengan hak yang melewati trigger, atau membiarkan
CHECK lama gagal. Pilih yang pertama dan tulis alasannya di file; jangan
melonggarkan trigger.

---

## 3. Lima endpoint

Semuanya di bawah `/internal/v1`, di belakang `X-Internal-API-Key`. Empat dari
lima memakai **AgentSession** (Bearer `session_token`) karena terminalnya
ditentukan sesi — pola yang sama dengan tiga gerbang kesiapan, dan alasan yang
sama: petugas tidak boleh mengunci atau membuka loket orang lain.

Tidak satu pun menuntut cakupan. Mengunci meja sendiri bukan kewenangan yang
dibagi-bagi; keenam cakupan yang ada tidak menggambarkannya, sama seperti
`cs/dashboard`.

### 3.1 `POST /internal/v1/auth/desk-pin` — setel / ganti PIN

Penjaga: **AgentSession**. Letakkan di grup `/auth` yang sudah ada, bersama
`/password`, bukan di `/terminals` — yang disetel milik **petugas**, bukan milik
loket, dan ikut pindah saat ia pindah meja.

```jsonc
// request
{ "current_pin": "481902",   // wajib HANYA bila PIN sudah pernah disetel
  "new_pin": "730514" }

// 200
{ "data": { "desk_pin_set_at": "2026-10-14T02:11:04Z" }, "meta": { ... } }
```

| Kode | HTTP | Kapan |
|---|---|---|
| `VALIDATION_ERROR` | 422 | `new_pin` bukan tepat 6 digit angka |
| `CRED_WEAK_PIN` | 422 | berurutan (`123456`) atau berulang (`111111`) — aturannya sudah ada, lihat catatan di bawah |
| `AGENT_CREDENTIAL_INVALID` | 403 | `current_pin` salah saat mengganti |
| `AGENT_SESSION_INVALID` | 401 | Bearer tidak berlaku |

**Soal validator PIN lemah — periksa sebelum menulis.** Aturannya sudah ada
dan persis sama (tepat 6 digit, bukan satu angka berulang, bukan berurutan):
`validatePIN` di `internal/domain/onboarding/credential_service.go:224`,
bersama `isAllSameChar`, `isSequential`, dan `pinRegex`. Masalahnya ia
**unexported di paket domain lain**, jadi `internal/domain/cs` tidak bisa
memanggilnya.

Yang benar: **pindahkan keempatnya ke `internal/pkg/`** (mis.
`internal/pkg/pinrule`) dan panggil dari kedua sisi. Jangan menyalin — dua
salinan aturan PIN akan menyimpang, dan yang menyimpang diam-diam adalah
aturan keamanan. Pemindahan ini menyentuh jalur kredensial **nasabah**, jadi
test `credential_service_test.go` wajib tetap hijau dan itu bagian dari
langkah ini, bukan sesudahnya.

Hash dengan `PasswordHasher` yang sudah ada (`internal/domain/cs/agent_session_service.go:23`)
— Argon2id, antarmuka yang sama, **jangan bikin hasher kedua**. Enam digit
memang ruang tebak kecil; yang menjaganya adalah pembekuan setelah tiga kali
gagal di §3.3, bukan biaya hash-nya.

Setel PIN **mereset `failed_unlock_attempts` ke 0**. Peristiwa: `DESK_PIN_SET`.

### 3.2 `POST /internal/v1/terminals/lock`

Penjaga: **AgentSession**.

```jsonc
// request
{ "reason": "AGENT" }   // AGENT | IDLE. Default AGENT bila kosong

// 200
{ "data": { "terminal_id": "CS-FL-04", "status": "LOCKED",
            "locked_at": "2026-10-14T02:45:00Z", "reason": "AGENT" } }
```

| Kode | HTTP | Kapan |
|---|---|---|
| `AGENT_DESK_PIN_NOT_SET` | 422 | **baru** — petugas belum punya PIN, jadi tidak akan bisa membuka sendiri |
| `TERMINAL_NOT_ONLINE` | 409 | **baru** — hanya `ONLINE` yang bisa dikunci |

**Idempoten**: mengunci loket yang sudah `LOCKED` menjawab `200` dengan keadaan
yang sama, bukan `409`. Klien akan memanggil ini dari dua jalur (tombol dan
pintasan papan tik) dan balapan di antaranya tidak boleh berbunyi seperti galat.

Peristiwa: `DESK_LOCKED`, `details` memuat `reason` dan `active_queue_id` bila
ada panggilan berjalan.

### 3.3 `POST /internal/v1/terminals/unlock`

Penjaga: **AgentSession**.

```jsonc
// request
{ "pin": "730514" }

// 200
{ "data": { "terminal_id": "CS-FL-04", "status": "ONLINE",
            "locked_duration_seconds": 184 } }
```

| Kode | HTTP | Kapan |
|---|---|---|
| `AGENT_DESK_PIN_INVALID` | 403 | **baru** — PIN salah. `details.attempts_remaining` ikut dikirim |
| `TERMINAL_DESK_FROZEN` | 423 | **baru** — tiga kali gagal; hanya §3.5 yang membukanya |
| `TERMINAL_NOT_LOCKED` | 409 | **baru** — tidak sedang terkunci |

Gagal ketiga kali: `failed_unlock_attempts` jadi 3, `locked_reason` berubah jadi
`FAILED_UNLOCK`, statusnya **tetap `LOCKED`**. Tidak ada status `FROZEN`
terpisah — pembekuan adalah alasan terkunci, bukan keadaan baru, dan menambah
status keenam akan memaksa setiap pembaca status memeriksa dua hal.

Berhasil: `failed_unlock_attempts` kembali 0, `locked_at`/`locked_reason` jadi
NULL, status `ONLINE`. Peristiwa: `DESK_UNLOCKED` atau `DESK_UNLOCK_FAILED`.

**`DESK_UNLOCK_FAILED` wajib ditulis pada setiap kegagalan, bukan hanya yang
ketiga.** Pola percobaan gagal yang berhenti di dua, berulang tiap hari, di
loket yang sama, adalah justru yang dicari pemeriksa.

### 3.4 `GET /internal/v1/terminals/lock-state`

Penjaga: **AgentSession**. Ini yang membuat tirai selamat dari proses yang mati.

```jsonc
// 200
{ "data": { "locked": true, "locked_at": "...", "reason": "AGENT",
            "attempts_remaining": 2, "frozen": false,
            "desk_pin_set": true } }
```

Klien memanggilnya **sekali saat pemulihan sesi** — di `cs-halo-bca`, di
tempat yang sama dengan `auth/me` — supaya aplikasi yang baru dibuka kembali
mengangkat tirainya lagi alih-alih membuka layar nasabah.

Jangan jadikan ini endpoint polling. Keadaan kunci hanya berubah oleh tindakan
petugas itu sendiri, dan klien sudah tahu tindakannya sendiri.

### 3.5 `POST /internal/v1/terminals/unlock-override`

Penjaga: **AgentSession**. Jalan keluar dari loket beku, dengan dual-control
yang sudah ada — **jangan bikin mekanisme kedua**.

```jsonc
// request
{ "authorization_ref": "auth_7f3a…",   // dari POST /supervisors/authorize
  "password": "…",                     // kata sandi petugas, bukan PIN
  "new_pin": "204817" }                // opsional: sekalian setel ulang
```

Dua hal diminta bersamaan dan keduanya perlu: `authorization_ref` membuktikan
**supervisor hadir**, `password` membuktikan **petugasnya masih orang yang
sama**. Supervisor sendirian tidak boleh membuka meja orang lain lalu
meninggalkannya terbuka; petugas sendirian sudah terbukti tidak ingat PIN-nya.

| Kode | HTTP | Kapan |
|---|---|---|
| `SUPERVISOR_TOKEN_INVALID` | 403 | `authorization_ref` tidak berlaku/kedaluwarsa — kode yang sudah ada |
| `AGENT_CREDENTIAL_INVALID` | 403 | kata sandi salah |
| `AGENT_LOCKED` | 423 | kata sandi salah berkali-kali — jalur lockout lama tetap berlaku |

Peristiwa: `DESK_UNLOCKED` dengan `details.override = true` dan
`details.authorization_ref`.

---

## 4. Enam aturan yang ditegakkan service, bukan handler

Ini bagian yang paling mudah salah. Keenamnya ditulis sebagai test lebih dulu.

1. **Panggilan yang sedang berjalan TIDAK diputus oleh penguncian.** Ini bukan
   kelonggaran — ini keharusan. Petugas mengunci layar karena ada orang lain
   mendekat, bukan karena ingin memutus nasabah yang sedang diverifikasi.
   `video_call_sessions` tidak disentuh, hub signaling tidak ditutup, dan
   `call_ended` **tidak** dikirim. Sisi klien sudah dibangun dengan anggapan ini
   (`cs-halo-bca`, `useCallSession.ts` — `socket_closed` bukan penanda bongkar).
2. **Antrean berhenti mengalir, tanpa kode baru.** `can_take_calls` sudah
   `t.Status == TerminalOnline`. Yang perlu diperiksa: setiap tempat lain yang
   memilih terminal kandidat harus memakai pembacaan status yang sama, bukan
   `status <> 'OFFLINE'`. Telusuri dulu sebelum menulis.
3. **Sesi tidak diperpanjang dan tidak dihentikan.** `expires_at` berjalan terus
   selama terkunci. Giliran sembilan jam yang habis saat layar terkunci berakhir
   seperti biasa — dan `unlock` atas sesi yang sudah lewat tenggat menjawab
   `AGENT_SESSION_INVALID`, bukan membuka tirainya.
4. **`last_seen_at` tidak disentuh saat terkunci.** Loket terkunci adalah loket
   yang tidak ditunggui; menandainya "baru terlihat" tiap kali tirainya
   diperiksa akan membuat deteksi sesi terlantar buta.
5. **Logout saat terkunci tetap boleh, dan mengakhiri segalanya.** Terminal
   langsung `OFFLINE`, `locked_at`/`locked_reason` jadi NULL. Petugas yang
   ingat PIN-nya hanya setelah pulang tidak boleh meninggalkan loket beku untuk
   giliran berikutnya.
6. **Loket beku tidak menghalangi petugas LAIN membuka giliran di situ.**
   `failed_unlock_attempts` menempel di **petugas**, bukan di terminal. Petugas
   pengganti yang login dengan kata sandinya sendiri menjalankan ulang gerbang
   kesiapan dan mendapat terminal `ONLINE` bersih. Yang beku adalah tirai milik
   petugas sebelumnya, dan ia ikut selesai saat sesinya ditutup `SUPERSEDED`.

---

## 5. Kode galat baru

Tambahkan ke `internal/pkg/apperr/apperr.go`, mengikuti gaya yang ada —
pesan Bahasa Indonesia, cukup spesifik untuk ditampilkan apa adanya di layar
petugas:

```go
AgentDeskPINNotSet  = Error{http.StatusUnprocessableEntity, "AGENT_DESK_PIN_NOT_SET", "PIN meja belum disetel. Setel PIN sebelum mengunci layar.", nil}
AgentDeskPINInvalid = Error{http.StatusForbidden, "AGENT_DESK_PIN_INVALID", "PIN meja salah.", nil}
TerminalDeskFrozen  = Error{http.StatusLocked, "TERMINAL_DESK_FROZEN", "Loket dibekukan setelah tiga kali PIN salah. Perlu otorisasi supervisor.", nil}
TerminalNotOnline   = Error{http.StatusConflict, "TERMINAL_NOT_ONLINE", "Loket tidak sedang daring.", nil}
TerminalNotLocked   = Error{http.StatusConflict, "TERMINAL_NOT_LOCKED", "Loket tidak sedang terkunci.", nil}
```

`AgentDeskPINInvalid` mengisi `details.attempts_remaining` lewat pola
`lockedError()` yang sudah ada di `agent_session_service.go:342` — salin
polanya, jangan membangun cara kedua menyisipkan `details`.

---

## 6. Urutan kerja

Tiap langkah diselesaikan sampai `make check` hijau sebelum lanjut.

1. **Migrasi `000043`** (§2) + `make migrate-up` + `make migrate-status`.
2. **Entity & repository** — `internal/domain/cs/terminal.go` (status `LOCKED`,
   dua kolom baru), `entity.go` (tiga kolom `cs_agents`), lalu implementasinya
   di `internal/repository/postgres/`. Belum ada endpoint di sini.
3. **Pindahkan aturan PIN lemah** ke `internal/pkg/` (§3.1). Langkah tersendiri
   dan dikerjakan **sebelum** service, karena ia menyentuh jalur kredensial
   nasabah: selesai artinya `go test ./internal/domain/onboarding/...` hijau
   tanpa satu pun test diubah.
4. **Service** — tambahkan ke `terminal_service.go` dan `agent_session_service.go`
   yang **sudah ada**; jangan buat paket atau file service baru. Keenam aturan
   §4 jadi test di `service_test.go` sebelum handler-nya ada.
5. **Handler** — `internal/handler/cs_terminal_handler.go` (empat endpoint) dan
   `cs_auth_handler.go` (`desk-pin`). Keduanya file yang sudah ada.
6. **Rute** — `internal/router/router.go`, di grup `/terminals` dan `/auth` yang
   sudah ada. Ingat jebakan chi di komentar baris 1216: `/{terminal_id}`
   didaftarkan **terakhir**, jadi `/lock`, `/unlock`, `/lock-state`,
   `/unlock-override` harus berada **di atasnya**, atau `lock` akan terbaca
   sebagai sebuah `terminal_id`.
7. **Dokumentasi** — perbarui `cs-desktop-api-integration` (jumlah endpoint
   34 → 39, tabel kode galat) dan **§3.8 `cs-desktop-workflow-backend`**
   (§0.1 skill ini).

Verifikasi akhir: `make check`. Untuk jalur manualnya: `make infra-up`,
`make dev`, lalu login petugas → `desk-pin` → `lock` → `cs/dashboard` (pastikan
`can_take_calls` jadi `false`) → `unlock`.

---

## 7. Yang TIDAK dibangun, dan kenapa

| Diminta mockup Stitch | Keputusan |
|---|---|
| "membekukan terminal **CS-FL-04**" lalu butuh "otorisasi **fisik** Supervisor" | Dibangun sebagai §3.5, tapi lewat `authorization_ref` yang sudah ada. Tidak ada mekanisme "kehadiran fisik" di sistem ini, dan membuat endpoint yang mengaku memverifikasinya akan berbohong |
| `AES-GCM-256` di footer layar terkunci | Tidak ada apa pun yang dienkripsi dengan itu di jalur ini. Jangan tambahkan field yang menyatakannya |
| Kunci otomatis saat menganggur (`IDLE`) | `locked_reason = 'IDLE'` **disediakan** di skema, tapi pemicunya milik klien — server tidak tahu petugasnya diam. Jangan bangun penghitung idle di backend |
| Kunci jarak jauh oleh supervisor | Di luar cakupan. Butuh jawaban lebih dulu: apakah supervisor boleh mengunci meja petugas yang sedang melayani nasabah? Jangan tebak |
