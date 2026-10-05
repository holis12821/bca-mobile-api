# Handover — Yang Ditunggu dari Backend

> Daftar hal yang menghentikan pekerjaan client Android. Setiap butir punya:
> apa yang dibutuhkan, bentuk persisnya, apa yang macet tanpa itu, dan cara
> memastikan sudah benar.
>
> Urut dari yang paling memblokir. Butir 1 dan 2 menahan fitur yang kodenya
> **sudah selesai ditulis** dan hanya menunggu bahan dari backend.

**Diperbarui 2026-09-25 — sisi backend sudah dikerjakan.** Tujuh dari delapan
butir tertutup di repo ini.

Proyek ini **portofolio**, jadi butir yang semula menunggu keputusan pihak lain
diputuskan di dalam proyek sendiri: angka biaya dan limit kartu (butir 4) dan
lineup kartu per produk sekarang hidup di `migrations/000022_card_catalog_rates.up.sql`,
dan TURN untuk pengembangan dijalankan sendiri lewat `coturn` di
`deployments/docker-compose.yml` (butir 7). **Angka kartu itu bukan tarif resmi
BCA** — koheren antar tingkat kartu, dan diganti lewat admin API kalau layanan ini
pernah dipakai sungguhan.

Yang benar-benar tidak bisa dikarang tinggal satu: **hash SPKI (butir 5)**, karena
nilainya harus cocok dengan sertifikat TLS sungguhan. Hash palsu tidak membuat
pinning "berjalan" — ia mematikan aplikasi di lapangan. Tooling-nya sudah ada
(`make spki-hash HOST=...`); yang dibutuhkan adalah domain yang benar-benar
dilayani. Butir 1 juga masih menunggu satu langkah manusia: menyalin PEM ke repo
Android.

Tiap bagian di bawah dibuka dengan blok **Status**.

---

## Ringkasan

| # | Butir | Menahan apa | Pemilik | Status |
|---|---|---|---|---|
| 1 | Kunci publik PIN (`pin_public.pem`) | Login kode akses, seluruh transaksi finansial | Backend / Security | **Selesai di backend** — endpoint publik + `key_id`; tinggal serah-terima berkas ke repo Android |
| 2 | Algoritma tanda tangan biometrik | Login Face ID dan Touch ID | Backend / Security | **Selesai** — EC P-256 / `SHA256withECDSA`, terdokumentasi, ada vektor uji |
| 3 | Pengikatan `X-Device-ID` + keputusan §0 | Keamanan sesi onboarding | Backend | **Selesai** — `403 ONBOARDING_DEVICE_MISMATCH` di semua endpoint ber-`session_id` |
| 4 | Biaya dan limit kartu Paspor | Layar pilih kartu | ~~Product Owner~~ → diputuskan di proyek | **Selesai** — migrasi `000022`, angka portofolio yang koheren antar tingkat |
| 5 | Hash SPKI certificate pinning | Rilis produksi | Infrastruktur | **Masih ditunggu** — satu-satunya yang tidak bisa dikarang; `make spki-hash HOST=...` sudah disediakan |
| 6 | Nilai `period` mutasi yang diterima | Filter Mutasi selain 7 hari | Backend | **Selesai** — enam nilai, nilai tak dikenal ditolak |
| 7 | TURN/STUN + izin dependency WebRTC | Video call e-KYC | Backend + Engineering Manager | **Selesai untuk pengembangan** — `coturn` lokal jalan dengan `make infra-up` dan terkirim sebagai `ice_servers`; token signaling 5 menit sekali pakai. TURN produksi & izin dependency tetap keputusan di luar repo |
| 8 | Konflik `/registration/*` vs `/onboarding/*` | Kejelasan kontrak jangka panjang | Backend / Arsitek | **Selesai** — `/v1/onboarding/*` berlaku, `/registration/*` ditandai usang |

---

## 1. Kunci publik PIN — `assets/pin_public.pem`

**Ini blocker paling mahal.** Kodenya sudah jadi; yang hilang hanya berkas kuncinya.

