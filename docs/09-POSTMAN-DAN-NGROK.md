# 09 — Testing di Postman & Membuka API ke Internet

> Dari server lokal sampai URL HTTPS publik yang bisa dipanggil frontend. Scope: dokumentasi dan integrasi pribadi, bukan deployment production.

---

## 1. Jalankan server lokal

```bash
make infra-up        # Postgres + 2 Redis
make migrate-up
make seed            # data demo — idempoten, aman diulang
make run             # atau `make dev` untuk hot reload
```

Verifikasi:

```bash
curl -s localhost:8080/v1/health | jq
```

`database`, `redis_session`, dan `redis_cache` harus `"ok"`. Kalau salah satu `error`, container-nya belum siap — tunggu beberapa detik, jangan lanjut.

### Data demo

| Device ID | PIN | Nama | Rekening | Saldo |
|---|---|---|---|---|
| `device-nurholis-001` | `123456` | NURHOLIS MAJID | 1234567890, 1234567891 | Rp 50 jt, Rp 10 jt |
| `device-budi-001` | `654321` | BUDI SANTOSO | 9876543210 | Rp 25 jt |
| `device-siti-001` | `111111` | SITI RAHAYU | 5555666677 | Rp 100 jt |

Login memakai `device_id`, bukan username: satu device aktif terikat ke tepat satu user.

---

## 2. Kenapa PIN tidak bisa dikirim apa adanya

API menolak PIN plaintext. Yang diterima field `pin_encrypted` adalah:

```text
base64( RSA-OAEP-SHA256( {"pin":"123456","nonce":"<uuid>","ts":<unix>} ) )
```

Dan payload ditolak kalau `ts` lebih dari 60 detik lalu, atau `nonce`-nya sudah pernah dipakai. Tanpa dua hal itu, satu `pin_encrypted` yang tercuri bisa dipakai ulang selamanya.

Postman tidak punya RSA-OAEP di pre-request script-nya, jadi ada dua jalan:

### a. Endpoint dev (dipakai otomatis oleh collection)

```text
POST /v1/dev/encrypt-pin   {"pin": "123456"}
GET  /v1/dev/pin-public-key
```

Hanya di-mount saat `APP_ENV=development`. Di environment lain rute-nya tidak ada sama sekali dan menjawab 404.

Endpoint ini tidak memberi penyerang apa pun: isinya hanya operasi dengan kunci **publik**, yang memang sudah dipublikasikan API di `/v1/onboarding/credentials/public-key`. Tetap digerbang karena endpoint yang ada semata-mata untuk memudahkan testing tidak punya tempat di production.

### b. CLI, untuk curl dan shell

```bash
make pin PIN=123456
# atau
go run ./scripts/pinenc -pin 123456 -json
```

Hanya membaca kunci publik. Hasilnya sekali pakai dan mati dalam 60 detik — kalau login menjawab `AUTH_INVALID_PIN` padahal PIN-nya benar, ciphertext-nya sudah basi, bukan PIN-nya yang salah.

### c. Frontend melakukannya sendiri

Ini yang seharusnya dilakukan client sungguhan. Ambil PEM dari `/v1/dev/pin-public-key` (atau tanam di aplikasi), lalu di browser:

```js
const payload = JSON.stringify({ pin, nonce: crypto.randomUUID(), ts: Math.floor(Date.now() / 1000) });
const cipher = await crypto.subtle.encrypt(
  { name: 'RSA-OAEP' },                       // hash SHA-256 ditentukan saat importKey
  publicKey,                                   // importKey('spki', der, {name:'RSA-OAEP', hash:'SHA-256'}, ...)
  new TextEncoder().encode(payload)
);
const pinEncrypted = btoa(String.fromCharCode(...new Uint8Array(cipher)));
```

Di Android: `Cipher.getInstance("RSA/ECB/OAEPwithSHA-256andMGF1Padding")`.

---

## 3. Postman

Import dua file dari `docs/postman/`:

- `bca-mobile-api.postman_collection.json` — 53 request, 12 folder
- `bca-mobile-api.postman_environment.json` — environment `BCA Mobile — Local`

Pilih environment-nya di pojok kanan atas, lalu jalankan **Auth → Login (PIN)**.

Yang terjadi otomatis:

- pre-request script memanggil `/dev/encrypt-pin` dan mengisi `{{pin_encrypted}}`
- test script menyimpan `access_token` dan `refresh_token` ke environment
- collection-level Bearer auth memakai `{{access_token}}` untuk semua request lain

### Alur transfer

Urutannya mengikat, dan tiap langkah menyimpan variabel untuk langkah berikutnya:

```text
Account → Balance            → source_account_id
Transfer → 1. Inquiry        → inquiry_id       (umur 5 menit, sekali pakai)
Auth → PIN Verify            → verification_token (umur 120 detik, sekali pakai)
Transfer → 2. Execute        → transaction_id
```

Execute mengambil tujuan, bank, dan biaya dari inquiry yang tersimpan di server. Field di body hanya dipakai sebagai cross-check — kalau beda, jawabannya `422 INQUIRY_MISMATCH`, bukan diam-diam menuruti body.

Untuk e-wallet dan QRIS, ganti `purpose` di PIN Verify jadi `EWALLET_TOPUP` atau `QRIS_PAYMENT`. Token bertujuan `TRANSFER` akan ditolak di endpoint lain — itu memang disengaja.

### Menguji idempotency

Di **Transfer → 2. Execute**, header `X-Idempotency-Key` berisi `{{$guid}}` sehingga selalu baru. Ganti dengan string tetap, lalu kirim dua kali:

- respons kedua identik dengan yang pertama
- ada header `X-Idempotent-Replayed: true`
- di database tetap hanya ada satu transaksi

### WebSocket signaling

Tidak bisa disimpan di file collection. Buat manual: **New → WebSocket Request**

```text
ws://localhost:8080/v1/onboarding/video-call/signal?token=<signaling_token>&role=nasabah
```

`signaling_url` lengkap beserta token-nya ada di respons **Onboarding → 8. Join Video Call Queue**.

### Menjalankan seluruh collection sekaligus

```bash
npx newman run docs/postman/bca-mobile-api.postman_collection.json \
  -e docs/postman/bca-mobile-api.postman_environment.json \
  --folder "Health" --folder "Dev Helpers"
```

Jangan jalankan seluruh collection tanpa memilih folder: di dalamnya ada **Change PIN** (mengubah PIN user seed, membuat login berikutnya gagal) dan **Logout** (membatalkan token yang baru saja dipakai).

---

## 4. Membuka ke internet dengan ngrok

Untuk dokumentasi pribadi dan integrasi frontend, ngrok sudah cukup — tidak perlu VPS, domain, atau sertifikat.

```bash
brew install ngrok
ngrok config add-authtoken <token dari dashboard ngrok>

make tunnel                              # URL acak, ganti tiap restart
make tunnel DOMAIN=nama-anda.ngrok-free.app   # domain statis gratis dari dashboard
```

Hasilnya:

```text
Forwarding  https://nama-anda.ngrok-free.app -> http://localhost:8080
```

Base URL untuk frontend jadi `https://nama-anda.ngrok-free.app/v1`.

Daftar lengkap alamat per lingkungan — HTTP, `/internal/v1`, WebSocket
signaling, health, dan konstanta untuk aplikasi Android — ada di
[10-BASE-URL-DAN-ENDPOINT.md](10-BASE-URL-DAN-ENDPOINT.md).

### Empat hal yang harus disetel, kalau tidak frontend akan gagal

**1. `SIGNALING_BASE_URL`** — nilai ini ikut masuk ke `signaling_url` yang diterima client. Kalau masih `ws://localhost:8080`, frontend di device lain akan mencoba menyambung ke dirinya sendiri.

```bash
# .env
SIGNALING_BASE_URL=wss://nama-anda.ngrok-free.app
```

