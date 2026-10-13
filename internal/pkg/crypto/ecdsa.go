package crypto

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
)

// ErrInvalidECDSASignature is returned when a signature does not verify.
var ErrInvalidECDSASignature = errors.New("ecdsa signature invalid")

// ParseECDSAPublicKey accepts a PEM block or a bare base64 X.509 SubjectPublicKeyInfo.
//
// Both forms are accepted because the two clients send different ones: the Android
// Keystore hands back raw DER that the app base64-encodes, while keys pasted into
// configuration arrive as PEM.
func ParseECDSAPublicKey(encoded string) (*ecdsa.PublicKey, error) {
	var der []byte
	if block, _ := pem.Decode([]byte(encoded)); block != nil {
		der = block.Bytes
	} else {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("parse ecdsa public key: not PEM and not base64")
		}
		der = decoded
	}

	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse ecdsa public key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("parse ecdsa public key: unsupported key type %T", parsed)
	}
	return key, nil
}

// VerifyECDSASHA256 verifies a SHA256withECDSA signature over payload.
//
// Both encodings Android may produce are accepted: DER ASN.1, which is what
// java.security.Signature emits, and the raw 64-byte R||S some wrappers produce.
// Rejecting the raw form would fail verification on devices that are behaving
// correctly.
//
// Deliberately a separate function from the unexported verifySignature in
// internal/domain/auth: that one is on the biometric-login path, and the Phase 2
// decisions say not to touch login while hardening liveness.
func VerifyECDSASHA256(key *ecdsa.PublicKey, payload []byte, signature []byte) error {
	if key == nil {
		return errors.New("verify ecdsa: nil key")
	}
	digest := sha256.Sum256(payload)

	if ecdsa.VerifyASN1(key, digest[:], signature) {
		return nil
	}

	if len(signature) == rawSignatureLen {
		r := new(big.Int).SetBytes(signature[:rawSignatureLen/2])
		s := new(big.Int).SetBytes(signature[rawSignatureLen/2:])
		if ecdsa.Verify(key, digest[:], r, s) {
			return nil
		}
	}
	return ErrInvalidECDSASignature
}

// VerifyECDSASignatureBase64 is the wire-level entry point: a base64 signature over
// a payload string, against a base64 or PEM public key.
func VerifyECDSASignatureBase64(publicKey, payload, signatureBase64 string) error {
	key, err := ParseECDSAPublicKey(publicKey)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		// RawURLEncoding is tried as well so a client that URL-safe-encodes is
		// not rejected for a reason that has nothing to do with the signature.
		signature, err = base64.RawURLEncoding.DecodeString(signatureBase64)
		if err != nil {
			return fmt.Errorf("verify ecdsa: signature is not base64")
		}
	}
	return VerifyECDSASHA256(key, []byte(payload), signature)
}

// SHA256Hex is the lowercase hex digest used inside the canonical liveness payload.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

const rawSignatureLen = 64
