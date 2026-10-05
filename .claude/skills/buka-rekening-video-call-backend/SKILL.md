---
name: buka-rekening-video-call-backend
description: Video call e-KYC buka rekening sisi backend — antrean `POST /v1/onboarding/video-call/queue`, WebSocket signaling `GET /v1/onboarding/video-call/signal`, token agent `POST /v1/onboarding/video-call/agent-token`, hasil verifikasi `POST /v1/onboarding/video-call/result`, model Hub/Room, siklus status QUEUED→ACTIVE→COMPLETED, token signaling sekali pakai, dan transisi step VIDEO_CALL→CREDENTIALS. Gunakan saat mengerjakan `video_call_service.go`, `signaling_handler.go`, `internal/websocket/`, antrean Redis `onboarding:queue:active`, tabel `onboarding_video_calls`, atau integrasi sisi CS. Trigger juga pada "agent_assigned", "queue_update", "call_ended", "ice_candidate", "media_control", "signaling_url", "signaling token", "VIDEO_CALL_OUTSIDE_HOURS", "VIDEO_CALL_NOT_ACTIVE", "queue_number", "agent-token", "SignalingNotifier", "SignalingLifecycle", dan "nasabah tidak pernah tersambung ke petugas". JANGAN gunakan untuk OCR, biometrik, kredensial, atau submit akhir (itu `buka-rekening-backend`), OTP onboarding (itu `buka-rekening-otp`), katalog kartu (itu `buka-rekening-kartu`), atau sisi WebRTC Android (project `BcaMobile`, skill `buka-rekening-video-call`).
---

# Buka Rekening — Video Call e-KYC (Backend)

Verifikasi tatap muka digital antara calon nasabah dan petugas Halo BCA. Backend
mengurus **antrean dan signaling**; medianya mengalir langsung antar-peer lewat
DTLS-SRTP dan tidak pernah melewati server ini.

Identitasnya `session_id` onboarding, bukan token bearer — nasabah belum punya akun.

---

## ATURAN #0 — Backend tidak pernah menyentuh media

Tidak ada SFU, tidak ada MCU, tidak ada transcoding, dan **tidak ada perekaman di
server ini**. Yang dilayani hanya relai SDP, ICE, dan kontrol media.

Rekaman untuk POJK adalah urusan backend CS, yang mengirim `recording_id` saat
menyubmit hasil. Kalau ada permintaan "simpan videonya di API", jawabannya bukan
menambah kode di sini — itu perubahan arsitektur yang menuntut penyimpanan
terenkripsi, retensi 5 tahun, dan jalur audit tersendiri.

---

## ATURAN #1 — `agent_assigned` adalah pemicu seluruh panggilan

Nasabah **menunggu** `agent_assigned` sebelum membuat SDP offer. Tidak ada
offer berarti tidak ada answer, tidak ada ICE, dan tidak ada media.

Satu-satunya yang mengirimnya adalah `AgentSignalingURL`, yaitu saat CS memanggil
`POST /video-call/agent-token`. Kalau emisi itu hilang — misalnya `Notifier`
dibiarkan `nil` di router — panggilan **tidak pernah bisa dimulai**, dan
gejalanya di aplikasi adalah layar yang diam di "menunggu petugas" tanpa satu
pesan error. Ini pernah terjadi: seluruh emisi server→client memang belum ada,
dan satu-satunya pesan yang pernah terkirim ke nasabah adalah `instruction` yang
direlai dari agent.

Pengujiannya ada di `TestAgentPickupNotifiesNasabah`. Jangan hapus.

---

## ATURAN #2 — Identitas agent datang dari kredensial, bukan dari body

`X-Agent-Employee-ID` menyebut petugasnya, `X-Agent-API-Key` membuktikannya ke tabel
`cs_agents` (migrasi `000026`, hash Argon2id). Namanya **diambil dari baris itu**, bukan
dari permintaan. Service menerimanya sebagai satu `AgentInfo`:

```go
AgentSignalingURL(ctx, queueID string, agent AgentInfo, ip, ua string)
SubmitResult(ctx, req, agent AgentInfo, ip, ua string)
```