> **Status.** Sisi backend beres:
>
> - `GET /v1/auth/pin/public-key` — publik, tanpa token, `Cache-Control: max-age=300`,
>   bentuk respons sama dengan `GET /onboarding/credentials/public-key`
>   (`algorithm`, `key_id`, `public_key_pem`, plus `payload_shape`, `encoding`,
>   `max_skew_sec`).
> - `PIN_KEY_ID` (default `pin-key-v1`) menamai pasangan kunci aktif, dan
>   `encryption_key_id` dari client **dibandingkan**: yang tidak cocok dijawab
>   `422 AUTH_PIN_KEY_UNKNOWN` beserta `details.expected_key_id`. Berlaku di
>   `/auth/login/pin`, `/auth/pin/verify`, `/auth/pin/change`,
>   `/auth/access-code/change`, dan `/onboarding/credentials`.
> - Padding dipastikan OAEP-SHA256. `TestRSA_RejectsPKCS1v15Ciphertext`
>   (`internal/pkg/crypto/rsa_test.go`) menguncinya: ciphertext PKCS#1 v1.5 tidak
>   akan pernah terdekripsi.
> - `make pin-public-key` mencetak PEM yang perlu diserahkan, beserta `key_id`-nya.
>
> **Yang masih ditunggu:** tindakan manusia, bukan kode — jalankan
> `make pin-public-key`, taruh keluarannya di repo artefak internal dengan nama
> `pin-key-v1`, lalu salin ke `assets/pin_public.pem` di repo Android.

### Yang dibutuhkan

Kunci publik RSA-2048 dalam format PEM X.509 (`SubjectPublicKeyInfo`):

```
-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA...
-----END PUBLIC KEY-----
```

Pasangan kuncinya dibuat di sisi server; **privat tidak pernah meninggalkan backend**.

```bash
# Di repo ini: make keys sudah melakukannya, lalu
make pin-public-key     # cetak paruh publiknya + key_id

# Setara manual:
openssl genrsa -out pin_private.pem 2048
openssl rsa -in pin_private.pem -pubout -out pin_public.pem
```

### Kenapa memblokir

`PinEncryptor` membaca `assets/pin_public.pem`. Berkas itu belum ada, jadi
`encrypt()` mengembalikan null dan repository menolak lebih awal dengan
`CLIENT_PIN_KEY_MISSING`. Ini disengaja — PIN tidak boleh dikirim apa adanya.

Yang ikut terhenti: `POST /auth/login/pin`, `POST /auth/pin/verify`,
`POST /auth/pin/change`. Artinya **login, transfer, dan top up e-wallet tidak bisa
diuji ujung ke ujung** meski layar dan repositorinya sudah selesai.

### Pertanyaan yang perlu dijawab — terjawab

1. **Padding.** OAEP-SHA256, sesuai dugaan client
   (`RSA/ECB/OAEPWithSHA-256AndMGF1Padding`). PKCS#1 v1.5 **tidak** diterima, dan
   ada test yang menjaga itu. Plaintext tetap
   `{"pin":"...","nonce":"<uuid-v4>","ts":<unix>}`, nonce sekali pakai (diingat
   120 detik), skew maksimal 60 detik.
2. **Rotasi kunci.** Disetujui dan sudah dipasang:
   `GET /auth/pin/public-key` menyajikan `algorithm`, `key_id`, `public_key_pem`.
   Berkas di `assets/` dipakai sebagai cadangan saat endpoint tidak terjangkau.
   Client silakan mulai mengirim `encryption_key_id` — field-nya opsional, jadi
   build lama tidak terganggu, dan begitu dikirim, kunci yang kedaluwarsa
   terbaca sebagai `AUTH_PIN_KEY_UNKNOWN` alih-alih "PIN selalu salah".
3. **Apakah kunci PIN sama dengan kunci kredensial onboarding?** **Sama.** Satu
   pasangan RSA-2048, satu `key_id`. Dua endpoint yang menyajikannya
   (`/auth/pin/public-key` dan `/onboarding/credentials/public-key`) memberi
   jawaban identik, jadi satu jalur kode client cukup.

### Cara mengirim

Jangan lewat chat, email, atau tiket publik. Walaupun ini kunci **publik**,
ketertelusuran versinya penting: taruh di secret manager atau repo artefak
internal, beri nama bersama `key_id`-nya, mis. `pin-key-v1`.

### Selesai bila

