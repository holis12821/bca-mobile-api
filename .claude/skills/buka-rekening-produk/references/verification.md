# Checklist verifikasi lintas repo — katalog produk buka rekening

Dikerjakan **sebelum** pekerjaan dinyatakan selesai. Backend yang benar tapi
client yang masih memilih produk berdasarkan posisi array adalah pekerjaan yang
belum selesai — dan kegagalannya tidak memunculkan error di mana pun.

Repo client: `BcaMobile` (Android, Kotlin/Compose).
Layar: `ui/screen/buka_rekening/pilih_jenis/`.

---

## A. Yang WAJIB berubah di sisi Android

Tanpa ini, menyalakan katalog justru lebih berbahaya daripada membiarkan
`strings.xml`.

- [ ] **`PilihJenisEvent.ProductSelected(index)` berhenti menjadi sumber kebenaran.**
      Hari ini `BukaRekeningSyaratKetentuanViewModel` dan
      `BukaRekeningPilihKartuViewModel` memanggil `ProductType.fromIndex(index)`
      (`domain/onboarding/model/OnboardingModels.kt`), yang mengandaikan urutan
      kartu di layar sama dengan urutan deklarasi enum Kotlin. Server mengurutkan
      lewat `display_order` dan bisa menyembunyikan produk, jadi andaian itu
      patah tanpa suara: nasabah membuka rekening yang bukan pilihannya.
      Yang disimpan di `BukaRekeningFlowState` harus `product_type`, bukan indeks.
- [ ] **`ProductType.fromIndex` dihapus** setelah pemanggil terakhirnya hilang.
      Dibiarkan hidup, fungsi itu akan dipakai lagi oleh layar berikutnya.
- [ ] **`POPULAR_PRODUCT_INDEX` di `BukaRekeningRingkasanUiState` diganti**
      `is_popular` dari katalog. Badge "Paling Populer" milik data, bukan posisi.
- [ ] **`defaultJenisRekeningList()` jadi fallback, bukan sumber utama.**
      Entri `strings.xml` **tidak dihapus** (aturan string additive-only di
      `CLAUDE.md` repo Android) — dipakai saat katalog menjawab
      `503 ONBOARDING_CATALOG_UNAVAILABLE` atau perangkat offline.
- [ ] **`icon_key` dan `style` dipetakan ke drawable + design token**, bukan ke
      `Color(0xFF…)`. Nilai enum yang tidak dikenal → ikon bawaan, kartu tetap
      terbaca, tidak crash.
- [ ] **`min_initial_deposit` diformat di client** (`500000` → "Rp 500.000").
      Server tidak pernah mengirim string terformat.
- [ ] **`availability_status != "AVAILABLE"` membuat kartu tidak bisa dipilih**
      dan menampilkan alasan dari `availability_reason_key`. Produk tutup tetap
      tampil — yang hilang hanya kemampuan memilihnya.
- [ ] `./scripts/check-hardcoded-ui.sh` di repo Android tetap 0 pelanggaran.

## B. Kesepakatan kontrak yang harus dikonfirmasi dua arah

- [ ] Daftar nilai `icon_key`, `style`, `badge_key`, `availability_status`, dan
      `availability_reason_key` — sama persis dengan tabel enum di `SKILL.md`.
      Nilai baru butuh kesepakatan lebih dulu; client lama harus mengabaikannya
      dengan aman.
- [ ] `page.consent` tetap tiga potong (`prefix`/`link`/`suffix`). Client mencetak
      bagian tengah tebal + berwarna; menggabungkannya memaksa pencarian substring
      yang pecah pada setiap perbaikan kata.
- [ ] `catalog_version` ikut dikirim kembali? **Tidak.** `POST /sessions` tidak
      menerima `product_catalog_version` dari client — server mengisinya sendiri
      dari katalog yang sedang berlaku. Beda dari `accepted_tnc_version`, yang
      memang harus datang dari client karena ia bukti persetujuan.
- [ ] Client menghormati `Cache-Control: public, max-age=300` dan mengirim
      `If-None-Match`. Menahan katalog lebih lama hanya menampilkan setoran awal
      yang keliru.
- [ ] Nilai `product_type` di response sama persis dengan `ProductType.wireValue`
      di client: `TAHAPAN_BCA`, `TAHAPAN_XPRESI`, `TABUNGANKU`.

## C. Uji bersama di perangkat

Jalankan dengan `make tunnel` + flavor `ngrok` di sisi Android.

- [ ] Layar memuat tiga produk dari server; matikan Wi-Fi → fallback
      `strings.xml` tampil, bukan layar error.
- [ ] `ONBOARDING_PRODUCTS_MAINTENANCE=TABUNGANKU` → kartu TabunganKu tampil
      redup dengan alasan, dua produk lain tetap bisa dipilih.
- [ ] `FEATURE_ONBOARDING_PRODUCT_CATALOG=false` → layar tetap berfungsi penuh
      lewat fallback, dan `POST /sessions` **tetap berhasil**.
- [ ] Ubah `display_order` di database sehingga urutannya terbalik → produk yang
      dipilih nasabah tetap produk yang benar sampai layar Ringkasan dan sampai
      baris `onboarding_sessions.product_type`. **Ini uji paling penting di
      daftar ini**; kalau lolos, kopling indeks benar-benar sudah hilang.
- [ ] Ubah `min_initial_deposit` → layar menampilkan angka baru tanpa rebuild APK.
- [ ] Buat sesi, lalu periksa di `psql`: `product_catalog_version` dan
      `min_initial_deposit_shown` terisi dan **cocok dengan yang tampil di layar**.
- [ ] APK build lama (tanpa pemanggilan katalog) masih bisa membuat sesi; kedua
      kolom itu `NULL` dan tidak ada error.

## D. Jejak dokumen

- [ ] `docs/01-API-SPECIFICATION.md`, `docs/06-BUKA-REKENING-API-SPEC.md`,
      `docs/02-DATABASE-SCHEMA.md`, `docs/03-REDIS-STRATEGY.md`, `docs/postman/`,
      `README.md` — diperbarui di commit yang sama.
- [ ] `CLAUDE.md` repo ini: satu baris di tabel skill.
- [ ] `CLAUDE.md` repo Android: §Integrasi API + tabel status fitur (baris
      "Pilih jenis rekening"), dan `navigation.md` bila ada route yang bergeser.
- [ ] Angka setoran awal di database sudah angka resmi, **atau** komentar
      migrasinya masih menyatakan dengan jelas bahwa itu data desain.
