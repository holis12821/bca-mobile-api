---
name: buka-rekening-kartu
description: >-
  Katalog dan pemilihan jenis kartu Paspor BCA pada flow buka rekening — endpoint
  GET /v1/onboarding/products/{type}/cards, step CARD_SELECTION, field card_type
  pada create session, PUT /sessions/{id}/card, tabel card_products +
  product_card_options, cache Redis berbasis catalog_version, feature flag, dan
  audit trail pemilihan kartu. Gunakan saat membuat atau memodifikasi katalog
  kartu, biaya/limit kartu debit, stok kartu per wilayah, atau propagasi pilihan
  kartu ke submit dan core banking. Trigger juga pada "katalog kartu", "kartu
  Paspor", "card_type", "CARD_SELECTION", "stok kartu per wilayah", dan "biaya
  administrasi kartu". JANGAN gunakan untuk endpoint OCR/biometrik/video call
  (itu skill `buka-rekening-backend`), endpoint auth/login/PIN (itu skill
  `bca-mobile-backend`), atau UI Android (project terpisah).
---

# Pilih Jenis Kartu Paspor — Buka Rekening

Kontrak API yang mengikat: `docs/08-PILIH-KARTU-API-SPEC.md`. Setiap bentuk
payload, kode error, dan nama field mengikuti dokumen itu; skill ini mengatur
**cara** mengerjakannya.

## Batas wilayah

**Trigger**: endpoint `/v1/onboarding/products/*/cards`, field `card_type` di mana
pun, tabel `card_products`/`product_card_options`, step `CARD_SELECTION`, stok
kartu, biaya administrasi kartu, estimasi pengiriman kartu fisik.

**Jangan trigger** untuk: OCR/Dukcapil, biometrik, video call, kredensial, dan
pembuatan rekening itu sendiri — semua ada di skill `buka-rekening-backend`.
Endpoint auth/PIN ada di `bca-mobile-backend`. UI/Compose Android project terpisah.

> **Catatan lintas repo:** project Android menyebut skill backend onboarding
> `buka-rekening-api` atau `buka-rekening-backend`, sementara
> `07-BUKA-REKENING-BACKEND-SKILL-PROMPTS.md` menyebutnya
> `buka-rekening-onboarding`. Di repo ini nama yang berlaku adalah
> **`buka-rekening-backend`** — itu yang dipakai di seluruh dokumen ini.

**Prompt implementasi bertahap**: `references/prompts.md` — delapan fase,
masing-masing dengan *Definition of Done*.

**Checklist verifikasi dengan tim Android**: `references/verification.md` —
dikerjakan sebelum menutup pekerjaan.

---

## Informasi yang wajib dikumpulkan lebih dulu

**Jangan mulai menulis kode** sebelum sembilan hal ini punya jawaban tertulis.
Kalau salah satu belum ada, tanyakan; jangan diisi tebakan yang kelihatan masuk akal.

Status terkininya dipelihara di **`docs/08-PILIH-KARTU-API-SPEC.md` §17**. Butir
1, 4, 5, dan 6 sudah terjawab di spec; butir **2, 3, 7, 8, 9 masih kosong**.
Cek §17 dulu sebelum bertanya — jangan menanyakan ulang yang sudah terjawab.

| # | Yang harus diketahui | Kenapa penting | Sumber |
| --- | --- | --- | --- |
| 1 | Daftar kartu per produk tabungan — apakah `TAHAPAN_XPRESI` dan `TABUNGANKU` menawarkan tiga kartu yang sama seperti `TAHAPAN_BCA`? | Menentukan isi `product_card_options`; salah di sini membuat nasabah ditawari kartu yang tidak bisa diterbitkan | Product owner |
| 2 | Angka resmi biaya administrasi, biaya penerbitan, dan biaya penggantian per kartu | Nilai di `strings.xml` client (Rp14.000/16.000/19.000) adalah **data desain**, bukan angka resmi produk | Product owner / tarif resmi |
| 3 | Angka resmi keempat limit per kartu | Sama seperti di atas; limit salah berujung sengketa | Product owner |
| 4 | Aturan eligibility — umur minimum, setoran awal minimum, apakah Platinum butuh syarat tambahan | Menentukan `NOT_ELIGIBLE` dan kapan dicek (sebelum OCR, data umur belum ada) | Risk / compliance |
| 5 | Apakah stok kartu dipantau per wilayah, dan dari sistem mana angkanya | Menentukan perlu-tidaknya `region_code` dan integrasi inventaris | Operasional kartu |
| 6 | SLA pengiriman kartu fisik dan apakah ambil di cabang tersedia untuk semua kartu | Mengisi objek `delivery` | Operasional kartu |
| 7 | Siapa yang boleh mengubah katalog dan lewat antarmuka apa | Menentukan admin endpoint + otorisasi | Engineering manager |
| 8 | Format `card_type` yang dipakai core banking saat permintaan cetak kartu | Kalau berbeda dengan enum kita, butuh tabel pemetaan | Tim core banking |
| 9 | Berapa lama jejak audit pemilihan kartu harus disimpan | Menentukan retensi `onboarding_card_selection_log` | Compliance |

