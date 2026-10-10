package onboarding

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Validasi e-KTP yang tidak bergantung pada Dukcapil.
//
// Kenapa ini ada: sebelumnya satu-satunya pemeriksaan adalah bentuk NIK (16 digit,
// kode provinsi 11–94, DD/MM masuk akal) lalu pencocokan Dukcapil. Di lingkungan
// tanpa Dukcapil, itu berarti foto apa pun yang kebetulan memuat 16 digit lolos
// sebagai e-KTP. Dibuktikan dengan mengunggah persegi abu-abu polos: diterima
// dengan accuracy 99,4 dan identitas lengkap.
//
// Yang dipakai di sini tidak butuh layanan luar sama sekali, karena **NIK
// Indonesia memvalidasi dirinya sendiri**:
//
//	PP RR SS DD MM YY NNNN
//	│  │  │  │  │  │  └── nomor urut (0001..9999)
//	│  │  │  │  │  └───── tahun lahir, 2 digit
//	│  │  │  │  └──────── bulan lahir
//	│  │  │  └─────────── tanggal lahir, +40 bila perempuan
//	│  │  └────────────── kode kecamatan
//	│  └───────────────── kode kabupaten/kota
//	└──────────────────── kode provinsi
//
// Tanggal lahir dan jenis kelamin karena itu bisa **dicocokkan** dengan hasil OCR.
// Dua sumber independen yang harus sepakat jauh lebih kuat daripada satu sumber
// yang dipercaya — dan menangkap OCR yang salah baca satu digit NIK, serta foto
// dokumen yang bukan e-KTP milik orang yang sama.

// provinceCodes adalah kode provinsi BPS yang benar-benar ada.
//
// Rentang 11–94 yang dipakai ValidateNIK menerima banyak angka yang bukan
// provinsi mana pun (20, 30, 40, 44, ...). Daftar eksplisit menolak itu.
// 38 provinsi per pemekaran Papua 2022–2023.
var provinceCodes = map[string]string{
	"11": "ACEH",
	"12": "SUMATERA UTARA",
	"13": "SUMATERA BARAT",
	"14": "RIAU",
	"15": "JAMBI",
	"16": "SUMATERA SELATAN",
	"17": "BENGKULU",
	"18": "LAMPUNG",
	"19": "KEPULAUAN BANGKA BELITUNG",
	"21": "KEPULAUAN RIAU",
	"31": "DKI JAKARTA",
	"32": "JAWA BARAT",
	"33": "JAWA TENGAH",
	"34": "DI YOGYAKARTA",
	"35": "JAWA TIMUR",
	"36": "BANTEN",
	"51": "BALI",
	"52": "NUSA TENGGARA BARAT",
	"53": "NUSA TENGGARA TIMUR",
	"61": "KALIMANTAN BARAT",
	"62": "KALIMANTAN TENGAH",
	"63": "KALIMANTAN SELATAN",
	"64": "KALIMANTAN TIMUR",
	"65": "KALIMANTAN UTARA",
	"71": "SULAWESI UTARA",
	"72": "SULAWESI TENGAH",
	"73": "SULAWESI SELATAN",
	"74": "SULAWESI TENGGARA",
	"75": "GORONTALO",
	"76": "SULAWESI BARAT",
	"81": "MALUKU",
	"82": "MALUKU UTARA",
	"91": "PAPUA",
	"92": "PAPUA BARAT",
	"93": "PAPUA SELATAN",
	"94": "PAPUA TENGAH",
	"95": "PAPUA PEGUNUNGAN",
	"96": "PAPUA BARAT DAYA",
}

// ktpMarkers adalah penanda tata letak e-KTP.
//
// Yang dicari adalah **label**, bukan nilai: nilainya berbeda tiap orang, labelnya
// sama di setiap kartu. Masing-masing ditulis beberapa varian karena OCR rutin
// salah baca satu-dua huruf dan kadang menghilangkan titik dua.
var ktpMarkers = [][]string{
	{"NIK"},
	{"NAMA"},
	{"TEMPAT/TGL LAHIR", "TEMPAT/TGLLAHIR", "TGL LAHIR", "TEMPAT TGL LAHIR"},
	{"JENIS KELAMIN", "JENISKELAMIN"},
	{"ALAMAT"},
	{"RT/RW", "RTRW"},
	{"KEL/DESA", "KEL / DESA", "KELURAHAN", "KEL/DESA"},
	{"KECAMATAN"},
	{"AGAMA"},
	{"STATUS PERKAWINAN", "STATUSPERKAWINAN"},
	{"KEWARGANEGARAAN", "KEWARGANEGARAN"},
	{"BERLAKU HINGGA", "BERLAKUHINGGA"},
	{"PEKERJAAN"},
	{"GOL. DARAH", "GOL DARAH", "GOLDARAH"},
	{"PROVINSI"},
	{"NEGARA REPUBLIK INDONESIA", "REPUBLIK INDONESIA"},
}

