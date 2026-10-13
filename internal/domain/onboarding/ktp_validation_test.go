package onboarding

import (
	"strings"
	"testing"
)

// Teks e-KTP yang realistis, dipakai beberapa test di berkas ini.
const fullKTPText = `PROVINSI DKI JAKARTA
KOTA JAKARTA SELATAN
NIK : 3174082104950001
Nama : MUHAMMAD ARDAN PRAYOGI
Tempat/Tgl Lahir : Jakarta, 21-04-1995
Jenis Kelamin : LAKI-LAKI
Gol. Darah : O
Alamat : Jl. Sudirman Kav. 45 No. 12B
RT/RW : 003/005
Kel/Desa : Senayan
Kecamatan : Kebayoran Baru
Agama : Islam
Status Perkawinan : Belum Kawin
Pekerjaan : Karyawan Swasta
Kewarganegaraan : WNI
Berlaku Hingga : SEUMUR HIDUP
`

func TestAssessKTPText_AcceptsRealCard(t *testing.T) {
	got := AssessKTPText(fullKTPText)
	if !got.IsKTP {
		t.Fatalf("kartu lengkap harus diterima, penanda ditemukan %d/%d",
			got.MarkersFound, got.MarkersTotal)
	}
	if got.Confidence() <= 50 {
		t.Errorf("kartu lengkap seharusnya berkeyakinan tinggi, dapat %v", got.Confidence())
	}
}

// Kartu yang terbaca sebagian — sudut terpotong, satu sisi gelap — harus tetap
// lolos. Gerbangnya untuk menolak dokumen lain, bukan untuk menuntut foto sempurna.
func TestAssessKTPText_AcceptsPartiallyReadCard(t *testing.T) {
	partial := `NIK : 3174082104950001
Nama : MUHAMMAD ARDAN PRAYOGI
Tempat/Tgl Lahir : Jakarta, 21-04-1995
Jenis Kelamin : LAKI-LAKI
Alamat : Jl. Sudirman Kav. 45
`
	if got := AssessKTPText(partial); !got.IsKTP {
		t.Fatalf("kartu terbaca sebagian harus diterima, penanda %d", got.MarkersFound)
	}
}

func TestAssessKTPText_RejectsNonKTP(t *testing.T) {
	cases := map[string]string{
		"kosong":                 "",
		"spasi saja":             "   \n\t\n  ",
		"teks acak":              "halo dunia, ini bukan kartu identitas",
		"struk":                  "TOKO MAKMUR\nTotal : 45000\nKembali : 5000\nTerima kasih",
		"16 digit tanpa konteks": "3174082104950001",
		"kartu lain": `SURAT IZIN MENGEMUDI
Nama : MUHAMMAD ARDAN
Berlaku Hingga : 21-04-2027
`,
	}

	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if got := AssessKTPText(text); got.IsKTP {
				t.Fatalf("harus ditolak, penanda ditemukan %d: %q", got.MarkersFound, text)
			}
		})
	}
}

// Confidence adalah rasio penanda, bukan laporan-diri mesin OCR. Nilai 99,4 yang
// dulu tersimpan di accuracy_percent tidak pernah mengukur apa pun.
func TestAssessKTPText_ConfidenceIsMarkerShare(t *testing.T) {
	got := AssessKTPText(fullKTPText)
	want := float64(got.MarkersFound) * 100 / float64(got.MarkersTotal)
	if got.Confidence() != want {
		t.Fatalf("want %v, got %v", want, got.Confidence())
	}
	if AssessKTPText("").Confidence() != 0 {
		t.Error("teks kosong harus berkeyakinan 0")
	}
}

func TestBirthDateFromNIK(t *testing.T) {
	cases := []struct {
		nik  string
		want string
		ok   bool
	}{
		{"3174082104950001", "1995-04-21", true}, // laki-laki, DD=21
		{"3174084504950002", "1995-04-05", true}, // perempuan, DD=45 → 5
		{"3201013112990001", "1999-12-31", true}, // 31 Desember
		{"3201010102000001", "2000-02-01", true}, // tahun 2000
		{"320101", "", false},                    // terlalu pendek
		{"3201013202990001", "", false},          // DD=32, tidak ada
		{"3201013113990001", "", false},          // MM=13, tidak ada
		{"3201013002990001", "", false},          // 30 Februari, tidak ada
	}

	for _, c := range cases {
		got, ok := BirthDateFromNIK(c.nik)
		if ok != c.ok {
			t.Errorf("%s: ok=%v, mau %v", c.nik, ok, c.ok)
			continue
		}
		if ok && got.Format("2006-01-02") != c.want {
			t.Errorf("%s: dapat %s, mau %s", c.nik, got.Format("2006-01-02"), c.want)
		}
	}
}

