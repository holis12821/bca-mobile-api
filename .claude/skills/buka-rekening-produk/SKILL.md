---
name: buka-rekening-produk
description: >-
  Katalog jenis rekening tabungan pada layar PERTAMA flow buka rekening —
  endpoint GET /v1/onboarding/products dan administrasinya GET/PUT
  /internal/v1/onboarding/products (cakupan CARD_ADMIN), tabel onboarding_products +
  onboarding_product_features + onboarding_product_page, setoran awal minimum,
  fitur per produk, badge "Paling Populer", copy halaman (heading, subtitle,
  kotak Persiapan Dokumen, kalimat S&K), cache Redis berbasis catalog_version,
  dan pencatatan product_catalog_version pada sesi. Gunakan saat membuat atau
  memodifikasi daftar produk tabungan yang ditawarkan, setoran awal minimum,
  teks fitur produk, status produk tutup/maintenance, atau copy layar Pilih
  Jenis Rekening. Trigger juga pada "katalog produk", "jenis rekening",
  "product_type", "Tahapan BCA", "Tahapan Xpresi", "TabunganKu",
  "setoran awal minimum", "min_initial_deposit", "Paling Populer",
  "ONBOARDING_CATALOG_UNAVAILABLE", "Persiapan Dokumen", "admin katalog produk",
  "PUT /internal/v1/onboarding/products", "ONBOARDING_PRODUCT_INVALID_VALUE",
  "ONBOARDING_PRODUCT_CATALOG_CONFLICT", "memindahkan badge Paling Populer", dan
  "features null vs kosong". JANGAN gunakan
  untuk katalog KARTU Paspor dan step CARD_SELECTION (itu skill
  `buka-rekening-kartu`), untuk isi Syarat & Ketentuan (itu `GET /onboarding/tnc`
  di skill `buka-rekening-backend`), untuk OCR/biometrik/video call/submit (itu
  `buka-rekening-backend`), atau untuk UI Compose Android (repo terpisah).
---

# Pilih Jenis Rekening — Katalog Produk Buka Rekening

Layar Android: `ui/screen/buka_rekening/pilih_jenis/BukaRekeningPilihJenisScreen.kt`
di repo `BcaMobile`. Layar **pertama** `GraphAuth` flow buka rekening — tampil
sebelum S&K, sebelum kartu, sebelum sesi lahir.

Envelope, bentuk error, dan `meta.request_id` mengikuti `docs/01-API-SPECIFICATION.md`.
Dokumen itu yang mengikat; skill ini mengatur **cara** mengerjakannya.

## Batas wilayah

**Trigger**: endpoint `/v1/onboarding/products` (tanpa `/cards` di belakangnya),
tabel `onboarding_products` / `onboarding_product_features` /
`onboarding_product_page`, nilai enum `onboarding_product_type`, setoran awal
minimum, daftar fitur produk tabungan, badge "Paling Populer", status produk
tutup, dan copy layar Pilih Jenis Rekening.

**Jangan trigger** untuk:

| Kalau pekerjaannya tentang | Skill-nya |
|---|---|
| Katalog kartu Paspor, `card_type`, step `CARD_SELECTION`, biaya/limit kartu | `buka-rekening-kartu` |
| Isi S&K, `accepted_tnc_version`, `TNC_VERSION_OUTDATED` | `buka-rekening-backend` |
| OCR, Dukcapil, data pribadi, biometrik, kredensial, submit | `buka-rekening-backend` |
| Antrean & signaling video call | `buka-rekening-video-call-backend` |
| OTP onboarding | `buka-rekening-otp` |
| Auth/PIN/saldo/transaksi nasabah | `bca-mobile-backend` |

Gampang tertukar dengan `buka-rekening-kartu` karena **dua-duanya memakai kata
"produk"**. Pembedanya: skill ini tentang *produk tabungan yang dipilih nasabah*
(`TAHAPAN_BCA`), `buka-rekening-kartu` tentang *kartu debit untuk produk itu*
(`PASPOR_BLUE`). Rute `/products/{product_type}/cards` milik skill kartu,
`/products` milik skill ini.