Tidak ada `agent_employee_id` maupun `agent_name` di body endpoint mana pun. Selama
keduanya field payload, siapa pun yang memegang `INTERNAL_API_KEY` — satu secret yang sama
untuk seluruh integrasi CS — bisa mengaku sebagai pegawai mana pun, dan string itulah yang
masuk `onboarding_audit_logs.actor` serta tampil ke layar nasabah lewat `agent_assigned`.

Urutan tetap seperti dulu dan tetap penting: nasabah butuh nama petugas **saat panggilan
dimulai**, karena `agent_assigned` dikirim dari `agent-token`. Yang berubah hanya dari mana
nama itu berasal.

Dua penjaga, dua pertanyaan berbeda — dan `AgentAuth` dipasang **sesudah**
`InternalAPIKey`, karena verifikasi Argon2 mahal (64 MB × 4 thread) dan tidak boleh bisa
dipicu lalu lintas yang belum membuktikan dirinya sebagai sistem CS.

Tiga jebakan yang sudah pernah menggigit:

1. `UpdateResult` dipanggil dengan `agentName = ""`, yang **menimpa** nama yang
   baru tercatat. Kolomnya selalu berakhir kosong, dan `call_ended` kehilangan
   satu-satunya sumber namanya. Sekarang service meneruskan `vc.AgentName`, dan
   SQL-nya memakai `COALESCE(NULLIF($4, ''), agent_name)` sebagai pertahanan
   kedua.
2. `MarkActive` memakai pola yang sama untuk `agent_employee_id`, `agent_name`,
   dan `started_at` — CS yang meminta token dua kali (menyambung ulang) tidak
   boleh menghapus nama atau menggeser waktu mulai.
3. `agent_employee_id` di `UpdateResult` dulu memakai `COALESCE(NULLIF($3,''), kolom)` —
   urutan terbalik, jadi pelapor **menimpa** atribusi panggilan. Sekarang
   `COALESCE(kolom, NULLIF($3,''))`: yang sudah tercatat menang, dan service menolak
   pelapor yang bukan petugas pengambil dengan `409 VIDEO_CALL_AGENT_MISMATCH`.

---

## ATURAN #2b — Satu panggilan, satu petugas, dan hasil hanya dari petugas itu

Tiga penjaga yang bekerja bersama, dan ketiganya dulu tidak ada:

| Penjaga | Di mana | Kalau tidak ada |
|---|---|---|
| ACTIVE di bawah petugas lain → `409 VIDEO_CALL_ALREADY_TAKEN` | `AgentSignalingURL` | Dua petugas dapat token; `Hub.Register` menutup socket yang pertama, tapi `MarkActive` ber-COALESCE mempertahankan identitas yang pertama — yang bicara dengan nasabah dan yang tercatat di audit jadi dua orang berbeda |
| status harus `ACTIVE` → `422 VIDEO_CALL_NOT_ACTIVE` | `SubmitResult` | Satu permintaan bisa menyelesaikan panggilan yang belum pernah terjadi dan memindahkan sesi ke `CREDENTIALS` tanpa verifikasi tatap muka |
| pelapor = petugas pengambil → `409 VIDEO_CALL_AGENT_MISMATCH` | `SubmitResult` | Petugas mana pun bisa menandatangani verifikasi milik orang lain |

