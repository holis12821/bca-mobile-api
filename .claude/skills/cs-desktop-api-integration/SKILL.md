---
name: cs-desktop-api-integration
description: Integrasi API untuk aplikasi desktop petugas CS Halo BCA — tiga puluh empat endpoint sisi operator: identitas & sesi petugas (`auth/login`, `auth/logout`, `auth/me`, `auth/password`), pendaftaran & perubahan kewenangan petugas serta direktori pegawai (`POST /agents`, `PATCH /agents/{npp}`, `hris/employees/{npp}`), siklus hidup terminal dan tiga gerbang kesiapan (`terminals`, `terminals/readiness`, `healthcheck`, `pii-ack`, `activate`, `deactivate`, `supervisors`, `supervisors/authorize`), beranda petugas & jejak audit (`cs/dashboard`, `cs/audit-events`), penyelesaian eskalasi Tier 2 (`GET /internal/v1/escalations`, `PATCH /internal/v1/escalations/{id}`), video call e-KYC (`video-call/queued`, `agent-token`, `signal`, `result`), pemantauan sesi onboarding (`GET /internal/v1/onboarding/sessions` dan `/sessions/{id}` berisi PII tersamar), pencarian & profil nasabah (`/internal/v1/customers`), tiket layanan (`/internal/v1/tickets`), administrasi katalog kartu, serta penjadwalan ulang video call sisi nasabah. Autentikasi empat lapis: `X-Internal-API-Key` (sistem), `X-Agent-Employee-ID` + `X-Agent-API-Key` (petugas, Argon2id ke `cs_agents`), cakupan `cs_agents.scopes` — `VIDEO_CALL`, `CUSTOMER_PII`, `CARD_ADMIN`, `TICKET`, `AUDIT_READ`, `ESCALATION_REVIEW` — dan `Authorization: Bearer <session_token>` untuk endpoint yang terminalnya ditentukan sesi. Juga memuat protokol WebSocket signaling dari sudut pandang agent, WebRTC sisi answerer, kebijakan penyamaran PII, dan jejak akses `cs_access_logs`. Trigger juga pada "ESCALATION_REVIEW", "AUDIT_READ", "eskalasi Tier 2", "ESCALATION_SELF_RESOLVE", "ESCALATION_CLAIMED_BY_OTHER", "AGENT_SELF_UPDATE", "cabut hak petugas", dan "ubah cakupan petugas". Gunakan saat membangun atau men-debug aplikasi desktop/klien CS yang memakai API ini, merancang kewenangan petugas CS, menyambungkan antrean atau pemantauan sesi ke layar petugas, atau menjawab "endpoint CS apa yang tersedia hari ini". Trigger juga pada "aplikasi desktop CS", "petugas CS", "Halo BCA desktop", "X-Agent-API-Key", "X-Agent-Employee-ID", "cs_agents", "scopes petugas", "CUSTOMER_PII", "CARD_ADMIN", "INTERNAL_API_KEY", "agent-token", "video-call/queued", "signaling_url", "ice_servers", "nik_masked", "cs_access_logs", "pencarian nasabah", "profil nasabah untuk CS", "tiket layanan", "ticket_number", "TICKET_INVALID_TRANSITION", "jadwalkan panggilan nanti", "VIDEO_CALL_SCHEDULE_INVALID", "AGENT_AUTH_UNAVAILABLE", "VIDEO_CALL_NOT_ACTIVE", "antrean petugas", "session_token", "auth/me", "login petugas", "logout petugas", "cakupan petugas tidak bisa ditanyakan", "terminals/readiness", "gerbang kesiapan", "TERMINAL_NOT_READY", "TERMINAL_AGENT_BUSY", "AGENT_CREDENTIAL_INVALID", "AGENT_PASSWORD_NOT_SET", "AGENT_LOCKED", "SUPERVISOR_TOKEN_INVALID", "authorization_ref", "HRIS_UNAVAILABLE", "EMPLOYEE_NOT_FOUND", "cs/dashboard", "can_take_calls", dan "cs/audit-events". JANGAN dipakai untuk mengubah server video call itu sendiri — service, hub, atau migrasi (itu `buka-rekening-video-call-backend`), OCR/biometrik/kredensial/submit (itu `buka-rekening-backend`), OTP onboarding (itu `buka-rekening-otp`), katalog kartu sisi nasabah (itu `buka-rekening-kartu`), atau sisi WebRTC Android nasabah (project `BcaMobile`).
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

### 1.2 Autentikasi — empat lapis, empat pertanyaan berbeda

Jalur CS punya **empat** penjaga yang menjawab empat pertanyaan berbeda: *sistem
mana yang memanggil*, *petugas mana yang bertindak*, *boleh melakukan apa*, dan
*sedang bertugas di loket mana*.

**Lapis 1 — sistem (`middleware.InternalAPIKey`), wajib di semua endpoint CS:**

```
X-Internal-API-Key: <INTERNAL_API_KEY>
```

Dibandingkan constant-time (`subtle.ConstantTimeCompare`). `INTERNAL_API_KEY`
kosong **menolak semua permintaan**, bukan menerima header kosong; dan
`config.Validate` menolak start proses produksi tanpa kunci itu.

**Lapis 2 — petugas (`middleware.AgentAuth`), di setiap jalur operator yang
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

**Lapis 3 — cakupan (`cs_agents.scopes`), menentukan boleh melakukan APA:**

| Cakupan | Membuka |
|---|---|
| `VIDEO_CALL` | ambil panggilan, submit hasil, antrean, daftar sesi onboarding |
| `CUSTOMER_PII` | detail sesi berisi data pribadi, pencarian & profil nasabah |
| `CARD_ADMIN` | administrasi katalog kartu Paspor dan katalog produk tabungan |
| `TICKET` | tiket layanan |
| `AUDIT_READ` | jejak audit petugas & terminal — `GET /cs/audit-events` |
| `ESCALATION_REVIEW` | antrean kerja Tier 2 dan penutupan perkara `NEED_REVIEW` |