// minKTPMarkers adalah jumlah penanda minimum sebelum teks diperlakukan sebagai
// e-KTP.
//
// Lima dipilih supaya satu kartu yang terbaca sebagian — sudut terpotong, satu
// sisi gelap — tetap lolos, sementara foto yang bukan KTP tidak punya peluang:
// selembar struk atau layar ponsel tidak memuat lima label e-KTP sekaligus.
const minKTPMarkers = 5

// KTPTextAssessment melaporkan seberapa jauh teks ini menyerupai e-KTP.
type KTPTextAssessment struct {
	// MarkersFound adalah jumlah penanda tata letak yang ditemukan.
	MarkersFound int

	// MarkersTotal adalah jumlah penanda yang dicari, untuk menghitung rasio.
	MarkersTotal int

	// IsKTP true bila penanda yang ditemukan mencapai minKTPMarkers.
	IsKTP bool
}

// Confidence adalah rasio penanda yang ditemukan, 0–100.
//
// Ini **bukan** confidence OCR. Mesin OCR melaporkan seberapa yakin ia membaca
// piksel; angka ini melaporkan seberapa yakin yang terbaca itu e-KTP. Dulu
// nilai 99,4 dari mesin tiruan dipakai sebagai keduanya sekaligus, yang membuat
// gambar tanpa teks apa pun tampil sebagai hasil nyaris sempurna.
func (a KTPTextAssessment) Confidence() float64 {
	if a.MarkersTotal == 0 {
		return 0
	}
	return float64(a.MarkersFound) * 100 / float64(a.MarkersTotal)
}

// AssessKTPText memeriksa apakah teks OCR berasal dari e-KTP.
func AssessKTPText(rawText string) KTPTextAssessment {
	// Spasi dirapatkan jadi satu supaya "TEMPAT/TGL  LAHIR" tetap cocok, dan
	// huruf diseragamkan karena kapitalisasi hasil OCR tidak bisa diandalkan.
	haystack := strings.ToUpper(strings.Join(strings.Fields(rawText), " "))

	found := 0
	for _, variants := range ktpMarkers {
		for _, v := range variants {
			if strings.Contains(haystack, v) {
				found++
				break
			}
		}
	}

	return KTPTextAssessment{
		MarkersFound: found,
		MarkersTotal: len(ktpMarkers),
		IsKTP:        found >= minKTPMarkers,
	}
}

// BirthDateFromNIK membaca tanggal lahir yang tertanam di NIK.
//
// Tahun hanya dua digit, jadi abadnya harus ditebak. Aturannya: tahun yang
// menghasilkan usia masuk akal untuk pemohon rekening. YY di atas tahun ini
// berarti 19xx — orang tidak bisa lahir di masa depan.
func BirthDateFromNIK(nik string) (time.Time, bool) {
	if len(nik) < 12 {
		return time.Time{}, false
	}

	dd, err1 := strconv.Atoi(nik[6:8])
	mm, err2 := strconv.Atoi(nik[8:10])
	yy, err3 := strconv.Atoi(nik[10:12])
	if err1 != nil || err2 != nil || err3 != nil {
		return time.Time{}, false
	}

	// Perempuan: tanggal + 40.
	if dd > 40 {
		dd -= 40
	}
	if dd < 1 || dd > 31 || mm < 1 || mm > 12 {
		return time.Time{}, false
	}

	now := time.Now().UTC()
	year := now.Year()/100*100 + yy
	if year > now.Year() {
		year -= 100
	}

	date := time.Date(year, time.Month(mm), dd, 0, 0, 0, 0, time.UTC)
	// Date() menggulung tanggal yang tidak ada (31 Februari → 2/3 Maret). Yang
	// tergulung bukan tanggal yang valid, jadi ditolak alih-alih diterima diam-diam.
	if date.Day() != dd || date.Month() != time.Month(mm) {
		return time.Time{}, false
	}

	return date, true
}

