# Twilio — menelusuri OTP yang tidak sampai

Rujukan pendamping `SKILL.md`. Dipakai saat ada keluhan "OTP tidak masuk",
bukan saat menulis kode baru.

---

## 1. Urutan memeriksa, dari yang paling sering

Jangan mulai dari kode. Delapan dari sepuluh kasus berhenti di langkah 1–3.

1. **`SMS_PROVIDER` benar-benar terisi di proses yang jalan?** Bukan di `.env`
   laptop — di environment deployment-nya. Kosong di luar `development`
   seharusnya menolak boot, jadi kalau server hidup tapi tidak mengirim,
   curigai proses lama yang belum restart.
2. **Geo Permissions Indonesia terbuka?** Messaging → Settings → Geo
   Permissions. Kode `21408`.
3. **Akun masih trial dan nomornya belum diverifikasi?** Kode `21608`.
4. **Nomornya lolos normalisasi?** Cari `ErrInvalidPhone` di log. Nomor dengan
   prefiks telepon rumah atau salah jumlah digit ditolak sebelum HTTP.
5. **Twilio menerima pesannya?** Console → Monitor → Logs → Messaging. Cari
   `message_sid` dari log aplikasi.
6. **Operator menolak di hilir?** Status akhir `undelivered` dengan kode
   `30003`–`30008`. Di titik ini masalahnya bukan di repo ini.

---

> **Nomor tujuan tidak akan terlihat di log kita.** `21211` dan `21408`
> mengutip nomor lengkap di dalam pesan Twilio, dan pesan itu memang ikut
> di-log karena tanpa teksnya sebuah `400` tidak bisa didiagnosis. Deretan ≥ 7
> digit diganti `[number redacted]` sebelum sampai ke `slog`, jadi untuk
> memastikan nomor mana yang kena, pakai `phone_suffix` di baris log yang sama
> atau Messages log di console Twilio — bukan teks errornya.

## 2. Kode error yang paling sering muncul

Penolakan saat request (dibalas langsung, **tidak** di-retry):

| Kode | Arti | Tindakan |
|---|---|---|
| `20003` | Authentication Error | `SMS_ACCOUNT_SID`/`SMS_AUTH_TOKEN` salah, atau dipakai API Key `SK…` |
| `20404` | Resource not found | Account SID di URL tidak cocok dengan kredensialnya |
| `21211` | Invalid 'To' phone number | Nomor tidak lolos validasi Twilio meski lolos normalisasi lokal |
| `21408` | Permission to send to region not enabled | Buka Geo Permissions Indonesia |
| `20429` | Too Many Requests | Akun sedang dibatasi Twilio. Gateway mematuhi `Retry-After` selama ≤ 2 detik; lebih dari itu percobaan kedua dibatalkan dan jawabannya `503 OTP_DELIVERY_FAILED` |
| `21606` | 'From' bukan nomor yang sah / tidak SMS-capable | Periksa `SMS_SENDER` |
| `21608` | Akun trial, nomor tujuan belum diverifikasi | Verifikasi nomor tester, atau upgrade akun |
| `21610` | Penerima pernah mengirim STOP | Nomor itu memblokir pengirim; tidak bisa dipaksa dari sisi kita |
| `21612` | Tidak ada rute ke nomor itu | Ganti pengirim, atau pakai Messaging Service |

Diterima lalu gagal di hilir (`status: failed`/`undelivered`, dibalas sebagai
error oleh gateway):

| Kode | Arti |
|---|---|
| `30003` | Handset tidak terjangkau (mati / di luar jangkauan) |
| `30005` | Nomor tidak dikenal operator |
| `30006` | Nomor telepon rumah, atau operator tidak bisa menerima |
| `30007` | Disaring operator sebagai spam |
| `30008` | Gagal tanpa keterangan dari operator |

`30007` yang paling perlu dibaca pelan-pelan: operator Indonesia menyaring SMS
massal. Pesan OTP yang mengandung tautan jauh lebih sering kena. Jangan
menambahkan URL ke `OTPMessage`.

Daftar lengkap ada di dokumentasi Twilio; tabel ini hanya yang terlihat di
trafik OTP Indonesia.

---

## 3. Membaca log aplikasi

Pengiriman berhasil — satu baris per OTP:

```
level=INFO msg="sms delivered to provider" provider=twilio phone_suffix=7890
  operator=Telkomsel message_sid=SM… message_status=queued attempt=1
```

Gagal setelah retry habis:

```
level=ERROR msg="sms provider refused the OTP" provider=twilio phone_suffix=7890
  operator=Indosat error="twilio http 400: code 21608: unverified number"
```

Tidak ada provider terpasang (hanya mungkin di `development`):

```
level=INFO msg="mock sms gateway: OTP sent" to=+6281234567890 operator=Telkomsel otp=123456
```

`message_status=queued` berarti **Twilio menerima**, bukan **HP menerima**.
Tanpa `StatusCallback` (belum dibuat — lihat `SKILL.md` §2), status sampai di
handset hanya bisa dilihat dari console Twilio.

Yang tidak boleh ada di log mana pun di luar `development`: kode OTP, auth
token, nomor lengkap. Kalau salah satu muncul, itu bug — perbaiki sebelum
melanjutkan apa pun.

---

## 4. Perintah yang berguna

```bash
# Hanya baris pengiriman SMS
make dev 2>&1 | grep -E "sms (delivered|provider refused)|mock sms gateway"

# Keluhan per operator — apakah terpusat di satu operator?
grep "sms provider refused" server.log | grep -oE 'operator=[A-Za-z]+' | sort | uniq -c

# Pastikan tidak ada kebocoran kode (harus nol di luar development)
grep -nE '\botp=[0-9]{6}\b' server.log

# Uji gateway tanpa jaringan dan tanpa Docker
go test ./internal/pkg/sms/ -run Twilio -v
```

---

## 5. Audit trail

`OTP_SENT` membawa `delivered` (bool). Itu yang membedakan "nasabah salah ketik"
dari "SMS tidak pernah berangkat" saat menelusuri keluhan lama, dan satu-satunya
jejak yang tersisa setelah log rotasi. Query audit per sesi ada di skill
`buka-rekening-otp` §7.