**Prompt implementasi bertahap**: `references/prompts.md` — enam fase ber-*Definition of Done*.
**Checklist verifikasi lintas repo**: `references/verification.md` — dikerjakan sebelum menutup pekerjaan.

---

## Keadaan sekarang, dan apa yang rusak karenanya

Tidak ada endpoint katalog produk. Isi layar hidup sebagai 16 entri `strings.xml`
di dalam APK (`buka_rekening_tahapan_bca*`, `..._xpresi*`, `..._tabunganku*`),
dirakit oleh fungsi Kotlin `defaultJenisRekeningList()`. Sisi bank hanya punya
enum `onboarding_product_type` (migrasi `000010`) — tiga nama tanpa satu pun
atribut.

Tiga akibat nyata:

1. **Setoran awal minimum yang ditampilkan tidak ada arsipnya.** Nasabah melihat
   "Rp 500.000" dari string di APK. Begitu angkanya diperbarui, tidak ada catatan
   apa pun tentang angka yang dilihat nasabah yang mendaftar sebelum perubahan —
   padahal setoran awal adalah komitmen 30 hari kalender yang disebut pasal 4 S&K
   (`onboarding_tnc_sections`). Masalah yang sama persis dengan yang ditutup
   migrasi `000025` untuk teks S&K.
2. **Produk tidak bisa ditutup tanpa rilis aplikasi.**
   `ONBOARDING_PRODUCTS_MAINTENANCE` sudah ada di `internal/config/config.go` dan
   sudah dipakai katalog kartu, tapi layar pilih jenis tidak pernah bertanya ke
   server, jadi produk yang tutup tetap dipajang dan baru ditolak di
   `POST /sessions` dengan `422 ONBOARDING_PRODUCT_UNAVAILABLE` — setelah nasabah
   menyetujui S&K dan memilih kartu.
3. **Client memilih produk berdasarkan POSISI, bukan kode.**
   `PilihJenisEvent.ProductSelected(index)` → `ProductType.fromIndex(index)`
   (`domain/onboarding/model/OnboardingModels.kt`), dan layar Ringkasan memakai
   konstanta `POPULAR_PRODUCT_INDEX`. Urutan di `strings.xml` kebetulan sama
   dengan urutan deklarasi enum Kotlin. Begitu server mengurutkan, menyembunyikan,
   atau menambah satu produk, indeks itu menunjuk produk yang **salah** dan
   nasabah membuka rekening yang bukan pilihannya — tanpa error di mana pun.

Butir 3 adalah alasan paling penting response harus membawa `product_type` dan
`is_popular` per item. Lihat `references/verification.md`.

---

## Informasi yang wajib dikumpulkan lebih dulu

Berbeda dari `buka-rekening-kartu`, **tidak ada** dokumen spec yang sudah
menjawab butir-butir ini. Jangan mengisi tebakan; tanyakan.

| # | Yang harus diketahui | Kenapa penting | Sumber |
|---|---|---|---|
| 1 | Angka resmi setoran awal minimum per produk | Nilai di `strings.xml` (500.000 / 50.000 / 20.000) adalah **data desain**, bukan tarif produk. Angka ini ikut tercatat sebagai bukti apa yang dilihat nasabah | Product owner / tarif resmi |
| 2 | Daftar fitur resmi per produk, beserta urutannya | Teks fitur adalah janji produk ("Bebas tarik tunai di ribuan ATM"); salah di sini adalah salah informasi, bukan salah layout | Product owner / marketing |
| 3 | Apakah ketiga produk benar boleh dibuka lewat aplikasi, dan batas umurnya | Menentukan baris mana `is_active` dan kapan `availability_status` bukan `AVAILABLE` | Risk / compliance |
| 4 | Produk mana yang berbadge "Paling Populer", dan siapa yang boleh mengubahnya | Badge adalah dorongan pemasaran; sumbernya harus satu dan bisa diubah | Product owner |
| 5 | Apakah penawaran produk berbeda per wilayah | Menentukan perlu-tidaknya `region_code` di katalog ini. **Default: tidak**, jangan tambahkan tanpa jawaban | Product owner |
| 6 | ~~Siapa yang boleh mengubah katalog produk dan lewat antarmuka apa~~ **TERJAWAB** | Petugas ber-cakupan `CARD_ADMIN`, lewat `GET/PUT /internal/v1/onboarding/products`. Fase 6 sudah dikerjakan | Engineering manager |

