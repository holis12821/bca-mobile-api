# Prompt Implementasi — Pilih Jenis Kartu Paspor

Delapan fase berurutan. Setiap prompt punya *Definition of Done*; jangan lanjut
sebelum terpenuhi.

Path file mengikuti lapisan repo ini (lihat bagian "Struktur file yang disentuh"
di `SKILL.md`), bukan lapisan `service/`–`model/` yang dipakai dokumen asal.

## Daftar isi

- [Prompt 1: Migrasi Database & Seed Katalog](#prompt-1-migrasi-database--seed-katalog)
- [Prompt 2: Repository & Service Katalog + Cache](#prompt-2-repository--service-katalog--cache)
- [Prompt 3: Handler GET Katalog](#prompt-3-handler-get-katalog)
- [Prompt 4: card_type pada Create Session & State Machine](#prompt-4-card_type-pada-create-session--state-machine)
- [Prompt 5: PUT Kartu pada Sesi Berjalan](#prompt-5-put-kartu-pada-sesi-berjalan)
- [Prompt 6: Propagasi ke Submit & Core Banking](#prompt-6-propagasi-ke-submit--core-banking)
- [Prompt 7: Admin API & Feature Flag](#prompt-7-admin-api--feature-flag)
- [Prompt 8: Observability & Audit](#prompt-8-observability--audit)

---

## Prompt 1: Migrasi Database & Seed Katalog

```text
Buat migrasi database untuk katalog kartu Paspor sesuai §11 di
docs/08-PILIH-KARTU-API-SPEC.md.

Cek dulu nomor migrasi terakhir di migrations/ lalu pakai nomor berikutnya,
berpasangan .up.sql dan .down.sql.

Yang dibuat:
1. Tabel card_products — identitas kartu, fee, empat limit, delivery, eligibility,
   is_active.
2. Tabel product_card_options — pemetaan produk↔kartu, display_order, is_default,
   is_popular, badge_key, availability_status, availability_reason_key, region_code.
3. Unique index parsial: satu default per (product_type, region_code).
4. ALTER onboarding_sessions: card_type, card_selected_at, card_catalog_version.
5. Tabel onboarding_card_selection_log untuk audit, termasuk kolom
   monthly_admin_fee_shown.
6. Migrasi down yang benar-benar mengembalikan skema.

Seed hanya untuk lingkungan dev: tiga kartu (PASPOR_BLUE, PASPOR_GOLD,
PASPOR_PLATINUM) dipetakan ke TAHAPAN_BCA dengan PASPOR_BLUE sebagai default dan
is_popular. Beri komentar di file seed bahwa angka fee dan limit adalah placeholder
dev dan wajib diganti angka resmi sebelum staging.

JANGAN menanam angka fee/limit produksi — angka di contoh payload §4 adalah
placeholder desain dari strings.xml client, lihat §17 untuk statusnya.
JANGAN menyimpan warna atau path gambar kartu di tabel mana pun.
```

**Definition of Done**: migrasi up/down jalan bersih dua kali berturut-turut
(`make migrate-up`, `make migrate-down`, `make migrate-up`); unique index menolak
dua default pada produk yang sama; seed hanya aktif di dev.

---

## Prompt 2: Repository & Service Katalog + Cache

```text
Buat internal/repository/postgres/card_repo.go,
internal/repository/redis/card_cache.go, dan tipe + service katalog di
internal/domain/onboarding/ (tipe di entity.go, interface di repository.go,
logika di card_service.go).

Interface repository didefinisikan di domain, implementasinya di repository —
domain tidak boleh mengimpor paket repository.

Repository:
- ListCards(ctx, productType, regionCode) ([]CardOption, error) — join card_products
  dengan product_card_options, filter is_active, urut display_order. Baris dengan
  region_code cocok menang atas baris nasional (region_code NULL).
- GetCard(ctx, productType, cardType) (CardOption, error)
- BumpCatalogVersion(ctx) (string, error) — format YYYY-MM-DD.n

Service:
- GetCatalog(ctx, productType, regionCode) (Catalog, error) dengan urutan:
  baca onb:cards:version → coba key onb:cards:{product}:{region}:{version} →
  cache miss: baca DB, susun payload, tulis Redis TTL 15 menit.
- Redis error bukan kegagalan request: fallback ke DB, log warning, tetap balas 200.
- default_card_type diambil dari is_default; bila kartu itu tidak AVAILABLE,
  turunkan ke kartu AVAILABLE pertama. Jangan serahkan keputusan ini ke client.
- Katalog kosong → error domain CardCatalogEmpty.

Tulis unit test untuk: cache hit, cache miss, Redis down, default tidak tersedia,
override per wilayah, dan katalog kosong. Pakai mock in-memory di paket test yang
sama, mengikuti pola test domain yang sudah ada.
```

**Definition of Done**: enam test di atas hijau; Redis dimatikan, endpoint tetap
melayani.

---

## Prompt 3: Handler GET Katalog

```text
Buat internal/handler/card_handler.go untuk
GET /v1/onboarding/products/{product_type}/cards, dan daftarkan rutenya di
internal/router/router.go bersama rute /v1/onboarding lain.

Ketentuan:
- Tanpa Authorization, tanpa session_id. Wajib header X-Device-Id.
- Query opsional region_code.
- Validasi product_type terhadap enum; di luar enum → 404 ONBOARDING_PRODUCT_UNKNOWN.
- Produk maintenance → 422 ONBOARDING_PRODUCT_UNAVAILABLE.
- Katalog kosong → 404 CARD_CATALOG_EMPTY.
- Rate limit 60/jam per device → 429 RATE_LIMIT_EXCEEDED dengan
  details.retry_after_seconds dan header Retry-After. Pakai middleware.RateLimit
  yang sudah ada, dengan keyFunc berbasis X-Device-Id.
- Set ETag: "<catalog_version>" dan Cache-Control: public, max-age=900.
  If-None-Match cocok → 304 tanpa body.
- Bentuk payload persis §4 docs/08-PILIH-KARTU-API-SPEC.md.
- Feature flag onboarding.card_selection.enabled = false → balas CARD_CATALOG_EMPTY.

Tambahkan integration test yang membandingkan JSON hasil dengan contoh di §4,
dan satu test yang gagal bila payload memuat pola hex warna (#RRGGBB) atau string
nominal berformat "Rp".
```

**Definition of Done**: test perbandingan payload hijau; test penjaga hex/"Rp"
benar-benar gagal saat sengaja disisipkan.

---

## Prompt 4: card_type pada Create Session & State Machine

```text
Ubah internal/handler/onboarding_handler.go dan
internal/domain/onboarding/session_service.go.

1. CreateSessionRequest menerima card_type dan card_catalog_version, keduanya opsional.
2. Sisipkan CARD_SELECTION ke state machine tepat setelah TNC. Step lain tidak bergeser.
3. Perilaku sesuai §7 docs/08-PILIH-KARTU-API-SPEC.md:
   - card_type valid & AVAILABLE → simpan, card_selected=true, current_step=OCR
   - card_type kosong → current_step=CARD_SELECTION
   - card_type tidak dikenal → 422 CARD_TYPE_INVALID
   - card_type tidak tersedia → 409 CARD_TYPE_UNAVAILABLE
4. Validasi card_type terhadap katalog produk sesi, bukan terhadap daftar global.
5. card_catalog_version berbeda dari versi terkini → sesi tetap dibuat,
   sertakan meta.catalog_outdated=true, catat di audit.
6. Fallback client lama: bila X-App-Version di bawah ambang yang dikonfigurasi dan
   card_type kosong, pakai kartu default produk dan lanjut ke OCR. Tulis eksplisit,
   beri komentar kenapa.
7. Respons memuat objek card seperti contoh spesifikasi.
8. Tulis onboarding_card_selection_log saat kartu tersimpan, termasuk
   monthly_admin_fee_shown dari katalog versi yang dikirim client.

Perbarui test state machine yang ada supaya urutan baru terbaca, tanpa mengubah
ekspektasi step setelah OCR.
```

**Definition of Done**: keempat cabang §7 punya test; test flow lama (tanpa
`card_type`, client lama) tetap sampai `COMPLETED`.

---

## Prompt 5: PUT Kartu pada Sesi Berjalan

```text
Tambahkan handler PUT /v1/onboarding/sessions/{session_id}/card di
internal/handler/card_handler.go, logikanya di
internal/domain/onboarding/card_service.go, sesuai §8 spesifikasi.

Ketentuan:
- Boleh dipanggil selama steps_completed.submitted == false; setelah itu 409 CARD_LOCKED.
- Validasi kartu terhadap katalog produk sesi: CARD_TYPE_INVALID,
  CARD_TYPE_UNAVAILABLE, CARD_NOT_ELIGIBLE (sertakan details.reason_key).
- Sesi tidak ada → 404 ONBOARDING_NOT_FOUND; kedaluwarsa → 422
  ONBOARDING_SESSION_EXPIRED.
- Respons mengembalikan current_step sesi yang sebenarnya, bukan selalu OCR.
  Bila sesi berada di CARD_SELECTION, majukan ke OCR.
- Setiap perubahan menulis onboarding_card_selection_log dengan from_card_type dan
  to_card_type.
- Rate limit 10 per session.
- Perubahan kartu tidak boleh menyentuh data step lain (OCR, biometrik, kredensial).

Test: ubah kartu di CARD_SELECTION, ubah dari REVIEW (current_step tetap REVIEW),
tolak setelah submit, tolak kartu milik produk lain, dan pastikan panggilan
berturut-turut idempoten untuk card_type yang sama.
```

**Definition of Done**: kelima test di atas hijau; tidak ada jalur kode yang
mengubah kartu setelah submit.

---

## Prompt 6: Propagasi ke Submit & Core Banking

```text
Ubah internal/domain/onboarding/submit_service.go dan
internal/domain/onboarding/core_banking.go.

1. Tolak submit bila steps_completed.card_selected == false:
   422 ONBOARDING_INCOMPLETE dengan details.missing_step = "CARD_SELECTION".
2. Saat membuat rekening, kirim permintaan penerbitan kartu ke core banking
   memakai pemetaan card_type → kode kartu core banking. Bentuk konfigurasinya
   ada di §10 docs/08-PILIH-KARTU-API-SPEC.md; nilai kodenya belum dikonfirmasi
   (§17). Tolak saat startup bila ada card_type tanpa pemetaan.
3. Respons submit memuat objek card sesuai §10: masked_number, status
   (REQUESTED | PRINTING | SHIPPED), dan delivery dengan estimasi tanggal dihitung
   dari delivery_days_min/max katalog.
4. Penerbitan kartu gagal sementara core banking rekeningnya sudah jadi → rekening
   tetap ACTIVE, card.status = REQUESTED, dan kegagalan masuk antrean retry.
   Jangan menggagalkan seluruh submit karena kartu.
5. Hormati X-Idempotency-Key yang sudah ada: submit ulang tidak boleh menghasilkan
   dua permintaan cetak kartu.

Ingat: submit menolak dengan PROVIDER_NOT_CONFIGURED bila provisioner tidak
tersedia. Penerbitan kartu mengikuti gerbang APP_ENV yang sama — mock core
banking hanya untuk development.

Test: submit tanpa kartu ditolak, submit normal menghasilkan tepat satu permintaan
cetak, submit ulang dengan idempotency key sama tidak menambah permintaan cetak,
dan kegagalan penerbitan kartu tidak membatalkan rekening.
```

**Definition of Done**: keempat test hijau; retry idempoten terbukti lewat test,
bukan asumsi.

---

## Prompt 7: Admin API & Feature Flag

```text
Buat endpoint admin internal untuk mengelola katalog, dilindungi
middleware.InternalAPIKey (header X-Internal-API-Key) yang sudah dipakai endpoint
CS onboarding, plus peran admin:

- GET    /internal/v1/cards
- PUT    /internal/v1/cards/{card_type}              (fee, limit, delivery, is_active)
- PUT    /internal/v1/products/{product_type}/cards/{card_type}
         (display_order, is_default, is_popular, badge_key, availability_status,
          availability_reason_key, region_code)

Ketentuan:
- Setiap write menaikkan catalog_version tepat satu kali per transaksi, bukan per baris.
- Setiap write menulis audit: aktor, waktu, nilai lama, nilai baru.
- Menonaktifkan kartu yang sedang menjadi default ditolak kecuali default baru
  disertakan dalam permintaan yang sama.
- badge_key dan style divalidasi terhadap daftar nilai yang dikenal client.
  Nilai di luar daftar ditolak 422 dengan pesan yang menyebutkan nilai yang sah.
- Feature flag onboarding.card_selection.enabled dibaca dari konfigurasi runtime,
  bukan environment variable yang butuh restart.

Test: menaikkan versi menyegarkan respons katalog publik; menonaktifkan default
ditolak; style/badge tidak dikenal ditolak; mematikan flag mengembalikan flow lama.
```

**Definition of Done**: mematikan flag membuat seluruh test flow lama hijau tanpa
perubahan kode lain.

---

## Prompt 8: Observability & Audit

```text
Lengkapi instrumentasi untuk sisipan ini.

Metrik:
- onboarding_card_catalog_requests_total{product_type,cache_result}
- onboarding_card_selected_total{product_type,card_type}
- onboarding_card_changed_total{from,to}
- onboarding_card_unavailable_total{card_type,reason_key}
- Histogram latensi GET katalog, dipisah cache hit dan miss.

Log terstruktur lewat log/slog (tanpa PII): session_id, product_type, card_type,
catalog_version, request_id.

Audit query yang harus bisa dijawab dalam satu SELECT:
- "Kartu apa yang dipilih sesi X, dan biaya administrasi berapa yang ditampilkan
  saat itu?"
- "Berapa kali nasabah mengganti kartu sebelum submit, bulan ini?"

Tambahkan alert: rasio CARD_TYPE_UNAVAILABLE di atas 5% dari pemilihan dalam 15
menit — pertanda konfigurasi stok salah, bukan perilaku nasabah.
```

**Definition of Done**: kedua pertanyaan audit terjawab lewat satu query; alert
diuji dengan data sintetis.