- [ ] `assets/pin_public.pem` ada di repo Android
- [x] Enkripsi client dengan kunci itu berhasil didekripsi server (uji satu PIN dummy)
      — `make pin PIN=123456` dan `POST /v1/dev/encrypt-pin` menghasilkan ciphertext
      yang diterima keempat endpoint ber-PIN
- [x] Server menolak ciphertext PKCS#1 v1.5 — memastikan padding benar-benar OAEP
- [x] `key_id` tercatat (`PIN_KEY_ID`), dan cara rotasinya disepakati: terbitkan
      pasangan baru, naikkan `PIN_KEY_ID`, sajikan lewat endpoint publik

---

## 2. Algoritma tanda tangan biometrik

> **Status: selesai.** Kelima pertanyaan terjawab persis seperti rekomendasi
> client, dan sudah masuk `docs/01-API-SPECIFICATION.md` §2 beserta vektor uji.
> Server juga menerima **kedua** nama field — `signed_challenge` (nama di spec)
> dan `signature` (nama yang dipakai implementasi pertama) — jadi tidak ada
> client yang kena `VALIDATION_ERROR` karena memilih yang "salah".

### Yang dibutuhkan

`docs/01-API-SPECIFICATION.md` §2 mendefinisikan alur biometrik:

```
GET  /auth/biometric/challenge?device_id=...   → { challenge_id, challenge }
POST /auth/biometric/register                  ← { public_key, key_id, attestation }
POST /auth/login/biometric                     ← { challenge_id, signed_challenge, key_id }
```

Kelima hal yang dulu tidak disebut, sekarang disebut:

| Pertanyaan | Keputusan |
|---|---|
| Jenis kunci | **EC P-256**. Kunci lain ditolak saat register: `422 AUTH_BIOMETRIC_KEY_UNSUPPORTED` |
| Algoritma tanda tangan | **`SHA256withECDSA`** |
| Format `signed_challenge` | **Base64 dari DER** (keluaran bawaan `java.security.Signature`). Raw `r‖s` 64 byte masih diterima, tapi DER yang didokumentasikan |
| Format `public_key` | **Base64 X.509 SPKI tanpa header PEM**; PEM tetap diterima |
| Yang ditandatangani | **`challenge` apa adanya**, di-decode dari Base64 lebih dulu. Tidak ada pengikatan tambahan: tanpa `device_id`, tanpa prefiks panjang |

Respons challenge sekarang ikut membawa kontraknya (`algorithm`,
`signature_format`, `expires_in`, `expires_at`), supaya client tidak perlu
menebak dari prosa.

**Vektor uji** ada di `docs/01-API-SPECIFICATION.md` §2 —
`public_key` + `challenge` + `signed_challenge` yang valid, dijaga oleh
`TestBiometricLogin_PublishedTestVector`. ECDSA tidak deterministik, jadi tanda
tangan Anda sendiri tidak akan sama persis; yang harus sama adalah hasil
verifikasinya.

### Attestation

Field `attestation` di `/auth/biometric/register` diisi rantai sertifikat Android
Key Attestation (`KeyStore.getCertificateChain(alias)`). Keputusannya:

1. **Disimpan, tidak diverifikasi** ke akar Google.
2. Perangkat tanpa dukungan attestation (emulator, perangkat lama) **tidak
   ditolak**. Menolaknya akan mematikan login biometrik di perangkat sah
   sementara integrasi ini belum punya provider nyata untuk apa pun.
3. **Tidak ada syarat `security_level` minimal** — `TEE` maupun `StrongBox` tidak
   diwajibkan. Yang menanggung pengamanan di sini adalah pengikatan `key_id` ↔
   perangkat, challenge sekali pakai, dan pencabutan saat pendaftaran ulang.

Nilainya dibatasi 16 KB dan wajib base64 yang sah. Alasan lengkap ada di
`docs/04-SECURITY.md` §2.2, termasuk apa yang harus ditambahkan kalau verifikasi
penuh nanti diputuskan.

### Perilaku yang disepakati

- **Kunci hangus saat sidik jari baru didaftarkan.** Pendaftaran ulang
  **MENGGANTI**: semua kunci aktif nasabah itu pada perangkat itu dicabut lebih
  dulu, dan jumlahnya dilaporkan sebagai `replaced_keys` di respons `201`. Kunci
  yang tidak bisa menandatangani lagi tidak boleh tetap berlaku sebagai
  kredensial.