Konteks teknis yang sudah pasti, **tidak perlu ditanyakan**:

- Katalog dibaca sebelum sesi ada → tanpa `session_id`, tanpa `Authorization`.
- `product_type` adalah enum Postgres yang sudah dipakai `onboarding_sessions`
  dan `product_card_options`. Katalog ini **mengisi atributnya**, bukan membuat
  daftar produk kedua.
- Client memetakan `icon_key` ke drawable dan `style` ke design token. Tidak ada
  hex, tidak ada URL gambar — aturan token di `CLAUDE.md` repo Android.
- Karena repo ini portofolio: angka dan teks yang dipakai sekarang disalin apa
  adanya dari `strings.xml` supaya nasabah tidak melihat perubahan kata, dan
  **wajib ditandai di komentar migrasi** sebagai bukan tarif resmi BCA. Pola
  yang sama dengan `000025_onboarding_tnc.up.sql`.

---

## Kontrak endpoint

### `GET /v1/onboarding/products`

Publik — tanpa `Authorization`, tanpa `session_id`.

| Hal | Nilai |
|---|---|
| Header wajib | `X-Device-Id`. Kosong → `400 VALIDATION_ERROR` + `details.missing_header` |
| Query | tidak ada. **Jangan** menambah `region_code` sebelum butir 5 di atas terjawab |
| `ETag` | `"products-<catalog_version>"` |
| `Cache-Control` | `public, max-age=300` |
| `If-None-Match` cocok | `304` **tanpa body** — termasuk tanpa envelope |
| Rate limit | bucket **sendiri**: 30 / 5 menit per `X-Device-Id`, berlapis batas per-IP |

Alasan bucket sendiri, bukan menumpang bucket S&K atau katalog kartu: flow Android
memuat ketiganya pada tiga layar berurutan. Bucket bersama berarti ketiga endpoint
saling menghabiskan jatah dan nasabah menerima `429` di tengah pendaftaran. Catatan
ini sudah tertulis di `internal/router/router.go` untuk `/tnc`; jangan diulang
kesalahannya di sini.

Rutenya didaftarkan **di luar** grup ber-limit-IP milik endpoint bersesi, sama
seperti `/tnc` dan `/products/{product_type}/cards`: batas itu ada untuk mencegah
penelusuran `session_id` dan pemerasan OTP, yang tidak berlaku untuk teks publik
dan cacheable.

### Bentuk response

```json
{
  "status": "success",
  "data": {
    "catalog_version": "2026-10-07.1",
    "page": {
      "heading": "Pilih Jenis Rekening",
      "subtitle": "Pilih jenis rekening yang sesuai dengan kebutuhan dan gaya hidup Anda.",
      "deposit_label": "Setoran Awal Minimum",
      "cta_label": "Lanjut",
      "notice": {
        "icon_key": "INFO",
        "title": "Persiapan Dokumen",
        "body": "Siapkan e-KTP fisik Anda dan pastikan berada di area dengan koneksi internet yang stabil untuk kelancaran video verifikasi."
      },
      "consent": {
        "prefix": "Dengan melanjutkan, Anda menyetujui ",
        "link": "Syarat & Ketentuan",
        "suffix": " pembukaan rekening BCA."
      }
    },
    "products": [
      {
        "product_type": "TAHAPAN_BCA",
        "name": "Tahapan BCA",
        "description": "Tabungan utama untuk kemudahan transaksi harian dan proteksi finansial keluarga.",
        "min_initial_deposit": 500000,
        "currency": "IDR",
        "icon_key": "WALLET",
        "style": "PRIMARY",
        "features": [
          "Debit Mastercard",
          "m-BCA & KlikBCA",
          "Bebas tarik tunai di ribuan ATM"
        ],
        "is_popular": true,
        "badge_key": "MOST_POPULAR",
        "is_default": true,
        "display_order": 1,
        "availability_status": "AVAILABLE",
        "availability_reason_key": null
      }
    ]
  },
  "meta": { "request_id": "..." }
}
```

