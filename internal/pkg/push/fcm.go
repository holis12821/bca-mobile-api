package push

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
)

// FCM HTTP v1. The legacy /fcm/send endpoint is retired, so there is no server
// key to fall back on — every request carries an OAuth2 access token minted from
// the service account.
const (
	fcmSendEndpoint = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
	fcmScope        = "https://www.googleapis.com/auth/firebase.messaging"

	// Access tokens live an hour. Refreshing a minute early costs one extra
	// token per hour and removes the race where a token expires between the
	// staleness check and the request reaching Google.
	fcmTokenSkew = 60 * time.Second
)

// FCMConfig is the transport's half of config.Push.
type FCMConfig struct {
	// CredentialsFile is the path to the Google service account JSON. The
	// project id is read from that file rather than from a second variable:
	// the credential already names the project it can publish to, and a
	// separate FCM_PROJECT_ID could disagree with it, which fails as
	// "messages accepted, nothing delivered".
	CredentialsFile string

	// Timeout bounds one HTTP call — one token mint or one message. The push
	// runs inside a request that has already committed money, so a slow FCM has
	// to be cut off rather than allowed to hold the handler open.
	Timeout time.Duration
}

// serviceAccount is the part of the Google service account JSON this needs.
type serviceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
	TokenURI    string `json:"token_uri"`
}

// TokenInvalidator drops a push token FCM has rejected as permanently dead.
//
// Separate from TokenStore because the two have different lifetimes: reading
// tokens happens on every notification, clearing one happens only when FCM says
// the token will never work again.
type TokenInvalidator interface {
	ClearPushToken(ctx context.Context, token string) error
}

// FCMPusher delivers notifications through Firebase Cloud Messaging.
//
// This is the third Pusher in this package and the only one that delivers
// anything. LoggingPusher and NoopPusher are kept exactly as they were: a
// process without credentials must say so, not pretend.
type FCMPusher struct {
	tokens TokenStore
	dead   TokenInvalidator

	account  serviceAccount
	key      *rsa.PrivateKey
	sendURL  string
	http     *http.Client
	timeout  time.Duration
	nowFunc  func() time.Time
	mu       sync.Mutex
	bearer   string
	bearerTo time.Time
}

// NewFCMPusher validates the credentials and fails here, at boot, rather than on
// the first notification.
//
// A server that starts with unusable credentials looks healthy and silently
// delivers nothing — which is the exact failure this whole transport exists to
// remove. Router treats this error as fatal.
func NewFCMPusher(tokens TokenStore, dead TokenInvalidator, cfg FCMConfig) (*FCMPusher, error) {
	if tokens == nil || dead == nil {
		return nil, errors.New("fcm pusher: token store and invalidator are both required")
	}
	if cfg.CredentialsFile == "" {
		return nil, errors.New("fcm pusher: credentials file is empty")
	}

	raw, err := os.ReadFile(cfg.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("fcm pusher: read credentials: %w", err)
	}

	var acc serviceAccount
	if err := json.Unmarshal(raw, &acc); err != nil {
		return nil, fmt.Errorf("fcm pusher: parse credentials: %w", err)
	}
	if acc.Type != "service_account" {
		return nil, fmt.Errorf("fcm pusher: credentials type is %q, want service_account", acc.Type)
	}
	for field, value := range map[string]string{
		"project_id":   acc.ProjectID,
		"client_email": acc.ClientEmail,
		"private_key":  acc.PrivateKey,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("fcm pusher: credentials are missing %s", field)
		}
	}
	if acc.TokenURI == "" {
		acc.TokenURI = "https://oauth2.googleapis.com/token"
	}

	key, err := parsePKCS8RSAKey(acc.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("fcm pusher: private key: %w", err)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &FCMPusher{
		tokens:  tokens,
		dead:    dead,
		account: acc,
		key:     key,
		sendURL: fmt.Sprintf(fcmSendEndpoint, acc.ProjectID),
		http:    &http.Client{Timeout: timeout},
		timeout: timeout,
		nowFunc: time.Now,
	}, nil
}