Cakupannya diminta di titik pasang rute, bukan dibaca di handler, jadi sebuah endpoint
operator tidak bisa terpasang tanpa menyatakan kewenangan yang dituntutnya.

`CUSTOMER_PII` **dipisah** dari `VIDEO_CALL` meski aplikasi desktop yang sama memakai
keduanya: melayani panggilan menampilkan nasabah yang *sedang* bicara, sementara membuka
data pribadi menjangkau nasabah mana pun yang pernah mendaftar.

**Dua cakupan terakhir dibuka migrasi `000041`**, yang melebarkan CHECK
`cs_agents_scopes_valid` dari `000027`. Keduanya bukan penghalusan:

- `AUDIT_READ` — sebelum ini `GET /cs/audit-events` hanya menuntut identitas petugas, jadi
  setiap petugas terautentikasi bisa membaca jejak **rekannya**: jam login, loket, dan
  setiap otorisasi supervisor yang pernah gagal atas nama seseorang. **Pemanggil lama
  tanpa cakupan ini sekarang dijawab `403`** — ini perubahan yang memutus. `GET
  /cs/dashboard` tetap hanya menuntut identitas petugas, dan bedanya disengaja: beranda
  menjawab tentang pekerjaan petugas sendiri, jejak audit tentang pekerjaan orang lain.
- `ESCALATION_REVIEW` — sengaja bukan `VIDEO_CALL`. Sebuah `APPROVED` di jalur eskalasi
  memindahkan nasabah ke `CREDENTIALS`, keputusan sebesar keputusan panggilannya sendiri;
  cakupan yang sama dengan panggilan akan memberikan tepat itu kepada setiap petugas
  Tier 1 — termasuk kepada yang baru saja mengaku tidak sanggup memutuskannya.

Satu petugas boleh memegang beberapa cakupan. Penyelia biasanya memegang `VIDEO_CALL`,
`CUSTOMER_PII`, `TICKET`, `AUDIT_READ`, dan `ESCALATION_REVIEW` sekaligus; petugas
panggilan biasa hanya `VIDEO_CALL`. Peninjau Tier 2 bisa memegang `ESCALATION_REVIEW`
**tanpa** `VIDEO_CALL` — menutup perkara tidak menuntut kewenangan mengambil panggilan.

Aplikasi desktop **menanyakan cakupannya lewat `GET /internal/v1/auth/me`** (nomor 17).
Jangan lagi menyimpulkannya dari endpoint mana yang dijawab `403` — cara itu menuntut
setiap menu dicoba sekali untuk diketahui, dan setiap percobaan memicu verifikasi
Argon2id (64 MB × 4 thread). Panggil `auth/me` sekali saat aplikasi dibuka, lalu
sembunyikan menu yang cakupannya tidak ada.

**Lapis 4 — sesi petugas (`middleware.AgentSession`), di endpoint yang terminalnya
ditentukan SESI:**

```
Authorization: Bearer <session_token>
```

Token itu lahir di `POST /internal/v1/auth/login` dan **hanya terkirim sekali**; server
menyimpan hash-nya, dan tidak ada endpoint yang bisa mengembalikannya lagi. Dipakai di
seluruh `/terminals/*` kecuali pendaftaran dan pembacaan, seluruh `/supervisors/*`, dan
`auth/logout`.

Alasannya bukan kerapian: endpoint-endpoint itu **tidak punya `{terminal_id}` di path**.
Terminalnya datang dari sesi, jadi petugas tidak bisa menyatakan kesiapan atas loket yang
bukan tempat ia masuk, menanyakan kesiapan loket orang lain, atau menutup giliran orang
lain. Kalau `terminal_id` ada di body, satu nilai yang disunting klien sudah cukup untuk
ketiganya.

Lapis 4 **menambah**, tidak mengganti: ketiga header di atas tetap wajib. Tiga lapis
pertama membuktikan *siapa*; lapis keempat membuktikan *sedang bertugas di mana*.

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
3. **`INTERNAL_API_KEY` masih satu kunci bersama**, tapi ia tidak lagi menentukan
   kewenangan: yang membuka jalur tertentu adalah cakupan petugasnya. Kunci sistem hanya
   menjawab "sistem mana yang memanggil".
4. **Penjaga ini tidak fail-open.** Postgres yang tersendat dijawab
   `503 AGENT_AUTH_UNAVAILABLE`, bukan diluluskan: jalur yang menentukan siapa
   bertanggung jawab atas sebuah verifikasi tidak boleh pernah fail-open.
   `lookup` yang nil (perakitan rute salah) menolak semuanya.

**Kredensial petugas untuk development.** `make seed` menanam tiga petugas dengan
cakupan berbeda, **digerbangi `APP_ENV=development`**:

| `X-Agent-Employee-ID` | `X-Agent-API-Key` | Nama | Cakupan |
|---|---|---|---|
| `CS-1042` | `dev-agent-key` | Sarah Adisti | `VIDEO_CALL` |
| `OPS-2001` | `dev-cardadmin-key` | Budi Hartono | `CARD_ADMIN` |
| `SPV-3001` | `dev-spv-key` | Rina Kusuma | `VIDEO_CALL`, `CUSTOMER_PII`, `TICKET`, `AUDIT_READ`, `ESCALATION_REVIEW` |
| `SPV-3002` | `dev-tier2-key` | Dewi Lestari | `CUSTOMER_PII`, `ESCALATION_REVIEW` |

Empat, bukan satu yang memegang semuanya: pemisahan kewenangan yang tidak pernah diuji
terpisah akan terlihat berfungsi sampai orang pertama yang hanya punya satu scope
mencobanya.