- **Masa berlaku challenge.** 60 detik dan benar-benar sekali pakai: server
  mengambilnya dengan `GETDEL`, jadi challenge yang sudah dipakai ditolak
  `401 AUTH_TOKEN_INVALID` — bukan hanya yang kedaluwarsa.
- **Beberapa perangkat per nasabah: boleh.** Pencabutan dibatasi satu perangkat,
  jadi mendaftar di tablet tidak mematikan biometrik di ponsel.
- `device_id` di body register diterima tapi tidak menentukan apa pun;
  pengikatan diambil dari access token. Body yang menyebut perangkat lain ditolak
  `403 AUTH_DEVICE_NOT_RECOGNIZED`.
- Respons register sekarang `{ biometric_id, key_id, registered_at, replaced_keys }`,
  bukan lagi hanya `{ message }`.

### Selesai bila

- [x] Kelima baris tabel di atas terjawab dan masuk ke `01-API-SPECIFICATION.md` §2
- [x] Kebijakan attestation ditulis (disimpan, tidak diverifikasi, tanpa level minimal)
- [x] Ada satu pasangan uji: challenge contoh + tanda tangan valid
- [x] Perilaku pendaftaran ulang setelah kunci hangus disepakati (mengganti + mencabut)

Detail sisi Android ada di skill `android-biometric-keystore`.

---

## 3. Pengikatan perangkat dan keputusan terbuka onboarding

> **Status: selesai.** Empat keputusan §0 sudah dijawab dan ditulis sebagai
> bagian **§0** baru di `docs/06-BUKA-REKENING-API-SPEC.md`.

Client **sudah** mengirim `X-Device-ID` dan `X-Request-ID` di semua request, jadi
bagian client selesai. Sisi server sekarang:

1. **`X-Device-ID` diverifikasi** terhadap `device_id` yang tersimpan saat sesi
   dibuat, dan berbeda dijawab **`403 ONBOARDING_DEVICE_MISMATCH`** — bukan lagi
   `404`. Berlaku di **semua** endpoint yang menerima `session_id`:
   `GET`/`DELETE /sessions/{id}`, `PUT /sessions/{id}/card`, `POST /ocr`,
   `GET /ocr/{session_id}`, `POST /personal-data`, `POST /verify-otp`,
   `POST /resend-otp`, `POST /biometric`, `POST /video-call/queue`,
   `POST /credentials`, `POST /submit`. Sebelumnya hanya dua endpoint OTP yang
   memeriksanya, jadi `session_id` yang bocor masih bisa dijalankan sampai
   submit dari ponsel lain.
   Header yang **tidak** dikirim tetap lolos, supaya build yang sudah ada di
   tangan tester tidak terputus di tengah flow.
2. **Pasang ulang aplikasi memutus draf, dan itu memang yang diinginkan.**
   `ANDROID_ID` berganti, sesi lama tidak bisa dilanjutkan, dan sesinya
   kedaluwarsa sendiri setelah 24 jam. Pengikatan yang selamat dari pemasangan
   ulang harus mempercayai nilai pilihan client, dan nilai seperti itu tidak
   mengikat apa pun.
3. **`X-Device-ID` wajib untuk `GET /products/{type}/cards`** — sudah sejak
   awal: header itu satu-satunya identitas sebelum sesi ada, dan batas laju
   katalog bergantung padanya. Tanpa header: `400 VALIDATION_ERROR` dengan
   `details.missing_header`.
4. **`X-Request-ID` tidak dipantulkan.** Server membuat id sendiri; itu yang
   muncul di `meta.request_id` dan di header respons `X-Request-ID`. Id pilihan
   client bisa bertabrakan atau disamakan sengaja, dan korelasi log lintas
   nasabah jadi tidak bisa dipercaya.

### Selesai bila

- [x] Server menolak request dengan device id berbeda dari pemilik sesi
- [x] Empat keputusan di §0 terjawab dan §0 diperbarui

---

## 4. Biaya dan limit kartu Paspor

> **Status: selesai — diputuskan di dalam proyek.** Karena ini portofolio, angka
> tidak ditunggu dari product owner; nilainya diputuskan di
> `migrations/000022_card_catalog_rates.up.sql` beserta alasan tiap pilihan.
>
> **Ini bukan tarif resmi BCA**, dan bukan salinan `strings.xml` client.

