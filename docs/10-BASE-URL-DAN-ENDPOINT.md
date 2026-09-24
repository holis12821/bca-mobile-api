# 10 — Base URL & Endpoint Pendukung

> Satu tempat untuk menjawab "API-nya di mana". Kontrak payload ada di
> `01-API-SPECIFICATION.md` dan `06-BUKA-REKENING-API-SPEC.md`; dokumen ini
> hanya soal **alamat** — per lingkungan, per kanal, dan variabel apa yang
> menentukannya.
>
> Untuk langkah menjalankan server dan Postman, lihat `09-POSTMAN-DAN-NGROK.md`.

---

## 1. Tiga kanal, tiga base URL

Servisnya satu proses, tapi klien memanggilnya lewat tiga pintu yang berbeda
dan **tidak** semuanya diturunkan dari alamat yang sama.

| Kanal | Prefix | Penjaga | Ditentukan oleh |
|---|---|---|---|
| API nasabah | `/v1` | JWT (sebagian publik) | `SERVER_HOST` + `SERVER_PORT` |
| API internal | `/internal/v1` | `X-Internal-API-Key` | idem |
| WebSocket signaling | `/v1/onboarding/video-call/signal` | token di query string | **`SIGNALING_BASE_URL`** |

> ### ⚠ Base URL WebSocket berdiri sendiri
>
> Server **tidak** menurunkan alamat WebSocket dari alamat HTTP-nya. Nilai
> `SIGNALING_BASE_URL` diserahkan apa adanya ke klien sebagai `signaling_url`
> (`internal/domain/onboarding/video_call_service.go:290`):
>
> ```text
> {SIGNALING_BASE_URL}/v1/onboarding/video-call/signal?token=<token>
> ```
>
> Kalau nilainya masih default `ws://localhost:8080`, HP tester menyambung ke
> **dirinya sendiri** dan video call tidak akan pernah tersambung — tanpa error
> yang menunjuk ke sini. Di luar `APP_ENV=development`, `config.Validate`
> menolak start kalau skemanya bukan `wss://`
> (`internal/config/config.go:255`).

---

## 2. Base URL per lingkungan

### 2a. Lokal

```text
HTTP API       http://localhost:8080/v1
API internal   http://localhost:8080/internal/v1
WebSocket      ws://localhost:8080
```

Port dari `SERVER_PORT` (default `8080`). `make dev` dan `make run` menjalankan
server yang sama dan tidak bisa bersamaan — yang kedua kalah merebut port.
Butuh dua instance: `make run PORT=8081`.

`SERVER_HOST` default `0.0.0.0`, jadi server sudah mendengarkan di semua
antarmuka. Perangkat lain di Wi-Fi yang sama bisa memanggil
`http://<IP-laptop>:8080/v1` tanpa tunnel — tapi itu HTTP polos, dan Android
memblokir cleartext secara default (lihat §4c).

### 2b. ngrok (tunnel untuk tester Android)

Domain statis yang dipakai proyek ini:

```text
HTTP API       https://fibromatous-jerald-postsurgical.ngrok-free.dev/v1
API internal   https://fibromatous-jerald-postsurgical.ngrok-free.dev/internal/v1
WebSocket      wss://fibromatous-jerald-postsurgical.ngrok-free.dev
```

> Domain ini **hanya hidup selama `make tunnel` berjalan** di laptop developer.
> Ini tunnel pengembangan yang mengarah ke mesin pribadi, bukan deployment —
> jangan dipakai sebagai alamat rilis. Kalau tunnel mati, semua klien yang
> menunjuk ke sini dapat error ngrok, bukan error API.

Domain statis (gratis, dari dashboard ngrok) dipakai supaya URL-nya **tidak
berubah tiap restart** — tanpa itu, setiap `make tunnel` menghasilkan host acak
dan setiap tester harus mengubah konstanta di aplikasi.

Menjalankannya:

```bash
make tunnel DOMAIN=fibromatous-jerald-postsurgical.ngrok-free.dev
# terminal lain:
make tunnel-env    # tulis wss://<host> ke SIGNALING_BASE_URL di .env
```

`make tunnel-env` membaca URL dari agent API ngrok (`127.0.0.1:4040`) dan
menulisnya ke `.env`; `air` me-restart server sendiri. Kalau memakai `make run`,
hentikan dan jalankan ulang.

**Dua variabel wajib benar saat pakai tunnel:**

| Variabel | Nilai | Kalau salah |
|---|---|---|
| `SIGNALING_BASE_URL` | `wss://<host-ngrok>` | Video call tidak tersambung — HP menyambung ke dirinya sendiri |
| `TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | ngrok meneruskan dari loopback, jadi **semua** request tercatat sebagai satu IP: seluruh tester berbagi satu jatah rate limit, dan audit trail mencatat tunnel, bukan penelepon |

### 2c. Staging dan production

Belum ada deployment. Yang sudah pasti dari `config.Validate`
(`internal/config/config.go:235`) — di luar `APP_ENV=development` proses
**menolak start** kalau:

- `SIGNALING_BASE_URL` bukan `wss://`
- `INTERNAL_API_KEY` kosong atau berisi placeholder yang sudah dikenal
- `AES_KEY` atau `LOOKUP_HMAC_SECRET` kosong
- `DB_SSLMODE` masih `disable`

