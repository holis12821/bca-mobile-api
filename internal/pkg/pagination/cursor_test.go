package pagination_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/pagination"
)

func TestMutationCursor_EncodeDecodeRoundtrip(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()

	original := pagination.MutationCursor{
		Date:      now.Format("2006-01-02"),
		CreatedAt: now.Format(time.RFC3339Nano),
		ID:        id.String(),
	}

	encoded := original.Encode()
	if encoded == "" {
		t.Fatal("encoded cursor is empty")
	}

	decoded, err := pagination.DecodeMutationCursor(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.Date != original.Date {
		t.Fatalf("date mismatch: %s != %s", decoded.Date, original.Date)
	}
	if decoded.CreatedAt != original.CreatedAt {
		t.Fatalf("created_at mismatch: %s != %s", decoded.CreatedAt, original.CreatedAt)
	}
	if decoded.ID != original.ID {
		t.Fatalf("id mismatch: %s != %s", decoded.ID, original.ID)
	}
}

func TestMutationCursor_MalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"not base64", "!!!invalid!!!"},
		{"valid base64 but bad json", "aGVsbG8="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pagination.DecodeMutationCursor(tt.input)
			if err == nil {
				t.Fatal("expected error for malformed cursor")
			}
		})
	}
}

func TestHistoryCursor_EncodeDecodeRoundtrip(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()

	original := pagination.HistoryCursor{
		CreatedAt: now.Format(time.RFC3339Nano),
		ID:        id.String(),
	}

	encoded := original.Encode()
	decoded, err := pagination.DecodeHistoryCursor(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.CreatedAt != original.CreatedAt {
		t.Fatalf("created_at mismatch")
	}
	if decoded.ID != original.ID {
		t.Fatalf("id mismatch")
	}
}

func TestHistoryCursor_MalformedInput(t *testing.T) {
	_, err := pagination.DecodeHistoryCursor("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error")
	}
}