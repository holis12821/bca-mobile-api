// Package sms provides the OTP delivery gateway shared by the registration and
// onboarding flows.
//
// The choice between "send it", "log it" and "refuse" is made once, from the
// environment, instead of each flow wiring its own gateway — see NewProvider
// and the switch in internal/router that calls it. A mock that a production
// deployment could inherit is the failure this package is arranged to prevent.
package sms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Gateway sends a one-time password to a phone number.
//
// Implementations are responsible for normalising the number: the callers hand
// over whatever the nasabah typed. See NormalizePhone.
type Gateway interface {
	SendOTP(ctx context.Context, phone, otp string) error
}

// Verifier is a provider that owns the OTP itself.
//
// The difference from Gateway is who generates the code. Gateway is handed a code
// we made, hashed and stored; a Verifier makes its own, keeps it, and is the only
// thing that can say whether the nasabah typed it correctly. Twilio Verify works
// this way, and it is the only channel a Twilio trial account can use at all —
// the Messages API refuses custom message bodies with error 572006.
//
// Everything else stays ours: session and step validation, device binding, the
// failure counter and 30-minute lockout, the per-session resend quota, the
// per-number hourly ceiling, and the audit trail. What we give up is otp_debug,
// because the code never passes through this process.
type Verifier interface {
	// StartVerification asks the provider to generate and send a code over ch.
	//
	// An empty ch means ChannelSMS. A ch the deployment has not allowed is
	// ErrChannelNotAllowed, refused before any call to the provider.
	StartVerification(ctx context.Context, phone string, ch Channel) error

	// CheckVerification reports whether code is the one the provider sent.
	//
	// A wrong code is (false, nil) — not an error. Errors are reserved for "we
	// could not ask": ErrVerifyExpired when nothing is pending for this number,
	// ErrVerifyRateLimited when the provider has cut us off.
	//
	// The channel is deliberately absent: a code read out over the phone and a
	// code sent by SMS are checked at the same endpoint, so the caller does not
	// have to remember how it was delivered.
	CheckVerification(ctx context.Context, phone, code string) (approved bool, err error)

	// CodeTTL is how long the provider keeps a code alive.
	//
	// It is the provider's setting, not ours, and the only honest source for the
	// countdown the app shows. Hardcoding our own copy of it is how an app ends
	// up displaying a timer that disagrees with the code in the nasabah's hand.
	CodeTTL() time.Duration
}

// Channel names how a verification code reaches the nasabah.
//
// Only the two Twilio Verify channels this service uses are named here. Verify
// also accepts whatsapp, email and sna; none of them is wired, and naming them
// would suggest otherwise.
type Channel string

const (
	ChannelSMS  Channel = "sms"
	ChannelCall Channel = "call"
)

// ErrChannelNotAllowed means the caller asked for a channel this deployment has
// not enabled in SMS_VERIFY_CHANNELS. The domain turns it into 400.
//
// Refused locally, before the provider is called: every channel costs money and
// has its own Geo Permissions checkbox in the Twilio console, so "which channels
// may this deployment use" is a deployment decision, not a request parameter.
var ErrChannelNotAllowed = errors.New("sms: channel is not enabled in SMS_VERIFY_CHANNELS")

// ErrVerifyExpired means the provider has no pending verification for this
// number: it expired, or it was already used. Maps to OTP_EXPIRED.
var ErrVerifyExpired = errors.New("sms: no pending verification for this number")

// ErrVerifyRateLimited means the provider itself refused — too many sends or too
// many check attempts. Our own counters are tighter, so reaching this means the
// provider's limits differ from ours, which is worth a log line.
var ErrVerifyRateLimited = errors.New("sms: verification provider rate limit reached")

// ErrNotConfigured is returned by the production placeholder. The onboarding
// flow turns it into 503 OTP_DELIVERY_FAILED: the code is stored and still
// valid, but the response must not claim an SMS was sent.
var ErrNotConfigured = errors.New("sms gateway is not configured")

// OTPMessage is the text every provider sends.
//
// Indonesian, because the nasabah reads it. Under 160 characters so it stays
// one SMS segment — a two-segment OTP costs twice and can arrive out of order.
// The anti-fraud line is not decoration: social-engineering calls asking for
// "kode verifikasi" are the most common way these codes are lost.
func OTPMessage(otp string) string {
	return otp + " adalah kode OTP BCA mobile Anda. Berlaku 5 menit. " +
		"RAHASIAKAN kode ini. BCA tidak pernah meminta kode OTP."
}

