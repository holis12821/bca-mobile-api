package onboarding

import (
	"context"
	"log/slog"
	"strings"
)

// Known test NIKs that the mock Dukcapil client accepts.
var mockDukcapilNIKs = map[string]string{
	"3174082104950001": "MUHAMMAD ARDAN PRAYOGI",
	"3174084504950002": "SITI NURHALIZA",
	"3201010101900001": "TEST USER MALE",
	"3201014101900001": "TEST USER FEMALE",
}

// MockDukcapilClient is a development/staging stub that verifies NIK against
// a hardcoded set of test identities. Production should use the real HTTP client.
type MockDukcapilClient struct{}

func NewMockDukcapilClient() *MockDukcapilClient {
	return &MockDukcapilClient{}
}

func (m *MockDukcapilClient) VerifyNIK(_ context.Context, nik, nama string) (bool, error) {
	expected, ok := mockDukcapilNIKs[nik]
	if !ok {
		slog.Info("mock dukcapil: unknown NIK", "nik", nik)
		return false, nil
	}

	match := strings.EqualFold(expected, nama)
	slog.Info("mock dukcapil verify",
		"nik", nik,
		"expected", expected,
		"given", nama,
		"match", match,
	)
	return match, nil
}

// MockOCREngine returns a hardcoded OCR result for development.
type MockOCREngine struct{}

func NewMockOCREngine() *MockOCREngine {
	return &MockOCREngine{}
}

func (m *MockOCREngine) ExtractText(_ context.Context, _ []byte) (string, float64, error) {
	// Return a realistic KTP text layout for testing
	raw := `PROVINSI DKI JAKARTA
KOTA JAKARTA SELATAN
NIK : 3174082104950001
Nama : MUHAMMAD ARDAN PRAYOGI
Tempat/Tgl Lahir : Jakarta, 21-04-1995
Jenis Kelamin : LAKI-LAKI
Alamat : Jl. Sudirman Kav. 45 No. 12B
RT/RW : 003/005
Kel/Desa : Senayan
Kecamatan : Kebayoran Baru
Agama : Islam
Status Perkawinan : Belum Kawin
`
	return raw, 99.4, nil
}

// MockObjectStorage stores nothing and returns a fake path.
type MockObjectStorage struct{}

func NewMockObjectStorage() *MockObjectStorage {
	return &MockObjectStorage{}
}

func (m *MockObjectStorage) Upload(_ context.Context, bucket, key string, _ []byte, _ string) (string, error) {
	return "s3://" + bucket + "/" + key, nil
}

func (m *MockObjectStorage) Delete(_ context.Context, _, _ string) error {
	return nil
}