**`consent` dipecah tiga** dengan alasan yang sama seperti `TNCConsent`: bagian
tengahnya dicetak tebal dan berwarna oleh aplikasi. Satu kalimat utuh memaksa
client mencari substring, dan substring itu pecah pada setiap perbaikan kata.

**`page` ikut dilayani** supaya mengubah "Paling Populer", judul, atau isi kotak
Persiapan Dokumen tidak menuntut rilis APK — sejajar dengan `GET /onboarding/tnc`
yang sudah melayani `heading`/`subtitle`/`agree_cta` miliknya sendiri.

### Enum yang dikenal client

| Field | Nilai sah | Dipetakan client ke |
|---|---|---|
| `icon_key` | `WALLET`, `CARD`, `SAVINGS` | `ic_account_balance_wallet`, `ic_credit_card`, `ic_savings` |
| `page.notice.icon_key` | `INFO` | `ic_info` |
| `style` | `PRIMARY`, `SECONDARY`, `NEUTRAL` | pasangan token `AppColor.*100`/`*900` |
| `badge_key` | `MOST_POPULAR`, `null` | teks badge sudut kartu |
| `availability_status` | `AVAILABLE`, `DISABLED`, `COMING_SOON` | kartu redup + alasan |
| `availability_reason_key` | `TEMPORARILY_DISABLED`, `MAINTENANCE`, `COMING_SOON`, `null` | kalimat alasan |

Nilai di luar daftar harus **diabaikan dengan aman** oleh client (ikon bawaan,
kartu tetap terbaca). Menambah nilai baru berarti memperbarui tabel ini **dan**
`references/verification.md` di commit yang sama — kalau tidak, client lama
menampilkan kartu tanpa ikon tanpa ada yang tahu.

### Error

| Kondisi | Respons |
|---|---|
| `X-Device-Id` kosong | `400 VALIDATION_ERROR`, `details.missing_header: "X-Device-Id"` |
| Tidak ada satu pun produk aktif | `503 ONBOARDING_CATALOG_UNAVAILABLE` — **kode baru**, pola `TNC_UNAVAILABLE` |
| Feature flag katalog mati | `503 ONBOARDING_CATALOG_UNAVAILABLE`, client jatuh ke `strings.xml` |
| Melewati rate limit | `429 RATE_LIMIT_EXCEEDED` + `Retry-After` |

`ONBOARDING_PRODUCT_UNKNOWN` (404) dan `ONBOARDING_PRODUCT_UNAVAILABLE` (422)
**sudah ada** di `internal/pkg/apperr/apperr.go` dan dipakai di jalur
`POST /sessions` serta katalog kartu. Pakai ulang, jangan bikin kembarannya.

`GET /v1/onboarding/products/{product_type}` (satu produk) **sengaja tidak
dibuat**: layar hanya butuh daftar, dan rute itu bertabrakan secara visual
dengan `/products/{product_type}/cards` milik skill kartu.

---

## Aturan wajib

1. **Payload tidak memuat nilai visual.** Tidak ada hex, gradient, nama drawable,
   atau URL gambar. Hanya enum `icon_key` dan `style`. Mengirim hex membuat client
   melanggar aturan token repo Android dan perubahannya akan ditolak di review.
