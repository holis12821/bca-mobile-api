---
name: git-branch-workflow
description: Alur branch, commit, push, dan pull request wajib untuk bca-mobile-api — menentukan branch mana yang dibuat sebelum satu baris kode pun ditulis, dari mana ia bercabang, dan lewat mana ia sampai ke `main`. Gunakan SEBELUM mulai mengerjakan endpoint baru, migrasi, perbaikan bug, atau fase lanjutan dari pekerjaan besar, dan saat diminta commit, push, membuka PR, merge, atau menyiapkan rilis. Trigger juga pada "tambah endpoint", "kerjakan fitur", "fitur baru", "fix bug ini", "bikin branch", "branch apa", "commit ini", "push ya", "buka PR", "pull request", "merge ke main", "siap rilis", "mulai fase berikutnya", "pengerjaan panjang", "working branch", "feature branch", "branch development", "staging", "langsung di main saja", "hotfix", "urgent", "mendesak", "production bermasalah", "perbaiki yang sudah rilis", dan setiap permintaan yang akan menghasilkan commit. `main` tidak boleh disentuh langsung untuk alasan apa pun — termasuk perbaikan satu baris dan keadaan mendesak — jadi baca skill ini lebih dulu bahkan saat permintaannya terdengar sepele atau terburu-buru. Berlaku juga saat pengguna tidak menyebut git sama sekali — kalau hasil pekerjaannya berupa commit, alur ini yang menentukan tempatnya. JANGAN dipakai untuk memilih lapisan kode atau desain endpoint (itu `bca-mobile-backend` dan skill domain lainnya), isi pesan rilis ke nasabah, atau konfigurasi CI.
---

# Alur Branch, Commit & PR — bca-mobile-api

Skill ini menjawab satu pertanyaan yang harus dijawab **sebelum** mengedit berkas:
pekerjaan ini tinggal di branch mana, dan lewat jalan mana ia sampai ke `main`?

Menjawabnya di akhir pekerjaan sudah terlambat — memindahkan commit yang sudah jadi
ke branch yang benar jauh lebih mahal daripada membuat branch lebih dulu.

Disalin dari skill bernama sama di repo `bca_mobile` (klien Android) supaya kedua
sisi memakai bentuk branch yang sama; gerbang sebelum merge dan jebakan commit-nya
diganti dengan milik repo ini.

## Aturan yang tidak berubah

**`main` tidak pernah disentuh langsung — tanpa pengecualian.** Jangan mengubah,
membuat, atau menghapus berkas apa pun sementara branch aktif adalah `main`. Ini
berlaku untuk **semua** berkas, bukan hanya kode: `CLAUDE.md`, isi `.claude/skills/`,
`docs/`, `README.md`, `Makefile`, `migrations/`. Tidak ada perbaikan yang cukup kecil
untuk dikecualikan, dan tidak ada keadaan yang cukup mendesak — yang mendesak dapat
branch-nya sendiri, lihat §Hotfix.

Jadi sebelum mengedit berkas pertama, periksa:

```bash
git branch --show-current      # kalau menjawab "main", berhenti dan buat branch
```

Satu-satunya hal yang boleh terjadi pada `main` adalah menerima merge dari
`feature/` atau `hotfix/`. `main` hanya bertambah lewat merge, tidak pernah lewat
editor.

**`git commit`, `git push`, membuat branch, dan membuka PR hanya saat diminta
eksplisit.** Skill ini mengatur *bentuk*-nya, bukan memberi izin melakukannya sendiri.
Jadi urutannya: pengguna meminta pekerjaan → Anda sebutkan branch apa yang perlu
dibuat → pengguna setuju → baru dibuat. Ini sejalan dengan **ATURAN #1 `CLAUDE.md`**
(berkas baru dikonfirmasi lebih dulu): di repo ini yang dikonfirmasi bukan cuma
berkas, tapi juga setiap tindakan git yang terlihat orang lain.

Kalau pengguna meminta pekerjaan yang jelas akan mengubah berkas sementara Anda di
`main`, sebutkan branch yang dibutuhkan **sebelum** mulai, bukan setelah berkasnya
berubah. Menanyakannya di tengah pekerjaan memaksa pengguna memilih antara
menyetujui branch atau membuang pekerjaan yang sudah jalan.

## Pilih bentuknya dulu

Satu pertanyaan yang memutuskan: **pekerjaan ini selesai dalam sekali duduk, atau
terbagi fase yang jalan berminggu-minggu?**

