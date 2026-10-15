package pinrule

import (
	"errors"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// Paket ini dipanggil dua domain yang tidak saling kenal: kredensial nasabah di
// internal/domain/onboarding (kode akses + PIN transaksi) dan PIN meja petugas di
// internal/domain/cs. Itu sebabnya aturannya pindah ke sini — dua salinan aturan
// keamanan menyimpang, dan yang menyimpang diam-diam adalah yang paling mahal.
//
// Test di bawah mengikat KONTRAK yang dibagi keduanya: kode galat apa yang keluar untuk
// masukan apa. Perilaku helper-nya (AllSameChar, Sequential) sudah diuji dari sisi
// onboarding; yang belum pernah diuji di batas paket adalah kedua fungsi exported-nya.

func TestValidatePIN(t *testing.T) {
	cases := []struct {
		name string
		pin  string
		want error
	}{
		{"enam digit acak", "730514", nil},
		{"dua angka berulang bukan satu", "121212", nil},

		{"kosong", "", apperr.CredWeakPIN},
		{"lima digit", "73051", apperr.CredWeakPIN},
		{"tujuh digit", "7305148", apperr.CredWeakPIN},
		{"ada huruf", "73051a", apperr.CredWeakPIN},
		{"spasi di tengah", "730 14", apperr.CredWeakPIN},
		{"satu angka berulang", "111111", apperr.CredWeakPIN},
		{"berurutan naik", "123456", apperr.CredWeakPIN},
		{"berurutan turun", "654321", apperr.CredWeakPIN},

		// Tiga karakter berurutan cukup, di mana pun posisinya. Ini yang membedakan
		// aturan ini dari implementasi lama yang baru berbunyi di empat karakter.
		{"tiga berurutan di tengah", "834590", apperr.CredWeakPIN},
		{"dua berurutan saja masih lolos", "953410", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePIN(tc.pin)
			if !errors.Is(err, tc.want) {
				t.Errorf("ValidatePIN(%q) = %v, mau %v", tc.pin, err, tc.want)
			}
		})
	}
}

// Kode akses nasabah membolehkan huruf; PIN tidak. Keduanya memakai helper yang sama,
// jadi yang perlu dijaga adalah batas itu tidak kabur ke salah satu sisi.
func TestValidateAccessCode(t *testing.T) {
	cases := []struct {
		name string
		code string
		want error
	}{
		{"huruf dan angka campur", "b7k2m9", nil},
		{"semua angka boleh", "730514", nil},

		{"kosong", "", apperr.CredWeakAccessCode},
		{"lima karakter", "b7k2m", apperr.CredWeakAccessCode},
		{"ada simbol", "b7k2m-", apperr.CredWeakAccessCode},
		{"satu huruf berulang", "aaaaaa", apperr.CredWeakAccessCode},
		{"berurutan huruf", "abcdef", apperr.CredWeakAccessCode},

		// Besar-kecil huruf disamakan sebelum diperiksa: "ABCdef" sama mudahnya
		// ditebak dengan "abcdef", dan yang mengetiknya tidak merasa berbeda.
		{"berurutan beda kapitalisasi", "ABCdef", apperr.CredWeakAccessCode},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAccessCode(tc.code)
			if !errors.Is(err, tc.want) {
				t.Errorf("ValidateAccessCode(%q) = %v, mau %v", tc.code, err, tc.want)
			}
		})
	}
}

// Kode galatnya berbeda, dan bedanya terlihat di layar: satu jalur menyetel kode akses
// login nasabah, satu lagi PIN. Menukar keduanya akan menyuruh orang memperbaiki hal
// yang salah.
func TestValidatorsReturnDistinctCodes(t *testing.T) {
	if errors.Is(ValidatePIN("111111"), apperr.CredWeakAccessCode) {
		t.Error("PIN lemah tidak boleh menjawab dengan kode galat kode akses")
	}
	if errors.Is(ValidateAccessCode("aaaaaa"), apperr.CredWeakPIN) {
		t.Error("kode akses lemah tidak boleh menjawab dengan kode galat PIN")
	}
}
