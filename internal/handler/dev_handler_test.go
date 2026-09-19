package handler

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func testKeyPair(t *testing.T) *crypto.RSAKeyPair {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	kp, err := crypto.ParseRSAKeyPair(privPEM, pubPEM)
	if err != nil {
		t.Fatalf("parse key pair: %v", err)
	}
	return kp
}

func postEncryptPIN(t *testing.T, h *DevHandler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/dev/encrypt-pin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.EncryptPIN(rr, req)
	return rr
}

// The whole point of the endpoint: what it returns must decrypt back to the
// PIN that was sent, using the server's own key. If this drifts, every login
// from Postman fails with AUTH_INVALID_PIN and the cause looks like a PIN bug.
func TestDevEncryptPIN_RoundTrips(t *testing.T) {
	kp := testKeyPair(t)
	rr := postEncryptPIN(t, NewDevHandler(kp), `{"pin":"123456"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var envelope struct {
		Data struct {
			PINEncrypted string `json:"pin_encrypted"`
			Nonce        string `json:"nonce"`
			TS           int64  `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	payload, err := kp.DecryptPIN(envelope.Data.PINEncrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if payload.PIN != "123456" {
		t.Errorf("expected pin 123456, got %q", payload.PIN)
	}
	if payload.Nonce != envelope.Data.Nonce || payload.Nonce == "" {
		t.Errorf("nonce in ciphertext (%q) must match the reported one (%q)", payload.Nonce, envelope.Data.Nonce)
	}
	if err := crypto.ValidatePINTimestamp(payload, crypto.MaxPINTimestampSkew); err != nil {
		t.Errorf("fresh payload must pass the skew check: %v", err)
	}
}

// Two calls with the same PIN must not produce the same ciphertext: the nonce
// is what stops a captured pin_encrypted from being replayed forever.
func TestDevEncryptPIN_NonceIsFreshPerCall(t *testing.T) {
	h := NewDevHandler(testKeyPair(t))

	first := postEncryptPIN(t, h, `{"pin":"123456"}`)
	second := postEncryptPIN(t, h, `{"pin":"123456"}`)

	if first.Body.String() == second.Body.String() {
		t.Error("identical ciphertext for two calls — nonce is not being regenerated")
	}
}

func TestDevEncryptPIN_RejectsEmptyPIN(t *testing.T) {
	h := NewDevHandler(testKeyPair(t))

	for _, body := range []string{`{"pin":""}`, `{"pin":"   "}`, `{}`, `not json`} {
		if rr := postEncryptPIN(t, h, body); rr.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rr.Code)
		}
	}
}

func TestDevPINPublicKey_ReturnsPEM(t *testing.T) {
	kp := testKeyPair(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/dev/pin-public-key", nil)
	rr := httptest.NewRecorder()
	NewDevHandler(kp).PINPublicKey(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var envelope struct {
		Data struct {
			PublicKeyPEM string `json:"public_key_pem"`
			Algorithm    string `json:"algorithm"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !strings.Contains(envelope.Data.PublicKeyPEM, "BEGIN PUBLIC KEY") {
		t.Error("response does not carry a PEM public key")
	}
	if strings.Contains(envelope.Data.PublicKeyPEM, "PRIVATE") {
		t.Fatal("private key material leaked through the public-key endpoint")
	}
	if envelope.Data.Algorithm != "RSA-OAEP-SHA256" {
		t.Errorf("algorithm must tell the client which padding to use, got %q", envelope.Data.Algorithm)
	}
}