| Perkiraan | Bentuk |
|---|---|
| Satu-dua fase, selesai dalam hitungan hari sampai ~2 minggu | **Jalur pendek** — satu `working/` langsung dari `main` |
| Banyak fase, perkiraan lebih dari 2 minggu sampai sebulan | **Jalur panjang** — `feature/` dari `main`, lalu `working/` per fase dari `feature/` |

Kalau ragu, tanyakan ke pengguna berapa lama dan berapa fase. Menebak ke arah yang
salah mahal di kedua sisi: jalur panjang untuk pekerjaan sehari menambah dua merge
tanpa guna, sementara jalur pendek untuk pekerjaan sebulan menyimpan satu branch
raksasa yang tidak pernah bisa dites sebagian.

### Jalur pendek

```text
main
 └── working/kunci-meja-petugas        ← seluruh pekerjaan di sini
```

### Jalur panjang

Satu `feature/` menampung seluruh pekerjaan besar; tiap fase dapat `working/`-nya
sendiri yang bercabang dari sana, lalu kembali ke sana setelah selesai.

```text
main
 └── feature/cs-halo-bca
      ├── working/fase-1-sesi-petugas  ← selesai → merge ke feature/cs-halo-bca
      ├── working/fase-2-kesiapan-terminal
      └── working/fase-3-kunci-meja
```

Alasan fase tidak bercabang langsung dari `main`: fase 2 hampir selalu butuh hasil
fase 1. Dari `main`, fase 2 mulai tanpa pekerjaan fase 1 dan konfliknya baru
ketahuan di akhir. Dari `feature/`, fase 2 mulai di atas fase 1 — seperti keadaan
sebenarnya nanti di produksi.

Di repo ini ada satu akibat tambahan yang mahal: **nomor migrasi**. Dua fase yang
bercabang dari `main` masing-masing akan mengambil nomor berikutnya yang sama, dan
tabrakannya baru terlihat saat merge — saat salah satunya sudah dijalankan di
database orang. Dari `feature/`, fase berikutnya melihat migrasi fase sebelumnya dan
mengambil nomor sesudahnya.

## Jalan menuju main

`development` adalah branch **berumur panjang** — staging tempat pengujian. Dibuat
sekali dari `main`; kalau belum ada, buat dan katakan Anda membuatnya. Jangan
membuat `development` baru per fitur.

```text
1. working/<fase>      --merge-->  feature/<nama>     (jalur panjang; jalur pendek lewati)
2. feature/<nama>      --merge-->  development        ← diuji di sini
3. feature/<nama>      --- PR --->  main              ← rilis, setelah pengujian lulus
```

Perhatikan langkah 3: yang masuk `main` adalah **`feature/`**, bukan `development`.

Ini bagian yang paling sering salah, jadi alasannya perlu dipegang: `development`
bercampur. Ia memuat beberapa pekerjaan sekaligus, termasuk yang pengujiannya gagal
dan yang belum diputuskan ikut rilis. Me-merge `development` ke `main` berarti
menyeret semuanya — termasuk yang belum siap. `feature/` memuat tepat satu pekerjaan
yang batasnya jelas, jadi itulah yang pantas jadi isi rilis.

`development` lulus uji membuktikan *pekerjaan itu* sehat bersama yang lain. Ia
tidak membuat `development` jadi kandidat rilis.

## Penamaan

Prefix dengan garis miring, sisanya kebab-case, bahasa apa pun yang paling jelas.

| Prefix | Isi | Contoh |
|---|---|---|
| `feature/` | Payung pekerjaan besar, satu per pekerjaan | `feature/cs-halo-bca` |
| `working/` | Satu fase atau satu pekerjaan pendek | `working/fase-3-kunci-meja` |
| `fix/` | Perbaikan bug yang tidak mendesak | `fix/cs-build-docs-tests` |
| `hotfix/` | Perbaikan mendesak untuk yang sudah rilis | `hotfix/saldo-tersedia-abaikan-hold` |
| `development` | Staging, tanpa akhiran | `development` |

Nama menyebut **apa yang dikerjakan**, bukan nomor tiket sendirian: `working/BCA-412`
tidak memberi tahu siapa pun isinya saat daftar branch dibaca enam bulan kemudian.
Nomor tiket boleh ikut setelah namanya — `working/fase-3-kunci-meja-BCA-412`.

