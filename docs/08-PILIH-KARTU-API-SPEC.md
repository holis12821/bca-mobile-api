# API Specification — Pilih Jenis Kartu Paspor (Sisipan Flow Buka Rekening)

> Sisipan langkah **Pilih Kartu** di antara *Pilih Jenis Rekening* dan *Syarat & Ketentuan*.
> Mengikuti konvensi envelope, auth header, dan error code di `01-API-SPECIFICATION.md`,
> serta melanjutkan kontrak onboarding di `06-BUKA-REKENING-API-SPEC.md`.
>
> Layar client yang dilayani: `BukaRekeningPilihKartuScreen.kt`.

> ### ⚠ Angka dalam dokumen ini bukan angka resmi
>
> Seluruh nominal biaya dan limit pada contoh payload (`14000`, `16000`, `19000`,
> limit tarik tunai, dst.) **disalin dari `strings.xml` client** — lihat §1. Itu
> **data desain untuk layar**, bukan tarif produk yang disahkan.
>
> **Jangan menyemai angka-angka itu ke database staging atau produksi.** Angka
> resmi harus diminta ke product owner lebih dulu; lihat §17.
>
> Contoh di dokumen ini ada untuk menunjukkan **bentuk** payload — nama field,
> tipe data, susunan objek — bukan **isinya**.

---

## 1. Kenapa sisipan ini butuh endpoint sendiri

Sampai hari ini layar pilih kartu memakai data lokal: `defaultKartuPasporList()` membaca
biaya administrasi dan limit dari `strings.xml` (`buka_rekening_kartu_blue_biaya`,
`..._tarik_tunai`, dst). Akibatnya:

- Mengubah biaya admin Rp14.000 → Rp15.000 berarti **rilis ulang APK**.
- Kartu yang stoknya habis di satu wilayah tetap terlihat bisa dipilih.
- Server tidak pernah tahu kartu mana yang dipilih nasabah, jadi `POST /onboarding/submit`
  tidak bisa membuat permintaan cetak kartu.
- Tidak ada jejak audit "nasabah melihat biaya berapa saat memilih" — ini yang diminta
  saat sengketa biaya.

Empat masalah itu yang diselesaikan dokumen ini.

---

## 2. Posisi dalam flow

```
Pilih Jenis Rekening            → GET  /v1/onboarding/products/{product_type}/cards
        ↓                          (tanpa session — session belum ada)
Pilih Kartu Paspor       ←── SISIPAN BARU
        ↓
Syarat & Ketentuan              → POST /v1/onboarding/sessions   { product_type, card_type }
        ↓
Panduan Foto → OCR → ... → Ringkasan → Berhasil
```

**Yang penting dan mudah salah:** di client, sesi onboarding baru dibuat di layar S&K
(`AuthGraph.kt`, `composable<BukaRekeningSyaratKetentuan>`), yaitu **sesudah** kartu dipilih.
Jadi endpoint katalog **tidak boleh** mensyaratkan `session_id`. Pilihan kartu ikut terkirim
saat sesi dibuat.

Tetap sediakan jalur kedua (`PUT .../card`) untuk kasus nasabah menekan Back dari S&K,
melanjutkan draf, atau mengubah kartu dari layar Ringkasan.

---

## 3. Daftar Endpoint

```
GET  /v1/onboarding/products/{product_type}/cards   ← katalog kartu per produk (publik, cacheable)
POST /v1/onboarding/sessions                        ← DIUBAH: terima field `card_type`
PUT  /v1/onboarding/sessions/{session_id}/card      ← set/ubah kartu pada sesi berjalan
GET  /v1/onboarding/sessions/{session_id}           ← DIUBAH: balas objek `card`
POST /v1/onboarding/submit                          ← DIUBAH: balas `card` + estimasi kirim

GET  /internal/v1/cards                                     ← admin: baca katalog
PUT  /internal/v1/cards/{card_type}                         ← admin: fee, limit, delivery, is_active
PUT  /internal/v1/products/{product_type}/cards/{card_type} ← admin: urutan, default, badge, stok
```

Tiga endpoint `/internal/v1/*` dilindungi `X-Internal-API-Key` — middleware yang
sama dengan endpoint CS onboarding. **Model otorisasi di atas API key itu (siapa
yang boleh menulis katalog) belum diputuskan; lihat §17.**

---

## 4. GET Katalog Kartu

> Nominal pada contoh respons di bawah adalah placeholder desain. Yang mengikat
> dari seksi ini adalah **nama field, tipe, dan susunan objek** — bukan angkanya.
> Lihat banner di kepala dokumen dan §17.

**`default_card_type` bisa berupa string kosong.** Itu terjadi bila tidak ada
satu pun kartu yang `AVAILABLE` — misalnya seluruh kartu habis stok di wilayah
itu. Kartunya tetap dikirim beserta `availability.reason_key` masing-masing,
hanya tanpa pilihan awal.

