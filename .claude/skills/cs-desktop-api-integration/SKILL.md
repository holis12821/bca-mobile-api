---
name: cs-desktop-api-integration
description: Integrasi API untuk aplikasi desktop petugas CS Halo BCA — tujuh endpoint sisi CS (`GET /v1/onboarding/video-call/queued`, `POST /video-call/agent-token`, `GET /video-call/signal`, `POST /video-call/result`, `GET /sessions/{id}/audit`, `GET /monitoring`, `/internal/v1/cards`), autentikasi dua lapis `X-Internal-API-Key` + `X-Agent-Employee-ID`/`X-Agent-API-Key`, WebSocket signaling dari sudut pandang agent, WebRTC sisi answerer, dan submit hasil verifikasi e-KYC. Gunakan saat membangun atau men-debug aplikasi desktop/klien CS yang memakai API ini, merancang autentikasi petugas CS, menyambungkan antrean video call ke layar petugas, atau menjawab "endpoint CS apa yang tersedia hari ini". Trigger juga pada "aplikasi desktop CS", "petugas CS", "Halo BCA desktop", "X-Agent-API-Key", "X-Agent-Employee-ID", "cs_agents", "INTERNAL_API_KEY", "agent-token", "video-call/queued", "signaling_url", "ice_servers", "call_duration_seconds", "recording_id", "AGENT_AUTH_UNAVAILABLE", "VIDEO_CALL_NOT_ACTIVE", dan "antrean petugas". JANGAN dipakai untuk mengubah server video call itu sendiri — service, hub, atau migrasi (itu `buka-rekening-video-call-backend`), OCR/biometrik/kredensial/submit (itu `buka-rekening-backend`), OTP onboarding (itu `buka-rekening-otp`), katalog kartu sisi nasabah (itu `buka-rekening-kartu`), atau sisi WebRTC Android nasabah (project `BcaMobile`).
---

# Integrasi API — Aplikasi Desktop Petugas CS Halo BCA

Skill ini menentukan **apa yang benar-benar bisa disambungkan** oleh aplikasi
desktop petugas CS. Seluruh isinya dibaca dari kode di repo ini
(`internal/router/router.go`, `internal/middleware/internal_api_key.go`,
`internal/domain/onboarding/`, `internal/handler/`), bukan dari rencana.

Kalau ada selisih dengan `docs/06-BUKA-REKENING-API-SPEC.md`, **laporkan
selisihnya** — jangan pilih salah satu diam-diam.

> **Perubahan dari dokumen lama.** Skill ini menggantikan
> `docs/cs-desktop-api-integration.md`, yang sudah **salah** di bagian auth: ia
> menyebut tidak ada identitas per petugas dan `agent_employee_id` datang dari
> body tanpa verifikasi. Itu benar sebelum `middleware.AgentAuth` ada. Lihat
> §1.2 untuk kontrak yang berlaku sekarang, dan §6 untuk daftar koreksinya.

---

## ATURAN #0 — Aplikasi desktop melayani satu panggilan, bukan satu pembukaan rekening

Alur buka rekening punya sepuluh langkah, dan nasabah mengerjakan sembilan di
antaranya sendiri di aplikasi Android:

```
TNC → CARD_SELECTION → OCR → PERSONAL_DATA → OTP_VERIFY → BIOMETRIC
                                                              ↓
                                            ┌──── VIDEO_CALL ─┘  ← SATU-SATUNYA
                                            │                      langkah yang
                                            ↓                      butuh petugas
                                     CREDENTIALS → REVIEW → COMPLETED
```

**Petugas menyentuh tepat satu langkah: `VIDEO_CALL`.** Itu bukan
penyederhanaan — sembilan langkah lainnya tidak punya endpoint sisi CS sama
sekali, dan `POST /v1/onboarding/video-call/result` adalah satu-satunya tempat
di seluruh API tempat tindakan petugas memindahkan langkah nasabah.

Konsekuensi yang menentukan arsitektur aplikasi desktop: ia **bukan** aplikasi
yang mendampingi nasabah sepanjang pembukaan rekening. Ia aplikasi yang melayani
satu panggilan video, lalu melepasnya. Sisanya pemantauan.