2. **Nominal integer rupiah penuh**, bukan string terformat: `500000`, bukan
   `"Rp 500.000"`. Pemformatan milik client.
3. **Label statis dikirim sebagai key** (`badge_key`, `availability_reason_key`),
   bukan kalimat Indonesia. Kecuali `page.*`, `name`, `description`, dan
   `features[]` — itu memang teks yang dilayani supaya bisa diubah tanpa rilis.
4. **`product_type` adalah satu-satunya identitas produk.** Response tidak pernah
   menuntut client menyimpulkan produk dari posisi array. Urutan tampilan ada di
   `display_order`, dan `display_order` bukan identitas.
5. **Produk tidak pernah dibaca langsung dari database di jalur panas.** Selalu
   lewat cache berbasis `catalog_version`; database hanya untuk cache miss dan
   admin write. Galat cache **turun ke database**, bukan ke halaman error.
6. **Baris produk tidak pernah dihapus.** `onboarding_sessions.product_type`
   menunjuk ke sini. Produk yang dihentikan di-set `is_active = FALSE`.
7. **Setoran awal yang ditampilkan ikut tercatat di sesi.** `POST /sessions`
   menyimpan `product_catalog_version` dan `min_initial_deposit_shown`. Yang perlu
   dibuktikan saat sengketa adalah angka **yang dilihat nasabah saat itu**, bukan
   angka hari ini — alasan yang sama dengan `monthly_admin_fee_shown` pada
   `onboarding_card_selection_log`.
8. **Field baru selalu nullable dengan default aman.** APK lama tidak mengenal
   katalog ini sama sekali dan harus tetap bisa membuat sesi.
9. **Katalog bisa dimatikan lewat feature flag** tanpa rollback deployment, dan
   matinya katalog **tidak boleh** mematikan `POST /sessions`.
10. **Jangan mengubah urutan atau nama nilai `onboarding_product_type`.** Enum itu
    sudah dipakai tiga tabel dan dibandingkan sebagai string oleh client.
11. **Nilai AWAL katalog ikut migrasi, bukan seeder.** Seeder menolak jalan di luar
    `APP_ENV=development`, jadi staging akan menjawab layar kosong. Pelajaran dari
    `000025`. **Perubahan sesudahnya lewat `PUT /internal/v1/onboarding/products`**
    (cakupan `CARD_ADMIN`), yang menulis dalam satu transaksi, menaikkan
    `catalog_version` **sekali**, dan menghapus cache-nya sendiri.
12. **Memindahkan badge "Paling Populer" wajib mengirim KEDUA produk** dalam satu
    permintaan — yang kehilangan dan yang mendapat. `idx_onboarding_products_one_popular`
    adalah unique index berekspresi dan **tidak bisa `DEFERRABLE`**, jadi pelanggarannya
    terdeteksi pada statement itu juga. Di dalamnya penulisan berjalan dua langkah: semua
    baris ditulis dengan kedua flag dipaksa `FALSE` lebih dulu, baru yang diminta
    dinyalakan. Mengirim hanya yang mendapat dijawab
    `409 ONBOARDING_PRODUCT_CATALOG_CONFLICT`.
13. **`DEL onboarding:products:v1:catalog` adalah bagian dari setiap penulisan.** Kunci
    itu **tidak** memuat versinya — berbeda dari katalog kartu — jadi `catalog_version`
    yang naik tanpa `DEL` tetap menyajikan katalog lama sampai TTL 24 jam habis. API
    admin melakukannya sendiri; perubahan lewat SQL manual harus melakukannya.
14. **Jalur admin TIDAK membaca `FEATURE_ONBOARDING_PRODUCT_CATALOG`.** Katalog yang
    dimatikan karena isinya salah adalah justru saat isinya paling perlu diubah. Itu
    sebabnya `ProductAdminService` adalah service tersendiri, bukan method di
    `ProductService`.