Client harus memperlakukan `""` sebagai "tidak ada kartu terpilih" dan tidak
mencari kartu dengan `card_type` kosong. Tipenya sengaja tetap string, bukan
`null`, supaya klien dengan field non-nullable tidak gagal mem-parsing.

**`ETag` mengidentifikasi representasi, bukan hanya versi katalog.** Bentuknya
`"{product_type}:{region}:{catalog_version}"`, dengan `region` berisi `nat`
untuk permintaan tanpa `region_code`. Isi respons berbeda per produk dan per
wilayah, jadi ETag yang hanya memuat versi akan membuat katalog wilayah
dijawab `304` oleh klien yang memegang katalog nasional.

```
GET /v1/onboarding/products/{product_type}/cards
```

### Path & Query

| Nama | Wajib | Keterangan |
|---|---|---|
| `product_type` (path) | ya | `TAHAPAN_BCA` \| `TAHAPAN_XPRESI` \| `TABUNGANKU` |
| `region_code` (query) | tidak | Kode wilayah untuk cek stok kartu fisik, mis. `DKI` |

### Header

| Header | Keterangan |
|---|---|
| `X-Device-Id` | Wajib. Dipakai untuk rate limit karena belum ada session/token |
| `If-None-Match` | Opsional. Isi `ETag` dari respons sebelumnya |

Endpoint ini **tidak** butuh `Authorization` maupun `session_id`.

### Response `200 OK`

```json
{
  "status": "success",
  "data": {
    "catalog_version": "2026-09-22.1",
    "product_type": "TAHAPAN_BCA",
    "default_card_type": "PASPOR_BLUE",
    "currency": "IDR",
    "cards": [
      {
        "card_type": "PASPOR_BLUE",
        "name": "Blue Mastercard",
        "network": "MASTERCARD",
        "tier_key": "DEBIT",
        "style": "BLUE",
        "badge_key": "RECOMMENDED_BEGINNER",
        "is_popular": true,
        "display_order": 1,
        "fees": {
          "monthly_admin": 14000,
          "card_issuance": 0,
          "card_replacement": 15000
        },
        "limits": {
          "cash_withdrawal": 10000000,
          "transfer_bca": 50000000,
          "transfer_interbank": 15000000,
          "debit_purchase": 50000000
        },
        "availability": {
          "status": "AVAILABLE",
          "reason_key": null
        },
        "delivery": {
          "physical_card_available": true,
          "estimated_days_min": 3,
          "estimated_days_max": 7,
          "branch_pickup_available": true
        },
        "eligibility": {
          "min_age": 17,
          "min_initial_deposit": 500000
        }
      },
      {
        "card_type": "PASPOR_GOLD",
        "name": "Gold Mastercard",
        "network": "MASTERCARD",
        "tier_key": "DEBIT",
        "style": "GOLD",
        "badge_key": "FLEXIBLE_TRANSACTION",
        "is_popular": false,
        "display_order": 2,
        "fees": { "monthly_admin": 16000, "card_issuance": 0, "card_replacement": 15000 },
        "limits": {
          "cash_withdrawal": 15000000,
          "transfer_bca": 75000000,
          "transfer_interbank": 20000000,
          "debit_purchase": 75000000
        },
        "availability": { "status": "AVAILABLE", "reason_key": null },
        "delivery": {
          "physical_card_available": true,
          "estimated_days_min": 3,
          "estimated_days_max": 7,
          "branch_pickup_available": true
        },
        "eligibility": { "min_age": 17, "min_initial_deposit": 500000 }
      },
      {
        "card_type": "PASPOR_PLATINUM",
        "name": "Platinum Mastercard",
        "network": "MASTERCARD",
        "tier_key": "PLATINUM_DEBIT",
        "style": "PLATINUM",
        "badge_key": "MAX_LIMIT",
        "is_popular": false,
        "display_order": 3,
        "fees": { "monthly_admin": 19000, "card_issuance": 0, "card_replacement": 15000 },
        "limits": {
          "cash_withdrawal": 20000000,
          "transfer_bca": 100000000,
          "transfer_interbank": 25000000,
          "debit_purchase": 100000000
        },
        "availability": { "status": "OUT_OF_STOCK", "reason_key": "STOCK_EMPTY_IN_REGION" },
        "delivery": {
          "physical_card_available": false,
          "estimated_days_min": null,
          "estimated_days_max": null,
          "branch_pickup_available": true
        },
        "eligibility": { "min_age": 17, "min_initial_deposit": 500000 }
      }
    ]
  },
  "meta": { "request_id": "req_...", "timestamp": "2026-09-22T09:00:00Z" }
}
```

