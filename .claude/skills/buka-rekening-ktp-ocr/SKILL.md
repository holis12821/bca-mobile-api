---
name: buka-rekening-ktp-ocr
description: Verifikasi foto e-KTP pada flow buka rekening — POST /v1/onboarding/ocr, dari mana teks kartu berasal (mesin OCR server atau teks on-device ML Kit dari client), tiga lapisan validasi tanpa Dukcapil (tata letak e-KTP, bentuk NIK, konsistensi NIK dengan tanggal lahir/jenis kelamin/provinsi), mode registri DUKCAPIL_MODE off|mock|real, penyimpanan foto lewat KTP_STORAGE_DRIVER, dan arti accuracy_percent serta dukcapil_checked. Gunakan saat "OCR KTP menerima foto apa pun", "data KTP tidak sesuai fotonya", "foto KTP tidak tersimpan", "OCR_NOT_KTP padahal KTP asli", "OCR_DUKCAPIL_MISMATCH untuk KTP sungguhan", "accuracy selalu 99.4", atau saat mengubah parser KTP, ambang validasi, atau sumber teks OCR. Trigger juga pada "OCR_NOT_KTP", "OCR_DUKCAPIL_MISMATCH", "client_ocr_text", "dukcapil_checked", "accuracy_percent", "AssessKTPText", "ValidateKTPConsistency", "ValidateNIKStructure", "BirthDateFromNIK", "provinceCodes", "ktpMarkers", "DUKCAPIL_MODE", "KTP_STORAGE_DRIVER", "localfs", "MockOCREngine", "photo_path", dan "no_enrolled_reference". JANGAN gunakan untuk verifikasi wajah dan liveness (itu `liveness-403-forbidden`), endpoint onboarding lain seperti personal-data atau submit (itu `buka-rekening-backend`), OTP (itu `buka-rekening-otp`), atau layar kamera dan picker galeri di Android (project terpisah).
---

# Verifikasi e-KTP — dari foto ke identitas

Satu hal yang menentukan cara membaca seluruh berkas ini: **server tidak punya
mesin OCR sendiri.** Teks kartu datang dari pengenalan ML Kit di perangkat. Yang
dikerjakan server adalah **memvalidasi** teks itu, dan validasinya cukup ketat
untuk menolak dokumen lain serta menangkap OCR yang salah baca — tanpa Dukcapil.

Kontrak: `docs/06-BUKA-REKENING-API-SPEC.md` §OCR. Kalau berbeda, spec yang menang.
Berkas inti: `internal/domain/onboarding/ocr_service.go`, `ktp_validation.go`,
`ktp_parser.go`, `internal/repository/localfs/`.

---

## 1. Latar: apa yang dulu terjadi

Penting karena menjelaskan kenapa bentuknya seperti sekarang.

`MockOCREngine.ExtractText` menerima `_ []byte` — **gambarnya dibuang** — dan
selalu mengembalikan teks e-KTP hardcoded milik satu identitas uji dengan
confidence 99,4. Mock itu dipasang `ProvidersFor` untuk setiap build development.
Akibatnya, diuji langsung ke server:

```
persegi abu-abu polos 1920×1080, tanpa teks apa pun
  → 200 OK, accuracy_percent 99.4, dukcapil_match true,
    identitas lengkap, step maju ke PERSONAL_DATA
```

Tiga kebohongan sekaligus, dan tiga perbaikan yang menyusul:

| Dulu | Sekarang |
|---|---|
| Mesin OCR mengarang teks | Tidak ada mock. Tanpa teks → `OCR_NOT_KTP` |
| `accuracy_percent` konstanta 99,4 | Rasio label e-KTP yang benar-benar terbaca |
| `dukcapil_match: true` tanpa registri | `dukcapil_checked` memisahkan "dicek" dari "cocok" |

Dan dua lagi yang berakar di tempat yang sama: `MockObjectStorage.Upload` tidak
menulis apa pun, jadi foto "tidak terlampir" (`photo_path` menunjuk objek yang
tidak pernah ada) **dan** face match biometrik tidak punya pembanding
(`no_enrolled_reference`).

---

## 2. Pipeline `ProcessKTP`