### Angka yang dipakai

| kartu | admin/bln | penerbitan | penggantian | tarik tunai | transfer BCA | antar bank | debit/hari |
|---|---|---|---|---|---|---|---|
| Blue | 15.000 | 0 | 25.000 | 7.000.000 | 50.000.000 | 15.000.000 | 50.000.000 |
| Gold | 17.000 | 0 | 25.000 | 10.000.000 | 75.000.000 | 20.000.000 | 75.000.000 |
| Platinum | 20.000 | 0 | 50.000 | 12.500.000 | 100.000.000 | 25.000.000 | 100.000.000 |

Alasan pilihannya:

- **Naik bertingkat tanpa kecuali.** Setiap limit dan biaya bulanan naik dari Blue
  ke Gold ke Platinum. Katalog yang tingkatnya tidak berurutan membuat layar pilih
  kartu kehilangan alasan keberadaannya.
- **Penerbitan nol untuk ketiganya.** Kartu pertama saat buka rekening tidak
  ditagih; biaya penggantian yang menanggung kartu kedua. Kalau penerbitan ikut
  ditagih, nasabah membayar dua kali untuk satu kartu.
- **Penggantian Platinum dua kali Blue/Gold** — satu-satunya biaya yang tidak naik
  bertingkat di ketiganya, karena yang membedakan hanya biaya cetak kartunya.
- **Bukan `14000`/`16000`/`19000`.** Itu angka `strings.xml` client — data desain
  layar. Kalau angka itu yang dipakai, tidak ada yang curiga saat ia muncul di
  database, dan `monthly_admin_fee_shown` di audit log akan menjadi bukti tertulis
  bahwa nasabah diberi tahu tarif yang salah.
- **Bukan `11111`/`22222`/`33333`.** Angka palsu seeder sebelumnya sudah hilang
  dari repo: katalog sekarang ikut ke setiap environment, jadi ia harus bisa
  dipakai.
- **`min_age` tetap 17 untuk ketiganya.** Menaikkannya untuk Platinum akan
  menuliskan aturan yang tidak ada yang menegakkan — tidak ada kode yang menolak
  pemohon per kartu berdasarkan umur. `min_initial_deposit` dibedakan
  (500 ribu / 1 juta / 10 juta) dan sifatnya informatif: client menampilkannya.

### Lineup per produk

`TAHAPAN_BCA` ketiga kartu · `TAHAPAN_XPRESI` Blue + Gold · `TABUNGANKU` Blue saja.
Default selalu Blue. Menawarkan kartu bersyarat setoran awal Rp10 juta pada produk
setoran awal Rp20 ribu berarti memajang pilihan yang pasti tidak bisa diambil, dan
memasang kartu termahal sebagai default akan menagih nasabah untuk pilihan yang
tidak pernah ia buat.

### Di mana angkanya hidup

Di migrasi, **bukan** di seeder. Sebelumnya `scripts/seed/main.go` satu-satunya
yang mengisi `card_products`, dan seeder itu menolak jalan di luar
`APP_ENV=development` — jadi staging tidak akan pernah punya katalog. Sekarang
seeder hanya memeriksa katalognya ada (`verifyCardCatalog`), dan
`FEATURE_CARD_SELECTION=true` di `.env.example` karena layarnya sudah punya isi.

Penggantian dengan tarif resmi — kalau pernah dibutuhkan — lewat admin API
katalog (`PUT /internal/v1/cards/{card_type}`), yang menaikkan `catalog_version`
dan menulis nilai lama + baru ke `card_catalog_audit_log`. **Bukan** dengan
menambah migrasi baru: tarif adalah operasi, bukan skema.

### Selesai bila

- [x] Angka diputuskan dan tertulis (`migrations/000022`, `docs/08` §17)
- [x] Tabel `card_products` terisi angka itu di setiap environment yang dimigrasi,
      bukan hanya development
- [x] `migrate down 1` lalu `up` diuji: `down` tidak melanggar foreign key
      `account_cards`/`onboarding_sessions`, dan `up` memulihkan katalog utuh

---

## 5. Hash SPKI untuk certificate pinning