Header respons: `ETag: "2026-09-22.1"` dan `Cache-Control: public, max-age=900`.
Bila `If-None-Match` cocok → `304 Not Modified` tanpa body.

### Urutan dan default

- Server mengirim `cards` **sudah terurut** sesuai `display_order`. Client tidak menyortir ulang.
- `default_card_type` adalah kartu yang terpilih saat layar pertama kali dibuka. Kalau nilainya
  menunjuk kartu yang `availability.status != "AVAILABLE"`, server wajib menurunkannya ke kartu
  tersedia pertama — jangan biarkan client menebak.

### Error Codes

| Code | HTTP | Keterangan |
|---|---|---|
| `ONBOARDING_PRODUCT_UNKNOWN` | 404 | `product_type` di luar enum |
| `ONBOARDING_PRODUCT_UNAVAILABLE` | 422 | Produk sedang maintenance |
| `CARD_CATALOG_EMPTY` | 404 | Produk valid tapi belum punya satu pun kartu aktif |
| `RATE_LIMIT_EXCEEDED` | 429 | Sertakan `details.retry_after_seconds` |

---

## 5. Aturan Penyajian Data untuk Client

Aturan di `CLAUDE.md` project Android mengunci hal ini, dan backend yang melanggarnya akan
memaksa client menanam nilai visual. **Wajib dipatuhi di sisi server:**

1. **Tidak ada warna di payload.** Tidak ada hex, tidak ada gradient stop, tidak ada URL
   gambar kartu. Yang dikirim hanya `style` (`BLUE` \| `GOLD` \| `PLATINUM`), dipetakan client
   ke token `CardArt` di `ui/theme/Color.kt`. Menambah gaya baru berarti menambah token
   di client lebih dulu — koordinasikan, jangan kirim nilai yang belum dikenal.
2. **Nominal dikirim sebagai integer rupiah penuh**, bukan string terformat.
   `14000`, bukan `"Rp14.000"`. Pemformatan milik client.
3. **Label statis dikirim sebagai key, bukan kalimat.** `badge_key: "RECOMMENDED_BEGINNER"`,
   bukan `"Rekomendasi Pemula"`. Client memetakannya ke `strings.xml`. Key yang tidak dikenal
   client → badge tidak ditampilkan, bukan crash.
4. **Nama produk boleh berupa teks apa adanya.** `name: "Blue Mastercard"` adalah data
   produk, bukan label antarmuka — ini pengecualian sah dari aturan §3 `strings.xml`.
5. **Semua field opsional wajib punya nilai default yang aman** supaya client versi lama
   tetap jalan saat field baru ditambahkan.

---

## 6. Enum Values

### card_type
```
PASPOR_BLUE | PASPOR_GOLD | PASPOR_PLATINUM
```

### style (pemetaan visual di client)
```
BLUE | GOLD | PLATINUM
```

### tier_key
```
DEBIT | PLATINUM_DEBIT
```

### badge_key
```
RECOMMENDED_BEGINNER | FLEXIBLE_TRANSACTION | MAX_LIMIT
```

### availability.status
```
AVAILABLE | OUT_OF_STOCK | DISABLED | NOT_ELIGIBLE
```

### availability.reason_key
```
STOCK_EMPTY_IN_REGION | TEMPORARILY_DISABLED | PRODUCT_MISMATCH | AGE_REQUIREMENT
```

### current_step — urutan baru

```
TNC → CARD_SELECTION → OCR → PERSONAL_DATA → OTP_VERIFY → BIOMETRIC
    → VIDEO_CALL → CREDENTIALS → REVIEW → COMPLETED
```

`CARD_SELECTION` disisipkan tepat setelah `TNC`. Lihat §7 untuk kapan step ini dilewati.

---

## 7. POST Sessions — Perubahan

```
POST /v1/onboarding/sessions
```

### Request

```json
{
  "product_type": "TAHAPAN_BCA",
  "card_type": "PASPOR_BLUE",
  "card_catalog_version": "2026-09-22.1",
  "device_id": "d_abc123",
  "accepted_tnc_version": "2026-09-01"
}
```

| Field | Wajib | Keterangan |
|---|---|---|
| `card_type` | tidak | Kosongkan bila client belum menampilkan layar kartu |
| `card_catalog_version` | tidak | Versi katalog yang dilihat nasabah; dicatat untuk audit biaya |

### Perilaku

| Kondisi | `current_step` hasil | `steps_completed.card_selected` |
|---|---|---|
| `card_type` valid & tersedia | `OCR` | `true` |
| `card_type` kosong | `CARD_SELECTION` | `false` |
| `card_type` tidak dikenal | tolak `422 CARD_TYPE_INVALID` | — |
| `card_type` tidak tersedia | tolak `409 CARD_TYPE_UNAVAILABLE` | — |

