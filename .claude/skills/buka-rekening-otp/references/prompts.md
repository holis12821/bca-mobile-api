# Prompt Implementasi — Verifikasi OTP Onboarding

Dipakai berurutan. Setiap fase berhenti di titik yang bisa diuji sebelum lanjut.
Kontrak yang dirujuk: `06-BUKA-REKENING-API-SPEC.md` §3a–§3c dan `SKILL.md` skill ini.

---

## Fase 0 — Putuskan dulu, jangan menebak

Sebelum menulis apa pun, jawab tiga pertanyaan di `SKILL.md` §5 bersama pemilik produk:

1. Hubungan antara rate limit verifikasi (5/5 menit) dan blokir (30 menit setelah 5 gagal).
   Mana yang menyala lebih dulu? Apakah penolakan rate limit menambah penghitung gagal?
2. Apakah regenerasi otomatis setelah 3 kali gagal memotong kuota kirim ulang 3/jam?
3. Apakah kirim ulang mereset penghitung gagal?

Tulis jawabannya ke `SKILL.md` §5 dan `06-BUKA-REKENING-API-SPEC.md`. Fase berikutnya
tidak boleh dimulai sebelum ini beres — salah tebak di sini berarti nasabah terblokir
tanpa sebab, atau sebaliknya ambang blokirnya bisa diputar.

---

## Fase 1 — Penerbitan OTP pada `personal-data`

OTP pertama lahir dari endpoint yang sudah ada, bukan endpoint baru.

Yang harus terjadi saat `POST /v1/onboarding/personal-data` berhasil:

- Terbitkan kode 6 digit dari sumber acak yang layak kriptografi.
- Simpan hash-nya beserta waktu terbit, dengan umur sesuai keputusan Fase 0.
- Nolkan penghitung percobaan gagal untuk sesi itu.
- Kirim SMS **setelah** penyimpanan berhasil.
- Balas `otp_sent_to` (tersamar), `otp_expires_at`, dan `current_step: "OTP_VERIFY"`.
- Catat audit `OTP_SENT` dengan pemicu `personal_data`.

Selesai bila: balasan `personal-data` membawa tiga field itu, dan kode aslinya tidak
muncul di response maupun log.

---

## Fase 2 — `POST /v1/onboarding/verify-otp`

Urutan pemeriksaan penting — yang paling murah dan paling menentukan lebih dulu:

1. Sesi ada dan cocok dengan `X-Device-ID`. Tidak cocok → tolak.
2. Sesi belum kedaluwarsa → kalau tidak, `ONBOARDING_SESSION_EXPIRED`.
3. `current_step == "OTP_VERIFY"` → kalau tidak, `ONBOARDING_INVALID_STEP`.
4. Sesi tidak sedang terblokir → kalau ya, `OTP_BLOCKED` beserta
   `details.retry_after_seconds`, dan **jangan** menambah penghitung gagal.
5. OTP aktif masih ada dan belum lewat masa berlaku → kalau tidak, `OTP_EXPIRED`.
6. Bandingkan hash dengan fungsi waktu-tetap.

Bila cocok: tandai `otp_verified`, majukan `current_step` ke `BIOMETRIC`, buang OTP
aktif, catat `OTP_VERIFIED`.

Bila tidak cocok: tambah penghitung gagal, catat `OTP_FAILED`, lalu terapkan ambang
dari Fase 0 — regenerasi, blokir, atau balas `OTP_INVALID`.

Selesai bila: seluruh butir `SKILL.md` §8 yang menyangkut verifikasi lolos.

---

## Fase 3 — `POST /v1/onboarding/resend-otp`

1. Pemeriksaan sesi dan step sama seperti Fase 2 butir 1–3.
2. Sesi terblokir → `OTP_BLOCKED`. Kirim ulang tidak boleh jadi jalan memutar blokir.
3. Kuota kirim ulang masih ada → kalau habis, `RATE_LIMIT_EXCEEDED` dengan
   `details.retry_after_seconds`.
4. Terbitkan OTP baru, batalkan yang lama seketika.
5. Balas `otp_sent_to` dan `otp_expires_at` yang baru.
6. Catat `OTP_SENT` dengan pemicu `resend`.

Selesai bila: kode lama benar-benar ditolak setelah kirim ulang, dan kuota ditegakkan.

---

## Fase 4 — SMS gateway

Satu antarmuka, dua implementasi:

- Produksi memakai gateway sungguhan. Kegagalan kirim dibalas sebagai error yang
  jelas — jangan menelan diam-diam, karena nasabah tidak akan pernah menerima kode
  dan tidak tahu harus kirim ulang.
- Staging dan test memakai tiruan: catat kode ke log **hanya** di lingkungan
  non-produksi, pengiriman selalu berhasil.

Selesai bila: test bisa berjalan tanpa menyentuh jaringan, dan tidak ada jalur di
mana kode OTP tercatat di lingkungan produksi.

---

## Fase 5 — Penutup

- Jalankan seluruh daftar uji `SKILL.md` §8.
- Telusuri log satu kali lagi khusus mencari kebocoran kode OTP.
- Perbarui `06-BUKA-REKENING-API-SPEC.md` bila ada nilai yang bergeser dari Fase 0.
- Pastikan `otp_sent_to` tersamar di **setiap** endpoint yang mengembalikannya,
  termasuk `GET /sessions/{id}`.
