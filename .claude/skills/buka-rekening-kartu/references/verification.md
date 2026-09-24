# Verifikasi Lintas Tim — Pilih Jenis Kartu Paspor

Dikerjakan **sebelum menutup pekerjaan**, bersama tim client Android. Tujuh butir
ini yang paling sering menjadi sumber bug integrasi setelah backend dianggap
selesai.

## Checklist

- [ ] Nilai `style` yang dikirim hanya `BLUE`, `GOLD`, `PLATINUM`
- [ ] Nilai `badge_key` yang dikirim sudah punya padanan di `strings.xml` client
- [ ] Tidak ada hex warna atau string `"Rp..."` di seluruh payload
- [ ] Urutan `cards` dari server sama dengan urutan yang client tampilkan
- [ ] Client mengirim `card_catalog_version` yang ia terima, apa adanya
- [ ] `CARD_SELECTION` dikenali `OnboardingStep.fromWire` di client **sebelum**
      sisipan diaktifkan di produksi
- [ ] Kartu `OUT_OF_STOCK` tampil tapi tidak bisa dipilih — bukan disembunyikan

## Catatan butir terakhir

Butir terakhir adalah keputusan produk, bukan teknis: menyembunyikan kartu
membuat nasabah bertanya ke call center kenapa Platinum hilang. Menampilkannya
sebagai tidak tersedia menjawab pertanyaan itu sebelum ditanyakan.

## Kenapa butir keenam berurutan

`CARD_SELECTION` disisipkan ke state machine di sisi server. Kalau client lama
menerima step yang tidak dikenal `OnboardingStep.fromWire`, flow onboarding
berhenti di layar kosong — bukan gagal dengan pesan. Aktifkan feature flag
`onboarding.card_selection.enabled` **hanya setelah** build client yang mengenal
step itu sudah tersebar.

Urutan aman:

1. Backend deploy dengan flag `false` — perilaku lama, tidak ada yang berubah.
2. Client rilis dengan dukungan `CARD_SELECTION` dan katalog kartu.
3. Flag dinaikkan ke `true` setelah adopsi versi client mencukupi.
4. Rollback = turunkan flag, tanpa rollback deployment backend.