Branch yang sudah ada sebelum aturan ini dan sudah di-push — mis. `feat/…` — **jangan
diganti namanya.** Mengganti nama branch yang sudah di-remote memaksa setiap orang
yang menariknya membereskan salinannya sendiri, dan itu harga yang jauh lebih besar
daripada satu nama yang tidak seragam. Selesaikan di nama lamanya; yang berikutnya
pakai bentuk di atas.

## Hotfix

Keadaan mendesak **bukan** izin menyentuh `main` langsung. Ia hanya memperpendek
jalannya, dan jalannya tetap lewat branch.

```text
main
 └── hotfix/<nama>   --- PR --->  main          (rilis perbaikan)
                     --merge-->   development   (dan setiap feature/ yang masih hidup)
```

Bercabang dari **`main`**, bukan dari `development` atau `feature/`: yang diperbaiki
adalah keadaan yang sudah ada di tangan nasabah, dan itulah yang ada di `main`.
Bercabang dari `development` akan menyeret pekerjaan yang belum dirilis ke dalam
perbaikan mendesak — tepat yang paling tidak diinginkan saat sedang terdesak.

Boleh melewati singgah di `development` sebelum masuk `main`; itu makna "mendesak" di
sini. Tapi **gerbang di bawah tidak boleh dilewati** — justru saat terdesak,
perbaikan yang merusak hal lain paling mahal harganya.

Setelah masuk `main`, **merge balik** `hotfix/` ke `development` dan ke setiap
`feature/` yang masih berjalan. Tanpa ini perbaikannya hilang dari pekerjaan yang
sedang jalan, lalu kambuh begitu pekerjaan berikutnya dirilis — dan kambuhnya sulit
dipercaya, karena semua orang ingat bug itu sudah pernah ditutup.

## Sebelum merge atau membuka PR ke mana pun

Jangan mengusulkan merge sebelum semuanya hijau — angkanya disebut apa adanya, dan
kegagalan dilaporkan, bukan dibulatkan jadi "sudah lewat":

```bash
make check              # lint + vet + test -race, seluruh paket
make migrate-status     # kalau menyentuh migrations/: nomornya naik, DB tidak dirty
make ledger-check       # kalau menyentuh jalur uang: dua-duanya HARUS nol baris
```

Satu test yang gagal adalah alasan cukup untuk tidak merge. Laporkan keluarannya ke
pengguna dan biarkan mereka yang memutuskan, alih-alih memilih sendiri untuk lanjut.

Dua kegagalan yang **bukan** regresi kode, dan jangan "diperbaiki" dengan mengubah
kode: test Redis/idempotency gagal dengan `rootless Docker not found` saat Docker
mati — itu lingkungan. Dan `make test-concurrent` yang flaky **adalah** bug sungguhan;
yang itu jangan diabaikan.

Kalau commit-nya menambah migrasi, **uji `down`-nya juga**: `migrate down 1` lalu
`up` lagi. Pengujiannya di database sekali pakai, bukan di DB dev — `down` yang salah
yang baru ketahuan saat rollback sungguhan adalah rollback yang gagal di tengah.

## Commit

Ruang lingkup satu commit = satu perubahan yang bisa dijelaskan dalam satu kalimat.
Kalau butuh kata "dan" dua kali, itu dua commit.

**Stage berkas yang Anda sentuh, jangan `git add -A`,** lalu sebutkan apa yang
sengaja ditinggalkan. `.env` dan `keys/` sudah ter-ignore, tapi `-A` tetap menyeret
berkas sementara yang belum masuk `.gitignore` — dan skrip percobaan milik satu orang
yang ikut ke `main` akan dijalankan orang lain.

Pesan commit menjelaskan **kenapa**, karena *apa*-nya sudah ada di diff. Untuk
perbaikan bug, sebut gejala yang dialami nasabah atau pemanggil API lebih dulu — itu
yang dicari orang saat menelusuri kambuhnya setahun kemudian. Bahasa Indonesia, sama
seperti pesan error nasabah. Akhiri dengan baris atribusi yang diminta harness.

```text
tolak debet rekening yang bukan milik pemanggil

source_account_id datang dari request, dan executor mengunci lalu mendebet UUID apa
pun yang diberikan tanpa filter user_id. Satu user bisa mendebet rekening user lain
hanya dengan menebak UUID-nya — dan transfer-nya berhasil, lengkap dengan struk.

Kepemilikan sekarang dicek lewat accounts.FindOwnedByID sebelum saldo disentuh, dan
jawabannya 403 ACCOUNT_FORBIDDEN, bukan 404: UUID-nya tidak bisa dienumerasi, jadi
tidak ada yang bocor, sementara kode yang berbeda membedakan satu baris perbaikan
dari sehari menebak.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
```