> **Status: masih ditunggu — dan ini satu-satunya butir yang TIDAK saya isi
> sendiri.** Hash SPKI harus cocok byte-per-byte dengan kunci publik sertifikat
> yang benar-benar dilayani. Angka karangan tidak membuat pinning "berjalan": ia
> membuat setiap koneksi ditolak, dan aplikasi di lapangan mati sampai ada rilis
> baru. Tidak ada nilai portofolio yang bisa menggantikannya.
>
> `make spki-hash HOST=api.bcamobile.id` sudah disediakan supaya keluarannya
> konsisten, termasuk tanggal kedaluwarsa sertifikatnya. Pinning-nya sendiri
> dikonfigurasi di sisi Android (`NetworkModule.certificatePinner()`), bukan di
> repo ini.

`NetworkModule.certificatePinner()` sudah siap tetapi daftar pinnya kosong,
sehingga pinning tidak aktif walau `CERTIFICATE_PINNING_ENABLED = true` di release.

### Yang dibutuhkan

Hash SPKI base64 untuk `api.bcamobile.id` — **sertifikat aktif dan minimal satu
cadangan**. Tanpa cadangan, perpanjangan sertifikat akan mematikan aplikasi di
lapangan sampai ada rilis baru.

```bash
make spki-hash HOST=api.bcamobile.id

# Setara manual:
openssl s_client -servername api.bcamobile.id -connect api.bcamobile.id:443 \
  | openssl x509 -pubkey -noout \
  | openssl pkey -pubin -outform der \
  | openssl dgst -sha256 -binary \
  | openssl enc -base64
```

### Selesai bila

- [ ] Minimal dua hash (aktif + cadangan) diterima beserta tanggal kedaluwarsanya
- [ ] Prosedur rotasi disepakati: siapa memberi tahu, berapa lama sebelum ganti

---

## 6. Nilai `period` mutasi yang diterima

> **Status: selesai.** Daftarnya tertutup dan ditulis di
> `docs/01-API-SPECIFICATION.md` §4. `LAST_MONTH` **didukung** — client boleh
> terus mengirim keempat nilai apa adanya.

| `period` | Rentang (tanggal WIB, kedua ujung inklusif) |
|---|---|
| `LAST_7_DAYS` | 6 hari lalu … hari ini |
| `LAST_30_DAYS` | 29 hari lalu … hari ini |
| `LAST_90_DAYS` | 89 hari lalu … hari ini |
| `THIS_MONTH` | tanggal 1 bulan ini … hari ini |
| `LAST_MONTH` | tanggal 1 bulan lalu … hari terakhir bulan lalu |
| `CUSTOM` | `from` … `to` (wajib keduanya, `YYYY-MM-DD`) |

Dua hal yang ikut diperbaiki, keduanya pernah menyembunyikan bug client:

- **Nilai tak dikenal sekarang ditolak** `400 VALIDATION_ERROR` dengan
  `details.allowed_values`. Dulu nilai salah tulis lolos sebagai "tanpa filter
  tanggal", jadi layar Mutasi menampilkan seluruh riwayat rekening seolah-olah
  itu 7 hari terakhir.
- **`CUSTOM` menerima `from`/`to` maupun `start_date`/`end_date`.** Spec menulis
  yang kedua, handler dibangun untuk yang pertama; client yang memilih pasangan
  yang didokumentasikan tanggalnya diabaikan diam-diam.

Batas hari dihitung di `Asia/Jakarta`, bukan dengan `CURRENT_DATE`.

### Selesai bila

- [x] Daftar lengkap nilai `period` yang diterima ditulis di spec
- [x] `LAST_MONTH` didukung — tidak perlu pengganti

---

## 7. Video call e-KYC