---

## 1. Alamat dan kredensial

### 1.1 Alamat per lingkungan

| Lingkungan | REST | WebSocket signaling |
|---|---|---|
| Lokal | `http://localhost:8080/v1/` | `ws://localhost:8080` |
| ngrok (tester) | `https://<domain>.ngrok-free.dev/v1/` | `wss://<domain>.ngrok-free.dev` |
| Staging | `https://api-staging.bcamobile.id/v1/` | `wss://…` — belum dikonfirmasi infra |
| Produksi | `https://api.bcamobile.id/v1/` | `wss://…` — belum dikonfirmasi infra |

Alamat signaling **bukan** turunan alamat REST: ia datang dari
`SIGNALING_BASE_URL` di konfigurasi server (default `ws://localhost:8080`,
`internal/config/config.go`). Dan aplikasi desktop **tidak boleh merakitnya
sendiri** — `signaling_url` datang utuh di response `agent-token`.

`config.Validate` menolak start di luar development kalau `SIGNALING_BASE_URL`
tidak berawalan `wss://`. Jadi di staging dan produksi, signaling selalu TLS.

### 1.2 Autentikasi — dua lapis, dua pertanyaan berbeda

Jalur CS punya **dua** penjaga yang menjawab dua pertanyaan berbeda: *sistem
mana yang memanggil*, dan *petugas mana yang bertindak*.

**Lapis 1 — sistem (`middleware.InternalAPIKey`), wajib di semua endpoint CS:**

```
X-Internal-API-Key: <INTERNAL_API_KEY>
```

Dibandingkan constant-time (`subtle.ConstantTimeCompare`). `INTERNAL_API_KEY`
kosong **menolak semua permintaan**, bukan menerima header kosong; dan
`config.Validate` menolak start proses produksi tanpa kunci itu.

**Lapis 2 — petugas (`middleware.AgentAuth`), hanya di dua endpoint yang
mengatribusikan tindakan ke orang:**

```
X-Agent-Employee-ID: CS-1042
X-Agent-API-Key:     <kunci petugas>
```

Baris dicari dengan `employee_id` di tabel `cs_agents` (migrasi
`000026_cs_agents`), lalu `api_key_hash`-nya diverifikasi **Argon2id** —
PHC yang sama dengan `users.pin_hash`. Hanya baris `is_active = true` yang
dicari; mencabut hak petugas = set `is_active = false`, bukan hapus baris, supaya
panggilan lama tetap punya rujukan namanya.

Yang harus dipahami sebelum merancang autentikasi aplikasi desktop:

1. **Identitas petugas sekarang diverifikasi.** `agent_employee_id` dan
   `agent_name` **sudah dihapus dari body** `agent-token` dan `result` —
   bukan "ada tapi diabaikan", karena field yang dibiarkan ada membuat pemanggil
   mengira ia masih berfungsi. `actor` di audit trail (`agent:CS-1042`) dan nama
   yang tampil ke layar nasabah lewat `agent_assigned` keduanya berasal dari
   kredensial, bukan dari payload.
2. **Urutan penjaganya bukan selera.** `AgentAuth` dipasang **setelah**
   `InternalAPIKey` karena verifikasi Argon2 mahal (64 MB × 4 thread) dan tidak
   boleh bisa dipicu lalu lintas yang belum membuktikan dirinya sebagai sistem CS.
3. **`INTERNAL_API_KEY` masih satu kunci bersama untuk seluruh `/internal/v1`** —
   termasuk administrasi katalog kartu. Cakupan kewenangan terpisah untuk video
   call vs administrasi katalog **belum ada** (§5).
4. **Penjaga ini tidak fail-open.** Postgres yang tersendat dijawab
   `503 AGENT_AUTH_UNAVAILABLE`, bukan diluluskan: jalur yang menentukan siapa
   bertanggung jawab atas sebuah verifikasi tidak boleh pernah fail-open.
   `lookup` yang nil (perakitan rute salah) menolak semuanya.

