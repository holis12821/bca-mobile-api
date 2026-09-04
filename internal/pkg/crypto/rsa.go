package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// PINPayload is the decrypted PIN transport payload.
// The client encrypts {"pin":"123456","nonce":"<uuid>","ts":<unix>}
// with the server's RSA public key. Without nonce+ts, a captured
// pin_encrypted replays forever.
type PINPayload struct {
	PIN   string `json:"pin"`
	Nonce string `json:"nonce"`
	TS    int64  `json:"ts"`
}

// RSAKeyPair holds RSA keys for PIN encryption/decryption.
type RSAKeyPair struct {
	PrivateKey *rsa.PrivateKey
	PublicKey  *rsa.PublicKey
}

// LoadRSAKeyPair reads PEM files from disk.
func LoadRSAKeyPair(privatePath, publicPath string) (*RSAKeyPair, error) {
	privPEM, err := os.ReadFile(privatePath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	pubPEM, err := os.ReadFile(publicPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}

	return ParseRSAKeyPair(privPEM, pubPEM)
}

// ParseRSAKeyPair parses PEM-encoded RSA key pair.
func ParseRSAKeyPair(privPEM, pubPEM []byte) (*RSAKeyPair, error) {
	privBlock, _ := pem.Decode(privPEM)
	if privBlock == nil {
		return nil, fmt.Errorf("no PEM block found in private key")
	}

	privKey, err := x509.ParsePKCS1PrivateKey(privBlock.Bytes)
	if err != nil {
		// Try PKCS8
		key, err2 := x509.ParsePKCS8PrivateKey(privBlock.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		var ok bool
		privKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
	}

	pubBlock, _ := pem.Decode(pubPEM)
	if pubBlock == nil {
		return nil, fmt.Errorf("no PEM block found in public key")
	}

	pubIface, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	pubKey, ok := pubIface.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not RSA")
	}

	return &RSAKeyPair{PrivateKey: privKey, PublicKey: pubKey}, nil
}

// EncryptPIN encrypts a PIN payload with RSA-2048 OAEP-SHA256.
// Returns base64-encoded ciphertext.
func (kp *RSAKeyPair) EncryptPIN(payload PINPayload) (string, error) {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	// RSA OAEP with SHA-256 — NOT PKCS#1 v1.5
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, kp.PublicKey, jsonBytes, nil)
	if err != nil {
		return "", fmt.Errorf("rsa encrypt: %w", err)
	}

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptPIN decrypts a base64-encoded RSA-OAEP-SHA256 ciphertext
// and returns the PINPayload.
func (kp *RSAKeyPair) DecryptPIN(encrypted string) (*PINPayload, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, kp.PrivateKey, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("rsa decrypt: %w", err)
	}

	var payload PINPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	return &payload, nil
}

// ValidatePINTimestamp checks that the payload timestamp is within maxSkew.
func ValidatePINTimestamp(payload *PINPayload, maxSkew time.Duration) error {
	diff := time.Since(time.Unix(payload.TS, 0))
	if diff < 0 {
		diff = -diff
	}
	if diff > maxSkew {
		return fmt.Errorf("timestamp skew %v exceeds max %v", diff, maxSkew)
	}
	return nil
}

// NonceChecker checks and marks nonces as used.
// Implemented by Redis (pin_nonce:{nonce}, TTL 120s).
type NonceChecker interface {
	// CheckAndMark returns true if the nonce was NOT seen before (fresh).
	// It atomically marks the nonce as used.
	CheckAndMark(nonce string) (fresh bool, err error)
}

// MaxPINTimestampSkew is the maximum allowed clock skew for PIN payloads.
const MaxPINTimestampSkew = 60 * time.Second