package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// DevHandler exposes development-only helpers so the API can be exercised from
// Postman, curl, or a frontend running against a tunnel.
//
// The router mounts it ONLY when APP_ENV=development. Everything here is built
// on the PIN *public* key — which the API already publishes at
// GET /v1/onboarding/credentials/public-key — so it hands a caller nothing they
// could not compute themselves. It stays gated regardless: endpoints that exist
// to make testing convenient have no business in a production deployment.
type DevHandler struct {
	pinKeys *crypto.RSAKeyPair
}

func NewDevHandler(pinKeys *crypto.RSAKeyPair) *DevHandler {
	return &DevHandler{pinKeys: pinKeys}
}

type devEncryptPINRequest struct {
	PIN string `json:"pin"`
}

type devEncryptPINResponse struct {
	PINEncrypted string `json:"pin_encrypted"`
	Nonce        string `json:"nonce"`
	TS           int64  `json:"ts"`
	ExpiresIn    int    `json:"expires_in"`
	Algorithm    string `json:"algorithm"`
	Note         string `json:"note"`
}

// EncryptPIN handles POST /v1/dev/encrypt-pin
//
// Produces the exact `pin_encrypted` value the API expects: RSA-OAEP-SHA256
// over {"pin","nonce","ts"}, base64-encoded. A fresh nonce and timestamp are
// minted per call, so the result is single-use and dies after 60 seconds —
// the same anti-replay rules a real client obeys.
//
// The same value is accepted by /auth/login/pin, /auth/pin/verify,
// /auth/pin/change, /registration/complete, and /onboarding/credentials.
func (h *DevHandler) EncryptPIN(w http.ResponseWriter, r *http.Request) {
	var req devEncryptPINRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	pin := strings.TrimSpace(req.PIN)
	if pin == "" || len(pin) > 64 {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if h.pinKeys == nil || h.pinKeys.PublicKey == nil {
		response.Err(w, r, apperr.InternalError)
		return
	}

	payload := crypto.PINPayload{
		PIN:   pin,
		Nonce: uuid.NewString(),
		TS:    time.Now().Unix(),
	}

	encrypted, err := h.pinKeys.EncryptPIN(payload)
	if err != nil {
		slog.Error("dev encrypt pin failed", "error", err)
		response.Err(w, r, apperr.InternalError)
		return
	}

	response.Success(w, r, http.StatusOK, devEncryptPINResponse{
		PINEncrypted: encrypted,
		Nonce:        payload.Nonce,
		TS:           payload.TS,
		ExpiresIn:    int(crypto.MaxPINTimestampSkew.Seconds()),
		Algorithm:    "RSA-OAEP-SHA256",
		Note:         "Dev helper. Single-use, expires in 60s. Not mounted when APP_ENV != development.",
	})
}

// PINPublicKey handles GET /v1/dev/pin-public-key
//
// The PEM a client needs to do the encryption itself — WebCrypto
// (RSA-OAEP / SHA-256) in a browser, or the Android Keystore provider.
func (h *DevHandler) PINPublicKey(w http.ResponseWriter, r *http.Request) {
	if h.pinKeys == nil || h.pinKeys.PublicKey == nil {
		response.Err(w, r, apperr.InternalError)
		return
	}

	pemBytes, err := h.pinKeys.PublicKeyPEM()
	if err != nil {
		slog.Error("dev export public key failed", "error", err)
		response.Err(w, r, apperr.InternalError)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"algorithm":      "RSA-OAEP-SHA256",
		"key_id":         "pin-key-v1",
		"public_key_pem": string(pemBytes),
		"payload_shape":  `{"pin":"123456","nonce":"<uuid-v4>","ts":<unix-seconds>}`,
		"encoding":       "base64(RSA-OAEP-SHA256(json))",
		"max_skew_sec":   int(crypto.MaxPINTimestampSkew.Seconds()),
	})
}