Perhatikan `wss://`, bukan `ws://` — halaman HTTPS menolak WebSocket tidak terenkripsi. Restart server setelah mengubahnya.

**2. `CORS_ALLOWED_ORIGINS`** — hanya kalau frontend-nya berjalan di browser. Default-nya kosong, artinya tidak ada origin yang diizinkan: itu default yang benar untuk API mobile, dan tidak berpengaruh ke Postman atau curl (keduanya tidak mengirim header `Origin`). Tapi frontend web akan diblokir browser sebelum request-nya sampai ke server.

```bash
# .env
CORS_ALLOWED_ORIGINS=http://localhost:3000,http://localhost:5173
```

Isi origin frontend-nya, bukan URL ngrok-nya.

**3. Halaman peringatan ngrok** — di paket gratis, request dengan User-Agent browser menerima halaman HTML interstitial, bukan JSON. `fetch()` dari aplikasi web kena ini, dan gejalanya membingungkan: response 200 tapi `JSON.parse` gagal. Kirim header berikut di semua request frontend:

```js
headers: { 'ngrok-skip-browser-warning': 'true' }
```

Nilainya bebas; yang dibaca hanya keberadaan header-nya.

**4. `APP_ENV`** — biarkan `development` supaya `/v1/dev/encrypt-pin` tetap ada. Konsekuensinya sadar: selama tunnel hidup, siapa pun yang tahu URL-nya bisa memanggil API ini, termasuk endpoint dev. Karena itu:

- matikan tunnel saat tidak dipakai (`Ctrl+C`), jangan biarkan menyala semalaman
- jangan taruh data pribadi asli di database ini
- kalau URL-nya sempat dibagikan luas, ganti `INTERNAL_API_KEY` dan pakai domain statis yang baru

Rate limit tetap berlaku (login 5 / 15 menit per device, 20 / 15 menit per IP), jadi tunnel yang terbuka tidak berarti PIN bisa di-brute force.

### Checklist sebelum memberi URL ke frontend

```bash
curl -s https://nama-anda.ngrok-free.app/v1/health | jq '.data'
curl -s -X POST https://nama-anda.ngrok-free.app/v1/dev/encrypt-pin \
  -H 'Content-Type: application/json' -d '{"pin":"123456"}' | jq '.data.expires_in'
```

Di Postman, ganti `base_url` di environment jadi `https://nama-anda.ngrok-free.app/v1` — seluruh collection langsung menunjuk ke tunnel tanpa perubahan lain.

---

## 5. Kalau nanti pindah ke VPS

Stack production ada di `deployments/docker-compose.prod.yml` (nginx + api + Postgres + 2 Redis, hanya nginx yang membuka port). Yang sudah siap:

- `Dockerfile` — multi-stage, distroless, non-root
- `.env.prod.example` — semua secret yang harus diisi, lengkap dengan cara membuatnya
- nama environment variable di compose sudah dicocokkan dengan `internal/config/config.go`

Yang **belum** ada dan harus dibuat sebelum `make prod-up` bisa jalan:

1. `deployments/nginx/conf.d/*.conf` — compose me-mount direktori ini; kalau tidak ada, container nginx gagal start. Config-nya wajib meneruskan header `Upgrade` dan `Connection` untuk WebSocket signaling, dengan `proxy_read_timeout` panjang.
2. Service `certbot` untuk menerbitkan sertifikat TLS. Volume `certbot_webroot` sudah dideklarasikan, tapi tidak ada container yang mengisinya.
3. `deployments/uploads` dengan owner uid 65532 (image-nya non-root):

   ```bash
   mkdir -p deployments/uploads && sudo chown 65532:65532 deployments/uploads
   ```

4. `APP_ENV=production` mematikan `/v1/dev/*`. Frontend harus sudah melakukan enkripsi PIN sendiri (bagian 2c) sebelum titik ini.

Dan sebelum dianggap production sungguhan, yang di luar scope repo ini: private key di HSM bukan di filesystem, penetration test, serta review PCI-DSS / OJK / BI.