---

## Skema database

Migrasi berpasangan `.up.sql` + `.down.sql`. **Cek nomor terakhir dulu**
(`ls migrations/` atau `make migrate-status`) — jangan menebak nomornya.

```text
onboarding_products                  satu baris per nilai enum onboarding_product_type
  product_type  onboarding_product_type PRIMARY KEY   ← enum, bukan TEXT
  name, description                   TEXT NOT NULL
  min_initial_deposit                 BIGINT NOT NULL CHECK (>= 0)
  currency                            CHAR(3) NOT NULL DEFAULT 'IDR'
  icon_key, style                     TEXT NOT NULL + CHECK daftar enum
  badge_key                           TEXT NULL + CHECK
  is_popular, is_default, is_active   BOOLEAN NOT NULL
  display_order                       INT NOT NULL
  availability_status                 TEXT NOT NULL DEFAULT 'AVAILABLE' + CHECK
  availability_reason_key             TEXT NULL + CHECK
  created_at, updated_at              TIMESTAMPTZ

onboarding_product_features           fitur berurut, tabel terpisah
  product_type   FK onboarding_products ON DELETE CASCADE
  feature_order  INT, UNIQUE (product_type, feature_order)
  label          TEXT NOT NULL

onboarding_product_page               copy halaman, SATU baris
  id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id)
  heading, subtitle, deposit_label, cta_label
  notice_icon_key, notice_title, notice_body
  consent_prefix, consent_link, consent_suffix

onboarding_product_catalog_version    penanda versi, SATU baris
  id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id)
  version_date DATE, counter INT CHECK (> 0)

onboarding_sessions                   dua kolom baru, keduanya NULLABLE
  product_catalog_version    TEXT
  min_initial_deposit_shown  BIGINT
```

Keputusan skema yang punya alasan, jangan dibalik tanpa alasan baru:

- **`onboarding_product_features` tabel terpisah**, bukan JSONB dan bukan kolom
  berpola `feature_N`. Jumlah fiturnya berbeda per produk (layar sudah merender
  3 dan 2 fitur) dan berubah tiap revisi materi pemasaran. Menambah fitur keempat
  tidak boleh berarti menambah kolom. Pola `onboarding_tnc_sections`.
- **`product_type` bertipe enum**, bukan `TEXT`. `TEXT` di sini menciptakan sumber
  kebenaran kedua untuk daftar produk yang sama — persis yang dihindari komentar
  di `000019_card_products.up.sql` untuk `product_card_options`.
- **Versi katalog di Postgres**, bukan hanya di Redis. Redis adalah cache: restart
  atau eviction menghapusnya, sementara `ETag` yang sudah dipegang client — dan
  `product_catalog_version` yang sudah tersimpan di baris sesi — tetap merujuk
  versi itu. Alasan yang sama dengan `card_catalog_version`.
- **Maksimum satu `is_popular` dan satu `is_default`**, ditegakkan unique index
  berekspresi (`ON onboarding_products ((TRUE)) WHERE is_popular`). Dua produk
  "Paling Populer" membuat jawabannya bergantung `ORDER BY`, dan jawaban yang
  bergantung urutan baris bisa berubah sendiri.
- **`availability_status` selain `AVAILABLE` wajib menyebut
  `availability_reason_key`** (CHECK constraint). Tanpa alasan, nasabah hanya
  melihat kartu mati tanpa penjelasan.
- **Dua kolom di sesi, bukan tabel log.** Produk dipilih sekali dan sesi lahir
  sesudahnya — tidak ada jalur perubahan yang perlu dirunut, berbeda dari kartu
  yang bisa diganti lewat `PUT /sessions/{id}/card`.

Redis (`docs/03-REDIS-STRATEGY.md`):

```text
onboarding:products:v1:catalog        katalog aktif, TTL 24 jam
onboarding:products:v1:ver:<version>  snapshot per versi, TTL 24 jam
```