`SPV-3002` ada karena **four-eyes hanya bisa diuji dengan dua orang**: perkara yang
diajukan `CS-1042` ditutup `SPV-3001`, dan perkara yang diajukan `SPV-3001` ditutup
`SPV-3002`. `409 ESCALATION_CLAIMED_BY_OTHER` juga mustahil dipicu oleh satu peninjau.
Ia sengaja **tanpa** `VIDEO_CALL`.

Ketiganya juga diberi **kata sandi** yang sama untuk `auth/login`:
`KataSandiPanjang2026`. Panjangnya bukan selera — minimal 12 karakter, di bawah itu
dijawab `422 AGENT_PASSWORD_WEAK`, dan kata sandi seed yang ditolak endpoint-nya sendiri
adalah seed yang menyesatkan.

`make seed` juga menanam **loket dan supervisor**, karena keduanya tidak punya endpoint
pembuat lengkap: `cs_supervisors` tidak punya endpoint pembuat sama sekali, jadi tanpa
seed gerbang 1 tidak bisa dilewati di development.

| Loket | Lokasi | | Supervisor | Lokasi | Token |
|---|---|---|---|---|---|
| `WKS-SMG-0842` (Loket 4) | KCU Semarang | | `SPV-0021` Budi Hartono | KCU Semarang | `123456` |
| `WKS-JKT-0117` (Loket 1) | KCU Jakarta Thamrin | | `SPV-0022` Rina Kusuma | KCU Jakarta Thamrin | `654321` |

Token supervisor di-hash Argon2id seperti kunci petugas dan **tidak pernah** dikembalikan
endpoint mana pun — `GET /supervisors` memuat nama dan lokasi saja. `#BCA-AUTH-…` yang
muncul di layar adalah `authorization_ref`, **rujukan tanda terima**, bukan tokennya.

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

| # | Method | Path | Penjaga | Guna |
|---|---|---|---|---|
| 1 | GET | `/v1/onboarding/video-call/queued` | internal key | Daftar antrean yang menunggu |
| 2 | POST | `/v1/onboarding/video-call/agent-token` | + agent `VIDEO_CALL` | Ambil panggilan, dapat `signaling_url` |
| 3 | GET | `/v1/onboarding/video-call/signal?token=` | token sekali pakai | WebSocket signaling (role agent) |
| 4 | POST | `/v1/onboarding/video-call/result` | + agent `VIDEO_CALL` | Submit hasil verifikasi |
| 5 | GET | `/v1/onboarding/sessions/{session_id}/audit` | internal key | Jejak audit satu sesi |
| 6 | GET | `/v1/onboarding/monitoring` | internal key | Hitungan sesi, panjang antrean, peringatan |
| 7 | GET/PUT | `/internal/v1/cards`, `/cards/{card_type}`, `/products/{product_type}/cards/{card_type}` | + agent `CARD_ADMIN` | Administrasi katalog kartu |
| 8 | GET | `/internal/v1/onboarding/sessions` | + agent `VIDEO_CALL` | Daftar sesi onboarding (tanpa PII) |
| 9 | GET | `/internal/v1/onboarding/sessions/{session_id}` | + agent `CUSTOMER_PII` | Detail sesi + data pribadi tersamar |
| 10 | GET | `/internal/v1/customers?q=` | + agent `CUSTOMER_PII` | Cari nasabah (cocok persis) |
| 11 | GET | `/internal/v1/customers/{user_id}` | + agent `CUSTOMER_PII` | Profil nasabah |
| 12 | POST/GET | `/internal/v1/tickets` | + agent `TICKET` | Buat / daftar tiket |
| 13 | GET/PATCH | `/internal/v1/tickets/{ticket_number}` | + agent `TICKET` | Detail / ubah tiket |
| 14 | POST | `/internal/v1/tickets/{ticket_number}/notes` | + agent `TICKET` | Catatan tindak lanjut |
| 15 | POST | `/internal/v1/auth/login` | kunci sistem SAJA | NPP + kata sandi → `session_token` bertenggat |
| 16 | POST | `/internal/v1/auth/password` | + identitas petugas | Setel / ganti kata sandi petugas |
| 17 | GET | `/internal/v1/auth/me` | + identitas petugas | Siapa saya, cakupan apa, ada giliran terbuka atau tidak |
| 18 | POST | `/internal/v1/auth/logout` | + `session_token` | Tutup giliran, terminal → `OFFLINE` |
| 19 | GET | `/internal/v1/hris/employees/{employee_id}` | + identitas petugas | Lookup NPP ke direktori pegawai (sistem LUAR) |
| 20 | POST | `/internal/v1/agents` | + identitas petugas | Daftarkan petugas; dual-control di BODY; `api_key` sekali tampil |
| 21 | POST | `/internal/v1/terminals` | + identitas petugas | Daftarkan loket |
| 22 | GET | `/internal/v1/terminals/{terminal_id}` | kunci sistem SAJA | Baca loket sebelum ada yang masuk |
| 23 | GET | `/internal/v1/supervisors?location=` | + `session_token` | Daftar supervisor; TIDAK memuat tokennya |
| 24 | POST | `/internal/v1/supervisors/authorize` | + `session_token` | Gerbang 1 `SUPERVISOR_AUTH` → `authorization_ref` |
| 25 | POST | `/internal/v1/terminals/healthcheck` | + `session_token` | Gerbang 2 `DEVICE_HEALTHCHECK` (pernyataan klien) |
| 26 | POST | `/internal/v1/terminals/pii-ack` | + `session_token` | Gerbang 3 `PII_ACK` + `pact_version` |
| 27 | GET | `/internal/v1/terminals/readiness` | + `session_token` | Ketiga gerbang sekaligus + `can_activate` |
| 28 | POST | `/internal/v1/terminals/activate` | + `session_token` | → `ONLINE`; menuntut ketiga gerbang lolos |
| 29 | POST | `/internal/v1/terminals/deactivate` | + `session_token` | → `OFFLINE` tanpa menutup sesi |
| 30 | GET | `/internal/v1/cs/dashboard` | + identitas petugas | Beranda petugas: hari ini, antrean, terminal |
| 31 | GET | `/internal/v1/cs/audit-events` | + agent `AUDIT_READ` | Tiga belas peristiwa audit petugas & terminal |
| 32 | PATCH | `/internal/v1/agents/{employee_id}` | + identitas petugas | Ubah cakupan / cabut hak petugas; dual-control di BODY |
| 33 | GET | `/internal/v1/escalations` | + agent `ESCALATION_REVIEW` | Antrean kerja Tier 2, terlama dulu |
| 34 | PATCH | `/internal/v1/escalations/{escalation_id}` | + agent `ESCALATION_REVIEW` | `CLAIM` pegang perkara, `RESOLVE` tutup |