**Kredensial petugas untuk development.** `make seed` menanam satu petugas,
**digerbangi `APP_ENV=development`**:

```
X-Agent-Employee-ID: CS-1042
X-Agent-API-Key:     dev-agent-key     (nama: Sarah Adisti)
```

Di luar development, baris `cs_agents` dibuat oleh yang mengoperasikan integrasi
CS dengan kunci acak, lewat jalur yang sama dengan pendistribusian
`INTERNAL_API_KEY`. Jangan pernah pakai `dev-agent-key` di staging — seeder-nya
memang digerbangi justru karena satu `make seed` yang salah arah akan membuat
kunci yang diketahui umum bisa menandatangani hasil verifikasi identitas.

**Penyimpanan di sisi desktop:** kedua kunci di penyimpanan kredensial OS —
bukan di berkas konfigurasi, dan bukan di `localStorage` kalau dibangun dengan
Electron. Sampai autentikasi berbasis direktori pegawai ada (§5), aplikasi
desktop hanya di jaringan internal atau di belakang VPN.

### 1.3 Bentuk response

Semua REST memakai envelope yang sama dengan aplikasi nasabah:

```json
{ "status": "success", "data": { … },
  "meta": { "request_id": "…", "timestamp": "…" } }
```

```json
{ "status": "error",
  "error": { "code": "…", "message": "…", "details": { … } },
  "meta": { "request_id": "…", "timestamp": "…" } }
```

`error.message` **sudah berbahasa Indonesia** dan lebih spesifik daripada teks
cadangan apa pun yang ditulis client. Tampilkan itu, jangan ganti dengan kalimat
sendiri. `meta.request_id` satu-satunya pegangan saat melaporkan keluhan ke
backend — tampilkan di keadaan gagal.

---

## 2. Endpoint yang tersedia hari ini

Tujuh, dan hanya tujuh.

| # | Method | Path | Penjaga | Guna |
|---|---|---|---|---|
| 1 | GET | `/v1/onboarding/video-call/queued` | internal key | Daftar antrean yang menunggu |
| 2 | POST | `/v1/onboarding/video-call/agent-token` | internal key **+ agent** | Ambil panggilan, dapat `signaling_url` |
| 3 | GET | `/v1/onboarding/video-call/signal?token=` | token sekali pakai | WebSocket signaling (role agent) |
| 4 | POST | `/v1/onboarding/video-call/result` | internal key **+ agent** | Submit hasil verifikasi |
| 5 | GET | `/v1/onboarding/sessions/{session_id}/audit` | internal key | Jejak audit satu sesi |
| 6 | GET | `/v1/onboarding/monitoring` | internal key | Hitungan sesi, panjang antrean, peringatan |
| 7 | GET/PUT | `/internal/v1/cards`, `/cards/{card_type}`, `/products/{product_type}/cards/{card_type}` | internal key | Administrasi katalog kartu |

Nomor 3 **tidak** memakai header apa pun — penjaganya token sekali pakai di
query string. Nomor 1, 5, dan 6 sengaja **hanya** berpenjaga kunci sistem:
melihat antrean, jejak audit, dan kesehatan sistem adalah tindakan
sistem/pengawas, tidak ada yang diatribusikan ke seseorang. Nomor 2 dan 4 jelas
tindakan seseorang, jadi butuh kedua lapis.

### 2.1 `GET /video-call/queued`

Pintu masuk seluruh aplikasi. Tanpa ini tidak ada cara menemukan `queue_id`
yang dibutuhkan endpoint berikutnya.

```json
{
  "calls": [
    { "queue_id": "q_abc123", "queue_number": "A-042",
      "session_id": "onb_9f8e7d6c5b4a",
      "position": 1, "waited_seconds": 95, "status": "QUEUED" }
  ],
  "operating_hours": { "start": "06:00", "end": "22:00", "timezone": "Asia/Jakarta" },
  "within_operating_hours": true
}
```

- `position` 1-based, **sama persis** dengan yang dilihat nasabah di layarnya.
  Keduanya dari sorted set Redis yang sama, jadi petugas dan nasabah tidak pernah
  menyebut angka berbeda.