| # | Langkah | Gagal → |
|---|---|---|
| 1 | `current_step == OCR` | `422 ONBOARDING_INVALID_STEP` |
| 2 | Rate limit 10/jam/sesi | `429 RATE_LIMIT_EXCEEDED` |
| 3 | **Ambil teks** (§3) | `422 OCR_NOT_KTP` |
| 4 | **Tata letak e-KTP** (§4a) | `422 OCR_NOT_KTP` |
| 5 | Parse field | — |
| 6 | **Bentuk NIK + konsistensi** (§4b, §4c) | `422 OCR_NOT_KTP` |
| 7 | Dukcapil, bila dikonfigurasi (§5) | `422 OCR_DUKCAPIL_MISMATCH`, `503 …TIMEOUT`/`…UNAVAILABLE` |
| 8 | Kualitas foto dari metadata capture | `422 OCR_PHOTO_BLURRY`/`_GLARE_DETECTED`/`_CORNERS_MISSING` |
| 9 | Unggah foto (terenkripsi) | 500 |
| 10 | Simpan hasil (PII terenkripsi) | 500, **foto dihapus** |
| 11 | `OCR` → `PERSONAL_DATA` | 500 |

Unggahan sengaja di langkah 9, **setelah** semua jalur penolakan: foto yang gagal
validasi tidak boleh tertinggal di bucket. Apa pun yang gagal setelahnya memanggil
`discardPhoto`.

---

## 3. Dari mana teksnya datang

`extractText` (`ocr_service.go`), urutannya mengikat:

1. `s.ocrEngine != nil` → mesin server. Mesin yang membaca **kosong** berarti foto
   ini tidak terbaca → `OCR_NOT_KTP`. **Tidak** jatuh ke teks client: mesin
   dikonfigurasi justru untuk menggantikan sumber itu.
2. `client_ocr_text` tidak kosong → dipakai, dicatat `text_source=client_ondevice`.
3. Keduanya tidak ada → `OCR_NOT_KTP`.

Yang **tidak boleh** ditambahkan sebagai langkah 4: apa pun yang menghasilkan
field tanpa membaca kartu. Itu bug yang baru saja dihapus.

### Batas yang harus dinyatakan apa adanya

Dengan teks dari client, server memvalidasi isinya tapi **tidak bisa membuktikan**
teks itu dibaca dari foto yang diunggah. Client yang dimodifikasi masih harus
membuat teks itu lolos sebagai e-KTP yang konsisten, dan sumbernya tercatat di
log — tapi jangan sebut jalur ini "server-side OCR". Untuk bank sungguhan,
pasang mesin OCR server lewat `OCREngine` (interface-nya sudah siap, dan client
tidak perlu diubah).

---

## 4. Tiga lapisan validasi, tanpa Dukcapil

### 4a. Tata letak e-KTP — `AssessKTPText`

Mencari **label**, bukan nilai: nilai berbeda tiap orang, label sama di setiap
kartu. 16 label dicari (`NIK`, `Nama`, `Tempat/Tgl Lahir`, `Jenis Kelamin`,
`Alamat`, `RT/RW`, `Kel/Desa`, `Kecamatan`, `Agama`, `Status Perkawinan`,
`Kewarganegaraan`, `Berlaku Hingga`, `Pekerjaan`, `Gol. Darah`, `PROVINSI`, nama
negara), masing-masing dengan beberapa varian ejaan karena OCR rutin salah baca
satu-dua huruf.

`minKTPMarkers = 5`. Dipilih supaya kartu yang terbaca sebagian — sudut terpotong,
satu sisi gelap — tetap lolos, sementara dokumen lain tidak punya peluang.
Terverifikasi di server: struk dengan 16 digit, SIM, dan teks acak semuanya
ditolak.

**`Confidence()` = rasio label yang ditemukan**, dan itulah `accuracy_percent`.
Bukan laporan-diri mesin OCR. Jangan kembalikan ke makna lama.

### 4b. Bentuk NIK — `ValidateNIKStructure`

Lebih ketat dari `ValidateNIK` lama (yang dipertahankan untuk pemanggil lain):

- 16 digit
- kode provinsi ada di `provinceCodes` — **38 provinsi** terdaftar eksplisit.
  Rentang lama 11–94 menerima 20, 30, 40, 44 yang bukan provinsi mana pun.
- nomor urut (digit 13–16) ≠ `0000` — tidak pernah diterbitkan; kemunculannya
  berarti OCR membaca empat digit terakhir dari tempat yang salah