Nomor 3 **tidak** memakai header apa pun — penjaganya token sekali pakai di query string.
Nomor 1, 5, 6, 15, dan 22 sengaja **hanya** berpenjaga kunci sistem, masing-masing dengan
alasannya sendiri: melihat antrean, jejak audit, dan kesehatan sistem adalah tindakan
sistem/pengawas yang tidak diatribusikan ke seseorang; `auth/login` justru tempat petugas
MEMBUKTIKAN dirinya, jadi menuntut kredensial petugas di sana akan membuatnya harus sudah
masuk untuk bisa masuk; dan `terminals/{id}` dibaca layar masuk **sebelum** ada petugas
yang masuk. Sisanya jelas tindakan seseorang.

**Nomor 31 berpindah penjaga, dan itu memutus pemanggil lama.** Ia dulu hanya menuntut
identitas petugas; sekarang `AUDIT_READ` (migrasi `000041`). Petugas yang sebelumnya
memakainya perlu cakupan itu ditambahkan lewat nomor 32 — tanpa itu jawabannya `403`.

Nomor 16, 17, 20, 21, dan 32 memakai identitas petugas tapi **tidak** menuntut cakupan
tertentu, dan itu disengaja. `auth/password` adalah jalur penyetelan PERTAMA —
pemanggilnya belum bisa punya sesi. `auth/me` justru yang MEMBERI TAHU cakupan, jadi
menuntut cakupan di sana memutar balik. Dan mendaftarkan/mengubah petugas atau memasang
terminal adalah pekerjaan supervisor/teknisi; tidak satu pun dari enam cakupan yang ada
menggambarkannya.

Yang menahan penyalahgunaan nomor 32 bukan cakupan, melainkan **larangan mengubah diri
sendiri** (`403 AGENT_SELF_UPDATE`) plus dual-control supervisor di body. Cakupan "boleh
memberi kewenangan" yang dipegang satu orang justru kewenangan yang bisa dipakai
menaikkan kewenangannya sendiri.

**Dua prefix, dan bedanya nyata.** Endpoint CS yang lebih tua ada di `/v1/onboarding/*`
dan ikut terkena rate limit, body limit, serta CORS jalur nasabah. Yang lebih baru ada di
`/internal/v1/*` dan tidak. Endpoint CS berikutnya menyusul ke `/internal/v1`.

Nomor 8–34 dirinci di `docs/01-API-SPECIFICATION.md` §11; di bawah hanya yang berkaitan
langsung dengan satu panggilan video. **Urutan pemanggilannya** pada hari kerja petugas
ada di §3.1.

### 2.1 `GET /video-call/queued`

Pintu masuk seluruh aplikasi. Tanpa ini tidak ada cara menemukan `queue_id`
yang dibutuhkan endpoint berikutnya.