> **Status: selesai untuk pengembangan.** Yang tersisa hanya keputusan di luar
> repo ini.
>
> - **Sisi CS sudah punya pintu masuk.** Sebelumnya tidak ada: `agent-token`
>   mensyaratkan `queue_id` dan satu-satunya endpoint lain yang menyentuh antrean
>   adalah `GET /monitoring`, yang hanya mengembalikan `queue_length` — sebuah angka,
>   tanpa satu pun pengenal panggilan. Antrean bisa penuh dan tetap tidak ada yang
>   bisa dilayani. Sekarang `GET /video-call/queued` memberi daftar + `queue_id`.
> - **Identitas petugas sekarang diautentikasi**, bukan diklaim di body:
>   `X-Agent-Employee-ID` + `X-Agent-API-Key` diverifikasi ke tabel `cs_agents`
>   (migrasi `000026`). Dulu siapa pun yang memegang `INTERNAL_API_KEY` bisa mengaku
>   sebagai pegawai mana pun, dan string itulah yang masuk audit trail sebagai
>   `actor` serta tampil ke layar nasabah. Satu panggilan kini terikat ke satu
>   petugas, dan hasilnya hanya diterima dari petugas itu.
>
> - **Ketentuan token signaling dikonfirmasi dan dipasang**: 5 menit (dulu 1 jam)
>   dan **sekali pakai** — `jti` ditandai terpakai pada sambungan WebSocket
>   pertama, jadi URL yang tersalin tidak bisa dipakai ulang. Sambungan yang
>   terputus join antrean lagi untuk mendapat token baru.
>   `signaling_expires_in` ikut di respons join antrean.
> - **`ice_servers`** dikirim di respons `POST /video-call/queue` dan
>   `POST /video-call/agent-token`, dalam bentuk `RTCIceServer` yang bisa
>   diteruskan apa adanya ke `PeerConnection`.
> - **TURN-nya sudah ada, bukan lagi menunggu.** `deployments/docker-compose.yml`
>   menjalankan `coturn` (naik bersama `make infra-up`), dan `.env.example`
>   menunjuk ke sana. Jadi video call bisa diuji ujung ke ujung secara lokal tanpa
>   menunggu siapa pun.

### TURN untuk pengembangan

```bash
make infra-up    # Postgres + 2x Redis + coturn

# periksa TURN-nya hidup
docker exec bca-coturn turnutils_stunclient 127.0.0.1   # harus mencetak "reflexive addr"
docker inspect --format '{{.State.Health.Status}}' bca-coturn
```

```bash
STUN_URLS=stun:localhost:3478
TURN_URLS=turn:localhost:3478?transport=udp
TURN_USERNAME=bcadev
TURN_CREDENTIAL=localdev_turn_123
```

Dua hal yang sering salah:

- **`localhost` hanya benar untuk klien di mesin yang sama.** Dari HP atau
  emulator, ganti ke alamat LAN mesin pengembang — kalau tidak, HP mencari TURN di
  dirinya sendiri dan panggilan gagal dengan gejala yang mirip "TURN tidak ada".
- **Kredensial di atas khusus lokal dan ikut di repo.** Deployment sungguhan
  memakai TURN milik infrastruktur dengan kredensial berumur pendek (REST API
  credential), bukan satu username statis.

Kalau `TURN_URLS` diisi tanpa username/credential, daftar TURN-nya **diabaikan**
dan dicatat sebagai error saat startup: server TURN tanpa kredensial menolak semua
alokasi, jadi menerbitkannya hanya memberi aplikasi server yang pasti gagal.
`ice_servers` kosong tetap jawaban yang sah — itu cara client membedakan "belum
dikonfigurasi" dari "dikonfigurasi tapi rusak".

### Yang masih di luar repo ini

- **Kredensial TURN produksi.** Bukan pekerjaan kode: begitu kredensialnya ada,
  yang berubah hanya empat variabel environment.
- **Izin menambah dependency WebRTC** di sisi Android. Keputusan Engineering
  Manager, dan tidak ada yang bisa dikerjakan backend untuk mempercepatnya.
- **Aplikasi petugas CS belum ada** — tidak ada panel web maupun aplikasi agent di
  repo mana pun. Backend sudah memaparkan seluruh yang dibutuhkannya (daftar
  antrean, ambil panggilan, menyambung sebagai peer yang menjawab, kirim instruksi,
  submit hasil), tapi tanpa klien itu alurnya berhenti setelah nasabah mengantre:
  socket-nya terbuka, `queue_update` sampai, lalu tidak ada yang mengambil
  panggilannya. Kalau ada laporan "video call tidak pernah tersambung" sementara
  `make check` hijau, periksa dulu apakah ada klien agent yang berjalan.
- **Baris `cs_agents` untuk non-development.** Isinya kredensial, jadi tidak ikut di
  migrasi: `make seed` hanya menanam petugas dev (`CS-1042` / `dev-agent-key`) dan
  digerbangi `APP_ENV=development`. Di staging dan produksi barisnya dibuat yang
  mengoperasikan integrasi CS dengan kunci acak, lewat jalur yang sama dengan
  pendistribusian `INTERNAL_API_KEY`.

