package crypto_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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