// Push delivers one notification to every device the user has registered.
//
// Three properties worth keeping:
//
//   - No tokens is not an error. A customer who has never opened the app on a
//     device has nothing to deliver to, and the in-app row is already written.
//   - One failing token does not cancel the rest. A single dead handset must not
//     stop the notification reaching the phone in the customer's hand.
//   - SECURITY notifications ignore push_notification_enabled. Decided with the
//     product owner: "your access code was changed" is precisely the message
//     someone who muted notifications still needs. The type is read from the
//     data map Notifier already fills, so the Pusher interface stays as it is.
func (p *FCMPusher) Push(ctx context.Context, userID uuid.UUID, title, body string, data map[string]string) error {
	overrideMuted := data["type"] == notify.TypeSecurity

	tokens, err := p.tokens.ListPushTokens(ctx, userID, overrideMuted)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}

	bearer, err := p.accessToken(ctx)
	if err != nil {
		return err
	}

	var failures []string
	for _, token := range tokens {
		sendErr := p.send(ctx, bearer, token, title, body, data)
		if sendErr == nil {
			continue
		}

		var fcmErr *fcmError
		if errors.As(sendErr, &fcmErr) && fcmErr.permanent() {
			// The token will never work again, so it is cleared. Only for
			// UNREGISTERED and INVALID_ARGUMENT: clearing on a transient fault
			// would stop this customer's push until they reopen the app, which
			// turns a five-minute FCM outage into a silent permanent one.
			if clearErr := p.dead.ClearPushToken(ctx, token); clearErr != nil {
				slog.Error("clear dead push token failed",
					"user_id", userID, "error", clearErr)
			} else {
				slog.Info("push token cleared after FCM rejected it",
					"user_id", userID, "fcm_error", fcmErr.code)
			}
			continue
		}
		failures = append(failures, sendErr.Error())
	}

	if len(failures) > 0 {
		return fmt.Errorf("fcm: %d of %d devices failed: %s",
			len(failures), len(tokens), strings.Join(failures, "; "))
	}
	return nil
}

// send posts one message. The data map is forwarded as-is: Notifier already put
// `type` and `deep_link` in it, and the client reads those keys.
func (p *FCMPusher) send(ctx context.Context, bearer, token, title, body string, data map[string]string) error {
	message := map[string]any{
		"message": map[string]any{
			"token": token,
			"notification": map[string]string{
				"title": title,
				"body":  body,
			},
			"data": data,
		},
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal fcm message: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.sendURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build fcm request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("call fcm: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return parseFCMError(resp)
}

// fcmError carries the FCM error code so Push can tell "this handset is gone"
// from "Google is having a bad minute".
type fcmError struct {
	status  int
	code    string
	message string
}

func (e *fcmError) Error() string {
	return fmt.Sprintf("fcm http %d: %s: %s", e.status, e.code, e.message)
}

// permanent reports whether the token should be dropped.
//
// UNREGISTERED: the app was uninstalled or the token rotated. INVALID_ARGUMENT:
// the token is malformed and re-sending cannot fix it. Everything else —
// UNAVAILABLE, INTERNAL, QUOTA_EXCEEDED, THIRD_PARTY_AUTH_ERROR, and any
// authentication problem of ours — leaves the token alone.
func (e *fcmError) permanent() bool {
	return e.code == "UNREGISTERED" || e.code == "INVALID_ARGUMENT"
}

func parseFCMError(resp *http.Response) error {
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				Type      string `json:"@type"`
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}

	body := make([]byte, 0, 512)
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err == nil {
		body = buf.Bytes()
	}
	_ = json.Unmarshal(body, &payload)

	// The machine-readable code lives in details[].errorCode; error.status is
	// the generic gRPC name and is only a fallback. Reading status alone maps a
	// stale token (NOT_FOUND) onto nothing actionable.
	code := payload.Error.Status
	for _, d := range payload.Error.Details {
		if d.ErrorCode != "" {
			code = d.ErrorCode
			break
		}
	}
	if code == "" {
		code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
	}
	// A 404 with no usable body still means this token is gone.
	if code == "NOT_FOUND" {
		code = "UNREGISTERED"
	}

	return &fcmError{
		status:  resp.StatusCode,
		code:    code,
		message: payload.Error.Message,
	}
}

// accessToken returns a cached OAuth2 access token, minting a new one when the
// old one is within fcmTokenSkew of expiring.
//
// Hand-rolled rather than pulled from golang.org/x/oauth2/google: the whole flow
// is one signed assertion and one form post, and this module stays deliberately
// thin on dependencies. The signing itself is RS256 over a fixed claim set,
// which the tests pin.
func (p *FCMPusher) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.bearer != "" && p.nowFunc().Add(fcmTokenSkew).Before(p.bearerTo) {
		return p.bearer, nil
	}

	assertion, err := p.signedAssertion()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.account.TokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint fcm access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint fcm access token: http %d", resp.StatusCode)
	}

	var minted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if minted.AccessToken == "" {
		return "", errors.New("mint fcm access token: response carried no access_token")
	}
	if minted.ExpiresIn <= 0 {
		minted.ExpiresIn = 3600
	}

	p.bearer = minted.AccessToken
	p.bearerTo = p.nowFunc().Add(time.Duration(minted.ExpiresIn) * time.Second)
	return p.bearer, nil
}

// signedAssertion builds the RS256 JWT that buys an access token.
func (p *FCMPusher) signedAssertion() (string, error) {
	now := p.nowFunc()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   p.account.ClientEmail,
		"scope": fcmScope,
		"aud":   p.account.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}

	return signingInput + "." + enc.EncodeToString(signature), nil
}

// parsePKCS8RSAKey reads the PEM private key out of a service account file.
// Google issues PKCS#8; PKCS#1 is accepted too so a hand-converted key works.
func parsePKCS8RSAKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is %T, want RSA", key)
		}
		return rsaKey, nil
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return key, nil
}