- Panggilan yang sudah diambil petugas lain **hilang dari daftar**. Itulah yang
  mencegah dua petugas mengambil panggilan yang sama — bukan penguncian di
  aplikasi desktop.
- `within_operating_hours: false` berarti antrean tidak menerima yang baru; yang
  sudah mengantre **tetap boleh dilayani**.
- Tidak memuat PII. Tidak ada nama, tidak ada NIK, tidak ada nomor HP — petugas
  memilih panggilan berdasarkan urutan, dan data pribadi baru terlihat di dalam
  panggilannya.
- Polling 5 detik cukup dan tidak perlu lebih cepat: rata-rata panggilan 3 menit
  (`avgCallDurationSeconds = 180`).

### 2.2 `POST /video-call/agent-token`

```
X-Internal-API-Key: <INTERNAL_API_KEY>
X-Agent-Employee-ID: CS-1042
X-Agent-API-Key: <kunci petugas>
```

```json
{ "queue_id": "q_abc123" }
```

Body-nya **hanya** `queue_id`. Identitas petugas datang dari header.

```json
{ "queue_id": "q_abc123", "session_id": "onb_9f8e…", "queue_number": "A-042",
  "signaling_url": "wss://…/v1/onboarding/video-call/signal?token=eyJ…",
  "expires_at": "2026-10-05T01:05:00Z",
  "ice_servers": [ { "urls": ["turn:…"], "username": "…", "credential": "…" } ] }
```

**Satu permintaan ini melakukan empat hal di server**, dan urutannya penting
dipahami karena menentukan apa yang boleh diasumsikan aplikasi desktop:

1. Status panggilan pindah `QUEUED` → `ACTIVE`, `started_at` dan identitas agent
   tercatat.
2. **`agent_assigned` dikirim ke nasabah.** Inilah pemicu seluruh panggilan —
   aplikasi Android menunggu pesan ini untuk membuat SDP offer. Tanpa permintaan
   ini, nasabah menunggu selamanya.
3. Panggilan dikeluarkan dari antrean, posisi semua yang di belakang bergeser.
4. Token signaling diterbitkan untuk role `agent`.

Karena itu: **jangan memanggil endpoint ini sampai petugas benar-benar siap
menyambung.** Memanggilnya untuk "melihat dulu" sudah memberi tahu nasabah bahwa
petugas datang.

Nama yang dilihat nasabah adalah `name` dari baris `cs_agents`, bukan string yang
dikirim aplikasi desktop. Kalau namanya salah di layar nasabah, yang diperbaiki
barisnya di database — bukan payload.

`422 VIDEO_CALL_NOT_ACTIVE` berarti panggilannya sudah `COMPLETED` atau
`CANCELLED` — muat ulang antrean, jangan coba lagi.

### 2.3 `GET /video-call/signal?token=` — WebSocket

URL dipakai **apa adanya** dari `signaling_url`. Jangan merakit, jangan menambah
parameter, jangan menyimpannya untuk dipakai lagi.

**Tokennya sekali pakai.** Server mengonsumsi `jti`-nya di Redis
(`ConsumeSignalingToken`) saat socket pertama dibuka, jadi upaya kedua ke URL
yang sama dijawab `401 token already used` — selalu, bukan kadang-kadang. Kalau
socket putus, pemulihannya memanggil ulang `POST /video-call/agent-token` untuk
mendapat URL baru. Umur token 5 menit, dan itu cukup hanya untuk **membuka**
socket: petugas menunggu di dalam socket, bukan di luarnya.

Redis yang gagal dijawab `503 signaling unavailable`, bukan diluluskan —
fail-open di sini akan menerima replay setiap kali Redis tersendat.

Role ada **di dalam token**, bukan di query string. Aplikasi desktop tidak pernah
menyatakan dirinya agent — server yang menentukan dari token.

#### Protokol pesan, dari sudut pandang petugas

Satu amplop JSON untuk semua, bidang yang tidak relevan dihilangkan.

**Dikirim aplikasi desktop:**