// Tahun dua digit tidak boleh jatuh di masa depan.
func TestBirthDateFromNIK_TwoDigitYearNeverFuture(t *testing.T) {
	// YY=99 harus jadi 1999, bukan 2099.
	got, ok := BirthDateFromNIK("3201012112990001")
	if !ok {
		t.Fatal("harus terbaca")
	}
	if got.Year() != 1999 {
		t.Fatalf("mau 1999, dapat %d", got.Year())
	}
}

func TestValidateNIKStructure(t *testing.T) {
	cases := map[string]struct {
		nik     string
		wantErr bool
	}{
		"valid":                   {"3174082104950001", false},
		"kode provinsi tidak ada": {"2074082104950001", true},
		"nomor urut 0000":         {"3174082104950000", true},
		"bukan 16 digit":          {"317408210495000", true},
		"ada huruf":               {"31740821049500A1", true},
		"tanggal tidak valid":     {"3174083202950001", true},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateNIKStructure(c.nik)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v, mau error=%v", err, c.wantErr)
			}
		})
	}
}

// Inti validasi tanpa Dukcapil: NIK memuat tanggal lahir dan jenis kelamin,
// jadi keduanya harus sepakat dengan yang terbaca di muka kartu.
func TestValidateKTPConsistency(t *testing.T) {
	base := KTPData{
		NIK:          "3174082104950001",
		NamaLengkap:  "MUHAMMAD ARDAN PRAYOGI",
		TanggalLahir: "1995-04-21",
		JenisKelamin: "LAKI_LAKI",
		Provinsi:     "DKI JAKARTA",
	}

	if err := ValidateKTPConsistency(base); err != nil {
		t.Fatalf("data konsisten harus lolos: %v", err)
	}

	t.Run("tanggal lahir berbeda", func(t *testing.T) {
		d := base
		d.TanggalLahir = "1995-04-22"
		if err := ValidateKTPConsistency(d); err == nil {
			t.Fatal("harus ditolak")
		}
	})

	t.Run("jenis kelamin berbeda", func(t *testing.T) {
		d := base
		d.JenisKelamin = "PEREMPUAN"
		if err := ValidateKTPConsistency(d); err == nil {
			t.Fatal("harus ditolak")
		}
	})

	t.Run("provinsi berbeda", func(t *testing.T) {
		d := base
		d.Provinsi = "JAWA BARAT"
		if err := ValidateKTPConsistency(d); err == nil {
			t.Fatal("harus ditolak")
		}
	})

	// Field yang gagal dibaca OCR bukan bukti kartunya palsu.
	t.Run("field kosong tidak dianggap bertentangan", func(t *testing.T) {
		d := KTPData{NIK: base.NIK, NamaLengkap: base.NamaLengkap}
		if err := ValidateKTPConsistency(d); err != nil {
			t.Fatalf("field kosong harus dilewati, bukan ditolak: %v", err)
		}
	})

	// Satu digit NIK yang salah baca adalah kesalahan OCR yang paling sering, dan
	// inilah yang menangkapnya: NIK-nya sendiri masih berbentuk sah.
	t.Run("satu digit NIK salah baca tertangkap", func(t *testing.T) {
		d := base
		d.NIK = "3174082204950001" // 21 → 22
		if err := ValidateKTPConsistency(d); err == nil {
			t.Fatal("NIK yang salah satu digit harus tertangkap lewat tanggal lahir")
		}
	})
}

