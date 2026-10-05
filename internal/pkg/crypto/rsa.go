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
	"strings"
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

	// KeyID names this pair for the client. It travels with the published PEM
	// and comes back as `encryption_key_id`, which is what makes a key rotation
	// diagnosable: a client still encrypting under the previous key is told so
	// instead of being told its PIN is wrong.
	KeyID string
}

// DefaultPINKeyID is used when no PIN_KEY_ID is configured. It matches the
// value the API published before the field existed, so an app that hardcoded
// it keeps working.
const DefaultPINKeyID = "pin-key-v1"

// ActiveKeyID is the key id to publish and to compare against.
func (kp *RSAKeyPair) ActiveKeyID() string {
	if kp == nil || strings.TrimSpace(kp.KeyID) == "" {
		return DefaultPINKeyID
	}
	return kp.KeyID
}

// AcceptsKeyID reports whether a client-supplied encryption_key_id names the
// key this server decrypts with. An empty id is accepted: builds already in
// testers' hands do not send the field, and rejecting them would turn a
// diagnostic into an outage.
func (kp *RSAKeyPair) AcceptsKeyID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	return strings.EqualFold(id, kp.ActiveKeyID())
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

// LoadRSAPublicKey reads only the public half from disk. Encrypting a PIN
// needs nothing else, so a client-side tool never has to open the private key.
func LoadRSAPublicKey(publicPath string) (*RSAKeyPair, error) {
	pubPEM, err := os.ReadFile(publicPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}

	return ParseRSAPublicKey(pubPEM)
}

// ParseRSAPublicKey parses a PEM-encoded RSA public key into a pair whose
// PrivateKey is nil. EncryptPIN works; DecryptPIN will panic — by design,
// since a holder of the public key has no business decrypting.
func ParseRSAPublicKey(pubPEM []byte) (*RSAKeyPair, error) {
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

	return &RSAKeyPair{PublicKey: pubKey}, nil
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

// PublicKeyPEM returns the RSA public key in PEM-encoded PKIX format.
func (kp *RSAKeyPair) PublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(kp.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}), nil
}

// MaxPINTimestampSkew is the maximum allowed clock skew for PIN payloads.
const MaxPINTimestampSkew = 60 * time.Second