| `type` | Bidang | Kapan |
|---|---|---|
| `join` | `session_id`, `queue_id` | Segera setelah socket terbuka |
| `answer` | `sdp` | Setelah menerima `offer` dari nasabah |
| `ice_candidate` | `candidate{candidate,sdpMid,sdpMLineIndex}` | Setiap kandidat lokal |
| `instruction` | `text` | Petugas mengirim instruksi |
| `media_control` | `action` | `mute_audio` / `unmute_audio` / `switch_camera` |

**Diterima aplikasi desktop:**

| `type` | Bidang | Arti |
|---|---|---|
| `offer` | `sdp` | Nasabah memulai negosiasi. **Balas `answer`** |
| `ice_candidate` | `candidate{…}` | Kandidat dari nasabah |
| `media_control` | `action` | Nasabah membisukan diri / ganti kamera |

Tiga jenis pesan — `queue_update`, `agent_assigned`, `call_ended` — hanya
dikirim ke **nasabah**, tidak ke petugas (`SignalingNotifier` hanya punya
`SendToNasabah`). Abaikan kalau muncul.

`type` yang tidak dikenal **dicatat lalu diabaikan**, jangan memutus socket:
server dan client berevolusi terpisah, dan satu pesan asing tidak boleh
mengakhiri verifikasi yang tidak murah diulang.

#### WebRTC: petugas adalah sisi yang menjawab

Nasabah membuat `offer`, petugas membuat `answer`. Tiga jebakan, dan ketiganya
hanya terlihat saat ada panggilan sungguhan:

**Ambil media lokal sebelum socket dibuka.** `offer` bisa tiba seketika setelah
`join`, dan `createAnswer()` yang dijalankan sebelum track kamera dan mikrofon
ditambahkan menghasilkan jawaban satu arah — petugas melihat dan mendengar
nasabah, nasabah tidak melihat dan tidak mendengar petugas. Ini kesalahan WebRTC
paling sering dan paling lambat terdeteksi.

**Antrekan kandidat ICE.** `addIceCandidate()` sebelum `setRemoteDescription()`
ditolak browser. Server merelai kandidat begitu petugas masuk room, jadi
urutannya memang bisa terbalik. Simpan di array, pasang setelah remote
description terpasang.

**Pakai `ice_servers` dari response, jangan tanam.** Kredensial TURN berganti
berkala dan milik penyedia (`STUN_URLS`, `TURN_URLS` di config server). Daftar
kosong adalah jawaban **sah** — artinya TURN belum dikonfigurasi, dan panggilan
akan jadi di jaringan ramah lalu gagal di NAT ketat. Itu kegagalan infrastruktur,
bukan bug aplikasi; tampilkan bedanya.

Media mengalir **langsung antar-peer** lewat DTLS-SRTP dan tidak pernah melewati
server. Jangan merekam di sisi petugas — rekaman POJK urusan backend CS, yang
mengirimkan `recording_id` saat menyubmit hasil.

### 2.4 `POST /video-call/result`

```
X-Internal-API-Key: <INTERNAL_API_KEY>
X-Agent-Employee-ID: CS-1042
X-Agent-API-Key: <kunci petugas>
```

```json
{ "session_id": "onb_9f8e…", "queue_id": "q_abc123",
  "result": "APPROVED",
  "ktp_shown_live": true, "identity_confirmed": true,
  "notes": "Nasabah kooperatif, e-KTP asli terverifikasi.",
  "call_duration_seconds": 195, "recording_id": "rec_xyz789" }
```

```json
{ "session_id": "onb_9f8e…", "result": "APPROVED", "current_step": "CREDENTIALS" }
```

- **Tidak ada `agent_employee_id` di body.** Mengirimnya tidak berbahaya tapi
  tidak berpengaruh — identitasnya dari header, dan itulah yang masuk audit.
- `result` hanya `APPROVED` atau `REJECTED`. Nilai lain `VALIDATION_ERROR`.
- **`APPROVED` memindahkan langkah nasabah ke `CREDENTIALS`.** Bukan membuat
  rekening — rekeningnya lahir tiga langkah kemudian di `POST /submit`. Jangan
  menulis "rekening berhasil dibuat" di layar petugas.
