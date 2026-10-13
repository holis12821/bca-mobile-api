# Prompt implementasi bertahap — katalog produk buka rekening

Enam fase. Tiap fase berhenti di keadaan yang bisa dijalankan dan diuji —
jangan menggabung dua fase supaya "sekalian". `make check` hijau adalah syarat
pindah fase, bukan syarat akhir.

Baca `SKILL.md` lebih dulu. Angka setoran awal dan teks fitur **tidak boleh**
masuk database sebelum butir 1 dan 2 tabel "Informasi yang wajib dikumpulkan"
terjawab; selama belum, pakai nilai dari `strings.xml` **dengan komentar migrasi
yang menyatakan itu data desain, bukan tarif resmi BCA**.

---

## Fase 1 — Skema

Buat pasangan migrasi `0000NN_onboarding_products.{up,down}.sql`. Nomor = migrasi
terakhir + 1; cek `ls migrations/` dulu, jangan menebak.

Isi:

1. Empat tabel sesuai bagian "Skema database" di `SKILL.md`.
2. Dua kolom nullable di `onboarding_sessions`: `product_catalog_version TEXT`,
   `min_initial_deposit_shown BIGINT`.
3. Unique index berekspresi untuk `is_popular` dan `is_default` (maksimum satu).
4. CHECK constraint: status selain `AVAILABLE` wajib punya `availability_reason_key`.
5. Baris isi untuk tiga produk + fitur-fiturnya + satu baris
   `onboarding_product_page` + satu baris versi katalog. Semuanya `ON CONFLICT
   ... DO UPDATE`, dan `document_id`/FK dicari lewat kunci alami — jangan menulis
   `1` untuk nilai BIGSERIAL, itu pecah begitu ada yang menjalankan `down` lalu
   `up` lagi.
6. `COMMENT ON TABLE` yang menyebut: angka setoran awal **bukan** tarif resmi
   (selama butir 1 belum terjawab), dan baris produk tidak pernah dihapus.

Tulis **kenapa** di komentar, bukan apa. Setiap keputusan yang berbeda dari cara
paling jelas harus menyebut alasannya di tempatnya.

**Definition of Done**
- `make migrate-up` lalu `make migrate-status` bersih.
- `migrate down 1` lalu `up` lagi berhasil — `down` benar-benar diuji, bukan dibaca.
- `INSERT` kedua baris `is_popular = TRUE` ditolak database.
- `INSERT` dengan `availability_status = 'DISABLED'` tanpa reason ditolak database.
- `psql` memperlihatkan tiga produk, fiturnya berurut, dan satu baris page.

---

## Fase 2 — Domain: tipe + interface

Di `internal/domain/onboarding/entity.go`, tambahkan tipe response beserta tag
JSON sesuai `SKILL.md`: `ProductCatalog`, `ProductOption`, `ProductPage`,
`ProductNotice`, `ProductConsent`. Ikuti gaya `TNCDocument` — komentar yang
menjelaskan untuk apa field itu ada, bukan mengulang namanya.

Di `repository.go`, tambahkan:

```go
type ProductCatalogRepository interface {
    ActiveCatalog(ctx context.Context) (*ProductCatalog, error)
}

type ProductCatalogCache interface {
    GetCatalog(ctx context.Context) (*ProductCatalog, error)
    SetCatalog(ctx context.Context, c *ProductCatalog) error
}
```

Cache boleh `nil` — teksnya kecil dan jarang berubah; melayaninya langsung dari
Postgres adalah deployment yang benar, hanya lebih lambat. Tulis itu di komentar,
sama seperti `TNCServiceConfig.Cache`.

Tambahkan `apperr.OnboardingCatalogUnavailable` (503,
`ONBOARDING_CATALOG_UNAVAILABLE`, pesan Bahasa Indonesia) di blok 503 bersebelahan
dengan `TNCUnavailable`.

**Definition of Done**
- `go build ./...` dan `go vet ./...` bersih.
- `grep -rn "internal/repository" internal/domain/ internal/handler/ | grep -v _test` tetap kosong.
- Tidak ada tipe baru yang memuat field warna, hex, atau nama drawable.

---

## Fase 3 — Service

`internal/domain/onboarding/product_service.go` — file baru, sejajar
`tnc_service.go`. Service sendiri, bukan bagian `SessionService`, karena isinya
dibaca **sebelum** sesi ada.

Perilaku:

1. Baca cache; miss atau galat → baca repo, lalu isi cache. Galat cache dicatat
   `slog` dan dilanjutkan, tidak pernah dinaikkan ke handler.
2. Katalog kosong (tidak ada produk aktif) → `apperr.OnboardingCatalogUnavailable`.
3. Terapkan `ProductsUnderMaintenance`: produk yang tutup **tetap dikembalikan**
   dengan `availability_status: "DISABLED"` dan
   `availability_reason_key: "MAINTENANCE"`. Jangan menyaringnya keluar.
4. Urutkan menurut `display_order`; produk tutup tidak dipindah ke bawah kecuali
   product owner memintanya.
5. Feature flag mati → `apperr.OnboardingCatalogUnavailable` tanpa menyentuh
   database.

**Definition of Done**
- Unit test dengan mock in-memory di paket yang sama, menutup: katalog normal,
  katalog kosong, cache miss, cache error (harus tetap berhasil lewat database),
  satu produk maintenance, flag mati.
- Maintenance **tidak** menghilangkan produk dari daftar — ada test khusus untuk ini.
- `make test` hijau.

---

## Fase 4 — Handler + rute

`GetProducts` di `internal/handler/onboarding_handler.go` — **bukan** file baru.
`GetTNC` sudah tinggal di sana dan ini endpoint publik onboarding yang sama
jenisnya.

1. `h.productService == nil` → `apperr.OnboardingCatalogUnavailable` (pola `GetTNC`).
2. `X-Device-Id` kosong → `400 VALIDATION_ERROR` + `details.missing_header`.
3. `ETag` dari `catalog_version` yang **benar-benar dilayani**, bukan dari apa pun
   yang datang dari request. `matchesETag` sudah ada — pakai itu.
4. `304` tanpa body sama sekali, termasuk tanpa envelope.
5. `Cache-Control: public, max-age=300`.
6. Galat internal dicatat dengan `request_id`, tanpa PII.

Di `internal/router/router.go`: konstanta `productCatalogRateLimit/Window` +
`productCatalogRateKey` (per `X-Device-Id`, jatuh ke IP bila header kosong),
lalu `r.Get("/products", onboardingH.GetProducts)` di grup sendiri, **di luar**
grup ber-limit-IP endpoint bersesi. Tulis alasannya di komentar — bucket terpisah
supaya tiga layar berurutan tidak saling menghabiskan jatah.

Hati-hati urutan pendaftaran rute: `/products` dan `/products/{product_type}/cards`
hidup di prefix yang sama. Pastikan keduanya benar-benar terpanggil, bukan saling
menelan.

**Definition of Done**
- Test handler: 200 lengkap, 400 tanpa `X-Device-Id`, 304 pada `If-None-Match`
  cocok, 200 pada ETag versi lama, 503 saat katalog kosong.
- Test yang membuktikan `/products` **dan** `/products/TAHAPAN_BCA/cards`
  dua-duanya tetap jalan.
- `curl` dengan `-H 'X-Device-Id: dev-1'` memperlihatkan bentuk response yang
  sama persis dengan contoh di `SKILL.md`.
- `304` dari `curl -i` tidak punya satu byte pun body.

---

## Fase 5 — Jejak di sesi

`SessionService.CreateSession` menyimpan `product_catalog_version` dan
`min_initial_deposit_shown` dari katalog yang sedang berlaku.

Yang **tidak** boleh berubah di fase ini:

- Validasi `product_type` tetap `pt.Valid()` + cek maintenance. Jangan mulai
  menuntut baris `onboarding_products` ada — katalog yang mati tidak boleh
  mematikan pembuatan sesi.
- Request tanpa katalog (APK lama) tetap sah; dua kolom itu tinggal `NULL`.

**Definition of Done**
- Test: sesi baru punya kedua kolom terisi; katalog mati → sesi tetap lahir
  dengan kolom `NULL`, bukan error.
- Test regresi `POST /sessions` yang sudah ada tetap hijau tanpa diubah.

---

## Fase 6 — Admin write ✅ SELESAI

Butir 6 terjawab: **petugas ber-cakupan `CARD_ADMIN`**, lewat `/internal/v1`.
`CARD_ADMIN` dan bukan cakupan kelima karena CHECK `cs_agents_scopes_valid` di migrasi
`000027` mengunci daftar cakupan ke empat nilai, dan mengatur katalog kartu Paspor
adalah pekerjaan administratif yang sama jenisnya.

Yang terbangun:

```
GET /internal/v1/onboarding/products   + petugas CARD_ADMIN
PUT /internal/v1/onboarding/products   + petugas CARD_ADMIN
```

**Definition of Done — semuanya terbukti:**
- ✅ Penulisan dua produk dalam satu transaksi menaikkan versi satu kali —
  `TestWriteProducts_BumpsVersionOncePerTransaction` (database sungguhan) dan
  `TestWriteProducts_TwoProductsBumpVersionOnce` (domain).
- ✅ Rute tidak terjangkau tanpa `X-Internal-API-Key`, dan tidak terjangkau dengan
  kunci sistem saja — `TestAdminProductRoutes_RequireSystemKeyAndAgent`.
- ✅ `make check` hijau.

### Empat keputusan yang dibuat saat mengerjakannya

**1. Penulisan DUA LANGKAH, dan tanpa itu memindahkan badge mustahil.**

Ini bug yang benar-benar terjadi dan hanya terlihat di database sungguhan.
`idx_onboarding_products_one_popular` adalah unique index **berekspresi**, dan unique
index tidak bisa `DEFERRABLE` — hanya unique *constraint* bisa, dan index parsial
berekspresi tidak bisa menjadi constraint. Jadi pelanggarannya terdeteksi pada statement
itu juga, bukan saat commit.

Karena produk ditulis urut `product_type`, `TABUNGANKU` mendapat `is_popular` **sebelum**
`TAHAPAN_BCA` melepasnya → `409` untuk permintaan yang seharusnya sah.

Perbaikannya: langkah pertama menulis semua kolom dengan `is_popular` dan `is_default`
dipaksa `FALSE`; langkah kedua menyalakan yang diminta. Regresinya dijaga
`TestWriteProducts_MovesSingletonFlagInOneRequest` — dan **tidak bisa** dijaga mock,
karena mock tidak punya unique index.

**2. `features` punya TIGA arti, bukan dua.**

`null` = jangan sentuh · `[]` = hapus semua · `[...]` = ganti berurut. Pointer ke slice,
bukan slice biasa, supaya `"features": []` bisa dibedakan dari field yang tidak dikirim.
Tanpa pembedaan itu, setiap penulisan harga akan menghapus teks fitur produknya.

**3. Jalur admin TIDAK membaca `FEATURE_ONBOARDING_PRODUCT_CATALOG`.**

Karena itu `ProductAdminService` adalah service tersendiri, bukan method di
`ProductService`. Katalog yang dimatikan karena isinya salah adalah justru saat isinya
paling perlu diubah; kalau admin menumpang service bernilai `enabled`, mematikan flag
akan sekaligus mematikan kemampuan membetulkannya.

**4. Invalidasi cache WAJIB `DEL`, berbeda dari katalog kartu.**

Kunci katalog kartu **memuat versinya**, jadi versi baru otomatis menghasilkan kunci
baru dan entri lama kedaluwarsa sendiri. Kunci katalog produk
(`onboarding:products:v1:catalog`) **tidak** memuat versi dan ber-TTL 24 jam, jadi
`catalog_version` yang naik tanpa `DEL` akan tetap menyajikan katalog lama sampai sehari
penuh.

`DEL` yang gagal **tidak** menggagalkan penulisan: transaksinya sudah commit, dan error
di titik itu akan membuat pemanggil mengulang permintaan yang sudah berhasil — menaikkan
versi sekali lagi untuk perubahan yang sama. Dicatat `slog.Error` beserta perintah `DEL`
yang perlu dijalankan manual.

### Yang masih terbuka sesudah Fase 6

- **Tidak ada tabel jejak audit** seperti `card_catalog_audit_log`. Aktor
  (`X-Admin-Actor`, jatuh ke NPP petugas) dan IP hanya masuk `slog`. Perubahan setoran
  awal adalah hal yang akan disengketakan, jadi ini pekerjaan yang masih perlu — dan ia
  menuntut migrasi tersendiri.
- **Copy halaman (`onboarding_product_page`) belum bisa ditulis lewat API.** Masih SQL
  + `DEL` manual.
- **Tidak ada `POST` produk baru.** Hanya `UPDATE`: produk baru menuntut nilai enum
  `onboarding_product_type` baru, dan nilai enum baru menuntut migrasi.

---

## Sebelum bilang selesai

```bash
make check
```

Lalu `references/verification.md` — checklist lintas repo dengan tim Android.
Pekerjaan ini belum selesai sebelum client berhenti memilih produk berdasarkan
indeks array.
