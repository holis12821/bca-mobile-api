// Command pinenc produces the `pin_encrypted` value the API expects, without
// the server running.
//
// The API never accepts a plaintext PIN: it wants base64 of
// RSA-OAEP-SHA256({"pin","nonce","ts"}), and rejects the payload if `ts` is
// more than 60 seconds old or the nonce has been seen before. That is trivial
// for a mobile client and impossible inside a Postman pre-request script, so
// this exists for the shell, and POST /v1/dev/encrypt-pin for Postman.
//
// Usage:
//
//	go run ./scripts/pinenc -pin 123456
//	go run ./scripts/pinenc -pin 123456 -json
//	go run ./scripts/pinenc -pin 123456 -key keys/pin_public.pem
//
// Only the PUBLIC key is read — never the private half.
//
// The output is single-use and dies after 60 seconds. If a login returns
// AUTH_INVALID_PIN with a PIN you know is right, the ciphertext went stale:
// generate a new one.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func main() {
	pin := flag.String("pin", "", "PIN or access code to encrypt (required)")
	keyPath := flag.String("key", "", "path to the PIN public key PEM (default: $PIN_PUBLIC_KEY_PATH, else keys/pin_public.pem)")
	asJSON := flag.Bool("json", false, "print the full payload as JSON instead of the ciphertext alone")
	flag.Parse()

	if *pin == "" {
		fmt.Fprintln(os.Stderr, "error: -pin is required")
		flag.Usage()
		os.Exit(2)
	}

	path := *keyPath
	if path == "" {
		path = os.Getenv("PIN_PUBLIC_KEY_PATH")
	}
	if path == "" {
		path = "keys/pin_public.pem"
	}

	keys, err := crypto.LoadRSAPublicKey(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load public key %s: %v\n", path, err)
		fmt.Fprintln(os.Stderr, "hint: run `make keys` first, or point -key at the right PEM")
		os.Exit(1)
	}

	payload := crypto.PINPayload{
		PIN:   *pin,
		Nonce: uuid.NewString(),
		TS:    time.Now().Unix(),
	}

	encrypted, err := keys.EncryptPIN(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: encrypt: %v\n", err)
		os.Exit(1)
	}

	if !*asJSON {
		fmt.Println(encrypted)
		return
	}

	out, err := json.MarshalIndent(map[string]any{
		"pin_encrypted": encrypted,
		"nonce":         payload.Nonce,
		"ts":            payload.TS,
		"expires_at":    time.Unix(payload.TS, 0).Add(crypto.MaxPINTimestampSkew).UTC().Format(time.RFC3339),
		"algorithm":     "RSA-OAEP-SHA256",
	}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(out))
}