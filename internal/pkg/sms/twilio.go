package sms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	twilioDefaultBaseURL = "https://api.twilio.com"
	twilioAPIVersion     = "2010-04-01"

	// twilioAttempts is 2, not more. Twilio bills per accepted message and the
	// nasabah is holding a 5-minute code: a long retry ladder spends their TTL
	// and can hand them two SMS for one request. One retry covers the dropped
	// connection; anything worse is better answered as OTP_DELIVERY_FAILED so
	// the app can offer "kirim ulang".
	twilioAttempts = 2
	twilioBackoff  = 500 * time.Millisecond

	// twilioMaxBackoff caps how long a Retry-After is allowed to hold the
	// handler. Twilio answers 429 with the seconds it wants us to wait, and
	// honouring a long one inside a request the nasabah is waiting on is worse
	// than giving up: SMS_TIMEOUT already allows two attempts to run for ~20s
	// against a 30s REQUEST_TIMEOUT. A wait longer than this is answered as
	// OTP_DELIVERY_FAILED so the app can offer "kirim ulang" immediately.
	twilioMaxBackoff = 2 * time.Second

	// twilioMaxBodyBytes bounds the error body read. Twilio's error JSON is a
	// few hundred bytes; anything larger is a proxy error page.
	twilioMaxBodyBytes = 1 << 16
)

// TwilioConfig is the transport's half of config.SMS.
type TwilioConfig struct {
	// AccountSID is the "AC…" account identifier, used as the basic-auth user.
	AccountSID string

	// AuthToken is the account's auth token (or an API key secret). Never
	// logged, never echoed into an error.
	AuthToken string

	// Sender is either a Twilio phone number in E.164 ("+1555…") or a
	// Messaging Service SID ("MG…"). A messaging service is what you want for
	// Indonesian traffic — it picks the route and sender id per destination
	// operator instead of pinning one number — so both shapes are accepted and
	// the prefix decides which parameter is sent.
	Sender string

	// BaseURL exists so the tests can point at an httptest server. Empty means
	// the real API.
	BaseURL string

	// Timeout bounds one HTTP call. The nasabah is staring at a spinner while
	// this runs, so a slow aggregator has to be cut off rather than allowed to
	// hold the handler open until the request timeout fires.
	Timeout time.Duration
}

// TwilioGateway delivers OTPs through Twilio's Messages API.
//
// This is the first real transport in this package. Everything it does is
// shaped by one rule: the code itself must never leave the process in anything
// but the message body — not in a log line, not in an error, not in a metric
// label.
type TwilioGateway struct {
	accountSID string
	authToken  string
	sender     string
	sendURL    string
	timeout    time.Duration
	http       *http.Client
}