Pengambilan ulang oleh petugas yang **sama** tetap diizinkan — itu jalur menyambung ulang
yang sah (lihat ATURAN #3).

**`MarkActive` yang gagal MENOLAK penerbitan token.** Dulu kegagalannya hanya dicatat lalu
token tetap keluar, dengan alasan statusnya bisa dibereskan saat hasil disubmit. Itu yang
membuat penjaga "status harus ACTIVE" di atas mustahil dipasang — dan panggilan berjalan
tanpa catatan siapa yang menanganinya. Menolak lebih baik: petugas bisa menekan tombolnya
lagi, panggilan yang tidak tercatat tidak bisa diperbaiki belakangan.

---

## ATURAN #3 — Token signaling 5 menit, sekali pakai, dan tidak bisa disambung ulang

```go
signalingTokenTTL = 5 * time.Minute   // video_call_service.go
```

Tokennya berjalan di **query string**, jadi URL yang tersalin adalah kredensial
yang tersalin. Dua penjaga bekerja bersama:

| Penjaga | Di mana | Kalau tidak ada |
|---|---|---|
| `jti` sekali pakai di Redis | `redis.SignalingTokenStore.ConsumeSignalingToken` | URL dari log atau tangkapan layar bisa dipakai pihak ketiga |
| Role ditandatangani di JWT | `crypto.GenerateSignalingToken`, dibaca `claims.Role` | Nasabah bisa menyambung sebagai agent dan menerima apa yang agent terima |

**Redis yang gagal berarti koneksi ditolak**, bukan diterima atas dasar percaya —
`fail open` di sini mengizinkan replay setiap kali Redis tersendat.

**Konsekuensi untuk client yang sering disalahpahami:** sambungan yang putus
**tidak bisa** disambung ulang ke URL yang sama. Token sudah habis dipakai, dan
upaya kedua dijawab `401 token already used`. Pemulihannya memanggil ulang
`POST /video-call/queue` — yang untuk sesi yang sudah mengantre mengembalikan
tiket yang sama dengan **`signaling_url` baru**. Kalau ada laporan "reconnect
selalu gagal", itu bukan bug server.

---

## ATURAN #4 — Step dipindahkan mesin step, bukan pemanggil

`SubmitResult` tidak pernah menulis `StepCredentials` begitu saja. Ia bertanya
lebih dulu:

```go
if CanTransition(session.CurrentStep, StepCredentials) { ... }
```

Dua hal yang dijaga:

- **Replay.** Hasil yang sudah tercatat (`VCStatusCompleted`) dikembalikan apa
  adanya tanpa menjalankan transisi lagi. Tanpa itu, submit kedua menarik sesi
  yang sudah sampai `REVIEW` atau `COMPLETED` **kembali** ke `CREDENTIALS`.
- **Urutan.** `APPROVED` untuk sesi yang sudah melewati `VIDEO_CALL` dicatat di
  rekaman panggilan lalu dibiarkan, dengan `slog.Warn`.

`REJECTED` **tidak** memindahkan step. Sesi tetap di `VIDEO_CALL`, jadi nasabah
bisa mengantre lagi.

---

## ATURAN #5 — Panggilan basi dibebaskan di `JoinQueue`, bukan oleh worker

`idx_vc_session_active_unique` (migrasi `000017`) adalah unique partial index pada
`session_id WHERE status IN ('QUEUED','ACTIVE')`. Jadi satu baris `ACTIVE` yang agennya
hilang tanpa menyubmit hasil **mengurung sesinya permanen**: ia sudah ter-`ZREM` sehingga
tidak ada petugas yang melihatnya lagi, dan baris antrean baru untuk sesi itu akan menabrak
indeks tersebut. Dulu tidak ada apa pun yang membereskannya — hanya intervensi manual di
database.

Pembebasannya ada di `JoinQueue`, satu tempat saja:

```go
activeCallMaxAge = 15 * time.Minute   // bukan 3 (avgCallDurationSeconds):
                                      // ini batas "pasti sudah salah", bukan durasi normal
```

ACTIVE yang lebih tua dari itu → `Cancel` + `ZREM` + audit, lalu nasabah mendapat tiket
baru. Yang **masih** berjalan dikembalikan apa adanya.

**Sengaja tanpa worker periodik.** Repo ini tidak punya satu pun job latar, dan
menambahkannya berarti menambah urusan graceful shutdown, kebocoran goroutine di test, dan
dua instance yang saling membatalkan panggilan yang sama. Pemulihan yang dijanjikan ATURAN
#3 memang berupa nasabah memanggil ulang `POST /video-call/queue` — jadi di sanalah
tempatnya. Kalau suatu saat ada yang mengusulkan reaper, pertanyaan pertamanya: gejala apa
yang tidak tertutup jalur ini?

Dua pembersihan lain, keduanya juga lazy:

- **`CancelForSession`** (dipenuhi `*VideoCallService`, dipanggil `SessionService.CancelSession`
  lewat antarmuka sempit `VideoCallCanceller`) — nasabah membatalkan sesinya. Tanpa ini
  barisnya tetap `QUEUED` dan tetap muncul di daftar petugas, padahal sesinya sudah
  di-soft-delete. `CANCELLED` adalah status yang sebelumnya **tidak pernah ditulis siapa
  pun**.
- **`ListQueued`** membuang anggota yang tidak bisa dilayani (lihat di bawah).

`videoCallService` karena itu dibangun **sebelum** `onboardingSessionService` di
`router.New`. Dibalik, `VideoCalls` jadi nil dan pembatalan sesi berhenti membersihkan
antrean — tanpa ada yang gagal dikompilasi.

---

## Alur penuh

```
                 nasabah                          backend                         CS
                    │                                │                             │
 1  POST /video-call/queue ─────────────────────────► │                             │
                    │        tiket + signaling_url    │ validasi step VIDEO_CALL    │
                    │ ◄────────────────────────────── │ jam operasional 06–22 WIB   │
                    │                                │ Postgres + ZADD Redis        │
                    │                                │                             │
 2  WS /signal?token=… (role=nasabah) ─────────────► │                             │
                    │                                │ jti dikonsumsi (sekali pakai)│
                    │                                │ Hub.Register → Room          │
                    │ ◄── queue_update (posisi) ───── │ OnNasabahConnected           │
                    │                                │                             │
 3a                 │                                │ ◄── GET  /video-call/queued ─│
                    │                                │  daftar + queue_id ─────────►│
 3b                 │                                │ ◄── POST /agent-token ───────│
                    │                                │  MarkActive: QUEUED→ACTIVE   │
                    │                                │  catat agent + started_at    │
                    │                                │  ZREM → antrean bergeser     │
                    │ ◄── agent_assigned ─────────── │  signaling_url agent ───────►│
                    │ ◄── queue_update (yang lain) ── │                             │
                    │                                │                             │
 4                  │                                │ ◄── WS /signal (role=agent) ─│
 5  offer ─────────────────────────────────────────► │ ──── relai ────────────────►│
                    │ ◄───────────────────────────── │ ◄─── answer ─────────────────│
 6  ice_candidate ◄────────── relai dua arah ──────► │ ◄── ice_candidate ──────────│
                    │                                │                             │
 7      ══════ media DTLS-SRTP langsung antar-peer, tidak lewat backend ══════     │
                    │                                │                             │
 8                  │ ◄── instruction ─────────────── │ ◄── instruction ─────────────│
 9  media_control ─────────── relai ───────────────► │ ──────────────────────────►│
                    │                                │                             │
10                  │                                │ ◄── POST /video-call/result ─│
                    │                                │  UpdateResult → COMPLETED    │
                    │                                │  ZREM + geser antrean        │
                    │                                │  APPROVED → step CREDENTIALS │
                    │ ◄── call_ended ──────────────── │                             │
                    │                                │                             │
11  GET /sessions/{id} ────────────────────────────► │ current_step = CREDENTIALS   │
```

Langkah 11 penting: client **tidak** menavigasi dari `result` di `call_ended`.
Arah ditentukan `current_step` dari server, dan `call_ended` hanya memberi tahu
panggilannya berakhir.

---

## Endpoint

| Method | Path | Penjaga | Handler |
|---|---|---|---|
| POST | `/v1/onboarding/video-call/queue` | — (step + jam operasional) | `OnboardingHandler.JoinVideoCallQueue` |
| GET | `/v1/onboarding/video-call/signal` | token JWT sekali pakai + `CheckOrigin` | `SignalingHandler.HandleSignaling` |
| GET | `/v1/onboarding/video-call/queued` | `X-Internal-API-Key` | `OnboardingHandler.ListQueuedVideoCalls` |
| POST | `/v1/onboarding/video-call/agent-token` | `X-Internal-API-Key` **+** `X-Agent-Employee-ID` + `X-Agent-API-Key` | `OnboardingHandler.IssueAgentSignalingToken` |
| POST | `/v1/onboarding/video-call/result` | `X-Internal-API-Key` **+** `X-Agent-Employee-ID` + `X-Agent-API-Key` | `OnboardingHandler.SubmitVideoCallResult` |

Yang menuntut kredensial petugas hanya dua yang terakhir: mengambil panggilan dan
memutuskan hasil verifikasi adalah tindakan **seseorang**. Melihat antrean, jejak audit,
dan `GET /monitoring` adalah tindakan sistem/pengawas — tidak ada yang diatribusikan ke
seseorang, jadi memasang `AgentAuth` di sana hanya akan membuat panel pemantauan CS
menjawab 403 tanpa alasan yang terlihat.

### `POST /video-call/queue`

```json
{ "session_id": "onb_9f8e7d6c5b4a" }
```

```json
{
  "queue_id": "q_abc123", "queue_number": "A-042",
  "position": 2, "estimated_wait_seconds": 180,
  "operating_hours": { "start": "06:00", "end": "22:00", "timezone": "Asia/Jakarta" },
  "signaling_url": "wss://…/v1/onboarding/video-call/signal?token=eyJ…",
  "signaling_expires_in": 300,
  "ice_servers": [ { "urls": ["turn:…"], "username": "…", "credential": "…" } ]
}
```

- **Idempoten.** Sesi yang sudah `QUEUED` atau `ACTIVE` mendapat tiket yang sama
  dengan `signaling_url` **baru**. Itulah jalur pemulihan sambungan yang putus.
- `estimated_wait_seconds` = `position × 180` (`avgCallDurationSeconds`).
- `ice_servers` **kosong adalah jawaban sah**, bukan kegagalan: TURN belum
  dikonfigurasi. Panggilan tetap jadi di jaringan ramah dan gagal di seluler
  ber-NAT ketat — dan itu kegagalan infrastruktur, bukan bug client.
- `queue_number` dari penghitung harian Redis `onboarding:queue:counter:{YYYY-MM-DD}`,
  TTL 25 jam.

### `GET /video-call/queued`

Pintu masuk sisi CS. Tanpa ini petugas **tidak punya cara menemukan** `queue_id` yang
dibutuhkan `agent-token`: satu-satunya endpoint lain yang menyentuh antrean adalah
`GET /monitoring`, dan ia hanya mengembalikan `queue_length` — sebuah angka, tanpa satu pun
pengenal panggilan. Antrean bisa penuh dan tetap tidak ada yang bisa dilayani.

```json
{
  "calls": [
    { "queue_id": "q_abc123", "queue_number": "A-042", "session_id": "onb_9f8e…",
      "position": 1, "waited_seconds": 95, "status": "QUEUED" }
  ],
  "operating_hours": { "start": "06:00", "end": "22:00", "timezone": "Asia/Jakarta" },
  "within_operating_hours": true
}
```

- Urutannya dari **sorted set Redis**, bukan `ORDER BY joined_at`, supaya posisi yang
  dilihat petugas sama persis dengan yang dilihat nasabah. Dua sumber urutan akan
  menghasilkan dua jawaban berbeda begitu salah satunya tertinggal.
- Panggilan yang sudah diambil agent **hilang dari daftar** — `agent-token` sudah
  menjalankan `ZREM`. Itu yang mencegah dua petugas mengambil panggilan yang sama.
- Daftar ini **membersihkan** anggota yang tidak bisa dilayani, tidak sekadar melewatinya:
  yang rekamannya tidak ada (`ZREM`) dan yang sesinya sudah kedaluwarsa atau dibatalkan
  (`Cancel` + `ZREM`). Sebelumnya keduanya hanya dilewati, dan itu menyisakan dua masalah —
  `ZRANK` milik nasabah tetap menghitungnya, jadi semua yang di belakangnya melihat posisi
  lebih besar daripada jumlah panggilan yang benar-benar ada, dan baris `QUEUED`-nya
  menggantung di Postgres selamanya.
- **`position` dihitung dari anggota yang dipertahankan**, bukan dari indeks daftar awal.
  Yang dibuang sudah keluar dari sorted set, jadi `ZRANK` sesudahnya sama dengan hitungan
  ini. Memakai indeks awal akan membuat angka petugas lebih besar satu untuk setiap anggota
  yang baru dibuang.
- Kegagalan pembersihan **tidak pernah** menggagalkan daftar: petugas tetap mendapat
  antreannya, dan pemanggilan berikutnya mencoba lagi.
- Tidak memuat PII. Petugas memilih berdasarkan urutan, bukan berdasarkan siapa nasabahnya.

### `POST /video-call/agent-token`

```json
{ "queue_id": "q_abc123" }
```

Identitas petugas di header, bukan di body (ATURAN #2).

Melakukan empat hal sekaligus, dan urutannya disengaja: `MarkActive` →
`agent_assigned` ke nasabah → `ZREM` → `queue_update` ke sisa antrean.

Panggilan ber-status `COMPLETED` atau `CANCELLED` ditolak
`422 VIDEO_CALL_NOT_ACTIVE`.

### `POST /video-call/result`

Bentuknya di `docs/06-BUKA-REKENING-API-SPEC.md` §5c. `result` hanya menerima
`APPROVED` atau `REJECTED`; nilai lain `VALIDATION_ERROR`.

---

## Protokol signaling (§5b)

Satu amplop untuk semua: `onboarding.SignalMessage`. Jenisnya konstanta di
`entity.go` (`SignalAgentAssigned`, `SignalCallEnded`, …) — **jangan** menulis
literal `"agent_assigned"` di tempat baru, satu salah ejaan menghasilkan pesan
yang terkirim tapi tidak pernah dikenali client, dan itu gagal tanpa jejak.

| Jenis | Arah | Dikirim oleh |
|---|---|---|
| `join` | client → server | diabaikan; room dibuat saat upgrade |
| `offer` | nasabah → agent | relai `Hub.RelayToPeer` |
| `answer` | agent → nasabah | relai |
| `ice_candidate` | dua arah | relai |
| `media_control` | dua arah | relai |
| `instruction` | agent → nasabah | `Hub.SendToSession`, **hanya** kalau pengirimnya agent |
| `queue_update` | server → nasabah | `OnNasabahConnected`, `broadcastQueuePositions` |
| `agent_assigned` | server → nasabah | `AgentSignalingURL` |
| `call_ended` | server → nasabah | `SubmitResult` |

`type` yang tidak dikenal **dicatat lalu diabaikan**, tidak memutus socket:
server dan client berevolusi terpisah, dan satu pesan asing tidak boleh
mengakhiri verifikasi identitas yang tidak murah diulang.

### Model Hub/Room

```
Hub.rooms: map[session_id]*Room   (RWMutex)
Room{ SessionID, QueueID, Nasabah *Client, Agent *Client }
```

Empat hal yang sudah benar dan mudah dirusak:

1. **`Client.Close()` hanya `close(c.done)`** — non-blocking. Itu yang membuat
   `Hub.Register` boleh memanggilnya sambil memegang `mu.Lock()` tanpa deadlock:
   `Unregister` milik koneksi lama berjalan belakangan, setelah lock dilepas.
   Kalau `Close()` suatu saat jadi menunggu sesuatu, deadlock-nya kembali.
2. **`Unregister` menjaga `room.Nasabah == client`** sebelum menihilkan. Tanpa
   itu, koneksi lama yang baru selesai teardown menihilkan koneksi **baru** yang
   sudah menggantikannya.
3. **`Client.Send` non-blocking** (`select` + `default`, buffer 64). Itu yang
   membuat `RelayToPeer` aman memanggilnya di bawah `RLock`. Buffer penuh
   menjatuhkan pesan dengan `slog.Warn` — bukan ideal, tapi jauh lebih baik
   daripada membekukan seluruh hub.
4. **`closeOnce`** mencegah `close` ganda pada channel yang sama.

### Keamanan WebSocket

`CheckOrigin` wajib ada: upgrade WebSocket **melewati CORS sepenuhnya**, jadi
tanpa pemeriksaan ini halaman web mana pun bisa membuka socket dengan token yang
dia phishing (cross-site WebSocket hijacking). `Origin` kosong diizinkan — itu
client native, yang memang tidak mengirimnya.

---

## Penyimpanan

### `onboarding_video_calls` (migrasi `000014`)

| Kolom | Catatan |
|---|---|
| `status` | `QUEUED` → `ACTIVE` (agent-token) → `COMPLETED` (result). `CANCELLED` dari `Cancel`: pembatalan sesi, panggilan basi, atau sesi yang hilang saat daftar antrean dibuka |
| `agent_employee_id`, `agent_name` | diisi `MarkActive`, dipertahankan `UpdateResult` |
| `started_at` | diisi sekali di `MarkActive` lewat `COALESCE` |
| `ended_at` | diisi `UpdateResult` |
| `result` | `APPROVED` / `REJECTED` |
| `recording_id` | dari backend CS; rekamannya tidak di sini |

Indeks parsial `idx_vc_status WHERE status = 'QUEUED'` — pemantauan antrean hanya
menyentuh yang mengantre.

### `cs_agents` (migrasi `000026`)

Siapa yang berwenang melayani verifikasi. `employee_id` PK, `name`, `api_key_hash`
(PHC Argon2id, format yang sama dengan `users.pin_hash`), `is_active`.

Hash ber-salt tidak bisa dicari balik, jadi pemanggil menyebut dirinya lebih dulu
(`X-Agent-Employee-ID`) lalu membuktikannya (`X-Agent-API-Key`): baris dicari dengan
`employee_id`, hash-nya diverifikasi Argon2. `is_active` mencabut hak tanpa menghapus baris,
supaya panggilan lama tetap punya rujukan nama petugasnya.

**Barisnya kredensial, bukan data referensi — tidak ikut di migrasi.** `make seed` menanam
`CS-1042` / `dev-agent-key` dan **digerbangi `APP_ENV=development`**; seeder ini tidak punya
gerbang environment sendiri, jadi tanpa gerbang itu satu `make seed` yang salah arah
membuat kunci yang diketahui umum bisa menandatangani hasil verifikasi.

### Redis

| Key | Tipe | Isi |
|---|---|---|
| `onboarding:queue:active` | sorted set | `queue_id` dengan score = waktu masuk (ms) |
| `onboarding:queue:counter:{YYYY-MM-DD}` | counter | nomor antrean harian, TTL 25 jam |
| `signaling:jti:{jti}` | penanda | token yang sudah dipakai |

`VideoCallQueueCache.List` memakai `ZRANGE` penuh. Itu disengaja: jam operasional
16 jam dan satu agent per panggilan membuat antreannya berskala puluhan. Kalau
suatu hari panjangnya ribuan, ganti `broadcastQueuePositions` jadi notifikasi
berjendela — jangan biarkan loop ini membesar diam-diam.

---

## Yang belum ada: aplikasi CS

Backend sudah memaparkan **seluruh** yang dibutuhkan sisi petugas — menemukan antrean,
mengambil panggilan, menyambung sebagai peer yang menjawab, mengirim instruksi, menyubmit
hasil. Yang belum ada adalah **aplikasinya**: tidak ada panel web maupun aplikasi agent di
repo mana pun.

Konsekuensinya alurnya berhenti di langkah 3a. Nasabah bisa mengantre, socket-nya terbuka,
dan `queue_update` sampai — lalu tidak ada yang mengambil panggilannya, selamanya. Kalau ada
laporan "video call tidak pernah tersambung" sementara `make check` hijau dan log bersih,
periksa dulu apakah ada klien agent yang berjalan sebelum menduga bug.

Yang dibutuhkan klien itu, berurutan:

1. `GET /video-call/queued` → pilih satu `queue_id`
2. `POST /video-call/agent-token` dengan `queue_id` di body dan kredensial petugas di
   header → `signaling_url`
3. Buka WebSocket ke URL itu (role `agent` sudah ada di dalam token)
4. Tunggu `offer` dari nasabah → balas `answer` → tukar `ice_candidate`
5. Kirim `instruction` selama panggilan
6. `POST /video-call/result` dengan `APPROVED` / `REJECTED`

Semuanya di belakang `X-Internal-API-Key` kecuali WebSocket, yang dijaga token sekali pakai.
Langkah 2 dan 6 menuntut `X-Agent-Employee-ID` + `X-Agent-API-Key` di atasnya, dan
petugasnya harus ada di `cs_agents` dengan `is_active`.

---

## Jebakan nyata di repo ini

**Jam service harus `s.clock()`, bukan `time.Now()`.** `isWithinOperatingHours`
memakai clock yang di-inject, dan `JoinQueue` pernah memakai `time.Now()` untuk
`joined_at` dan score antrean. Dua sumber waktu di satu service membuat test
berjam-palsu tidak konsisten dengan dirinya sendiri.

**Import cycle.** `internal/websocket` meng-import `internal/domain/onboarding`
(untuk `SignalMessage`), jadi `onboarding` **tidak boleh** meng-import
`websocket`. Emisi dari lapisan domain lewat antarmuka `SignalingNotifier` yang
dipenuhi `*websocket.Hub` secara struktural. Hal yang sama berlaku untuk
`SignalingLifecycle` di lapisan handler.

**Urutan konstruksi di router.** `sigHub` dibuat **sebelum** `videoCallService`,
karena service memerlukannya sebagai `Notifier`. Dibalik, `Notifier` jadi `nil`,
dan semuanya tetap dikompilasi dan tetap lulus test yang tidak memeriksa emisi —
sementara panggilan tidak pernah bisa dimulai di produksi.

**`OnNasabahConnected` dipanggil sesudah `Hub.Register`.** Sebelum itu belum ada
room, jadi pesannya hilang tanpa jejak. Konteksnya `context.WithoutCancel`:
konteks request selesai begitu `Run()` kembali pada sebagian server, sementara
pemberitahuan ini menyentuh Redis dan Postgres.

**Jam operasional memakai `time.LoadLocation("Asia/Jakarta")`** dan **mengizinkan**
kalau database zona waktu tidak ada. Menolak lebih aman secara prinsip, tapi
menutup antrean di container tanpa tzdata — dan gejalanya "video call hilang di
staging" yang sangat mahal ditelusuri.

---

## Sebelum bilang selesai

```bash
make check        # lint + vet + test
```

Lalu periksa yang tidak tertangkap test otomatis:

- `make infra-up` menjalankan **coturn**; tanpa itu `ice_servers` kosong dan
  panggilan gagal di jaringan ber-NAT ketat.
- Jalankan dua socket sungguhan (nasabah + agent) ke satu `session_id` dan
  pastikan `offer` dari satu sisi muncul di sisi lain.
- Sambungkan ulang dengan token yang sama → harus `401 token already used`.
- Submit `result` dua kali → yang kedua mengembalikan hasil tersimpan tanpa
  menggeser step.
- Ambil satu panggilan dari dua petugas berbeda → yang kedua `409 VIDEO_CALL_ALREADY_TAKEN`;
  yang sama dua kali → tetap 200.
- Submit `result` sebagai petugas lain → `409 VIDEO_CALL_AGENT_MISMATCH`, dan
  `agent_employee_id` di baris panggilan **tidak berubah**.
- Submit `result` untuk panggilan yang belum diambil → `422 VIDEO_CALL_NOT_ACTIVE`.
- Batalkan sesi yang sedang mengantre → barisnya `CANCELLED` dan hilang dari sorted set.
- Submit `APPROVED` lalu `GET /sessions/{id}` → `current_step = CREDENTIALS`.

Kalau ada selisih antara berkas ini dan `docs/06-BUKA-REKENING-API-SPEC.md` §5,
**laporkan selisihnya** — jangan pilih salah satu diam-diam.
