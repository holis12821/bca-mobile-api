package account

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// MaskPhone masks a phone number: 0812****5678.
//
// The previous version computed strings.Repeat("*", len-8) behind a guard that
// only rejected len <= 4, so any number 5–7 characters long panicked with
// "strings: negative Repeat count" and the endpoint answered 500.
func MaskPhone(phone string) string {
	const head, tail = 4, 4

	if len(phone) <= head {
		return phone
	}
	if len(phone) <= head+tail {
		// Too short to show both ends without overlapping: keep the head and
		// star out the rest.
		return phone[:head] + strings.Repeat("*", len(phone)-head)
	}
	return phone[:head] + strings.Repeat("*", len(phone)-head-tail) + phone[len(phone)-tail:]
}

// MaskEmail masks an email: n***s@gmail.com.
func MaskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email
	}
	local, domain := email[:at], email[at:]
	if len(local) <= 2 {
		return local + "***" + domain
	}
	return string(local[0]) + strings.Repeat("*", len(local)-2) + string(local[len(local)-1]) + domain
}

// MaskNIK menyisakan 4 digit depan dan 2 belakang: 3171**********34.
//
// Empat depan adalah kode wilayah — berguna memastikan asal nasabah tanpa
// mengidentifikasinya. Dua belakang cukup membedakan dua NIK yang mirip saat petugas
// mencocokkan dengan dokumen di tangannya.
//
// Di sini, bukan di paket yang memakainya, karena DUA jalur membutuhkannya: detail sesi
// onboarding sisi CS dan profil nasabah sisi CS. Dua salinan kebijakan penyamaran NIK
// adalah dua tempat yang harus diubah bersamaan, dan yang kedua akan terlewat.
func MaskNIK(nik string) string {
	const head, tail = 4, 2

	if len(nik) <= head+tail {
		return strings.Repeat("*", len(nik))
	}
	return nik[:head] + strings.Repeat("*", len(nik)-head-tail) + nik[len(nik)-tail:]
}

// MaskAccountNumber renders the last four digits: ****4567.
func MaskAccountNumber(accountNumber string) string {
	if accountNumber == "" {
		return ""
	}
	if len(accountNumber) <= 4 {
		return strings.Repeat("*", len(accountNumber))
	}
	return "****" + accountNumber[len(accountNumber)-4:]
}

// generateNumericOTP returns a uniformly distributed n-digit code from
// crypto/rand.
func generateNumericOTP(digits int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, n.Int64()), nil
}

func hashOTPCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}