// ValidateNIKStructure memeriksa bentuk NIK, termasuk kode provinsi yang nyata.
//
// Pelengkap ValidateNIK, bukan penggantinya: yang itu menjaga kontrak lama dan
// dipakai di tempat lain. Yang ini lebih ketat.
func ValidateNIKStructure(nik string) error {
	if err := ValidateNIK(nik); err != nil {
		return err
	}

	if _, ok := provinceCodes[nik[:2]]; !ok {
		return fmt.Errorf("kode provinsi %s tidak terdaftar", nik[:2])
	}

	// Nomor urut 0000 tidak pernah diterbitkan; kemunculannya berarti OCR
	// membaca empat digit terakhir dari tempat yang salah.
	if nik[12:16] == "0000" {
		return fmt.Errorf("nomor urut NIK tidak boleh 0000")
	}

	if _, ok := BirthDateFromNIK(nik); !ok {
		return fmt.Errorf("tanggal lahir di dalam NIK tidak valid")
	}

	return nil
}

// ValidateKTPConsistency mencocokkan NIK dengan field hasil OCR.
//
// Inilah pemeriksaan yang paling berguna tanpa Dukcapil, dan yang paling sering
// menangkap OCR yang salah: NIK punya tanggal lahir dan jenis kelamin di dalam
// dirinya, jadi keduanya harus sepakat dengan yang terbaca di muka kartu. Satu
// digit NIK yang salah baca hampir selalu merusak salah satu dari dua kecocokan
// ini.
//
// Field yang **kosong** tidak dianggap bertentangan: OCR sering gagal membaca
// satu baris, dan itu bukan bukti kartunya palsu. Yang ditolak hanya nilai yang
// terbaca dan **berbeda**.
func ValidateKTPConsistency(data KTPData) error {
	if err := ValidateNIKStructure(data.NIK); err != nil {
		return err
	}

	nikBirth, ok := BirthDateFromNIK(data.NIK)
	if !ok {
		return fmt.Errorf("tanggal lahir di dalam NIK tidak valid")
	}

	if data.TanggalLahir != "" {
		parsed, err := time.Parse("2006-01-02", data.TanggalLahir)
		if err != nil {
			return fmt.Errorf("tanggal lahir %q tidak bisa dibaca", data.TanggalLahir)
		}
		if !parsed.Equal(nikBirth) {
			return fmt.Errorf(
				"tanggal lahir %s tidak cocok dengan yang tertanam di NIK (%s)",
				data.TanggalLahir, nikBirth.Format("2006-01-02"),
			)
		}
	}

	if data.JenisKelamin != "" {
		nikGender := GenderFromNIK(data.NIK)
		if !sameGender(data.JenisKelamin, nikGender) {
			return fmt.Errorf(
				"jenis kelamin %q tidak cocok dengan yang tertanam di NIK (%s)",
				data.JenisKelamin, nikGender,
			)
		}
	}

	// Provinsi dibandingkan hanya bila OCR berhasil membacanya. Nama provinsi di
	// kartu lama bisa berbeda dari nama sekarang (pemekaran), jadi yang diuji
	// adalah saling-memuat, bukan kesamaan persis.
	if data.Provinsi != "" {
		expected := provinceCodes[data.NIK[:2]]
		got := strings.ToUpper(strings.TrimSpace(data.Provinsi))
		if expected != "" && !strings.Contains(expected, got) && !strings.Contains(got, expected) {
			return fmt.Errorf(
				"provinsi %q tidak cocok dengan kode provinsi di NIK (%s)",
				data.Provinsi, expected,
			)
		}
	}

	return nil
}

// sameGender membandingkan dua penulisan jenis kelamin.
//
// Hasil OCR bisa "LAKI-LAKI", "LAKI LAKI", atau "LAKI_LAKI"; konstanta internal
// memakai "LAKI_LAKI". Perbandingan huruf-per-huruf akan menolak kartu yang
// sebenarnya benar, jadi pemisahnya dibuang lebih dulu.
func sameGender(a, b string) bool {
	return normalizeGenderWord(a) == normalizeGenderWord(b)
}

func normalizeGenderWord(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", "_", "", " ", "").Replace(s)
	switch {
	case strings.HasPrefix(s, "LAKI"):
		return "LAKILAKI"
	case strings.HasPrefix(s, "PEREMPUAN"), strings.HasPrefix(s, "WANITA"):
		return "PEREMPUAN"
	default:
		return s
	}
}

// ProvinceNameFromNIK mengembalikan nama provinsi dari dua digit pertama NIK.
func ProvinceNameFromNIK(nik string) string {
	if len(nik) < 2 {
		return ""
	}
	return provinceCodes[nik[:2]]
}