Cabang "kosong → `CARD_SELECTION`" itu yang membuat client versi lama tetap jalan, dan yang
membuat client baru bisa memilih kartu setelah sesi ada.

Bila `card_catalog_version` yang dikirim sudah bukan versi terkini, sesi tetap dibuat, tetapi
server mencatat selisihnya di audit log dan menyertakan `meta.catalog_outdated: true` supaya
client bisa menyegarkan tampilan biaya sebelum layar Ringkasan.

### Response `201 Created` (tambahan pada payload lama)

```json
{
  "status": "success",
  "data": {
    "session_id": "onb_9f8e7d6c5b4a",
    "product": { "type": "TAHAPAN_BCA", "name": "Tahapan BCA", "currency": "IDR",
                 "min_initial_deposit": 500000,
                 "features": ["Paspor BCA Mastercard Debit", "m-BCA", "KlikBCA"] },
    "card": {
      "card_type": "PASPOR_BLUE",
      "name": "Blue Mastercard",
      "style": "BLUE",
      "fees": { "monthly_admin": 14000 },
      "catalog_version": "2026-09-22.1"
    },
    "current_step": "OCR",
    "expires_at": "2026-09-23T10:30:00Z"
  }
}
```

---

## 8. PUT Kartu pada Sesi Berjalan

```
PUT /v1/onboarding/sessions/{session_id}/card
```

Dipakai saat: nasabah menekan Back dari S&K lalu ganti kartu, melanjutkan draf yang berhenti
di `CARD_SELECTION`, atau mengubah kartu dari layar Ringkasan.

### Request

```json
{
  "card_type": "PASPOR_GOLD",
  "card_catalog_version": "2026-09-22.1"
}
```

### Response `200 OK`

```json
{
  "status": "success",
  "data": {
    "card": {
      "card_type": "PASPOR_GOLD",
      "name": "Gold Mastercard",
      "style": "GOLD",
      "fees": { "monthly_admin": 16000 },
      "catalog_version": "2026-09-22.1"
    },
    "current_step": "OCR",
    "steps_completed": { "card_selected": true }
  }
}
```

`current_step` yang dibalas adalah step berjalan sesi, **bukan** selalu `OCR` — bila nasabah
mengubah kartu dari Ringkasan, nilainya tetap `REVIEW`. Client menavigasi mengikuti nilai ini.

### Kapan boleh diubah

Boleh kapan saja selama `steps_completed.submitted == false`. Setelah submit, kartu terkunci:
permintaan cetak sudah masuk ke core banking.

### Error Codes

| Code | HTTP | Keterangan |
|---|---|---|
| `ONBOARDING_NOT_FOUND` | 404 | Sesi tidak dikenal |
| `ONBOARDING_SESSION_EXPIRED` | 422 | Sesi kedaluwarsa |
| `CARD_TYPE_INVALID` | 422 | `card_type` tidak ada di katalog produk sesi ini |
| `CARD_TYPE_UNAVAILABLE` | 409 | Stok habis atau kartu dinonaktifkan |
| `CARD_NOT_ELIGIBLE` | 422 | Umur/setoran awal tidak memenuhi; sertakan `details.reason_key` |
| `CARD_LOCKED` | 409 | Pengajuan sudah disubmit |

---

## 9. GET Session — Perubahan

Tambahan pada payload `GET /v1/onboarding/sessions/{session_id}`:

```json
{
  "card": {
    "card_type": "PASPOR_GOLD",
    "name": "Gold Mastercard",
    "style": "GOLD",
    "fees": { "monthly_admin": 16000 },
    "catalog_version": "2026-09-22.1"
  },
  "steps_completed": {
    "tnc_accepted": true,
    "card_selected": true,
    "ocr_verified": false
  }
}
```

`card` bernilai `null` bila sesi berhenti sebelum kartu dipilih. `steps_completed.card_selected`
adalah field **baru** — default `false` di client lama, jadi penambahan ini aman.

---

## 10. POST Submit — Perubahan

Tambahan pada payload respons `POST /v1/onboarding/submit`:

```json
{
  "card": {
    "card_type": "PASPOR_GOLD",
    "name": "Gold Mastercard",
    "masked_number": "•••• 5678",
    "status": "REQUESTED",
    "delivery": {
      "method": "COURIER",
      "estimated_arrival_from": "2026-09-25",
      "estimated_arrival_to": "2026-09-29",
      "tracking_number": null
    }
  }
}
```

Bila `steps_completed.card_selected == false` saat submit, server menolak dengan
`ONBOARDING_INCOMPLETE` dan `details.missing_step: "CARD_SELECTION"`.

### Pemetaan `card_type` ke core banking