// NewTwilioGateway validates the credentials and returns a ready gateway.
//
// Every problem it can see is reported here rather than at the first send,
// because router.New turns this error into a failed boot. Credentials that are
// configured and wrong must not produce a process that looks healthy while
// every OTP silently fails — that is the exact failure this transport exists
// to end.
func NewTwilioGateway(cfg TwilioConfig) (*TwilioGateway, error) {
	accountSID := strings.TrimSpace(cfg.AccountSID)
	authToken := strings.TrimSpace(cfg.AuthToken)
	sender := strings.TrimSpace(cfg.Sender)

	if accountSID == "" {
		return nil, errors.New("twilio: SMS_ACCOUNT_SID is empty")
	}
	if !validAccountSID(accountSID) {
		// A prefix check alone was not enough. "AC" plus a truncated paste
		// passes it, boots a healthy-looking process, and then 404s on every
		// single send — exactly the configured-and-wrong failure this
		// constructor exists to catch at boot. A real account sid is "AC"
		// followed by 32 hex digits, and nothing else is.
		return nil, errors.New(`twilio: SMS_ACCOUNT_SID must be "AC" followed by 32 hex digits (34 characters); an API key sid "SK…" or a truncated paste is not an account sid`)
	}
	if authToken == "" {
		return nil, errors.New("twilio: SMS_AUTH_TOKEN is empty")
	}
	if sender == "" {
		return nil, errors.New("twilio: SMS_SENDER is empty (a Twilio number in E.164, or a MG… messaging service sid)")
	}
	if !strings.HasPrefix(sender, "MG") && !strings.HasPrefix(sender, "+") {
		return nil, errors.New(`twilio: SMS_SENDER must be E.164 ("+1555…") or a messaging service sid ("MG…")`)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = twilioDefaultBaseURL
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &TwilioGateway{
		accountSID: accountSID,
		authToken:  authToken,
		sender:     sender,
		sendURL:    fmt.Sprintf("%s/%s/Accounts/%s/Messages.json", baseURL, twilioAPIVersion, url.PathEscape(accountSID)),
		timeout:    timeout,
		http:       &http.Client{Timeout: timeout},
	}, nil
}

// SendOTP delivers the code to phone, whichever operator owns the number.
func (g *TwilioGateway) SendOTP(ctx context.Context, phone, otp string) error {
	to, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	operator := OperatorOf(to)

	form := url.Values{
		"To":   {to},
		"Body": {OTPMessage(otp)},
	}
	// A messaging service carries its own sender pool and per-country routing;
	// a bare number does not. Sending the wrong parameter is a 400, so the
	// prefix decides.
	if strings.HasPrefix(g.sender, "MG") {
		form.Set("MessagingServiceSid", g.sender)
	} else {
		form.Set("From", g.sender)
	}

	var lastErr error
	for attempt := 1; attempt <= twilioAttempts; attempt++ {
		sid, status, err := g.post(ctx, form)
		if err == nil {
			slog.Info("sms delivered to provider",
				"provider", "twilio",
				"phone_suffix", suffix(to),
				"operator", operator,
				"message_sid", sid,
				"message_status", status,
				"attempt", attempt,
			)
			return nil
		}
		lastErr = err

		backoff := twilioBackoff
		var tErr *twilioError
		if errors.As(err, &tErr) {
			if !tErr.retryable() {
				break
			}
			// Twilio says how long to wait on a 429. Ignoring it is how a
			// rate-limited account turns into a tighter rate limit; honouring a
			// long one is how a handler the nasabah is waiting on stalls. So:
			// obey it while it fits the budget, give up when it does not.
			if tErr.retryAfter > 0 {
				if tErr.retryAfter > twilioMaxBackoff {
					break
				}
				backoff = tErr.retryAfter
			}
		}
		if ctx.Err() != nil {
			break
		}
		if attempt < twilioAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	slog.Error("sms provider refused the OTP",
		"provider", "twilio",
		"phone_suffix", suffix(to),
		"operator", operator,
		"error", lastErr,
	)
	return lastErr
}

// post performs one Messages.json call and returns the message sid and status.
func (g *TwilioGateway) post(ctx context.Context, form url.Values) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.sendURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("build twilio request: %w", err)
	}
	req.SetBasicAuth(g.accountSID, g.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("call twilio: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, twilioMaxBodyBytes))
	if readErr != nil {
		return "", "", fmt.Errorf("read twilio response: %w", readErr)
	}

	var payload struct {
		SID          string `json:"sid"`
		Status       string `json:"status"`
		Code         int    `json:"code"`
		Message      string `json:"message"`
		ErrorCode    *int   `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}
	// A body that will not parse is still a response: the status code decides,
	// and the unparsed body is simply not quoted back.
	_ = json.Unmarshal(body, &payload)

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		// Twilio can accept the request and still reject the message, in which
		// case it answers 201 with status "failed" and an error_code. Treating
		// that as success is how an undelivered OTP looks healthy in the logs.
		if payload.Status == "failed" || payload.Status == "undelivered" {
			code := 0
			if payload.ErrorCode != nil {
				code = *payload.ErrorCode
			}
			return "", "", &twilioError{
				status:  resp.StatusCode,
				code:    code,
				message: payload.ErrorMessage,
			}
		}
		return payload.SID, payload.Status, nil
	}

	return "", "", &twilioError{
		status:     resp.StatusCode,
		code:       payload.Code,
		message:    payload.Message,
		retryAfter: retryAfter,
	}
}

// parseRetryAfter reads the delay form of the header. The HTTP-date form is
// ignored on purpose: Twilio sends seconds, and a date would only be minutes
// away — past the point where waiting inside this request still helps.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// twilioError carries the HTTP status and Twilio's own error code so SendOTP
// can tell "this number will never work" from "Twilio is having a bad minute".
type twilioError struct {
	status  int
	code    int
	message string

	// retryAfter is the Retry-After the provider asked for, or 0 when it sent
	// none. Only meaningful when retryable() is true.
	retryAfter time.Duration
}

func (e *twilioError) Error() string {
	// Twilio's own text is quoted back because it is what makes a 400
	// diagnosable ("Permission to send an SMS has not been enabled for the
	// region…" is the Geo Permissions checkbox, and nothing else says so). It
	// is scrubbed first: that same sentence, and error 21211, echo the full
	// destination number, and this string is handed straight to slog by
	// SendOTP. A log line carrying a nasabah's phone number is a PII leak the
	// rest of this package goes out of its way to avoid.
	msg := scrubNumbers(e.message)
	if e.code != 0 {
		return fmt.Sprintf("twilio http %d: code %d: %s", e.status, e.code, msg)
	}
	return fmt.Sprintf("twilio http %d: %s", e.status, msg)
}

// accountSIDPattern is the exact shape of a Twilio account sid: "AC" and 32
// hex digits. Matched case-insensitively on the hex half only — the "AC" prefix
// is upper case in every sid Twilio issues, and accepting "ac…" would let a
// lower-cased paste through to a 404 at send time.
var accountSIDPattern = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)

func validAccountSID(sid string) bool {
	return accountSIDPattern.MatchString(sid)
}

// phoneLike matches a digit run long enough to be a phone number, including the
// separators Twilio uses when it quotes one back ("+62 812-3456-7890").
var phoneLike = regexp.MustCompile(`\+?[0-9][0-9 ().\-]{5,}[0-9]`)

// scrubNumbers removes phone numbers from provider-supplied text.
//
// The threshold is 7 digits: Twilio's error codes are four or five digits and
// have to survive, because the code is the thing you search the docs for.
func scrubNumbers(s string) string {
	return phoneLike.ReplaceAllStringFunc(s, func(m string) string {
		digits := 0
		for _, r := range m {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits < 7 {
			return m
		}
		return "[number redacted]"
	})
}

// retryable reports whether sending the same message again could succeed.
//
// 5xx and 429 never reached the operator, so a retry is a retry. Every 4xx is
// a rejection of this exact message — bad number, unverified trial
// destination, blocked region — and re-sending only burns the nasabah's
// remaining TTL.
func (e *twilioError) retryable() bool {
	return e.status >= 500 || e.status == http.StatusTooManyRequests
}

// --- Twilio Verify -----------------------------------------------------------
//
// A second Twilio transport, and a different contract: Verify generates, stores
// and checks the code itself. It lives here beside the Messages gateway because
// it is the same vendor and shares the credential rules, the number redaction and
// the "configured and wrong fails the boot" discipline.

const (
	twilioVerifyBaseURL = "https://verify.twilio.com"

	// twilioVerifyCodeTTL is Twilio's default verification lifetime, used when
	// SMS_VERIFY_CODE_TTL is unset. We do not control it: it is configured on
	// the Verify service in the console, and this value only has to agree with
	// it so the countdown the app shows matches the code in the nasabah's hand.
	twilioVerifyCodeTTL = 10 * time.Minute

	// twilioVerifyDefaultLocale is Indonesian, matching every other string the
	// nasabah reads.
	twilioVerifyDefaultLocale = "id"
)

// voiceLocaleSupported reports whether Twilio Verify can read a code out loud in
// locale.
//
// Verify's default voice template covers far fewer languages than its SMS one,
// and "id" is not among them — the full list is in Verify's supported-languages
// table. An unsupported locale is not rejected by the API: it silently falls back
// to English. Sending Locale=id on a call would therefore produce an
// English-spoken code while the request claimed otherwise, so the parameter is
// omitted instead and Twilio's own fallback is left to do its job visibly.
func voiceLocaleSupported(locale string) bool {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "ca", "da", "de", "en", "en-gb", "es", "fi", "fr", "it", "ja", "ko",
		"nb", "nl", "pl", "pt", "pt-br", "ru", "sv", "tl", "tr",
		"zh", "zh-cn", "zh-hk":
		return true
	default:
		return false
	}
}

// verifyServiceSIDPattern is "VA" plus 32 hex digits. Same reasoning as the
// account sid: a nearly-right value boots a healthy-looking process that answers
// 404 to every verification.
var verifyServiceSIDPattern = regexp.MustCompile(`^VA[0-9a-fA-F]{32}$`)

type TwilioVerifyConfig struct {
	AccountSID string
	AuthToken  string

	// ServiceSID is the Verify service ("VA…"). A trial account already has one
	// called "Try It Out Verify Service".
	ServiceSID string

	// BaseURL overrides the Verify host and comes from SMS_VERIFY_BASE_URL, not
	// SMS_BASE_URL. Wiring the Messages host in here sends every verification to
	// a 404, which post() reports as ErrVerifyExpired — a correctly configured
	// account answering OTP_EXPIRED to every nasabah. See ProviderConfig.
	BaseURL string

	// Channels is the allowlist from SMS_VERIFY_CHANNELS. Empty means SMS only:
	// a deployment that has not said it wants voice calls has not opened Voice
	// Geo Permissions for Indonesia either, and every call would fail anyway.
	Channels []string

	// Locale is the language Verify renders the code in. Empty means "id".
	Locale string

	// CodeTTL mirrors the expiry configured on the Verify service. Empty means
	// twilioVerifyCodeTTL.
	CodeTTL time.Duration

	Timeout time.Duration
}

// TwilioVerifier delivers and checks OTPs through Twilio Verify.
type TwilioVerifier struct {
	accountSID string
	authToken  string
	serviceSID string
	startURL   string
	checkURL   string
	channels   map[Channel]bool
	locale     string
	codeTTL    time.Duration
	timeout    time.Duration
	http       *http.Client
}

// NewTwilioVerifier validates the credentials and returns a ready verifier.
func NewTwilioVerifier(cfg TwilioVerifyConfig) (*TwilioVerifier, error) {
	accountSID := strings.TrimSpace(cfg.AccountSID)
	authToken := strings.TrimSpace(cfg.AuthToken)
	serviceSID := strings.TrimSpace(cfg.ServiceSID)

	if accountSID == "" {
		return nil, errors.New("twilio: SMS_ACCOUNT_SID is empty")
	}
	if !validAccountSID(accountSID) {
		return nil, errors.New(`twilio: SMS_ACCOUNT_SID must be "AC" followed by 32 hex digits (34 characters)`)
	}
	if authToken == "" {
		return nil, errors.New("twilio: SMS_AUTH_TOKEN is empty")
	}
	if serviceSID == "" {
		return nil, errors.New("twilio: SMS_VERIFY_SERVICE_SID is empty (a Verify service sid, \"VA…\")")
	}
	if !verifyServiceSIDPattern.MatchString(serviceSID) {
		return nil, errors.New(`twilio: SMS_VERIFY_SERVICE_SID must be "VA" followed by 32 hex digits`)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = twilioVerifyBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	// An unknown channel name fails the boot rather than being dropped from the
	// allowlist. "SMS_VERIFY_CHANNELS=sms,voice" is a plausible typo — "voice" is
	// what Twilio calls the product, "call" is what the API calls the channel —
	// and quietly narrowing the allowlist to sms would leave the operator
	// believing voice was enabled until the first nasabah needed it.
	channels := make(map[Channel]bool, len(cfg.Channels))
	for _, raw := range cfg.Channels {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		switch Channel(name) {
		case ChannelSMS, ChannelCall:
			channels[Channel(name)] = true
		default:
			return nil, fmt.Errorf("twilio: SMS_VERIFY_CHANNELS contains unknown channel %q (supported: sms, call)", raw)
		}
	}
	if len(channels) == 0 {
		channels[ChannelSMS] = true
	}

	locale := strings.TrimSpace(cfg.Locale)
	if locale == "" {
		locale = twilioVerifyDefaultLocale
	}

	codeTTL := cfg.CodeTTL
	if codeTTL <= 0 {
		codeTTL = twilioVerifyCodeTTL
	}

	if channels[ChannelCall] && !voiceLocaleSupported(locale) {
		// Not an error: Twilio falls back to English and the call still delivers
		// a usable code. But it is the kind of thing nobody discovers from a
		// dashboard, so it is said once, at boot, where it can be acted on.
		slog.Warn("twilio verify: locale is not available for the call channel, so spoken codes will be in English",
			"locale", locale,
			"channel", string(ChannelCall),
		)
	}

	return &TwilioVerifier{
		accountSID: accountSID,
		authToken:  authToken,
		serviceSID: serviceSID,
		startURL:   fmt.Sprintf("%s/v2/Services/%s/Verifications", baseURL, url.PathEscape(serviceSID)),
		checkURL:   fmt.Sprintf("%s/v2/Services/%s/VerificationCheck", baseURL, url.PathEscape(serviceSID)),
		channels:   channels,
		locale:     locale,
		codeTTL:    codeTTL,
		timeout:    timeout,
		http:       &http.Client{Timeout: timeout},
	}, nil
}

// CodeTTL reports the configured verification lifetime.
func (v *TwilioVerifier) CodeTTL() time.Duration { return v.codeTTL }

// StartVerification asks Twilio to generate and send a code over ch.
//
// ch is checked against the allowlist before the number is even parsed: a
// channel this deployment has not enabled must cost nothing, and an HTTP call
// that was never going to be allowed is a billable round trip plus a hole in the
// nasabah's per-number send budget.
func (v *TwilioVerifier) StartVerification(ctx context.Context, phone string, ch Channel) error {
	ch = Channel(strings.ToLower(strings.TrimSpace(string(ch))))
	if ch == "" {
		ch = ChannelSMS
	}
	if !v.channels[ch] {
		return fmt.Errorf("%w: %q", ErrChannelNotAllowed, string(ch))
	}

	to, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	operator := OperatorOf(to)

	form := url.Values{"To": {to}, "Channel": {string(ch)}}
	// Locale is sent only where Verify can honour it. On a call with an
	// unsupported locale Twilio silently speaks English, so sending the
	// parameter there would be a claim the response does not back up.
	if ch == ChannelSMS || voiceLocaleSupported(v.locale) {
		form.Set("Locale", v.locale)
	}

	payload, status, err := v.post(ctx, v.startURL, form)
	if err != nil {
		slog.Error("verify provider refused to send",
			"provider", "twilio_verify",
			"channel", string(ch),
			"phone_suffix", suffix(to),
			"operator", operator,
			"error", err,
		)
		return err
	}

	// Not retried, unlike the Messages transport. Verify keeps per-number send
	// counters of its own, and a blind retry here spends the nasabah's allowance
	// twice for one request.
	slog.Info("verification sent",
		"provider", "twilio_verify",
		"channel", string(ch),
		"phone_suffix", suffix(to),
		"operator", operator,
		"verification_sid", payload.SID,
		"verification_status", payload.Status,
		"http_status", status,
	)
	return nil
}

// CheckVerification reports whether code is the one Twilio sent.
func (v *TwilioVerifier) CheckVerification(ctx context.Context, phone, code string) (bool, error) {
	to, err := NormalizePhone(phone)
	if err != nil {
		return false, err
	}

	form := url.Values{"To": {to}, "Code": {code}}
	payload, _, err := v.post(ctx, v.checkURL, form)
	if err != nil {
		return false, err
	}

	// "approved" is the only answer that counts. Anything else — pending, canceled
	// — means the code did not match, which is not an error: it is the ordinary
	// wrong-guess path, and our own failure counter is what acts on it.
	return payload.Status == "approved", nil
}

// verifyPayload is the subset of a Verify response this transport reads.
type verifyPayload struct {
	SID     string `json:"sid"`
	Status  string `json:"status"`
	Valid   bool   `json:"valid"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (v *TwilioVerifier) post(ctx context.Context, endpoint string, form url.Values) (verifyPayload, int, error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	var payload verifyPayload

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return payload, 0, fmt.Errorf("build twilio verify request: %w", err)
	}
	req.SetBasicAuth(v.accountSID, v.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return payload, 0, fmt.Errorf("call twilio verify: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, twilioMaxBodyBytes))
	if readErr != nil {
		return payload, resp.StatusCode, fmt.Errorf("read twilio verify response: %w", readErr)
	}
	_ = json.Unmarshal(body, &payload)

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return payload, resp.StatusCode, nil
	}

	// 404 is routine, not a fault: Verify drops a verification once it expires or
	// is approved, so "not found" is exactly "the code the nasabah is holding is
	// no longer live". The domain turns it into OTP_EXPIRED.
	if resp.StatusCode == http.StatusNotFound {
		return payload, resp.StatusCode, ErrVerifyExpired
	}

	// 60202 max check attempts, 60203 max send attempts, plus plain 429.
	if resp.StatusCode == http.StatusTooManyRequests || payload.Code == 60202 || payload.Code == 60203 {
		return payload, resp.StatusCode, fmt.Errorf("%w: twilio verify code %d: %s",
			ErrVerifyRateLimited, payload.Code, scrubNumbers(payload.Message))
	}

	return payload, resp.StatusCode, fmt.Errorf("twilio verify http %d: code %d: %s",
		resp.StatusCode, payload.Code, scrubNumbers(payload.Message))
}