```json
{
  "calls": [
    { "queue_id": "q_abc123", "queue_number": "A-042",
      "session_id": "onb_9f8e7d6c5b4a",
      "position": 1, "waited_seconds": 95, "status": "QUEUED",
      "priority": "NORMAL", "service": "EKYC_ONBOARDING" }
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
- `priority` **diturunkan dari `waited_seconds`**, tidak disimpan: `HIGH` setelah
  menunggu 10 menit, `NORMAL` sebelum itu. Tidak ada sumber prioritas lain di sistem
  ini — nasabah yang belum punya rekening belum punya tier apa pun, jadi kolom
  prioritas akan berisi nilai yang sama di semua baris. Sepuluh menit juga bukan
  selera: `estimated_wait_seconds` yang dikirim ke nasabah dihitung dari rata-rata
  panggilan, jadi menunggu lebih lama dari dua kali itu berarti antreannya tidak
  bergerak sebagaimana ia diberi tahu. Urutkan tampilan dengan `priority` kalau mau,
  tapi `position` tetap yang disepakati dengan nasabah.
- `service` selalu `EKYC_ONBOARDING` hari ini — satu-satunya layanan yang mengantre di
  sini. Ada di kontrak supaya layar petugas tidak perlu berubah bentuk saat layanan
  kedua menyusul. Jangan menyaring berdasarkan nilai lain yang belum ada.

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

**`422 TERMINAL_NOT_ONLINE` adalah Rule 4, dan di sinilah satu-satunya tempat ia
ditegakkan.** Petugas yang terminalnya belum `ONLINE` tidak bisa mengambil panggilan —
selesaikan ketiga gerbang lalu `POST /terminals/activate` (§3.1). Pemeriksaannya
dilakukan **sebelum** barisnya dibaca, jadi petugas yang terminalnya belum aktif tidak
bisa menyimpulkan `queue_id` mana yang sah dari selisih pesan error. Jangan menampilkan
tombol "Ambil Panggilan" sebelum `can_take_calls` di `cs/dashboard` bernilai `true`.

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

Dua jenis pesan — `queue_update` dan `agent_assigned` — hanya dikirim ke **nasabah**.
Abaikan kalau muncul.

**`call_ended` sekarang sampai ke petugas juga**, dan aplikasi desktop harus
menanganinya — itu penanda sah untuk membongkar `PeerConnection`, tanpa perlu menebak dari
response HTTP yang mungkin terlewat:

| Bidang | Arti |
|---|---|
| `result` terisi (`APPROVED`/`REJECTED`) | Hasil submit, termasuk submit petugas itu sendiri |
| `reason` terisi, `result` kosong | Panggilan **dilepas**: nasabah membatalkan sesinya, atau barisnya basi |

Bedakan keduanya di UI: yang pertama berarti isi hasil verifikasi sudah tercatat, yang
kedua berarti kembali ke antrean karena lawan bicaranya sudah tidak ada.

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
- `result` ∈ `APPROVED` · `REJECTED` · `NEED_REVIEW`. Nilai lain `VALIDATION_ERROR`.
- **`APPROVED` memindahkan langkah nasabah ke `CREDENTIALS`.** Bukan membuat
  rekening — rekeningnya lahir tiga langkah kemudian di `POST /submit`. Jangan
  menulis "rekening berhasil dibuat" di layar petugas.
- **`REJECTED` tidak memindahkan langkah.** Nasabah tetap di `VIDEO_CALL` dan
  bisa mengantre lagi. Pastikan teks di aplikasi desktop tidak menyiratkan
  pengajuannya ditutup.
- **`rejection_reason` WAJIB saat `REJECTED`**, dan ber-enum: `IDENTITY_MISMATCH`,
  `INVALID_DOCUMENT`, `FACE_MISMATCH`, `SUSPICIOUS_ACTIVITY`,
  `INCOMPLETE_INFORMATION`, `OTHER`. Bukan teks bebas, karena alasan penolakan
  verifikasi identitas akan dilaporkan dan dihitung — teks bebas membuat "KTP tidak
  jelas", "ktp blur", dan "dokumen tidak terbaca" jadi tiga kategori berbeda. Pakai
  `notes` untuk keterangannya, bukan untuk kategorinya. Diabaikan pada hasil lain.
- **`NEED_REVIEW` juga tidak memindahkan langkah**, dan itu disengaja: tidak ada
  nilai baru di enum `onboarding_step`, jadi aplikasi Android tidak pernah menemui
  `current_step` yang tidak dikenalnya. Yang menahan nasabah supaya tidak mengantre
  lagi adalah **baris eskalasi**, bukan langkahnya — `POST /video-call/queue`
  menjawab `422 VIDEO_CALL_UNDER_REVIEW` selama eskalasinya terbuka, dan itu
  diperiksa **sebelum** jam operasional: nasabah yang sedang ditinjau perlu diberi
  tahu bahwa perkaranya ditangani, bukan soal jam layanan.
- **`notes` WAJIB saat `NEED_REVIEW`.** Eskalasi tanpa keterangan memaksa Tier 2
  mengulang seluruh panggilannya untuk tahu apa yang mengganjal. Kosong →
  `VALIDATION_ERROR`.
- **`escalation_queue` pada `NEED_REVIEW`** ∈ `TIER_2_VERIFICATION` ·
  `FRAUD_REVIEW` · `COMPLIANCE_REVIEW`. Kosong berarti `TIER_2_VERIFICATION`.
- **Response `NEED_REVIEW` memuat objek `escalation`** (`escalation_id`, `status`,
  `escalation_queue`, `raised_by_agent`, `raised_at`). Bedakan dengan `REJECTED`
  lewat field itu, **bukan** lewat `current_step` — keduanya meninggalkan nasabah di
  `VIDEO_CALL`, jadi `current_step` saja tidak bisa membedakan eskalasi dari
  penolakan.
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

### 3.1 Hari kerja petugas — sebelum dan sesudah panggilan

Diagram di atas adalah satu panggilan. Panggilan itu tidak bisa dimulai sebelum
urutan berikut selesai, dan nomor 2 di diagram (`agent-token`) **menolak** terminal
yang belum `ONLINE`:

```
 1  POST /auth/password        sekali saja, saat petugas pertama kali dapat kunci
 2  POST /auth/login           → session_token, next_step: TERMINAL_READINESS
 3  GET  /auth/me              cakupan apa yang dipegang → menu mana yang tampil
 4  GET  /terminals/readiness  ketiga gerbang, semuanya belum lolos
 5  GET  /supervisors          → pilih supervisor
 6  POST /supervisors/authorize   gerbang 1 → authorization_ref
 7  POST /terminals/healthcheck   gerbang 2 (probe di KLIEN)
 8  POST /terminals/pii-ack       gerbang 3 + pact_version
 9  GET  /terminals/readiness  can_activate: true
10  POST /terminals/activate   → ONLINE. BARU DI SINI antrean bisa diambil
11  GET  /cs/dashboard         beranda petugas
       … satu atau banyak panggilan: diagram §3 …