// Penulisan jenis kelamin dari OCR beragam; perbandingan huruf-per-huruf akan
// menolak kartu yang sebenarnya benar.
func TestValidateKTPConsistency_GenderSpellingVariants(t *testing.T) {
	for _, spelling := range []string{"LAKI-LAKI", "LAKI LAKI", "LAKI_LAKI", "laki-laki"} {
		d := KTPData{
			NIK:          "3174082104950001",
			TanggalLahir: "1995-04-21",
			JenisKelamin: spelling,
		}
		if err := ValidateKTPConsistency(d); err != nil {
			t.Errorf("%q harus diterima: %v", spelling, err)
		}
	}

	for _, spelling := range []string{"PEREMPUAN", "perempuan", "WANITA"} {
		d := KTPData{
			NIK:          "3174084504950002", // DD=45 → perempuan
			TanggalLahir: "1995-04-05",
			JenisKelamin: spelling,
		}
		if err := ValidateKTPConsistency(d); err != nil {
			t.Errorf("%q harus diterima: %v", spelling, err)
		}
	}
}

func TestProvinceNameFromNIK(t *testing.T) {
	if got := ProvinceNameFromNIK("3174082104950001"); got != "DKI JAKARTA" {
		t.Errorf("dapat %q", got)
	}
	if got := ProvinceNameFromNIK("2074082104950001"); got != "" {
		t.Errorf("kode tidak terdaftar harus kosong, dapat %q", got)
	}
	if got := ProvinceNameFromNIK("3"); got != "" {
		t.Errorf("NIK terlalu pendek harus kosong, dapat %q", got)
	}
}

// Provinsi Papua hasil pemekaran 2022–2023 harus dikenali; kode lama 11–94
// menerima banyak angka yang bukan provinsi mana pun.
func TestProvinceCodes_CoversNewProvincesAndRejectsGaps(t *testing.T) {
	for _, code := range []string{"93", "94", "95", "96"} {
		if provinceCodes[code] == "" {
			t.Errorf("kode provinsi %s harus terdaftar", code)
		}
	}
	for _, code := range []string{"20", "22", "30", "40", "50", "60", "70", "80", "90"} {
		if name := provinceCodes[code]; name != "" {
			t.Errorf("kode %s bukan provinsi, tapi terdaftar sebagai %q", code, name)
		}
	}
	if len(provinceCodes) != 38 {
		t.Errorf("Indonesia punya 38 provinsi, terdaftar %d", len(provinceCodes))
	}
}

func TestSplitObjectPath(t *testing.T) {
	cases := []struct {
		path   string
		bucket string
		key    string
		ok     bool
	}{
		{"local://onboarding-ktp/sess/photo.jpg", "onboarding-ktp", "sess/photo.jpg", true},
		{"s3://onboarding-ktp/sess/photo.jpg", "onboarding-ktp", "sess/photo.jpg", true},
		{"onboarding-ktp/sess/photo.jpg", "onboarding-ktp", "sess/photo.jpg", true},
		{"local://onboarding-ktp", "", "", false},
		{"", "", "", false},
	}

	for _, c := range cases {
		bucket, key, ok := SplitObjectPath(c.path)
		if ok != c.ok || bucket != c.bucket || key != c.key {
			t.Errorf("%q: dapat (%q, %q, %v), mau (%q, %q, %v)",
				c.path, bucket, key, ok, c.bucket, c.key, c.ok)
		}
	}
}

// Parser harus membaca provinsi dan kota dari baris HEADER e-KTP, yang tidak
// punya titik dua. Tanpa ini keduanya selalu kosong untuk kartu sungguhan.
func TestParseKTPFromText_ReadsHeaderLines(t *testing.T) {
	data := ParseKTPFromText(fullKTPText)

	if data.Provinsi != "DKI JAKARTA" {
		t.Errorf("provinsi: dapat %q", data.Provinsi)
	}
	if data.Kota != "JAKARTA SELATAN" {
		t.Errorf("kota: dapat %q", data.Kota)
	}
	if data.NIK != "3174082104950001" {
		t.Errorf("nik: dapat %q", data.NIK)
	}
	if !strings.Contains(data.Alamat, "Sudirman") {
		t.Errorf("alamat: dapat %q", data.Alamat)
	}
}

func TestParseKTPFromText_ReadsKabupaten(t *testing.T) {
	text := strings.Replace(fullKTPText, "KOTA JAKARTA SELATAN", "KABUPATEN BOGOR", 1)
	if got := ParseKTPFromText(text).Kota; got != "BOGOR" {
		t.Errorf("dapat %q", got)
	}
}