- **`REJECTED` tidak memindahkan langkah.** Nasabah tetap di `VIDEO_CALL` dan
  bisa mengantre lagi. Pastikan teks di aplikasi desktop tidak menyiratkan
  pengajuannya ditutup.
- `call_duration_seconds` dihitung aplikasi desktop dari saat
  `connectionState === "connected"`, bukan dari saat socket terbuka.
- **Idempoten lewat replay guard.** Submit kedua untuk panggilan yang sudah
  `COMPLETED` mengembalikan hasil tersimpan **tanpa** menjalankan transisi ulang.
  Itu disengaja: tanpanya, submit ganda menarik sesi yang sudah sampai `REVIEW`
  kembali ke `CREDENTIALS`. Jadi tombol kirim yang tertekan dua kali tidak
  merusak apa pun — tapi tetap matikan tombolnya saat permintaan jalan.

### 2.5 `GET /sessions/{session_id}/audit`

```json
{ "session_id": "onb_9f8e…", "count": 14,
  "events": [ { "event_type": "VIDEO_CALL_ENDED", "actor": "agent:CS-1042",
                "details": { … }, "created_at": "…" } ] }
```

Jenis peristiwa yang relevan untuk petugas: `VIDEO_CALL_QUEUED`,
`VIDEO_CALL_ENDED`, dan keseluruhan jejak langkah nasabah untuk memahami konteks
sebelum panggilan. `actor` berbentuk `agent:<employee_id>` untuk tindakan petugas
dan `system` untuk yang bukan.

`details` berbentuk JSON bebas per jenis peristiwa — tampilkan apa adanya sebagai
blok terformat, jangan mengurai per bidang. Bidangnya berubah tanpa rilis
aplikasi.

### 2.6 `GET /monitoring`

```json
{ "active_sessions": 37, "stuck_sessions": 2, "queue_length": 5,
  "alerts": [ { "rule": "…", "severity": "WARNING", "message": "…" } ] }
```

`severity` bernilai `WARNING` atau `CRITICAL`. `queue_length` hanya **hitungan** —
untuk daftarnya pakai `/video-call/queued`.

### 2.7 `/internal/v1/cards`

Administrasi katalog kartu Paspor. Bukan bagian alur video call, tapi dijaga
`X-Internal-API-Key` yang sama sehingga ikut terbuka di aplikasi desktop. Ia
sengaja di `/internal/v1`, bukan `/v1`: rate limit, body limit, dan CORS jalur
nasabah tidak berlaku untuk jalur operator.

Satu hal yang harus disampaikan di UI: **mengubah katalog menaikkan
`catalog_version`**, dan versi itu dipakai klien nasabah sebagai penanda cache.
Mengubah satu biaya membuat seluruh aplikasi nasabah memuat ulang katalognya.

---

## 3. Alur lengkap satu panggilan

```
 Petugas (desktop)                  Backend                      Nasabah (Android)
        │                              │                                │
 1  GET /video-call/queued ──────────► │                                │
        │ ◄── daftar + queue_id ────── │                                │
        │                              │ ◄───── WS /signal (nasabah) ───│
        │                              │ ──── queue_update ────────────►│
        │                              │                                │
 2  POST /agent-token ───────────────► │ InternalAPIKey → AgentAuth     │
        │                              │ MarkActive QUEUED→ACTIVE       │
        │                              │ ──── agent_assigned ─────────►│
        │ ◄── signaling_url + ICE ──── │ ZREM + queue_update ke sisanya │
        │                              │                                │
 3  getUserMedia() lokal               │                                │
 4  WS /signal?token= ──────────────► │ jti dikonsumsi, role dari token│
 5  join ────────────────────────────► │                                │
        │                              │ ◄───────── offer ──────────────│
        │ ◄──────── offer ──────────── │                                │
 6  answer ──────────────────────────► │ ────────── answer ───────────►│
 7  ice_candidate ◄── dua arah ─────► │ ◄──── ice_candidate ─────────►│
        │                              │                                │
 8      ══════ media DTLS-SRTP langsung antar-peer ══════               │
        │                              │                                │
 9  instruction ─────────────────────► │ ──── instruction ───────────►│
        │                              │                                │
10  POST /video-call/result ─────────► │ COMPLETED, step→CREDENTIALS   │
        │ ◄── current_step ─────────── │ ──── call_ended ────────────►│
        │                              │                                │
11      │                              │ ◄── GET /sessions/{id} ───────│
        │                              │ ──── CREDENTIALS ───────────►│
```