## Yang wajib ikut di commit yang sama

Ini jebakan terbesar repo ini, dan `CLAUDE.md` §Dokumen mengikatnya:

- **Mengubah bentuk response** → `docs/01-API-SPECIFICATION.md` **dan**
  `docs/postman/` di commit yang sama. Keduanya pernah melenceng jauh dari kode, dan
  yang menanggungnya adalah build Android yang pecah tanpa tahu sebabnya.
- **Menambah kolom** → migrasi di `migrations/` di commit yang sama, berpasangan
  `.up.sql` + `.down.sql`. Pernah ada dua endpoint yang 500 permanen karena menulis
  kolom yang tidak pernah dibuat.
- **Menambah route, paket, endpoint, dependency, atau skill** → `CLAUDE.md`
  diperbarui di commit yang sama; peta struktur dan tabel skill di sana adalah yang
  dibaca orang berikutnya lebih dulu.
- **Menambah method ke interface repository** → semua mock di test ikut di commit yang
  sama, atau paket test-nya tidak bisa dikompilasi.

## Pull request

PR adalah satu-satunya jalan masuk ke `main` (riwayat repo ini sudah memakainya:
`Merge pull request #1`). Yang di-push lebih dulu, PR-nya menyusul:

```bash
git push -u origin working/<nama>
gh pr create --base main --title "<satu kalimat>" --body "<isi>"
```

Isi PR menjawab tiga hal, dan tidak lebih: **apa yang berubah bagi pemanggil API**,
**kenapa**, dan **apa yang sudah dijalankan untuk membuktikannya** — sebutkan
`make check` beserta hasilnya, dan sebutkan kalau ada yang dilewati. Jangan menyalin
daftar commit; itu sudah ada di tab Commits.

Yang wajib disebut terpisah kalau ada, karena ketiganya mengubah cara PR itu dirilis:

1. **Migrasi baru** — nomornya, dan bahwa `down`-nya sudah diuji. Reviewer perlu tahu
   ada yang harus dijalankan di database sebelum versi ini naik.
2. **Perubahan bentuk response** — kode Android mana yang ikut terpengaruh. PR yang
   mengubah response tanpa menyebutnya akan lolos review dan pecah di perangkat.
3. **Endpoint atau kode galat baru** — reviewer membaca ini untuk memeriksa apakah
   `docs/01` dan Postman sudah ikut.

Akhiri body PR dengan baris atribusi yang diminta harness.

Jangan menutup PR dengan merge atas inisiatif sendiri, dan jangan `push --force` ke
branch yang PR-nya sudah dibaca orang: komentar reviewer menempel ke commit, dan
menulis ulang riwayatnya membuat komentar mereka menggantung tanpa konteks.

## Kalau pekerjaan sudah dimulai di main

Terjadi. Yang menentukan jalan keluarnya: perubahannya **sudah di-commit atau belum?**
Periksa dengan `git status` dan `git log origin/main..HEAD` sebelum memilih.

**Belum di-commit** — kasus paling umum, dan tidak ada yang perlu dihapus. Perubahan
yang belum di-commit ikut berpindah saat branch dibuat, dan `main` tidak tersentuh:

```bash
git switch -c working/<nama>       # perubahan terbawa; main tetap bersih
```

**Sudah di-commit tapi belum di-push** — commit-nya dipindahkan, lalu `main`
dikembalikan ke posisi remote:

```bash
git branch working/<nama>          # tandai posisi sekarang, commit ikut tersimpan
git reset --hard origin/main       # MENGHAPUS perubahan yang belum di-commit
git switch working/<nama>
```

`reset --hard` membuang perubahan yang belum di-commit tanpa bisa dibatalkan. Jalankan
`git status` lebih dulu, tunjukkan ke pengguna apa yang akan hilang, dan minta
persetujuan — jangan jalankan atas inisiatif sendiri. Kalau ada perubahan belum
di-commit yang bercampur, selamatkan dulu dengan `git stash` sebelum reset.

**Sudah di-push ke `main`** — berhenti dan serahkan keputusannya ke pengguna. Jangan
pernah menawarkan `push --force` ke `main`: orang lain mungkin sudah menariknya, dan
menulis ulang riwayat bersama merusak salinan mereka. Perbaikannya maju ke depan
lewat `hotfix/`, bukan dengan menghapus jejak.