Permintaan cetak kartu dikirim ke core banking memakai **kode kartu milik core
banking**, yang belum tentu sama dengan enum di dokumen ini. Pemetaannya disimpan
di konfigurasi runtime, bukan ditanam di kode:

Terimplementasi sebagai variabel environment, bukan YAML — repo ini tidak punya file
konfigurasi runtime lain, dan menambahkan satu format baru hanya untuk tiga baris tidak
sebanding:

```bash
# nilai di bawah PLACEHOLDER DEV; kode resmi belum dikonfirmasi, lihat §17 butir 8
ONBOARDING_CARD_CORE_BANKING_CODES=PASPOR_BLUE:CB-DEV-BLUE,PASPOR_GOLD:CB-DEV-GOLD,PASPOR_PLATINUM:CB-DEV-PLAT
```

Aturannya:

- `card_type` yang tidak punya pemetaan → **tolak saat startup**, jangan menunggu
  sampai ada nasabah submit. Konfigurasi yang bolong lebih baik ketahuan saat
  deploy daripada saat rekening gagal dibuatkan kartu.
- Penerbitan kartu gagal sementara rekening sudah jadi → rekening tetap `ACTIVE`,
  `card.status = REQUESTED`, kegagalan masuk antrean retry. Jangan menggagalkan
  seluruh submit karena kartu.
- Hormati `X-Idempotency-Key`: submit ulang tidak boleh menghasilkan dua
  permintaan cetak.

---

## 10b. Observability & Audit — yang terpasang

### Metrik

Registry counter/histogram in-process (`internal/pkg/metrics`), bukan Prometheus: repo ini
belum punya stack metrik, dan menambah dependensi beserta endpoint `/metrics` adalah
keputusan operasional yang lebih besar daripada sisipan ini. Bentuk datanya meniru model
Prometheus (nama + label, counter monotonik, histogram bucket kumulatif) supaya pemindahan
ke `client_golang` kelak hanya mengganti implementasi `Registry`.

| Metrik | Label |
|---|---|
| `onboarding_card_catalog_requests_total` | `product_type`, `cache_result` (`hit`/`miss`/`error`) |
| `onboarding_card_catalog_duration_ms` (histogram) | `cache_result` — hit dan miss terpisah |
| `onboarding_card_selected_total` | `product_type`, `card_type` |
| `onboarding_card_changed_total` | `from`, `to` |
| `onboarding_card_unavailable_total` | `card_type`, `reason_key` |
| `onboarding_card_issuance_total` | `card_type`, `outcome` (`ok`/`failed`) |

Counter juga disimpan dalam bucket per-menit (retensi 60 menit) supaya pertanyaan
"berapa dalam 15 menit terakhir" bisa dijawab — counter kumulatif saja akan tetap tenang
berjam-jam setelah stok salah dikonfigurasi.

### Alert

`CARD_UNAVAILABLE_RATIO_HIGH`, muncul di `GET /v1/onboarding/monitoring` (internal).

Berbunyi bila porsi penolakan `CARD_TYPE_UNAVAILABLE` melebihi **5%** dari seluruh upaya
pemilihan kartu dalam **15 menit** terakhir, dengan minimum 20 upaya sebagai sampel.
Penyebutnya adalah pemilihan + penggantian + penolakan; memakai hanya yang berhasil akan
membuat rasio meledak justru ketika hampir semuanya ditolak. Minimum sampel menahan alert
dari jam sepi, di mana satu penolakan di antara tiga upaya sudah 33% dan sepenuhnya normal.

Rasio, bukan jumlah mutlak: trafik yang naik dua kali lipat juga menaikkan jumlah penolakan
tanpa ada yang salah. Yang menandakan konfigurasi stok keliru adalah porsinya.

### Log terstruktur

`log/slog`, tanpa PII — `session_id`, `product_type`, `card_type`, `catalog_version`,
`request_id`. Tidak ada nama, NIK, nomor telepon, maupun nomor kartu; nomor kartu bahkan
tidak disimpan utuh di database, hanya bentuk tersamar empat digit terakhir.

### Query audit

Keduanya terjawab satu `SELECT`:

```sql
-- "Kartu apa yang dipilih sesi X, dan biaya administrasi berapa yang ditampilkan saat itu?"
SELECT session_id, to_card_type AS card_type, monthly_admin_fee_shown,
       catalog_version, created_at
FROM onboarding_card_selection_log
WHERE session_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- "Berapa kali nasabah mengganti kartu sebelum submit, bulan ini?"
-- from_card_type IS NOT NULL memisahkan penggantian dari pemilihan pertama.
SELECT COUNT(*) AS total_penggantian,
       COUNT(DISTINCT session_id) AS sesi_yang_mengganti,
       ROUND(COUNT(*)::numeric / NULLIF(COUNT(DISTINCT session_id), 0), 2) AS rata_per_sesi
FROM onboarding_card_selection_log
WHERE from_card_type IS NOT NULL
  AND created_at >= date_trunc('month', NOW());
```

