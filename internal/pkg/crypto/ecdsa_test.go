package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

func testKeyPair(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return key, base64.StdEncoding.EncodeToString(der)
}

func signASN1(t *testing.T, key *ecdsa.PrivateKey, payload string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(payload))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func TestVerifyECDSASignatureBase64_AcceptsValidDER(t *testing.T) {
	key, publicKey := testKeyPair(t)
	payload := "v1\nchallenge_id=chl_1\nnonce=abc\n"

	if err := VerifyECDSASignatureBase64(publicKey, payload, signASN1(t, key, payload)); err != nil {
		t.Fatalf("a valid signature must verify: %v", err)
	}
}

// Android wrappers sometimes emit raw R||S instead of DER. Rejecting that would
// fail verification on devices that are behaving correctly.
func TestVerifyECDSASignatureBase64_AcceptsRawRS(t *testing.T) {
	key, publicKey := testKeyPair(t)
	payload := "payload"
	digest := sha256.Sum256([]byte(payload))

	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	s.FillBytes(raw[32:])

	if err := VerifyECDSASignatureBase64(
		publicKey, payload, base64.StdEncoding.EncodeToString(raw),
	); err != nil {
		t.Fatalf("a raw R||S signature must verify: %v", err)
	}
}

// One byte of the payload changed must invalidate the signature — this is what
// makes the per-frame digests in the liveness payload worth anything.
func TestVerifyECDSASignatureBase64_RejectsTamperedPayload(t *testing.T) {
	key, publicKey := testKeyPair(t)
	signature := signASN1(t, key, "original payload")

	if err := VerifyECDSASignatureBase64(publicKey, "originai payload", signature); err == nil {
		t.Fatal("a signature over different bytes must not verify")
	}
}

func TestVerifyECDSASignatureBase64_RejectsOtherKey(t *testing.T) {
	key, _ := testKeyPair(t)
	_, otherPublic := testKeyPair(t)
	payload := "payload"

	if err := VerifyECDSASignatureBase64(otherPublic, payload, signASN1(t, key, payload)); err == nil {
		t.Fatal("a signature from a different key must not verify")
	}
}

func TestVerifyECDSASignatureBase64_RejectsGarbageSignature(t *testing.T) {
	_, publicKey := testKeyPair(t)

	if err := VerifyECDSASignatureBase64(publicKey, "payload", "@@not-base64@@"); err == nil {
		t.Fatal("a signature that is not base64 must be rejected")
	}
	if err := VerifyECDSASignatureBase64(
		publicKey, "payload", base64.StdEncoding.EncodeToString([]byte("short")),
	); err == nil {
		t.Fatal("a signature that is not a signature must be rejected")
	}
}

func TestParseECDSAPublicKey_AcceptsPEMAndBase64(t *testing.T) {
	key, base64Key := testKeyPair(t)

	if _, err := ParseECDSAPublicKey(base64Key); err != nil {
		t.Fatalf("base64 DER must parse: %v", err)
	}

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if _, err := ParseECDSAPublicKey(string(pemKey)); err != nil {
		t.Fatalf("PEM must parse: %v", err)
	}
}

func TestParseECDSAPublicKey_RejectsNonECKey(t *testing.T) {
	if _, err := ParseECDSAPublicKey("not a key at all"); err == nil {
		t.Fatal("garbage must not parse as a key")
	}
}

func TestVerifyECDSASHA256_RejectsNilKey(t *testing.T) {
	if err := VerifyECDSASHA256(nil, []byte("payload"), []byte("sig")); err == nil {
		t.Fatal("a nil key must be rejected rather than panicking")
	}
}

func TestSHA256Hex_MatchesKnownDigest(t *testing.T) {
	// The Android client builds the same digest for every frame; a drift here
	// breaks every signature. Pinned against a known value on purpose.
	if got := SHA256Hex([]byte("abc")); got !=
		"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA256Hex drifted: %s", got)
	}
}