Selain itu, semua integrasi eksternal (OCR, Dukcapil, biometrik, object storage,
core banking, SMS) hanya punya mock dan **menolak dengan `503
PROVIDER_NOT_CONFIGURED`** di luar development. Base URL yang benar tidak
membuat flow onboarding jalan di staging sampai provider sungguhan dipasang.

---

## 3. URL pendukung

### 3a. Health — tiga varian, beda kegunaan

| Endpoint | Auth | Untuk |
|---|---|---|
| `GET /v1/health` | tidak | Cek menyeluruh: ping DB + kedua Redis, **plus** config klien. `503` kalau DB atau Redis session mati |
| `GET /v1/health/live` | tidak | Liveness saja — tidak menyentuh DB. Untuk probe orchestrator |
| `GET /v1/health/config` | tidak | Hanya config klien: maintenance mode, `min_app_version`, feature flag |

```bash
curl -s https://fibromatous-jerald-postsurgical.ngrok-free.dev/v1/health | jq
```

`database`, `redis_session`, dan `redis_cache` harus `"ok"`.

`/v1/health/config` adalah yang dipanggil aplikasi Android saat start:

```json
{
  "maintenance_mode": false,
  "min_app_version": "1.0.0",
  "feature_flags": {
    "biometric_login": true,
    "qris_payment": true,
    "ewallet_topup": true,
    "onboarding": true
  }
}
```

Semuanya dari environment — bukan literal di handler, supaya gerbang
force-update dan saklar maintenance benar-benar bisa dioperasikan. Response
di-cache 5 menit di dalam proses, jadi perubahan variabel tidak langsung
terlihat.

### 3b. Dev-only — `/v1/dev/*`

```text
GET  /v1/dev/pin-public-key
POST /v1/dev/encrypt-pin      {"pin": "123456"}
```

**Hanya ter-mount saat `APP_ENV=development`** (`router.go:474`). Di environment
lain rutenya tidak ada sama sekali dan menjawab `404` lewat NotFound handler —
bukan `403`, karena memang tidak terdaftar.

Ada karena Postman tidak punya RSA-OAEP di pre-request script, sementara API
menolak PIN plaintext. Isinya cuma operasi dengan kunci **publik**, yang memang
sudah dipublikasikan di `/v1/onboarding/credentials/public-key` — tetap
digerbang karena endpoint yang ada semata-mata untuk memudahkan testing tidak
punya tempat di production.

Alternatif tanpa server: `go run ./scripts/pinenc`.

### 3c. Kunci publik kredensial — publik, bukan dev-only

```text
GET /v1/onboarding/credentials/public-key
```

Ini yang dipakai aplikasi Android sungguhan untuk mengenkripsi kode akses dan
PIN saat buka rekening. Response membawa `algorithm` (`RSA-OAEP-SHA256`),
`key_id`, dan kuncinya. Tersedia di semua environment.

### 3d. API internal — dua kelompok, satu kunci

Keduanya dijaga header `X-Internal-API-Key`. Tanpa `INTERNAL_API_KEY` yang
diset, middleware menolak **semua** request — tidak ada default, karena default
yang dikirim bersama repo adalah password yang sudah terpublikasi.

**Admin katalog kartu — di bawah `/internal/v1`:**

```text
GET  /internal/v1/cards
PUT  /internal/v1/cards/{card_type}
PUT  /internal/v1/products/{product_type}/cards/{card_type}
```

Ditaruh di prefix terpisah, bukan `/v1`, supaya rate limit, body limit, dan CORS
jalur nasabah tidak berlaku untuk jalur operator — satu operator yang menulis
katalog tidak berbagi jatah laju dengan nasabah.

**CS backend & monitoring onboarding — di bawah `/v1/onboarding`:**

```text
POST /v1/onboarding/video-call/result
POST /v1/onboarding/video-call/agent-token
GET  /v1/onboarding/sessions/{session_id}/audit
GET  /v1/onboarding/monitoring
```

Sengaja di luar batas per-IP grup onboarding: CS backend adalah satu alamat yang
melakukan banyak panggilan sah.

### 3e. WebSocket signaling video call

```text
GET {SIGNALING_BASE_URL}/v1/onboarding/video-call/signal?token=<token>
```

Token didapat dari `POST /v1/onboarding/video-call/queue` (nasabah) atau
`POST /v1/onboarding/video-call/agent-token` (agent CS). Klien **tidak** merakit
URL ini sendiri — server mengembalikannya utuh sebagai `signaling_url`; ikuti
nilai itu apa adanya.

---

## 4. Integrasi Android

### 4a. Konstanta base URL

Retrofit menuntut base URL diakhiri `/`:

```kotlin
// lewat ngrok — tester di luar jaringan lokal
const val BASE_URL = "https://fibromatous-jerald-postsurgical.ngrok-free.dev/v1/"

// emulator, server jalan di laptop yang sama
const val BASE_URL = "http://10.0.2.2:8080/v1/"
```

`10.0.2.2` adalah alias loopback host dari dalam emulator Android. `localhost`
di emulator adalah emulator itu sendiri — sama persis dengan jebakan
`SIGNALING_BASE_URL` di §1.

### 4b. WebSocket tidak ikut base URL di atas

Jangan menyusun URL signaling dari `BASE_URL`. Pakai `signaling_url` dari
response `video-call/queue`. Skemanya `wss://` lewat ngrok, `ws://` hanya di
lokal.

### 4c. Cleartext traffic

Lewat ngrok semuanya HTTPS, jadi tidak ada yang perlu disetel. Kalau menunjuk
langsung ke `http://10.0.2.2:8080` atau `http://<IP-laptop>:8080`, Android
memblokirnya sejak API 28 — butuh `android:usesCleartextTraffic="true"` atau
network security config yang mengizinkan host itu, **khusus build debug**.

### 4d. Halaman peringatan ngrok

ngrok gratis menyisipkan halaman interstitial untuk request yang User-Agent-nya
terlihat seperti browser. OkHttp tidak terkena, tapi kalau muncul HTML alih-alih
JSON, kirim header:

```text
ngrok-skip-browser-warning: true
```

### 4e. CORS tidak berlaku untuk Android

`CORS_ALLOWED_ORIGINS` kosong secara default — tidak ada origin browser yang
dipercaya. Aplikasi Android bukan browser dan tidak mengirim `Origin`, jadi
tidak terpengaruh. Variabel itu hanya perlu diisi kalau ada frontend web.

### 4f. Header yang dikirim di semua request

| Header | Kapan |
|---|---|
| `Authorization: Bearer <access_token>` | Semua endpoint setelah login |
| `X-Device-ID` | Login dan flow onboarding — satu device aktif terikat ke tepat satu user |
| `X-Idempotency-Key` | `POST /v1/onboarding/submit` dan jalur uang |

---

## 5. Variabel environment yang menentukan alamat

| Variabel | Default | Pengaruh |
|---|---|---|
| `SERVER_HOST` | `0.0.0.0` | Antarmuka yang didengarkan |
| `SERVER_PORT` | `8080` | Port HTTP; ikut menentukan `/v1` dan `/internal/v1` |
| `SIGNALING_BASE_URL` | `ws://localhost:8080` | Diserahkan apa adanya sebagai `signaling_url`. Wajib `wss://` di luar development |
| `TRUSTED_PROXIES` | _(kosong)_ | CIDR proxy yang boleh menyetel `X-Forwarded-For`. Kosong = header diabaikan. **Di belakang ngrok wajib `127.0.0.1/32,::1/128`** |
| `CORS_ALLOWED_ORIGINS` | _(kosong)_ | Origin browser yang dipercaya. Tidak relevan untuk Android |
| `INTERNAL_API_KEY` | _(tidak ada)_ | Tanpa ini, seluruh `/internal/v1` dan grup CS onboarding menolak |
| `APP_ENV` | `development` | Menentukan ada-tidaknya `/v1/dev/*`, `otp_debug`, dan mock provider |

Daftar lengkap variabel ada di `README.md` §Environment Variables.

---

## 6. Variabel Postman

Collection `docs/postman/bca-mobile-api.postman_collection.json` memakai:

| Variabel | Lokal | ngrok |
|---|---|---|
| `base_url` | `http://localhost:8080/v1` | `https://<host>/v1` |
| `internal_base_url` | `http://localhost:8080/internal/v1` | `https://<host>/internal/v1` |
| `device_id` | `device-nurholis-001` | sama |
| `pin` | `123456` | sama |

`make tunnel-env` mencetak nilai `base_url` yang benar setelah menyinkronkan
`.env`, jadi tinggal disalin.

---

## 7. Checklist pindah lingkungan

1. `SERVER_PORT` — masih 8080? Kalau tidak, semua base URL ikut berubah.
2. `SIGNALING_BASE_URL` — sudah `wss://` dengan host yang sama seperti HTTP?
   Jalankan `make tunnel-env`, jangan menyalin manual.
3. `TRUSTED_PROXIES` — di belakang tunnel atau load balancer? Kalau kosong,
   rate limit dan audit trail salah sasaran.
4. `INTERNAL_API_KEY` — sudah diset dan bukan placeholder?
5. `APP_ENV` — di luar `development`, `/v1/dev/*` hilang dan `otp_debug` tidak
   lagi muncul di response. Pastikan tester tidak bergantung padanya.
6. `curl <base>/v1/health` — `database`, `redis_session`, `redis_cache` semua
   `"ok"` sebelum menyerahkan URL ke tim Android.
7. Perbarui `base_url` dan `internal_base_url` di Postman.
