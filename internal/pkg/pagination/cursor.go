package pagination

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MutationCursor is a keyset pagination cursor for account_mutations.
// Matches ORDER BY transaction_date DESC, created_at DESC, id DESC.
type MutationCursor struct {
	Date      string `json:"d"` // YYYY-MM-DD
	CreatedAt string `json:"c"` // RFC3339
	ID        string `json:"i"` // UUID
}

// Encode returns a base64-encoded cursor string.
func (c MutationCursor) Encode() string {
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

// DecodeMutationCursor parses a base64-encoded cursor.
func DecodeMutationCursor(s string) (*MutationCursor, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding")
	}
	var c MutationCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("invalid cursor format")
	}
	// Validate fields
	if _, err := time.Parse("2006-01-02", c.Date); err != nil {
		return nil, fmt.Errorf("invalid cursor date")
	}
	if _, err := time.Parse(time.RFC3339Nano, c.CreatedAt); err != nil {
		return nil, fmt.Errorf("invalid cursor created_at")
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return nil, fmt.Errorf("invalid cursor id")
	}
	return &c, nil
}

// HistoryCursor is a keyset pagination cursor for transactions.
// Matches ORDER BY created_at DESC, id DESC.
type HistoryCursor struct {
	CreatedAt string `json:"c"` // RFC3339
	ID        string `json:"i"` // UUID
}

// Encode returns a base64-encoded cursor string.
func (c HistoryCursor) Encode() string {
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

// DecodeHistoryCursor parses a base64-encoded cursor.
func DecodeHistoryCursor(s string) (*HistoryCursor, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding")
	}
	var c HistoryCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("invalid cursor format")
	}
	if _, err := time.Parse(time.RFC3339Nano, c.CreatedAt); err != nil {
		return nil, fmt.Errorf("invalid cursor created_at")
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return nil, fmt.Errorf("invalid cursor id")
	}
	return &c, nil
}