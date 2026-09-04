package crypto_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func testAESKey() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return hex.EncodeToString(key)
}

func TestAES_EncryptDecrypt(t *testing.T) {
	a, err := crypto.NewAES(testAESKey())
	require.NoError(t, err)

	plaintext := []byte("sensitive PII data")
	ciphertext, err := a.Encrypt(plaintext)
	require.NoError(t, err)

	decrypted, err := a.Decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestAES_UniqueNonce(t *testing.T) {
	a, err := crypto.NewAES(testAESKey())
	require.NoError(t, err)

	plaintext := []byte("same input")
	ct1, err := a.Encrypt(plaintext)
	require.NoError(t, err)

	ct2, err := a.Encrypt(plaintext)
	require.NoError(t, err)

	assert.NotEqual(t, ct1, ct2, "two encryptions of same input must produce different ciphertext (unique nonce)")
}

func TestAES_DecryptCorrupted(t *testing.T) {
	a, err := crypto.NewAES(testAESKey())
	require.NoError(t, err)

	ciphertext, err := a.Encrypt([]byte("test"))
	require.NoError(t, err)

	// Corrupt the ciphertext
	ciphertext[len(ciphertext)-1] ^= 0xff

	_, err = a.Decrypt(ciphertext)
	assert.Error(t, err)
}

func TestAES_InvalidKeyLength(t *testing.T) {
	_, err := crypto.NewAES("abcdef") // too short
	assert.Error(t, err)
}