- tanggal di dalam NIK valid (`BirthDateFromNIK`)

### 4c. Konsistensi internal — `ValidateKTPConsistency`

**Ini pemeriksaan paling berguna di seluruh berkas ini.** NIK Indonesia
memvalidasi dirinya sendiri:

```
PP RR SS DD MM YY NNNN
│  │  │  │  │  │  └── nomor urut
│  │  │  │  │  └───── tahun lahir, 2 digit
│  │  │  │  └──────── bulan lahir
│  │  │  └─────────── tanggal lahir, +40 bila PEREMPUAN
│  │  └────────────── kode kecamatan
│  └───────────────── kode kabupaten/kota
└──────────────────── kode provinsi
```

Jadi tanggal lahir dan jenis kelamin **ada di dua tempat** dan harus sepakat.
Yang dicocokkan: NIK ↔ `tanggal_lahir`, NIK ↔ `jenis_kelamin`, dan kode provinsi
↔ header `PROVINSI` (saling-memuat, bukan sama persis — nama provinsi bisa
berubah karena pemekaran).

Terverifikasi di server, semuanya `OCR_NOT_KTP`:

| Kasus | Tertangkap oleh |
|---|---|
| tanggal lahir beda 1 hari | NIK ↔ tanggal lahir |
| jenis kelamin bertentangan | NIK ↔ jenis kelamin (aturan +40) |
| kode provinsi 20 | daftar provinsi |
| nomor urut 0000 | bentuk NIK |
| **satu digit NIK salah baca** | NIK ↔ tanggal lahir |

Baris terakhir yang paling sering terjadi di lapangan, dan NIK-nya sendiri masih
berbentuk sah — hanya pencocokan silang yang bisa menangkapnya.

**Field kosong dilewati, tidak dianggap bertentangan.** OCR sering gagal membaca
satu baris, dan itu bukan bukti kartunya palsu. Yang ditolak hanya nilai yang
terbaca dan berbeda. Jangan mengubah ini jadi "wajib ada" — gejalanya adalah
nasabah jujur yang ditolak karena satu baris gelap.

Sesudah lolos, `provinsi` dan `jenis_kelamin` yang kosong **diisi dari NIK**: NIK
sumber yang lebih andal, dan kecocokannya sudah dipastikan di atas.

---

## 5. Mode registri — `DUKCAPIL_MODE`

| Mode | Perilaku | Kapan |
|---|---|---|
| `off` (bawaan) | Registri tidak dihubungi. `dukcapil_checked: false` | Dev/portofolio |
| `mock` | Hanya 4 NIK uji; **KTP sungguhan ditolak** `OCR_DUKCAPIL_MISMATCH` | Menguji cabang match/mismatch |
| `real` | Butuh klien registri; tanpa itu menolak | **Wajib di luar development** |

`off` adalah bawaan, dan itu **disengaja**. `mock` lebih buruk daripada melewati:
ia menolak setiap NIK di luar empat identitas uji, jadi e-KTP asli dijawab
`OCR_DUKCAPIL_MISMATCH` — terbaca seolah kartu nasabah yang bermasalah. Kalau ada
laporan "KTP saya ditolak padahal asli", periksa variabel ini lebih dulu.

### `dukcapil_match` vs `dukcapil_checked`

Dua field, dan keduanya wajib dibaca bersama. `s.dukcapil == nil` dulu berarti
"anggap cocok", jadi response mengklaim `dukcapil_match: true` untuk identitas
yang tidak pernah diverifikasi siapa pun — dan client menggantungkan tombol
Lanjutkan pada nilai itu, sehingga kebohongan yang nyaman jadi penopang alur.

Aturan untuk client:
- lencana "terverifikasi" **hanya** bila `dukcapil_checked && dukcapil_match`
- **jangan memblokir** saat `dukcapil_checked == false` — kartunya sudah lolos
  ketiga lapisan di §4
- blokir hanya bila `dukcapil_checked && !dukcapil_match`

---

## 6. Penyimpanan foto — `KTP_STORAGE_DRIVER`

`local` (bawaan) → `internal/repository/localfs`, menulis ke
`KTP_STORAGE_LOCAL_PATH` (bawaan `uploads/`). `mock` tidak menyimpan apa pun dan
hanya untuk test; `Validate()` menolaknya di luar development.

Yang perlu diketahui:

- **`photo_path` adalah path penuh, bukan key**: `local://bucket/key`. Pecah
  dengan `SplitObjectPath` sebelum memanggil `ObjectStorage.Download`. Mengirim
  path penuh sebagai key adalah bug yang pernah ada di `loadReference` face
  match, dan tidak terlihat karena mock mengabaikan kedua argumennya.
- Baris lama bisa menyimpan `s3://…` dari mock — objeknya **tidak ada**. Skema
  yang berbeda itu yang membedakan baris yang bisa dibaca dari yang tidak.
- Isinya terenkripsi AES sebelum ditulis; `Download` mendekripsi di pemanggil.
- `localfs` menolak path traversal per segmen. `safeSegment` mengizinkan titik
  (nama berkas punya ekstensi), jadi `.` dan `..` ditolak **terpisah** di
  `isSafeSegment` — mengandalkan pemeriksaan prefiks akar saja tidak cukup,
  karena `bucket/..` saling menghapus jadi akar dan tetap lolos prefiks.
- Berkas ditulis atomik (temp + rename) dengan mode `0600`.

---

## 7. Parser — `ktp_parser.go`

Pemisah utamanya `:` dalam pola label-nilai. Dua hal yang **tidak** punya titik dua:

```
PROVINSI DKI JAKARTA        ← header
KOTA JAKARTA SELATAN        ← header (atau KABUPATEN …)
```

Keduanya dibaca di cabang non-titik-dua. Sebelum itu ditambahkan, `provinsi` dan
`kota` **selalu kosong** untuk setiap kartu — termasuk teks yang jelas-jelas
memuat keduanya. Kalau menyentuh cabang ini, pertahankan `continue`-nya: tanpa itu
baris header jatuh ke pencarian NIK dan bisa menyerap angka dari nama wilayah.

---

## 8. Triase

```
OCR_NOT_KTP
  → Baca log: "ocr rejected: ..." menyebut sebabnya dan text_source.
    a. "text is not an e-KTP" + markers_found rendah
       → foto bukan KTP, ATAU client tidak mengirim client_ocr_text.
         Cek: apakah ML Kit di perangkat menghasilkan teks? Foto dari galeri
         yang orientasinya salah (EXIF) terbaca kosong oleh OCR.
    b. "no NIK in text"      → 16 digit tidak ditemukan
    c. "KTP data inconsistent" + reason → §4c, reason-nya menyebut field mana

OCR_DUKCAPIL_MISMATCH untuk KTP asli
  → DUKCAPIL_MODE=mock. Setel ke "off". §5.

accuracy_percent selalu 99.4
  → Mesin tiruan masih terpasang. Tidak boleh ada lagi; `OCR: nil` di
    ProviderOptions adalah keadaan yang benar untuk repo ini.

Foto tidak terlampir / photo_path menunjuk objek yang tidak ada
  → KTP_STORAGE_DRIVER=mock, atau baris lama dengan skema s3://. §6.

no_enrolled_reference saat verifikasi wajah
  → Foto KTP tidak pernah tersimpan (§6), atau path dipakai sebagai key.
    Bukan masalah di lapisan biometrik — lihat `liveness-403-forbidden`.
```

---

## 9. Aturan yang berlaku terus

1. **Jangan pernah mengarang field KTP.** Tanpa teks, jawabannya penolakan. Mesin
   tiruan yang mengembalikan identitas untuk input apa pun sudah dihapus sekali;
   jangan dibuat ulang "sementara untuk development".
2. **Jangan satukan dua pertanyaan berbeda jadi satu angka.** "Apakah frame ini
   terbaca" (metadata capture) dan "berapa banyak kartu yang terbaca" (rasio
   label) diukur terpisah. Menyuapkan yang kedua ke `assessPhotoQuality` sempat
   menolak kartu yang baik sebagai buram.
3. **Jangan jadikan field kosong sebagai penolakan** di `ValidateKTPConsistency`.
4. **Jangan melonggarkan `off` jadi `dukcapil_match: true`.** "Tidak dicek"
   adalah keadaan tersendiri dan harus terlihat seperti itu.
5. **Foto hanya disimpan setelah seluruh validasi lolos**, dan dihapus pada setiap
   kegagalan sesudahnya.
6. **Jangan mengirim alasan internal ke client.** `OCR_NOT_KTP` tidak menyebut
   lapisan mana yang menolak; alasannya ada di log.