Langkah 11 menjelaskan kenapa aplikasi desktop tidak perlu memberi tahu nasabah
apa pun secara langsung: arah nasabah ditentukan `current_step` dari server,
bukan oleh pesan dari petugas.

---

## 4. Penanganan kegagalan

| Keadaan | Kode | Yang benar dilakukan |
|---|---|---|
| `X-Internal-API-Key` salah atau kosong | `403 FORBIDDEN` | Kembali ke layar masuk, sebut bahwa kunci sistemnya yang ditolak |
| Kredensial petugas salah, atau petugas `is_active = false` | `403 FORBIDDEN` | Minta petugas masuk ulang; kalau berulang, barisnya di `cs_agents` yang perlu diperiksa |
| Header petugas tidak dikirim di `agent-token`/`result` | `403 FORBIDDEN` | Bug aplikasi — kedua header wajib di dua endpoint itu |
| Postgres tersendat saat verifikasi petugas | `503 AGENT_AUTH_UNAVAILABLE` | Boleh dicoba lagi dengan backoff. Ini masalah infrastruktur, bukan kredensial |
| Panggilan sudah diambil petugas lain | `422 VIDEO_CALL_NOT_ACTIVE` | Muat ulang antrean, **jangan** coba lagi |
| `queue_id` tidak dikenal | `404 ONBOARDING_NOT_FOUND` | Muat ulang antrean |
| Sesi nasabah kedaluwarsa | `422 ONBOARDING_SESSION_EXPIRED` | Panggilan tidak bisa dilanjutkan; akhiri |
| `result` di luar APPROVED/REJECTED | `400 VALIDATION_ERROR` | Bug aplikasi, bukan kesalahan petugas |
| Socket signaling ditolak | `401 token already used` | Minta token baru lewat `agent-token` |
| Redis tidak bisa dijangkau saat buka socket | `503 signaling unavailable` | Coba lagi dengan backoff; tokennya belum terpakai |
| Socket putus di tengah panggilan | — | Minta token baru; `PeerConnection` **jangan** dibongkar, media bisa tetap mengalir |
| Di luar jam operasional | `422 VIDEO_CALL_OUTSIDE_HOURS` | Hanya memengaruhi nasabah yang mau mengantre; petugas tetap bisa melayani yang sudah di antrean |

Satu hal yang **tidak** boleh dilakukan: menyambung ulang ke `signaling_url`
yang sama. Itu dijamin gagal, dan mencoba lima kali dengan backoff hanya
menghasilkan lima kegagalan.

Catatan soal `403`: `InternalAPIKey` dan `AgentAuth` memakai kode dan pesan yang
**sama persis** (`FORBIDDEN` / "Akses ditolak."), jadi response-nya tidak
membedakan lapis mana yang menolak — itu disengaja, supaya penyerang tidak bisa
menebak kunci mana yang sudah benar. Aplikasi desktop harus mengandalkan
`meta.request_id` dan log server untuk membedakannya.

---

## 5. Yang belum ada di backend

Daftar pekerjaan backend yang dibutuhkan tiap layar yang belum punya API. Tanpa
ini, layar 8 dan 9 di berkas desain tidak bisa dibangun.