12  POST /terminals/deactivate → OFFLINE saat istirahat (sesi TETAP hidup)
13  POST /auth/logout          saat pulang: sesi ditutup DAN terminal → OFFLINE
```

Tiga hal yang gampang salah di urutan ini:

- **`next_step: "TERMINAL_READINESS"` di jawaban login bukan hiasan.** Ia dikirim
  server supaya aturan "sesi baru wajib lewat layar kesiapan" tidak hidup hanya di
  routing klien, yang bisa disunting. Jangan lompat dari login ke dashboard.
- **Gerbang berlaku 8 jam, dan yang kedaluwarsa dibedakan dari yang belum pernah
  lolos** (`expired: true` vs `passed: false`). Yang pertama diselesaikan dengan
  mengulang probe; yang kedua mungkin berarti satu layar dilewati.
- **`deactivate` dan `logout` bukan sinonim.** Istirahat menonaktifkan loket tanpa
  mengakhiri giliran; pulang melakukan keduanya. Memanggil `logout` untuk istirahat
  memaksa petugas mengulang ketiga gerbang sesudahnya.

---

## 4. Penanganan kegagalan

| Keadaan | Kode | Yang benar dilakukan |
|---|---|---|
| `X-Internal-API-Key` salah atau kosong | `403 FORBIDDEN` | Kembali ke layar masuk, sebut bahwa kunci sistemnya yang ditolak |
| Kredensial petugas salah, atau petugas `is_active = false` | `403 FORBIDDEN` | Minta petugas masuk ulang; kalau berulang, barisnya di `cs_agents` yang perlu diperiksa |
| Cakupan petugas kurang untuk jalur itu | `403 FORBIDDEN` | **Tidak terbedakan dari kredensial salah di response.** Jangan menunggu `403` untuk tahu — baca `scopes` dari `GET /auth/me` saat aplikasi dibuka, lalu sembunyikan menunya. Yang perlu diperbaiki: `scopes` di barisnya |
| Header petugas tidak dikirim di `agent-token`/`result` | `403 FORBIDDEN` | Bug aplikasi — kedua header wajib di dua endpoint itu |
| Postgres tersendat saat verifikasi petugas | `503 AGENT_AUTH_UNAVAILABLE` | Boleh dicoba lagi dengan backoff. Ini masalah infrastruktur, bukan kredensial |
| Panggilan sudah diambil petugas lain | `422 VIDEO_CALL_NOT_ACTIVE` | Muat ulang antrean, **jangan** coba lagi |
| `queue_id` tidak dikenal | `404 ONBOARDING_NOT_FOUND` | Muat ulang antrean |
| Sesi nasabah kedaluwarsa | `422 ONBOARDING_SESSION_EXPIRED` | Panggilan tidak bisa dilanjutkan; akhiri |
| `result` di luar APPROVED/REJECTED/NEED_REVIEW | `400 VALIDATION_ERROR` | Bug aplikasi, bukan kesalahan petugas |
| `REJECTED` tanpa `rejection_reason`, atau alasan di luar enam enum | `400 VALIDATION_ERROR` | Alasannya wajib dan ber-enum. Jangan kirim teks bebas ke field itu — tempatnya `notes` |
| `NEED_REVIEW` tanpa `notes` | `400 VALIDATION_ERROR` | Eskalasi tanpa keterangan memaksa Tier 2 mengulang seluruh panggilan |
| `NEED_REVIEW` saat repo eskalasi tidak terpasang | `503 PROVIDER_NOT_CONFIGURED` | Menolak, bukan menyimpan hasil tanpa eskalasi: sesi yang menggantung tanpa jalan keluar lebih buruk daripada permintaan yang ditolak |
| Nasabah mengantre lagi padahal eskalasinya terbuka | `422 VIDEO_CALL_UNDER_REVIEW` | Dilihat **nasabah**, bukan petugas. Perkaranya sedang ditangani; penyelesaiannya lewat `PATCH /escalations/{id}` (#34) |
| `escalation_id` tidak dikenal | `404 ESCALATION_NOT_FOUND` | Muat ulang antrean kerja |
| Perkara sudah ditutup petugas lain | `409 ESCALATION_ALREADY_CLOSED` | **Muat ulang daftarnya, jangan perbaiki formulirnya.** Bentuk permintaannya sah; keadaan perkaranya yang berubah |
| Perkara `IN_REVIEW` dipegang peninjau lain | `409 ESCALATION_CLAIMED_BY_OTHER` | `details.claimed_by_agent` menyebut siapa. Tampilkan namanya, jangan hanya "gagal" |
| Pengaju eskalasi mencoba memegang/menutup perkaranya sendiri | `403 ESCALATION_SELF_RESOLVE` | Four-eyes, dan **bukan bug**. Perkara itu harus diputus petugas lain. Sembunyikan tombolnya saat `raised_by_agent` = NPP pemakai aplikasi |
| `?queue=` atau `?status=` salah ketik di `/escalations` | `422 ESCALATION_QUEUE_UNKNOWN` / `422 ESCALATION_STATUS_UNKNOWN` | `details.allowed` memuat daftar yang sah. Ditolak, bukan dijawab daftar kosong — nol perkara karena salah ketik tidak bisa dibedakan dari antrean yang memang bersih |
| `RESOLVE` tanpa `notes`, atau `REJECTED` tanpa `rejection_reason` ber-enum | `400 VALIDATION_ERROR` | Bug aplikasi. `notes` wajib, dan alasan penolakan memakai enam enum yang SAMA dengan Tier 1 |
| `GET /cs/audit-events` dijawab `403` padahal dulu jalan | `403 FORBIDDEN` | **Bukan regresi.** Endpoint itu sekarang menuntut `AUDIT_READ` (migrasi `000041`); tambahkan cakupannya lewat `PATCH /agents/{npp}` |
| Petugas mengubah cakupan/keaktifan dirinya sendiri | `403 AGENT_SELF_UPDATE` | Four-eyes. Harus petugas lain, dan tetap dengan tanda tangan supervisor |
| `PATCH /agents/{npp}` untuk NPP yang belum pernah didaftarkan | `404 AGENT_NOT_FOUND` | Berbeda dari `EMPLOYEE_NOT_FOUND`: pegawainya ada, petugasnya belum. Jalan keluarnya `POST /agents` |
| `PATCH /agents/{npp}` dengan `"scopes": []` | `422 SCOPE_EMPTY` | Yang dimaksud hampir selalu `is_active: false`; pesannya menyebutkan itu |
| Terminal petugas belum `ONLINE` saat `agent-token` | `422 TERMINAL_NOT_ONLINE` | Rule 4. Selesaikan ketiga gerbang lalu `POST /terminals/activate` — lihat §3.1 |
| `activate` dengan gerbang belum lengkap | `422 TERMINAL_NOT_READY` | Pesannya **menyebut gerbang mana** yang kurang. Arahkan petugas ke layar itu, jangan tampilkan "belum siap" saja |
| Petugas login padahal masih aktif di terminal lain | `409 TERMINAL_AGENT_BUSY` | Giliran di loket sebelumnya belum ditutup. `auth/logout` di sana, atau hubungi pengawas |
| NPP atau kata sandi salah di `auth/login` | `401 AGENT_CREDENTIAL_INVALID` | Boleh dicoba lagi, **tapi ada lockout** — lihat baris di bawah |
| Kata sandi belum pernah disetel | `422 AGENT_PASSWORD_NOT_SET` | Arahkan ke `POST /auth/password` dengan kunci API petugas, bukan ke layar login |
| Terlalu banyak percobaan login gagal | `423 AGENT_LOCKED` | Jangan coba lagi otomatis; itu hanya memperpanjang blokirnya |
| Token supervisor salah | `401 SUPERVISOR_TOKEN_INVALID` | Dicatat sebagai `SUPERVISOR_AUTH_FAILED` di jejak audit — percobaan pemberian kewenangan yang ditolak justru yang paling perlu terbaca |
| Direktori pegawai tidak terpasang / tidak terjangkau | `503 HRIS_UNAVAILABLE` | **Boleh dicoba lagi.** Jangan tampilkan sebagai "NPP tidak ditemukan" — itu `404 EMPLOYEE_NOT_FOUND`, dan kesimpulannya berbeda |
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

Daftar ini **sudah banyak berkurang**. Dua yang terakhir dicoret: `PATCH /agents/{npp}`
(#32) dan cakupan `AUDIT_READ` (migrasi `000041`) sekarang ADA — jangan bangun ulang,
dan jangan lagi menganggap `GET /cs/audit-events` sebagai endpoint tanpa pembatas
kewenangan. Yang masih kosong:

| Kebutuhan | Endpoint yang belum ada | Catatan |
|---|---|---|
| OIDC / SSO direktori pegawai | — | Sesi bertenggat **sudah ada** (`auth/login` → `session_token`, lihat §1.2 lapis 4), begitu juga kata sandi per petugas. Yang belum: identitas dari penyedia luar, dan rotasi kunci API petugas |
| Pengukur SLA, CSAT, dan target shift | — | `cs/dashboard` mengirim ketiganya `null` dengan `measured` yang menyebutkannya. Tidak ada definisi SLA yang disepakati dan tidak ada mekanisme pengukuran kepuasan nasabah. **Jangan tampilkan `null` sebagai `0%`** |
| Integrasi HRIS sungguhan | — | `GET /hris/employees/{npp}` ada, tapi dilayani `cs.MockHRISDirectory` di `APP_ENV=development` dan `503 HRIS_UNAVAILABLE` di environment lain. HRIS adalah sistem luar; tidak ada tabel pegawai di database ini |
| Pratinjau dokumen/OCR sisi CS di ruang panggilan | — | Petugas melihat data pribadi tersamar lewat `/internal/v1/onboarding/sessions/{id}`, tapi tidak bisa membuka citra e-KTP atau selfie-nya. SCR-013 masih **SEBAGIAN** karenanya |
| `call_ended` ke sisi petugas saat nasabah menutup socket | — | `call_ended` **sudah** dikirim ke dua sisi saat hasil disubmit dan saat panggilan dilepas (sesi dibatalkan / baris basi). Yang belum: nasabah yang socketnya putus begitu saja — belum ada pemicu yang mengubah itu jadi peristiwa |
| Penjadwalan yang otomatis mengantre | — | `POST /v1/onboarding/video-call/schedule` **sudah ada**, tapi ia janji, bukan tempat: nasabah tetap memanggil `/video-call/queue` saat waktunya. Mengantre otomatis menuntut penjadwal sisi server, dan antrean yang terisi tanpa nasabah di socketnya akan dilayani petugas ke ruang kosong |
| Daftar jadwal untuk sisi CS | `GET /internal/v1/onboarding/schedules` | Tabelnya sudah ada beserta indeks `idx_vc_schedules_upcoming` ("siapa yang dijadwalkan dalam satu jam ke depan"), tapi belum ada endpoint yang membacanya |

**Sudah selesai** (dulu ada di daftar ini):

- Cakupan kewenangan terpisah — §1.2
- `GET /internal/v1/onboarding/sessions` dan `/sessions/{id}` dengan PII tersamar
- Pencarian nasabah dan profilnya — `/internal/v1/customers*`
- Tiket layanan — `/internal/v1/tickets*`
- Penjadwalan ulang video call — tombol "Jadwalkan Panggilan Nanti" **bisa dinyalakan**
- `call_ended` ke sisi petugas pada submit dan pelepasan panggilan
- **Sesi petugas bertenggat** — `auth/login` / `auth/logout` / `auth/me`, §1.2 lapis 4
- **Aplikasi desktop bisa menanyakan cakupannya** — `GET /auth/me` (nomor 17). Berhenti
  menyimpulkannya dari `403`
- **Siklus hidup terminal di server**, dan `agent-token` **menolak** terminal yang bukan
  `ONLINE`. Rule 4 ditegakkan, bukan dihias
- **Tiga gerbang kesiapan** — otorisasi supervisor, healthcheck, pakta integritas PII,
  masing-masing berlaku 8 jam, dengan `can_activate` dihitung server
- **Pendaftaran petugas lewat API** — `POST /agents`, dual-control di body, `api_key`
  diterbitkan server dan sekali tampil
- **Beranda petugas** — `GET /cs/dashboard`, satu permintaan untuk satu layar
- **Tiga belas peristiwa audit petugas & terminal** — `GET /cs/audit-events`, sekarang
  di belakang cakupan `AUDIT_READ` (migrasi `000041`). Yang ketiga belas `AGENT_UPDATED`
- **`NEED_REVIEW` dan `rejection_reason` ber-enum** di `POST /video-call/result`, plus
  `priority` dan `service` di `GET /video-call/queued`
- **Penyelesaian eskalasi Tier 2** — `GET /internal/v1/escalations` dan
  `PATCH /internal/v1/escalations/{id}` (nomor 33–34). Ini yang paling mendesak di daftar
  lama: perkara `NEED_REVIEW` yang tidak pernah ditutup membuat nasabahnya **tidak bisa
  mengantre lagi, selamanya**, dan satu-satunya pertolongan adalah `UPDATE` di database
  produksi. Four-eyes ditegakkan — pengaju tidak bisa memegang maupun menutup perkaranya
- **Mengubah cakupan / mencabut petugas** — `PATCH /internal/v1/agents/{employee_id}`
  (nomor 32): dual-control supervisor di body, `403 AGENT_SELF_UPDATE`, dan jejak
  `AGENT_UPDATED` yang menyebut cakupan yang dicabut

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
| §5: "Autentikasi per petugas — belum ada" | Sudah ada; yang belum tinggal OIDC/rotasi kunci |
| §5: "Cakupan kewenangan terpisah — belum ada" | Sudah ada: `cs_agents.scopes`, empat cakupan |
| §5: daftar sesi, detail sesi, pencarian & profil nasabah, tiket, penjadwalan ulang | Semuanya **sudah ada** — lihat §2 nomor 8–14 dan `docs/01-API-SPECIFICATION.md` §11–12 |
| §1.2: "Autentikasi — dua lapis" | **Empat lapis.** Lapis keempat `Authorization: Bearer <session_token>` dari `auth/login`, di endpoint yang terminalnya ditentukan sesi |
| §1.2: "Aplikasi desktop tidak bisa menanyakan cakupannya — ia mengetahuinya dari endpoint mana yang dijawab `403`" | **Salah sekarang.** `GET /internal/v1/auth/me` mengembalikan `scopes` dan sesi aktif. Pola `403` lama memicu satu verifikasi Argon2id per menu yang dicoba |
| §2: "empat belas endpoint" | **Tiga puluh empat.** Yang bertambah: identitas & sesi petugas, pendaftaran & perubahan kewenangan petugas, HRIS, terminal + tiga gerbang, supervisor, beranda, jejak audit, eskalasi Tier 2 |
| "Cakupan petugas ada empat" | **Enam.** `AUDIT_READ` dan `ESCALATION_REVIEW` ditambahkan migrasi `000041`, yang melebarkan CHECK `cs_agents_scopes_valid` |
| "`GET /cs/audit-events` cukup dengan identitas petugas" | **Menuntut `AUDIT_READ`.** Pemanggil lama tanpa cakupan itu dijawab `403` — satu-satunya perubahan di skill ini yang MEMUTUS pemanggil yang sudah jalan |
| §5: "Mengubah cakupan petugas — belum ada" | `PATCH /internal/v1/agents/{employee_id}` **sudah ada** (#32). Yang belum tinggal rotasi kunci API |
| §5: "Penyelesaian eskalasi Tier 2 — belum ada, masih SQL langsung" | `GET`/`PATCH /internal/v1/escalations` **sudah ada** (#33–34), dengan four-eyes: pengaju tidak bisa memegang maupun menutup perkaranya sendiri |
| "Status terminal hanya di klien, Rule 4 tidak bisa ditegakkan" | Status terminal **di server**. `agent-token` menolak terminal yang bukan `ONLINE`; `can_take_calls` di `cs/dashboard` adalah Rule 4 yang sudah dihitung |
| "`result` hanya menerima `APPROVED`/`REJECTED`" | Ada `NEED_REVIEW`, dan `rejection_reason` dibatasi enum — alasan penolakan verifikasi identitas akan dilaporkan dan dihitung |
| "`call_ended` hanya dikirim ke nasabah" | Sudah dikirim ke **dua sisi** saat submit hasil dan saat panggilan dilepas |
| "`/internal/v1/cards` dijaga kunci yang sama sehingga ikut terbuka" | Sekarang menuntut petugas ber-scope `CARD_ADMIN`. **Pemanggil lama yang hanya mengirim `X-Internal-API-Key` dijawab `403`** |

---

## 7. Rujukan

| Isi | Berkas |
|---|---|
| Desain aplikasi ini | `docs/cs-desktop-stitch-prompts.md` (repo desain) |
| Kontrak API onboarding | `docs/06-BUKA-REKENING-API-SPEC.md` §5 |
| Alamat per lingkungan | `docs/10-BASE-URL-DAN-ENDPOINT.md` (§2 alamat, §3e signaling, §4 integrasi Android) |
| Arsitektur video call sisi backend | `.claude/skills/buka-rekening-video-call-backend/SKILL.md` |
| Sisi nasabah (Android) | project `BcaMobile` → `.claude/skills/buka-rekening-video-call/SKILL.md` |
| Penjaga auth & cakupan | `internal/middleware/internal_api_key.go`, `migrations/000026_cs_agents.up.sql`, `migrations/000027_cs_agent_scopes.up.sql` |
| Seed petugas development | `scripts/seed/main.go` → `seedCSAgents` |
| Kontrak endpoint operator | `docs/01-API-SPECIFICATION.md` §11 (operator/CS) dan §12 (penjadwalan ulang) |
| Jejak akses petugas | `migrations/000029_cs_access_logs.up.sql`, `internal/domain/cs/` |
| Eskalasi Tier 2 | `migrations/000038_video_call_escalations.up.sql` + `000042_video_call_escalation_resolution.up.sql`; `internal/domain/onboarding/escalation_service.go`, `internal/handler/cs_escalation_handler.go` |
| Cakupan `AUDIT_READ` & `ESCALATION_REVIEW` | `migrations/000041_cs_scope_audit_read.up.sql`; `middleware.ScopeAuditRead`, `middleware.ScopeEscalationReview` |
| Perubahan kewenangan petugas | `cs.AgentSessionService.UpdateAgent`, `postgres.CSAgentRegistryRepo.Update` |
| Tiket layanan | `migrations/000030_service_tickets.up.sql`, `internal/domain/ticket/` |

Skill `buka-rekening-video-call-backend` adalah rujukan paling dalam untuk
perilaku server — model Hub/Room, siklus status, dan jebakan di repo ini. Baca
sebelum menyimpulkan ada bug di server.