Protokol signaling lengkap di `06-BUKA-REKENING-API-SPEC.md` §5b.

### Selesai bila

- [x] Ada TURN yang bisa dipakai untuk pengujian (coturn lokal, `make infra-up`)
- [x] Ketentuan masa berlaku token signaling dikonfirmasi (5 menit, sekali pakai)
- [x] Jalur `ice_servers` sampai ke client dan bisa diperiksa dari respons antrean
- [x] Sisi CS punya cara menemukan `queue_id` (`GET /video-call/queued`)
- [x] Identitas petugas diautentikasi dan terikat ke panggilan (`cs_agents`, `X-Agent-*`)
- [ ] Kredensial TURN produksi (empat variabel environment, tanpa perubahan kode)
- [ ] Dependency WebRTC disetujui
- [ ] Aplikasi/panel petugas CS (di luar repo ini)
- [ ] Baris `cs_agents` untuk staging & produksi (kunci acak, bukan dari seeder)

---

## 8. Dua keluarga endpoint untuk buka rekening

> **Status: selesai.** `/v1/onboarding/*` dinyatakan berlaku; `/v1/registration/*`
> ditandai usang di dokumennya **dan** di responsnya.

`01-API-SPECIFICATION.md` §9 mendefinisikan `/v1/registration/*` dengan
**Registration Token**, sementara `06-BUKA-REKENING-API-SPEC.md` mendefinisikan
`/v1/onboarding/*` dengan `session_id`. Keduanya menggambarkan fitur yang sama.

Client Android mengimplementasikan yang kedua, dan itu yang dipelihara.
`/v1/registration/*` **masih berjalan** untuk build lama — tidak dihapus
diam-diam — tetapi setiap responsnya sekarang membawa:

```
Deprecation: true
Link: </v1/onboarding/*>; rel="successor-version"
X-API-Deprecation-Info: docs/01-API-SPECIFICATION.md#9-registrasi-buka-rekening
```

Header `Sunset` **tidak** dikirim: belum ada tanggal penghapusan yang disepakati,
dan mengarangnya di sini adalah janji yang tidak bisa ditepati repo ini.

### Selesai bila

- [x] Satu keluarga dinyatakan berlaku (`/v1/onboarding/*`)
- [x] Yang tidak dipakai ditandai usang di dokumennya, bukan dihapus diam-diam

---

## Cara memakai dokumen ini

Tujuh dari delapan butir sudah dibuka, jadi pekerjaan client yang menunggu
backend bisa jalan.

Yang tersisa, dan kenapa:

| Butir | Siapa | Apa | Kenapa belum |
|---|---|---|---|
| 1 | Backend / Security | `make pin-public-key` → repo artefak internal → `assets/pin_public.pem` di repo Android | Satu langkah manusia; kodenya sudah selesai di kedua sisi |
| 5 | Infrastruktur | `make spki-hash` untuk sertifikat aktif **dan** cadangan, beserta tanggal kedaluwarsa | Hash harus cocok dengan sertifikat sungguhan. Nilai karangan tidak membuat pinning berjalan — ia menolak setiap koneksi dan mematikan aplikasi di lapangan |
| 7 | Infrastruktur / EM | Kredensial TURN produksi; izin dependency WebRTC | Pengujian lokal tidak lagi menunggu ini (coturn). Yang berubah nanti hanya empat variabel environment |

Butir 4 tidak lagi ada di daftar ini: angkanya diputuskan di dalam proyek dan
hidup di `migrations/000022_card_catalog_rates.up.sql`. Kalau angka resmi suatu
saat datang, masukkan lewat admin API katalog — **jangan** menambah migrasi baru,
karena tarif adalah operasi, bukan skema.

Setiap butir yang selesai: perbarui tabel Ringkasan di atas, lalu perbarui juga
spec yang terkait (`01-API-SPECIFICATION.md`, `06-BUKA-REKENING-API-SPEC.md`,
`08-PILIH-KARTU-API-SPEC.md`) dan koleksi Postman supaya sesi berikutnya tidak
mengulang pertanyaan yang sudah terjawab.