| Kebutuhan | Endpoint yang belum ada | Catatan |
|---|---|---|
| Autentikasi petugas berbasis direktori pegawai | `POST /internal/v1/auth/*` | Kredensial per petugas **sudah ada** (`cs_agents` + Argon2), tapi masih kunci statis yang didistribusikan manual. OIDC ke direktori pegawai, rotasi, dan sesi bertenggat belum ada |
| Cakupan kewenangan terpisah | — | `INTERNAL_API_KEY` masih membuka video call **dan** administrasi katalog dengan satu kunci |
| Daftar sesi onboarding | `GET /internal/v1/onboarding/sessions` | `/monitoring` hanya memberi hitungan |
| Detail sesi untuk CS | `GET /internal/v1/onboarding/sessions/{id}` | Endpoint nasabah yang ada **tidak** memuat PII, dan itu memang disengaja |
| Pencarian nasabah | `GET /internal/v1/customers?q=` | Untuk layar 8 |
| Profil nasabah untuk CS | `GET /internal/v1/customers/{id}` | Butuh keputusan privasi: bidang mana tersamar, mana bisa dibuka, dan pencatatan aksesnya |
| Tiket layanan | `/internal/v1/tickets/*` | Untuk layar 9. Tabel dan domainnya belum ada |
| Penjadwalan ulang video call | `POST /v1/onboarding/video-call/schedule` | Tombol "Jadwalkan Panggilan Nanti" di aplikasi Android **sudah dimatikan** karena ini |
| `call_ended` ke sisi petugas | — | `SignalingNotifier` hanya punya `SendToNasabah`. Nasabah yang menutup panggilan lebih dulu tidak terlihat di aplikasi desktop selain socket yang tertutup |

Dua yang paling layak dikerjakan lebih dulu, karena keduanya memperbaiki
kekurangan yang **sudah terasa** hari ini: cakupan kewenangan terpisah, dan
`call_ended` ke kedua sisi.

---

## 6. Koreksi terhadap `docs/cs-desktop-api-integration.md`

Dokumen itu digantikan skill ini. Yang berubah, supaya salinan lama yang masih
beredar tidak dipakai:

| Dokumen lama | Yang benar sekarang |
|---|---|
| "Satu kunci dibagikan semua petugas. Tidak ada identitas per orang." | `cs_agents` + `middleware.AgentAuth`: satu kunci per petugas, diverifikasi Argon2id |
| "`agent_employee_id` diisi petugas sendiri dan tidak diverifikasi" | Field itu **dihapus dari body**; identitas dari `X-Agent-Employee-ID` + `X-Agent-API-Key` |
| Body `agent-token` memuat `agent_employee_id` + `agent_name` | Body hanya `{ "queue_id": … }` |
| "`agent_name` secara teknis opsional, isi saja" | Nama datang dari baris `cs_agents`, tidak bisa dikarang pemanggil |
| "Kunci salah atau kosong → `401`" | `403 FORBIDDEN` (kedua lapis), dan `503 AGENT_AUTH_UNAVAILABLE` saat Postgres tersendat |
| §5: "Autentikasi per petugas — belum ada" | Sudah ada sebagian; yang belum adalah OIDC/rotasi dan cakupan kewenangan terpisah |

---

## 7. Rujukan

| Isi | Berkas |
|---|---|
| Desain aplikasi ini | `docs/cs-desktop-stitch-prompts.md` (repo desain) |
| Kontrak API onboarding | `docs/06-BUKA-REKENING-API-SPEC.md` §5 |
| Alamat per lingkungan | `docs/10-BASE-URL-DAN-ENDPOINT.md` (§2 alamat, §3e signaling, §4 integrasi Android) |
| Arsitektur video call sisi backend | `.claude/skills/buka-rekening-video-call-backend/SKILL.md` |
| Sisi nasabah (Android) | project `BcaMobile` → `.claude/skills/buka-rekening-video-call/SKILL.md` |
| Penjaga auth | `internal/middleware/internal_api_key.go`, `migrations/000026_cs_agents.up.sql` |
| Seed petugas development | `scripts/seed/main.go` → `seedCSAgents` |

Skill `buka-rekening-video-call-backend` adalah rujukan paling dalam untuk
perilaku server — model Hub/Room, siklus status, dan jebakan di repo ini. Baca
sebelum menyimpulkan ada bug di server.