Jawaban 2 dan 3 yang paling sering dilewati, dan paling berbahaya. **Jangan**
menyalin angka dari `strings.xml` client ke database produksi tanpa konfirmasi —
angka itu dibuat untuk desain layar.

Perangkapnya nyata: contoh payload di `docs/08` §4 memuat `14000`, `16000`, dan
`19000` — persis angka `strings.xml` itu. Spec sudah menandainya sebagai
placeholder (banner di kepala dokumen dan peringatan di §4), tapi angka itu tetap
terlihat seperti data sungguhan bagi yang membaca cepat. **Yang mengikat dari §4
adalah nama field dan bentuk objek, bukan nominalnya.**

Konteks teknis yang sudah pasti dan tidak perlu ditanyakan:

- Sesi onboarding dibuat **setelah** kartu dipilih (client membuatnya di layar
  S&K), jadi endpoint katalog tanpa session.
- Envelope, error shape, dan `meta.request_id` mengikuti `docs/01-API-SPECIFICATION.md`.
- Client memetakan `style` ke token `CardArt`; hanya `BLUE`, `GOLD`, `PLATINUM`
  yang dikenal.

---

## Aturan wajib

1. **Payload tidak memuat nilai visual.** Tidak ada hex warna, gradient, atau URL
   gambar kartu. Hanya enum `style`. Client memetakannya ke design token;
   mengirim hex membuat client melanggar aturan token dan perubahan akan ditolak
   di review.
2. **Nominal integer, bukan string terformat.** `14000`, bukan `"Rp14.000"`.
3. **Label statis dikirim sebagai key** (`badge_key`, `reason_key`), bukan kalimat
   Indonesia.
4. **Katalog tidak pernah dibaca langsung dari database di jalur panas.** Selalu
   lewat cache berbasis `catalog_version`; database hanya untuk cache miss dan
   admin write.
5. **Setiap pemilihan dan perubahan kartu ditulis ke audit log** beserta biaya yang
   ditampilkan saat itu. Ini syarat kepatuhan, bukan tambahan opsional.
6. **Kartu terkunci setelah submit.** Tidak ada jalur kode yang boleh mengubah
   `card_type` sesi yang sudah `submitted`.
7. **Field baru selalu nullable dengan default aman.** Client lama harus tetap jalan.
8. **Sisipan ini harus bisa dimatikan lewat feature flag** tanpa rollback deployment.
9. **Jangan mengubah urutan step yang sudah ada.** `CARD_SELECTION` disisipkan
   setelah `TNC`; step lain tidak bergeser namanya maupun artinya.

---

## Struktur file yang disentuh

Mengikuti lapisan repo ini — lihat `CLAUDE.md` untuk aturan arah impor. Domain
tidak boleh tahu SQL/Redis; handler tidak boleh mengimpor `repository/`.

```text
internal/
├── handler/
│   ├── card_handler.go             ← BARU: GET katalog + PUT kartu pada sesi
│   └── onboarding_handler.go       ← UBAH: terima card_type, balas objek card
├── domain/onboarding/
│   ├── entity.go                   ← UBAH: tipe CardOption/Catalog, step CARD_SELECTION
│   ├── repository.go               ← UBAH: interface CardRepository + CardCache
│   ├── card_service.go             ← BARU: katalog, cache, validasi pilihan
│   ├── session_service.go          ← UBAH: card_type saat create session
│   └── submit_service.go           ← UBAH: wajibkan card_selected, propagasi ke core banking
├── repository/postgres/
│   └── card_repo.go                ← BARU: implementasi CardRepository
├── repository/redis/
│   └── card_cache.go               ← BARU: cache katalog berbasis catalog_version
└── router/router.go                ← UBAH: daftarkan rute publik + admin

migrations/
├── 000019_card_products.up.sql     ← BARU (nomor menyesuaikan migrasi terakhir)
└── 000019_card_products.down.sql   ← BARU
```

Catatan penempatan yang berbeda dari dokumen asal:

- Repo ini **tidak** punya lapisan `service/` maupun `model/` terpisah. Logika
  bisnis dan tipe domain tinggal bersama di `internal/domain/<konteks>/`.
- Interface repository didefinisikan di `domain/onboarding/repository.go`,
  implementasinya di `repository/postgres` dan `repository/redis`.
- Semua rute didaftarkan di satu tempat: `internal/router/router.go`.
- Migrasi selalu berpasangan `.up.sql` + `.down.sql`, nomor berurutan.
  Cek migrasi terakhir dulu (`make migrate-status` atau `ls migrations/`)
  sebelum memilih nomor.
- Mock integrasi eksternal wajib bergerbang `APP_ENV` lewat
  `onboarding.ProvidersFor(devMode)` — jangan memasang mock tanpa gerbang.