// ProviderConfig is the transport's half of config.SMS. Keeping it here, as a
// plain struct, is what lets internal/pkg stay free of internal/config.
type ProviderConfig struct {
	// Provider names the transport: "twilio" is the one wired in. Empty means
	// no provider, which NewProvider rejects — the caller decides whether that
	// falls back to the mock or refuses.
	Provider string

	AccountSID string
	AuthToken  string
	Sender     string
	BaseURL    string
	Timeout    time.Duration

	// VerifyServiceSID names a Twilio Verify service ("VA…"). Only read by
	// NewVerifier; the Messages transport has no use for it.
	VerifyServiceSID string

	// VerifyBaseURL overrides the Verify host, for tests only.
	//
	// Separate from BaseURL above, and that separation is the whole point: the
	// two transports live on different hosts (api.twilio.com and
	// verify.twilio.com). Verify used to read BaseURL, so setting SMS_BASE_URL
	// to the Messages host pointed Verify at it, earned a 404, and the 404
	// handler reports "no pending verification" — the nasabah saw OTP_EXPIRED
	// with correct credentials and nothing in the logs said why.
	VerifyBaseURL string

	// VerifyChannels is the allowlist of channels this deployment may use.
	// Empty means SMS only.
	VerifyChannels []string

	// VerifyLocale is the language Verify renders the code in. Empty means "id".
	VerifyLocale string

	// VerifyCodeTTL must match the code expiry configured on the Verify service
	// in the Twilio console. It is reported to the app, not enforced here.
	VerifyCodeTTL time.Duration
}

// NewProvider builds the real gateway named by cfg.Provider.
//
// Every error it returns fails the boot. An unknown provider name is one of
// them: silently falling back to "no SMS" would mean a typo in SMS_PROVIDER
// costs every nasabah their OTP, and nothing in the logs would say why.
func NewProvider(cfg ProviderConfig) (Gateway, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "twilio":
		return NewTwilioGateway(TwilioConfig{
			AccountSID: cfg.AccountSID,
			AuthToken:  cfg.AuthToken,
			Sender:     cfg.Sender,
			BaseURL:    cfg.BaseURL,
			Timeout:    cfg.Timeout,
		})
	case "":
		return nil, errors.New("sms: SMS_PROVIDER is empty")
	default:
		return nil, fmt.Errorf("sms: unknown SMS_PROVIDER %q (supported here: twilio; use NewVerifier for twilio_verify)", cfg.Provider)
	}
}

// NewVerifier builds the Verifier named by cfg.Provider.
//
// Separate from NewProvider because the two answer different questions, and a
// caller has to know which one it wired: a Verifier cannot be handed a code, and
// a Gateway cannot be asked to check one.
func NewVerifier(cfg ProviderConfig) (Verifier, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "twilio_verify":
		return NewTwilioVerifier(TwilioVerifyConfig{
			AccountSID: cfg.AccountSID,
			AuthToken:  cfg.AuthToken,
			ServiceSID: cfg.VerifyServiceSID,
			// VerifyBaseURL, never BaseURL: see ProviderConfig.VerifyBaseURL.
			BaseURL:  cfg.VerifyBaseURL,
			Channels: cfg.VerifyChannels,
			Locale:   cfg.VerifyLocale,
			CodeTTL:  cfg.VerifyCodeTTL,
			Timeout:  cfg.Timeout,
		})
	case "":
		return nil, errors.New("sms: SMS_PROVIDER is empty")
	default:
		return nil, fmt.Errorf("sms: provider %q is not a verifier (did you mean a Gateway?)", cfg.Provider)
	}
}

// IsVerifierProvider reports whether SMS_PROVIDER names a provider that owns the
// code, so the caller knows which constructor to use.
func IsVerifierProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "twilio_verify")
}

// MockGateway logs the OTP instead of sending it. Development and test only.
//
// It still normalises the number and names the operator, so a number the real
// provider would reject fails here too — on a developer's machine, not on a
// tester's handset.
type MockGateway struct{}

func NewMockGateway() *MockGateway { return &MockGateway{} }

func (MockGateway) SendOTP(_ context.Context, phone, otp string) error {
	to, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	// The code is the whole point of the mock, so it is logged. The number is
	// not: this log line is the same shape the real gateway writes, and a full
	// E.164 number in a dev log is a PII leak the moment that log is shipped
	// anywhere. The suffix is enough to tell two testers apart.
	slog.Info("mock sms gateway: OTP sent",
		"phone_suffix", suffix(to),
		"operator", OperatorOf(to),
		"otp", otp,
	)
	return nil
}

// UnconfiguredGateway refuses to send and never logs the code. This is what a
// production process gets until a real provider is wired in: an OTP printed to
// the application log is an OTP in the log aggregator, readable by everyone
// with dashboard access.
type UnconfiguredGateway struct{}

func NewUnconfiguredGateway() *UnconfiguredGateway { return &UnconfiguredGateway{} }

func (UnconfiguredGateway) SendOTP(_ context.Context, phone, _ string) error {
	slog.Error("sms gateway not configured; OTP was generated but not delivered",
		"phone_suffix", suffix(phone),
	)
	return ErrNotConfigured
}

func suffix(phone string) string {
	if len(phone) <= 4 {
		return phone
	}
	return phone[len(phone)-4:]
}
