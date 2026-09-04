package crypto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func TestHMAC_Deterministic(t *testing.T) {
	h := crypto.NewHMACHasher("secret")
	hash1 := h.Hash("0812345678")
	hash2 := h.Hash("0812345678")
	assert.Equal(t, hash1, hash2)
}

func TestHMAC_LowercaseAndTrim(t *testing.T) {
	h := crypto.NewHMACHasher("secret")
	hash1 := h.Hash("ABC ")
	hash2 := h.Hash("abc")
	assert.Equal(t, hash1, hash2, "should normalize: lowercase + trim before hashing")
}

func TestHMAC_DifferentKeys(t *testing.T) {
	h1 := crypto.NewHMACHasher("key1")
	h2 := crypto.NewHMACHasher("key2")
	hash1 := h1.Hash("same-input")
	hash2 := h2.Hash("same-input")
	assert.NotEqual(t, hash1, hash2, "different HMAC keys should produce different hashes")
}

func TestHMAC_DifferentInputs(t *testing.T) {
	h := crypto.NewHMACHasher("secret")
	hash1 := h.Hash("input-a")
	hash2 := h.Hash("input-b")
	assert.NotEqual(t, hash1, hash2)
}