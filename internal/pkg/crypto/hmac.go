package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HMACHasher produces HMAC-SHA256 hashes for deterministic lookups
// (phone_hash, email_hash). NOT plain SHA-256 — that is rainbow-tableable.
type HMACHasher struct {
	key []byte
}

func NewHMACHasher(secret string) *HMACHasher {
	return &HMACHasher{key: []byte(secret)}
}

// Hash computes HMAC-SHA256 of the input after lowercasing and trimming.
// This ensures "ABC " and "abc" produce the same hash.
func (h *HMACHasher) Hash(input string) string {
	normalized := strings.ToLower(strings.TrimSpace(input))
	mac := hmac.New(sha256.New, h.key)
	mac.Write([]byte(normalized))
	return hex.EncodeToString(mac.Sum(nil))
}