`monthly_admin_fee_shown` disalin saat pemilihan, bukan di-join: yang perlu dibuktikan saat
sengketa adalah biaya yang **dilihat nasabah** saat itu, bukan biaya hari ini.

---

## 11. Skema Database

Mengikuti gaya `02-DATABASE-SCHEMA.md`.

```sql
-- Katalog kartu. Satu baris per jenis kartu, lintas produk.
CREATE TABLE card_products (
    card_type                TEXT PRIMARY KEY,
    name                     TEXT        NOT NULL,
    network                  TEXT        NOT NULL DEFAULT 'MASTERCARD',
    tier_key                 TEXT        NOT NULL,
    style                    TEXT        NOT NULL,
    currency                 CHAR(3)     NOT NULL DEFAULT 'IDR',

    fee_monthly_admin        BIGINT      NOT NULL,
    fee_card_issuance        BIGINT      NOT NULL DEFAULT 0,
    fee_card_replacement     BIGINT      NOT NULL DEFAULT 0,

    limit_cash_withdrawal    BIGINT      NOT NULL,
    limit_transfer_bca       BIGINT      NOT NULL,
    limit_transfer_interbank BIGINT      NOT NULL,
    limit_debit_purchase     BIGINT      NOT NULL,

    physical_card_available  BOOLEAN     NOT NULL DEFAULT TRUE,
    delivery_days_min        INT,
    delivery_days_max        INT,
    branch_pickup_available  BOOLEAN     NOT NULL DEFAULT TRUE,

    min_age                  INT         NOT NULL DEFAULT 17,
    min_initial_deposit      BIGINT      NOT NULL DEFAULT 0,

    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Kartu mana yang ditawarkan untuk produk mana, beserta urutan dan status stok.
CREATE TABLE product_card_options (
    product_type            TEXT    NOT NULL,
    card_type               TEXT    NOT NULL REFERENCES card_products(card_type),
    display_order           INT     NOT NULL,
    is_default              BOOLEAN NOT NULL DEFAULT FALSE,
    is_popular              BOOLEAN NOT NULL DEFAULT FALSE,
    badge_key               TEXT,
    availability_status     TEXT    NOT NULL DEFAULT 'AVAILABLE',
    availability_reason_key TEXT,
    region_code             TEXT,   -- NULL = berlaku nasional
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (product_type, card_type, COALESCE(region_code, ''))
);

CREATE UNIQUE INDEX idx_product_card_default
    ON product_card_options (product_type, COALESCE(region_code, ''))
    WHERE is_default;

-- Kolom tambahan di sesi onboarding.
ALTER TABLE onboarding_sessions
    ADD COLUMN card_type            TEXT REFERENCES card_products(card_type),
    ADD COLUMN card_selected_at     TIMESTAMPTZ,
    ADD COLUMN card_catalog_version TEXT;

-- Jejak audit perubahan pilihan kartu.
CREATE TABLE onboarding_card_selection_log (
    id              BIGSERIAL PRIMARY KEY,
    session_id      TEXT        NOT NULL,
    from_card_type  TEXT,
    to_card_type    TEXT        NOT NULL,
    catalog_version TEXT        NOT NULL,
    monthly_admin_fee_shown BIGINT NOT NULL,
    actor           TEXT        NOT NULL DEFAULT 'CUSTOMER',
    ip_address      INET,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

`monthly_admin_fee_shown` sengaja disalin, bukan di-join: yang perlu dibuktikan saat sengketa
adalah biaya **yang dilihat nasabah saat itu**, bukan biaya hari ini.

**Retensi `onboarding_card_selection_log` belum ditetapkan** — lihat §17. Sampai
compliance memutuskan, jangan memasang job pembersihan apa pun: menghapus jejak
audit lebih sulit dipulihkan daripada menyimpannya terlalu lama. Kebijakan
retensi tabel onboarding lain ada di migrasi `000016_add_data_retention_policies`.

**Seed hanya untuk dev.** Baris `card_products` di lingkungan dev wajib diberi
komentar bahwa fee dan limitnya placeholder, dan tidak boleh ikut ke staging
sebelum angka resmi turun.

`UNIQUE INDEX` parsial memastikan satu produk hanya punya satu kartu default per wilayah —
penjaga terhadap kesalahan konfigurasi admin yang paling sering terjadi.

---

## 12. Strategi Cache

Mengikuti `03-REDIS-STRATEGY.md`.

| Key | Isi | TTL |
|---|---|---|
| `onb:cards:version` | Versi katalog aktif, mis. `2026-09-22.1` | tanpa TTL |
| `onb:cards:{product_type}:{region}:{version}` | JSON katalog siap kirim | 15 menit |

Aturan:

- **Invalidasi berbasis versi, bukan hapus key.** Setiap tulis admin ke `card_products` atau
  `product_card_options` menaikkan `onb:cards:version` (format `YYYY-MM-DD.n`). Key lama
  kedaluwarsa sendiri. Tidak ada jendela kosong yang memukul database.
- `ETag` respons = nilai `catalog_version`. Client menyimpannya dan mengirim `If-None-Match`.
- Cache miss → baca PostgreSQL → tulis Redis → balas. Redis mati bukan alasan gagal: fallback
  langsung ke database, catat sebagai warning.
- Katalog **tidak boleh** dicache per-session. Tidak ada PII di dalamnya; per-session hanya
  memperbanyak key tanpa manfaat.

---

## 13. Konfigurasi & Kendali Operasional

Sumber kebenaran runtime adalah **PostgreSQL**; YAML hanya untuk seed di lingkungan dev.

| Kendali | Cara | Efek |
|---|---|---|
| Ubah biaya admin / limit | `UPDATE card_products` lewat admin API | Naikkan `catalog_version` |
| Kartu habis stok di wilayah | `availability_status = 'OUT_OF_STOCK'` + `region_code` | Client menampilkan kartu tapi tidak bisa dipilih |
| Tarik kartu dari penawaran | `is_active = FALSE` | Kartu hilang dari katalog |
| Ganti kartu default | `is_default` di `product_card_options` | Pilihan awal di layar berubah |
| Matikan sisipan ini sementara | Feature flag `onboarding.card_selection.enabled = false` | Katalog balas `CARD_CATALOG_EMPTY`; sesi dibuat tanpa `card_type` dan langsung ke `OCR` |

Feature flag terakhir itu jalan keluar bila sisipan bermasalah di produksi: flow lama kembali
utuh tanpa rollback APK.

Semua perubahan konfigurasi wajib masuk audit trail (`08-Audit` di skill backend): siapa,
kapan, nilai lama, nilai baru.

---

## 14. Rate Limiting

| Endpoint | Limit |
|---|---|
| `GET /onboarding/products/{type}/cards` | 60/jam per device (respons cacheable, ini hanya penjaga) |
| `PUT /onboarding/sessions/{id}/card` | 10/session |

---

## 15. Kompatibilitas

- Client lama tidak mengirim `card_type` → sesi dibuat dengan `current_step: CARD_SELECTION`.
  Supaya client lama tidak tersangkut di step yang tidak dikenalnya, server memakai
  `User-Agent`/`X-App-Version` untuk mem-fallback ke `OCR` dan memakai kartu default produk.
  Tulis fallback ini eksplisit di kode, jangan andalkan perilaku kebetulan.
- Field `card` pada `GET sessions` dan `POST submit` bersifat tambahan dan nullable.
- `CARD_SELECTION` adalah nilai enum baru pada `current_step`. Client yang belum mengenalnya
  memetakan step tidak dikenal ke `null` (`OnboardingStep.fromWire`), jadi tidak crash —
  tapi tetap butuh fallback di atas supaya tidak berhenti diam.

---

## 16. Checklist Selesai

- [x] `GET .../cards` balas kartu terurut untuk ketiga `product_type`. Lineup Xpresi
      (2 kartu) dan TabunganKu (1 kartu) di seed adalah **tebakan dev**, bukan penawaran
      resmi — §17 butir 1. Produk yang katalognya kosong tetap aman: sesinya berangkat
      dari `OCR`, bukan berhenti di `CARD_SELECTION` tanpa kartu untuk dipilih.
- [x] `ETag` + `304` bekerja; `Cache-Control` terpasang
- [x] Payload tidak memuat satu pun hex warna atau string nominal terformat
- [x] `default_card_type` selalu menunjuk kartu `AVAILABLE`
- [x] `POST sessions` dengan `card_type` → `current_step: OCR`
- [x] `POST sessions` tanpa `card_type` → `current_step: CARD_SELECTION`
- [x] `PUT .../card` menolak setelah submit dengan `CARD_LOCKED`
- [x] `POST submit` menolak sesi tanpa kartu dengan `details.missing_step: "CARD_SELECTION"`
      (hanya saat sisipan menyala — sesi lama tanpa `card_selected` tidak ikut tertolak)
- [x] Setiap pilihan/perubahan kartu tercatat di `onboarding_card_selection_log`
- [x] Menaikkan `catalog_version` benar-benar menyegarkan respons
- [x] Feature flag mati → flow lama jalan utuh
- [x] `POST submit` membalas objek `card` (§10): `masked_number`, `status`, dan
      `delivery` dengan estimasi tanggal dihitung dari `delivery_days_min/max` katalog
- [x] Penerbitan kartu gagal → rekening tetap `ACTIVE`, `card.status = REQUESTED`,
      kegagalan masuk antrean `onboarding_card_issuance` dengan `next_retry_at`
- [x] Submit ulang tidak menghasilkan dua permintaan cetak — dijaga `Idempotency-Key`
      di Redis DAN `UNIQUE(session_id)` di antrean, yang tidak bisa hilang karena eviction
- [x] Setiap `card_type` punya pemetaan kode core banking, diverifikasi saat startup
      (proses menolak start bila ada yang bolong, hanya ketika flag menyala)
- [x] Admin API katalog (§3) beserta jejak `card_catalog_audit_log`: satu kenaikan
      versi per transaksi, nilai lama + baru tersimpan
- [x] Metrik, alert rasio `CARD_TYPE_UNAVAILABLE`, dan kedua query audit (§10b)
- [ ] Tidak ada satu pun angka placeholder §4 yang tersemai di staging/produksi
      — **masih terbuka, dan ini satu-satunya yang menahan rilis.** Seluruh angka
      fee/limit dan kode core banking yang ada sekarang adalah placeholder dev
      (`11111`, `CB-DEV-BLUE`, …). Lihat §17.

---

## 17. Keputusan yang Belum Diambil

Lima hal di bawah **belum punya jawaban** dan tidak boleh ditebak. Sampai terisi,
implementasi boleh jalan dengan placeholder **di lingkungan dev saja**.

**Status:** seluruh kode yang bergantung pada kelima butir ini SUDAH ditulis dan diuji,
memakai placeholder dev yang digerbangi `APP_ENV` dan feature flag. Yang tersisa adalah
mengganti nilainya — bukan menulis kodenya. Tiap baris di bawah menyebut apa yang perlu
diganti dan di mana.

Skill `buka-rekening-kartu` memakai daftar ini sebagai gerbang: agent tidak mulai
menulis kode sebelum kolom "Jawaban" terisi.

| # | Pertanyaan | Dibutuhkan oleh | Pemilik | Jawaban | Placeholder yang dipakai sekarang |
|---|---|---|---|---|---|
| 1 | Kartu apa yang ditawarkan untuk Xpresi dan TabunganKu | seed, §4 | Product owner | _belum_ | Xpresi: Blue+Gold; TabunganKu: Blue — `scripts/seed/main.go` |
| 2 | Biaya administrasi, penerbitan, dan penggantian resmi per kartu | §4, §11, seed | Product owner / tarif resmi | _belum_ | `11111`/`22222`/`33333` — `scripts/seed/main.go` |
| 3 | Keempat limit resmi per kartu (`limit_cash_withdrawal`, `limit_transfer_bca`, `limit_transfer_interbank`, `limit_debit_purchase`) | §4, §11, seed | Product owner | _belum_ | angka berulang senada — `scripts/seed/main.go` |
| 7 | Siapa yang boleh menulis katalog, dan otorisasi apa di atas `X-Internal-API-Key` | §3, §13 | Engineering manager | _belum_ | `X-Internal-API-Key` + header `X-Admin-Actor` yang **hanya dicatat**, tidak diverifikasi — `CardAdminHandler.adminActor` |
| 8 | Kode kartu yang dipakai core banking untuk permintaan cetak | §10 | Tim core banking | _belum_ | `CB-DEV-BLUE`/`-GOLD`/`-PLAT` — `ONBOARDING_CARD_CORE_BANKING_CODES` di `.env.example` |
| 9 | Lama penyimpanan `onboarding_card_selection_log` | §11 | Compliance | _belum_ | tanpa batas; tidak ada job pembersihan yang dipasang, sesuai komentar tabelnya |

Penomoran mengikuti daftar sembilan informasi wajib di skill; butir 1, 4, 5, dan 6
sudah terjawab oleh dokumen ini:

| # | Pertanyaan | Terjawab di |
|---|---|---|
| 1 | Kartu apa saja per produk tabungan | §4 — ketiga `product_type` terdaftar |
| 4 | Aturan eligibility (umur, setoran awal) | §4 — `min_age`, `min_initial_deposit` |
| 5 | Stok kartu dipantau per wilayah | §4 & §11 — `region_code` |
| 6 | SLA pengiriman dan ambil di cabang | §4 & §11 — `delivery_days_min/max`, `branch_pickup_available` |

### Kenapa butir 2 dan 3 paling berbahaya

Angka di `strings.xml` client dibuat supaya layar terlihat benar, bukan supaya
tagihan benar. Menyalinnya ke database produksi menghasilkan sistem yang
**konsisten dengan mockup dan salah terhadap nasabah** — dan karena
`monthly_admin_fee_shown` ikut tercatat di audit log, angka yang salah itu
menjadi bukti tertulis bahwa nasabah diberi tahu tarif yang keliru.

