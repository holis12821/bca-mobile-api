package crypto_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func generateTestRSAKeyPair(t *testing.T) *crypto.RSAKeyPair {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: func() []byte { b, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey); return b }(),
	})

	kp, err := crypto.ParseRSAKeyPair(privPEM, pubPEM)
	require.NoError(t, err)
	return kp
}

func TestRSA_EncryptDecryptPIN(t *testing.T) {
	kp := generateTestRSAKeyPair(t)

	payload := crypto.PINPayload{
		PIN:   "123456",
		Nonce: "test-nonce-uuid",
		TS:    time.Now().Unix(),
	}

	encrypted, err := kp.EncryptPIN(payload)
	require.NoError(t, err)

	decrypted, err := kp.DecryptPIN(encrypted)
	require.NoError(t, err)
	assert.Equal(t, payload.PIN, decrypted.PIN)
	assert.Equal(t, payload.Nonce, decrypted.Nonce)
	assert.Equal(t, payload.TS, decrypted.TS)
}

func TestRSA_DecryptWithWrongKey(t *testing.T) {
	kp1 := generateTestRSAKeyPair(t)
	kp2 := generateTestRSAKeyPair(t)

	payload := crypto.PINPayload{PIN: "123456", Nonce: "n", TS: time.Now().Unix()}
	encrypted, err := kp1.EncryptPIN(payload)
	require.NoError(t, err)

	_, err = kp2.DecryptPIN(encrypted)
	assert.Error(t, err, "decrypting with wrong key should fail")
}

func TestValidatePINTimestamp_Valid(t *testing.T) {
	payload := &crypto.PINPayload{TS: time.Now().Unix()}
	err := crypto.ValidatePINTimestamp(payload, crypto.MaxPINTimestampSkew)
	assert.NoError(t, err)
}

func TestValidatePINTimestamp_SkewTooLarge(t *testing.T) {
	payload := &crypto.PINPayload{TS: time.Now().Add(-2 * time.Minute).Unix()}
	err := crypto.ValidatePINTimestamp(payload, crypto.MaxPINTimestampSkew)
	assert.Error(t, err, "timestamp >60s skew should be rejected")
}

func TestValidatePINTimestamp_FutureSkew(t *testing.T) {
	payload := &crypto.PINPayload{TS: time.Now().Add(2 * time.Minute).Unix()}
	err := crypto.ValidatePINTimestamp(payload, crypto.MaxPINTimestampSkew)
	assert.Error(t, err, "future timestamp >60s should be rejected")
}

// The padding is part of the contract: the client encrypts with
// RSA/ECB/OAEPWithSHA-256AndMGF1Padding, and PKCS#1 v1.5 must NOT decrypt. The
// two are indistinguishable from the outside — a v1.5 client sees only "PIN
// always wrong" — so this pins the one thing that tells them apart.
func TestRSA_RejectsPKCS1v15Ciphertext(t *testing.T) {
	kp := generateTestRSAKeyPair(t)

	plaintext := []byte(`{"pin":"123456","nonce":"n","ts":1}`)
	legacy, err := rsa.EncryptPKCS1v15(rand.Reader, kp.PublicKey, plaintext)
	require.NoError(t, err)

	_, err = kp.DecryptPIN(base64.StdEncoding.EncodeToString(legacy))
	assert.Error(t, err, "PKCS#1 v1.5 ciphertext must not decrypt: the server is OAEP-SHA256 only")
}

func TestRSA_KeyIDMatching(t *testing.T) {
	kp := generateTestRSAKeyPair(t)

	// No PIN_KEY_ID configured falls back to the id the API published before
	// the field existed, so a client that hardcoded it keeps working.
	assert.Equal(t, crypto.DefaultPINKeyID, kp.ActiveKeyID())

	kp.KeyID = "pin-key-v2"
	assert.Equal(t, "pin-key-v2", kp.ActiveKeyID())

	// Empty is accepted: builds that predate the field send nothing.
	assert.True(t, kp.AcceptsKeyID(""))
	assert.True(t, kp.AcceptsKeyID("PIN-KEY-V2"), "comparison is case-insensitive")
	assert.False(t, kp.AcceptsKeyID("pin-key-v1"), "a rotated-out key must be rejected by name")
}
