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

// MockDukcapilClient verifies NIK against a hardcoded set of test identities.
//
// Only reachable with DUKCAPIL_MODE=mock. It is NOT the default, because it
// refuses every NIK outside the four entries below — so a real e-KTP is rejected
// with OCR_DUKCAPIL_MISMATCH, which reads like the customer's card is bad. With
// DUKCAPIL_MODE=off the registry check is skipped and the response says so.
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

// MockObjectStorage stores nothing and returns a fake path.
//
// Kept only for tests that need an ObjectStorage which cannot fail. It is no
// longer the development default, because "stores nothing" produced two bugs
// that looked unrelated to storage: the KTP photo appeared not to attach (the
// photo_path column pointed at an object that never existed), and biometric face
// match had no reference to compare against. Use localfs.Storage instead.
type MockObjectStorage struct{}

func NewMockObjectStorage() *MockObjectStorage {
	return &MockObjectStorage{}
}

func (m *MockObjectStorage) Upload(_ context.Context, bucket, key string, _ []byte, _ string) (string, error) {
	return "s3://" + bucket + "/" + key, nil
}

// Download keeps nothing, so there is nothing to give back. Returning nil, nil
// rather than an error is deliberate: "no reference available" is a state the
// caller must already handle, and in that state it refuses rather than passes.
func (m *MockObjectStorage) Download(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}

func (m *MockObjectStorage) Delete(_ context.Context, _, _ string) error {
	return nil
}