TTL 24 jam mengikuti `tncTTL` dan `contentTTL` dengan alasan yang sama: isinya
sama untuk semua nasabah dan berubah beberapa kali setahun. Invalidasi terjadi
lewat kenaikan `catalog_version`, bukan `DEL` berpola.

---

## Struktur file yang disentuh

Arah impor satu arah — `domain/` tidak tahu SQL/Redis, `handler/` tidak mengimpor
`repository/`. Lihat `CLAUDE.md`.

```text
internal/
├── handler/
│   └── onboarding_handler.go        ← UBAH: method GetProducts (BUKAN file baru;
│                                       GetTNC sudah tinggal di sini)
├── domain/onboarding/
│   ├── product_service.go           ← BARU: katalog + cache, sejajar tnc_service.go
│   ├── entity.go                    ← UBAH: tipe ProductCatalog/ProductOption/ProductPage
│   ├── repository.go                ← UBAH: interface ProductCatalogRepository + Cache
│   └── session_service.go           ← UBAH: simpan product_catalog_version +
│                                       min_initial_deposit_shown saat create session
├── repository/postgres/
│   └── onboarding_product_repo.go   ← BARU
├── repository/redis/
│   └── onboarding_product_cache.go  ← BARU
├── pkg/apperr/apperr.go             ← UBAH: ONBOARDING_CATALOG_UNAVAILABLE (503)
├── config/config.go                 ← UBAH: FEATURE_ONBOARDING_PRODUCT_CATALOG
└── router/router.go                 ← UBAH: rute + konstanta rate limit sendiri

migrations/
├── 0000NN_onboarding_products.up.sql    ← BARU, nomor = migrasi terakhir + 1
└── 0000NN_onboarding_products.down.sql  ← BARU
```

`ProductsUnderMaintenance` (`ONBOARDING_PRODUCTS_MAINTENANCE`) **sudah ada** dan
sudah dipakai katalog kartu. Katalog produk membacanya juga, tapi menjawabnya
secara berbeda: produk yang tutup tetap **muncul di daftar** dengan
`availability_status: "DISABLED"` dan `availability_reason_key: "MAINTENANCE"`,
bukan menghilang dan bukan membuat seluruh endpoint `422`. Nasabah perlu tahu
produk itu ada dan sedang tutup — bukan bingung karena pilihannya lenyap.

---

## Dampak ke yang sudah berjalan

- **`POST /v1/onboarding/sessions`** mulai menyimpan `product_catalog_version` dan
  `min_initial_deposit_shown`. Keduanya nullable: request dari APK lama tetap sah
  (aturan wajib #8). Validasi `product_type` yang sudah ada **tidak berubah** —
  tetap `pt.Valid()` lalu cek maintenance, dan **tidak** boleh mulai menuntut
  baris `onboarding_products` ada, supaya katalog yang mati tidak mematikan
  pembuatan sesi (aturan wajib #9).
- **`GET /v1/onboarding/sessions/{id}`** boleh ikut membawa objek produk ringkas
  untuk layar Ringkasan. Opsional, dan hanya setelah fase 5.
- **Katalog kartu tidak disentuh.** `/products/{product_type}/cards` tetap milik
  `buka-rekening-kartu`; satu-satunya hubungannya adalah `product_type` yang sama.

## Dokumen yang wajib diperbarui di commit yang sama

Mengubah bentuk response tanpa memperbarui spec + Postman memecah build Android —
ini pernah terjadi di repo ini.

- `docs/01-API-SPECIFICATION.md` — endpoint + enum + error baru
- `docs/06-BUKA-REKENING-API-SPEC.md` — langkah katalog produk sebelum S&K
- `docs/02-DATABASE-SCHEMA.md` dan `docs/03-REDIS-STRATEGY.md`
- `docs/postman/` — request baru beserta contoh response
- `README.md` daftar endpoint + variabel env baru
- `CLAUDE.md` — satu baris di tabel skill
