package sms

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestGateway(t *testing.T, h http.HandlerFunc) (*TwilioGateway, *httptest.Server) {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	gw, err := NewTwilioGateway(TwilioConfig{
		AccountSID: "ACaaaabbbbccccddddeeeeffff00001111",
		AuthToken:  "secret-token",
		Sender:     "+15551234567",
		BaseURL:    srv.URL,
		Timeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewTwilioGateway: %v", err)
	}
	return gw, srv
}

func TestTwilioSendOTP(t *testing.T) {
	t.Parallel()

	var gotForm map[string][]string
	var gotUser, gotPass string
	var gotPath string

	gw, _ := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		body, _ := io.ReadAll(r.Body)
		parsed, _ := url.ParseQuery(string(body))
		gotForm = parsed

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"sid": "SM123", "status": "queued"})
	})

	// The number arrives the way the nasabah typed it, not in E.164.
	if err := gw.SendOTP(context.Background(), "0812-3456-7890", "123456"); err != nil {
		t.Fatalf("SendOTP: %v", err)
	}

	if want := "/2010-04-01/Accounts/ACaaaabbbbccccddddeeeeffff00001111/Messages.json"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotUser != "ACaaaabbbbccccddddeeeeffff00001111" || gotPass != "secret-token" {
		t.Errorf("basic auth = %q/%q, want the account sid and auth token", gotUser, gotPass)
	}
	if got := first(gotForm, "To"); got != "+6281234567890" {
		t.Errorf("To = %q, want the normalised E.164 number", got)
	}
	if got := first(gotForm, "From"); got != "+15551234567" {
		t.Errorf("From = %q, want the configured sender", got)
	}
	if _, ok := gotForm["MessagingServiceSid"]; ok {
		t.Error("MessagingServiceSid must not be sent alongside a bare From number")
	}
	if got := first(gotForm, "Body"); !strings.Contains(got, "123456") {
		t.Errorf("Body = %q, want it to carry the code", got)
	}
}

// A Messaging Service SID goes in its own parameter: sending it as From is a
// 400, and that would be an OTP nobody receives.
func TestTwilioSendOTPUsesMessagingService(t *testing.T) {
	t.Parallel()

	var gotForm map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM1","status":"accepted"}`))
	}))
	defer srv.Close()

	gw, err := NewTwilioGateway(TwilioConfig{
		AccountSID: "ACaaaabbbbccccddddeeeeffff00001111",
		AuthToken:  "secret-token",
		Sender:     "MGtest0000000000000000000000000000",
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("NewTwilioGateway: %v", err)
	}
	if err := gw.SendOTP(context.Background(), "081234567890", "654321"); err != nil {
		t.Fatalf("SendOTP: %v", err)
	}

	if got := first(gotForm, "MessagingServiceSid"); got != "MGtest0000000000000000000000000000" {
		t.Errorf("MessagingServiceSid = %q", got)
	}
	if _, ok := gotForm["From"]; ok {
		t.Error("From must not be sent alongside a messaging service")
	}
}

// An unroutable number must cost zero API calls: the aggregator bills per
// accepted message.
func TestTwilioSendOTPRejectsBadNumberWithoutCalling(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
	})

	if err := gw.SendOTP(context.Background(), "0215551234", "123456"); err == nil {
		t.Fatal("expected a rejection for a landline number")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("made %d HTTP calls for an invalid number, want 0", n)
	}
}

func TestTwilioRetriesServerErrorOnce(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM2","status":"queued"}`))
	})

	if err := gw.SendOTP(context.Background(), "081234567890", "123456"); err != nil {
		t.Fatalf("SendOTP: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2 (one failure, one retry)", n)
	}
}

// A 4xx is a rejection of this exact message. Re-sending burns the nasabah's
// remaining TTL and can double-bill, so it must not be retried.
func TestTwilioDoesNotRetryClientError(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":21608,"message":"unverified number"}`))
	})

	err := gw.SendOTP(context.Background(), "081234567890", "123456")
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", n)
	}
	if !strings.Contains(err.Error(), "21608") {
		t.Errorf("error should carry the Twilio code, got %v", err)
	}
}

// Twilio can answer 201 and still have rejected the message. Reporting that as
// success is how an undelivered OTP looks healthy.
func TestTwilioTreatsFailedStatusAsError(t *testing.T) {
	t.Parallel()

	gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM3","status":"failed","error_code":30008,"error_message":"unknown error"}`))
	})

	err := gw.SendOTP(context.Background(), "081234567890", "123456")
	if err == nil {
		t.Fatal("a 201 with status=failed must be an error")
	}
	if !strings.Contains(err.Error(), "30008") {
		t.Errorf("error should carry the Twilio error_code, got %v", err)
	}
}

// The code must never leave the process except in the message body.
func TestTwilioErrorNeverCarriesTheCode(t *testing.T) {
	t.Parallel()

	gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":20003,"message":"authenticate"}`))
	})

	err := gw.SendOTP(context.Background(), "081234567890", "998877")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "998877") {
		t.Fatalf("error leaked the OTP: %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked the auth token: %v", err)
	}
}

// Twilio quotes the destination number back inside its own error text, and
// SendOTP hands that text straight to slog. Error 21408 is the one every
// deployment meets first — it is the Geo Permissions checkbox — so this is not a
// hypothetical path, and a nasabah's phone number in the log aggregator is the
// PII leak the rest of this package avoids by only ever logging a suffix.
func TestTwilioErrorRedactsTheDestinationNumber(t *testing.T) {
	t.Parallel()

	gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":21408,"message":"Permission to send an SMS has not been enabled for the region indicated by the 'To' number +6281234567890."}`))
	})

	err := gw.SendOTP(context.Background(), "081234567890", "123456")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"6281234567890", "81234567890", "081234567890"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaked the destination number (%s): %v", leak, err)
		}
	}
	// The diagnosis has to survive the scrubbing, or the redaction has simply
	// traded one unusable log line for another.
	if !strings.Contains(err.Error(), "21408") {
		t.Errorf("error should still carry the Twilio code, got %v", err)
	}
	if !strings.Contains(err.Error(), "Geo") && !strings.Contains(err.Error(), "region") {
		t.Errorf("error should still carry Twilio's explanation, got %v", err)
	}
}

// A short Retry-After is obeyed; a long one is not waited out inside a request
// the nasabah is watching a spinner for.
func TestTwilioRetryAfter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		header    string
		wantCalls int32
	}{
		{"short enough to wait out", "1", 2},
		{"longer than the budget", "30", 1},
		{"not a number is ignored", "Wed, 01 Oct 2026 10:00:00 GMT", 2},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			gw, _ := newTestGateway(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":20429,"message":"Too Many Requests"}`))
			})

			if err := gw.SendOTP(context.Background(), "081234567890", "123456"); err == nil {
				t.Fatal("expected an error")
			}
			if n := calls.Load(); n != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", n, tc.wantCalls)
			}
		})
	}
}

func TestNewTwilioGatewayRejectsBadConfig(t *testing.T) {
	t.Parallel()

	cases := map[string]TwilioConfig{
		"no account sid": {AuthToken: "t", Sender: "+15551234567"},
		"api key sid":    {AccountSID: "SKaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", Sender: "+15551234567"},

		// The three shapes a prefix-only check used to wave through. Each one
		// boots a process that answers 404 to every send, which is the failure
		// this constructor exists to turn into a failed boot instead.
		"account sid truncated": {AccountSID: "ACxxx", AuthToken: "t", Sender: "+15551234567"},
		"account sid not hex":   {AccountSID: "ACtest0000000000000000000000000000", AuthToken: "t", Sender: "+15551234567"},
		"account sid too long":  {AccountSID: "ACaaaabbbbccccddddeeeeffff000011110", AuthToken: "t", Sender: "+15551234567"},

		"no auth token":   {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", Sender: "+15551234567"},
		"no sender":       {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t"},
		"sender not e164": {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", Sender: "BCA"},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewTwilioGateway(cfg); err == nil {
				t.Fatal("expected the boot to be refused")
			}
		})
	}
}

func TestNewProvider(t *testing.T) {
	t.Parallel()

	if _, err := NewProvider(ProviderConfig{Provider: "twilio", AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", Sender: "+15551234567"}); err != nil {
		t.Fatalf("twilio provider: %v", err)
	}
	if _, err := NewProvider(ProviderConfig{Provider: "indosat"}); err == nil {
		t.Fatal("an unknown provider must fail the boot, not fall back to silence")
	}
	// The old message listed twilio_verify as supported here, which it is not:
	// NewProvider has no case for it. Somebody reading that error would conclude
	// their value was wrong when the constructor was.
	_, err := NewProvider(ProviderConfig{Provider: "foo"})
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	if !strings.Contains(err.Error(), "NewVerifier") {
		t.Errorf("the error should point at NewVerifier for twilio_verify: %v", err)
	}
	if strings.Contains(err.Error(), "supported: twilio, twilio_verify") {
		t.Errorf("the error still claims NewProvider supports twilio_verify: %v", err)
	}
	if _, err := NewProvider(ProviderConfig{}); err == nil {
		t.Fatal("an empty provider must be an error here; the caller chooses the fallback")
	}
}

func first(form map[string][]string, key string) string {
	if v, ok := form[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

// --- Twilio Verify -----------------------------------------------------------

func newTestVerifier(t *testing.T, h http.HandlerFunc) (*TwilioVerifier, *httptest.Server) {
	t.Helper()
	return newTestVerifierCfg(t, h, TwilioVerifyConfig{Channels: []string{"sms", "call"}})
}

// newTestVerifierCfg fills in the credentials and the httptest base url, leaving
// the fields a test is actually about (channels, locale, ttl) to the caller.
func newTestVerifierCfg(t *testing.T, h http.HandlerFunc, cfg TwilioVerifyConfig) (*TwilioVerifier, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg.AccountSID = "ACaaaabbbbccccddddeeeeffff00001111"
	cfg.AuthToken = "secret-token"
	cfg.ServiceSID = "VAaaaabbbbccccddddeeeeffff00002222"
	cfg.BaseURL = srv.URL
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}

	v, err := NewTwilioVerifier(cfg)
	if err != nil {
		t.Fatalf("NewTwilioVerifier: %v", err)
	}
	return v, srv
}

func TestTwilioVerifyStart(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotForm map[string][]string
	var gotUser string
	v, _ := newTestVerifier(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, _, _ = r.BasicAuth()
		b, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"VE1","status":"pending"}`))
	})

	if err := v.StartVerification(context.Background(), "081234567890", ChannelSMS); err != nil {
		t.Fatalf("StartVerification: %v", err)
	}
	if want := "/v2/Services/VAaaaabbbbccccddddeeeeffff00002222/Verifications"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotUser != "ACaaaabbbbccccddddeeeeffff00001111" {
		t.Errorf("basic auth user = %q", gotUser)
	}
	// The number is normalised before it leaves, same as the Messages transport.
	if to := first(gotForm, "To"); to != "+6281234567890" {
		t.Errorf("To = %q, want +6281234567890", to)
	}
	if ch := first(gotForm, "Channel"); ch != "sms" {
		t.Errorf("Channel = %q, want sms", ch)
	}
	// Indonesian is in Verify's SMS template set, so the locale is sent.
	if loc := first(gotForm, "Locale"); loc != "id" {
		t.Errorf("Locale = %q, want id", loc)
	}
}

func TestTwilioVerifyStartRejectsBadNumberWithoutCalling(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	v, _ := newTestVerifier(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
	})

	if err := v.StartVerification(context.Background(), "0215551234", ChannelSMS); err == nil {
		t.Fatal("a landline must be refused")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("provider was called %d times for an unroutable number", n)
	}
}

func TestTwilioVerifyCheck(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		body    string
		wantOK  bool
		wantErr error
	}{
		{"approved", http.StatusOK, `{"status":"approved","valid":true}`, true, nil},
		// A wrong code is not an error: it is the ordinary path our own failure
		// counter acts on.
		{"wrong code", http.StatusOK, `{"status":"pending","valid":false}`, false, nil},
		// Verify drops a verification once it expires or is approved, so 404 means
		// the code the nasabah holds is no longer live.
		{"expired", http.StatusNotFound, `{"code":20404,"message":"not found"}`, false, ErrVerifyExpired},
		{"max check attempts", http.StatusTooManyRequests, `{"code":60202,"message":"max attempts"}`, false, ErrVerifyRateLimited},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, _ := newTestVerifier(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			ok, err := v.CheckVerification(context.Background(), "081234567890", "847291")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("approved = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

// Same rule as the Messages transport: neither the code nor the auth token may
// leave the process in an error, and Twilio's own text is scrubbed of numbers.
func TestTwilioVerifyErrorLeaksNothing(t *testing.T) {
	t.Parallel()

	v, _ := newTestVerifier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":60200,"message":"Invalid parameter To: +6281234567890"}`))
	})

	_, err := v.CheckVerification(context.Background(), "081234567890", "998877")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"998877", "secret-token", "6281234567890"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaked %q: %v", leak, err)
		}
	}
	if !strings.Contains(err.Error(), "60200") {
		t.Errorf("error should keep the Twilio code: %v", err)
	}
}

func TestNewTwilioVerifierRejectsBadConfig(t *testing.T) {
	t.Parallel()

	cases := map[string]TwilioVerifyConfig{
		"no service sid":               {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t"},
		"service sid truncated":        {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", ServiceSID: "VAxxx"},
		"service sid is a gateway sid": {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", ServiceSID: "ACaaaabbbbccccddddeeeeffff00001111"},
		"no account sid":               {AuthToken: "t", ServiceSID: "VAaaaabbbbccccddddeeeeffff00002222"},
		"no auth token":                {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", ServiceSID: "VAaaaabbbbccccddddeeeeffff00002222"},
		// A lower-cased paste is not an account sid. It used to boot cleanly and
		// then 404 on every verification, which this transport reports as
		// "nothing pending" — OTP_EXPIRED for every nasabah.
		"account sid lower-cased":   {AccountSID: "acaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", ServiceSID: "VAaaaabbbbccccddddeeeeffff00002222"},
		"account sid is an api key": {AccountSID: "SKaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", ServiceSID: "VAaaaabbbbccccddddeeeeffff00002222"},
		// An unknown channel name fails the boot rather than being dropped: an
		// operator who wrote "voice" believes voice is enabled.
		"unknown channel": {AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", ServiceSID: "VAaaaabbbbccccddddeeeeffff00002222", Channels: []string{"sms", "voice"}},
	}

	for name, cfg := range cases {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewTwilioVerifier(cfg); err == nil {
				t.Fatal("expected the boot to be refused")
			}
		})
	}
}

func TestNewVerifier(t *testing.T) {
	t.Parallel()

	if _, err := NewVerifier(ProviderConfig{
		Provider: "twilio_verify", AccountSID: "ACaaaabbbbccccddddeeeeffff00001111", AuthToken: "t", VerifyServiceSID: "VAaaaabbbbccccddddeeeeffff00002222",
	}); err != nil {
		t.Fatalf("twilio_verify: %v", err)
	}
	// A Gateway provider is not a Verifier, and silently accepting one would mean
	// a config that looks wired but can never check a code.
	if _, err := NewVerifier(ProviderConfig{Provider: "twilio"}); err == nil {
		t.Fatal("twilio (Messages) must not satisfy NewVerifier")
	}
	if !IsVerifierProvider("twilio_verify") || IsVerifierProvider("twilio") {
		t.Error("IsVerifierProvider must distinguish the two")
	}
}

// T5 — the call channel reaches Twilio as Channel=call.
//
// And without a Locale: Verify's voice template does not carry Indonesian, so
// Twilio would fall back to English anyway. Sending Locale=id there would be a
// parameter the response does not honour.
func TestTwilioVerifyStartCallChannel(t *testing.T) {
	t.Parallel()

	var gotForm map[string][]string
	v, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"VE2","status":"pending"}`))
	}, TwilioVerifyConfig{Channels: []string{"sms", "call"}})

	if err := v.StartVerification(context.Background(), "081234567890", ChannelCall); err != nil {
		t.Fatalf("StartVerification: %v", err)
	}
	if ch := first(gotForm, "Channel"); ch != "call" {
		t.Errorf("Channel = %q, want call", ch)
	}
	if loc, ok := gotForm["Locale"]; ok {
		t.Errorf("Locale must be omitted on a call it cannot be honoured for, got %q", loc)
	}
}

// T5b — a locale Verify can speak is sent on a call.
func TestTwilioVerifyStartCallSendsSupportedLocale(t *testing.T) {
	t.Parallel()

	var gotForm map[string][]string
	v, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"VE2","status":"pending"}`))
	}, TwilioVerifyConfig{Channels: []string{"call"}, Locale: "en"})

	if err := v.StartVerification(context.Background(), "081234567890", ChannelCall); err != nil {
		t.Fatalf("StartVerification: %v", err)
	}
	if loc := first(gotForm, "Locale"); loc != "en" {
		t.Errorf("Locale = %q, want en", loc)
	}
}

// T6 — a channel outside the allowlist costs nothing: no HTTP call at all.
func TestTwilioVerifyStartRefusesChannelOutsideAllowlist(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	v, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
	}, TwilioVerifyConfig{Channels: []string{"sms"}})

	err := v.StartVerification(context.Background(), "081234567890", ChannelCall)
	if !errors.Is(err, ErrChannelNotAllowed) {
		t.Fatalf("err = %v, want ErrChannelNotAllowed", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("provider was called %d times for a channel this deployment never enabled", n)
	}
}

// T7 — an empty channel is SMS, so an older app build that sends no channel keeps
// working unchanged.
func TestTwilioVerifyStartEmptyChannelIsSMS(t *testing.T) {
	t.Parallel()

	var gotForm map[string][]string
	v, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"VE3","status":"pending"}`))
	}, TwilioVerifyConfig{Channels: []string{"sms"}})

	if err := v.StartVerification(context.Background(), "081234567890", ""); err != nil {
		t.Fatalf("StartVerification: %v", err)
	}
	if ch := first(gotForm, "Channel"); ch != "sms" {
		t.Errorf("Channel = %q, want sms", ch)
	}
}

// T9 — regression: SMS_BASE_URL must not reach Verify.
//
// It used to. NewVerifier passed cfg.BaseURL — the Messages host — into the Verify
// transport, so a deployment that set SMS_BASE_URL sent every verification to
// api.twilio.com, got a 404, and post() reports a 404 as ErrVerifyExpired. The
// nasabah saw OTP_EXPIRED with perfectly good credentials, and no log line said
// otherwise. The two hosts are different, so the two settings are too.
func TestNewVerifierIgnoresMessagesBaseURL(t *testing.T) {
	t.Parallel()

	v, err := NewVerifier(ProviderConfig{
		Provider:         "twilio_verify",
		AccountSID:       "ACaaaabbbbccccddddeeeeffff00001111",
		AuthToken:        "t",
		VerifyServiceSID: "VAaaaabbbbccccddddeeeeffff00002222",
		BaseURL:          "http://messages-mock.invalid",
		// VerifyBaseURL deliberately empty: the real Verify host must be used.
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	tv, ok := v.(*TwilioVerifier)
	if !ok {
		t.Fatalf("NewVerifier returned %T", v)
	}
	const want = "https://verify.twilio.com/v2/Services/VAaaaabbbbccccddddeeeeffff00002222/Verifications"
	if tv.startURL != want {
		t.Errorf("startURL = %q, want %q", tv.startURL, want)
	}
	if strings.Contains(tv.startURL, "messages-mock") || strings.Contains(tv.checkURL, "messages-mock") {
		t.Error("SMS_BASE_URL leaked into the Verify transport")
	}
}

// T9b — SMS_VERIFY_BASE_URL is the setting that does move Verify.
func TestNewVerifierUsesVerifyBaseURL(t *testing.T) {
	t.Parallel()

	v, err := NewVerifier(ProviderConfig{
		Provider:         "twilio_verify",
		AccountSID:       "ACaaaabbbbccccddddeeeeffff00001111",
		AuthToken:        "t",
		VerifyServiceSID: "VAaaaabbbbccccddddeeeeffff00002222",
		VerifyBaseURL:    "http://verify-mock.invalid",
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if got := v.(*TwilioVerifier).startURL; !strings.HasPrefix(got, "http://verify-mock.invalid/") {
		t.Errorf("startURL = %q, want the SMS_VERIFY_BASE_URL host", got)
	}
}

// T15 — CodeTTL reports the configured expiry, defaulting to Twilio's 10 minutes.
func TestTwilioVerifyCodeTTL(t *testing.T) {
	t.Parallel()

	unset, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, _ *http.Request) {}, TwilioVerifyConfig{})
	if got := unset.CodeTTL(); got != 10*time.Minute {
		t.Errorf("unset CodeTTL = %v, want 10m", got)
	}

	set, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, _ *http.Request) {}, TwilioVerifyConfig{CodeTTL: 5 * time.Minute})
	if got := set.CodeTTL(); got != 5*time.Minute {
		t.Errorf("configured CodeTTL = %v, want 5m", got)
	}
}

// T16 — Verify is not retried. It keeps its own per-number send counter, and a
// blind second attempt spends the nasabah's allowance twice for one request.
func TestTwilioVerifyStartDoesNotRetry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	v, _ := newTestVerifier(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":20500,"message":"server error"}`))
	})

	if err := v.StartVerification(context.Background(), "081234567890", ChannelSMS); err == nil {
		t.Fatal("expected an error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("provider was called %d times, want exactly 1", n)
	}
}

// T11b — a plain 429 on start is a provider rate limit, same as on check.
func TestTwilioVerifyStartRateLimited(t *testing.T) {
	t.Parallel()

	v, _ := newTestVerifier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":60203,"message":"max send attempts reached"}`))
	})

	err := v.StartVerification(context.Background(), "081234567890", ChannelSMS)
	if !errors.Is(err, ErrVerifyRateLimited) {
		t.Fatalf("err = %v, want ErrVerifyRateLimited", err)
	}
}

// T14 — a provider slower than the timeout is an error, not a hung handler.
func TestTwilioVerifyStartTimesOut(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	v, _ := newTestVerifierCfg(t, func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}, TwilioVerifyConfig{Channels: []string{"sms"}, Timeout: 50 * time.Millisecond})

	// Registered after the server, so it runs before srv.Close: cleanups are
	// LIFO, and httptest's Close waits for outstanding handlers. Closing this
	// second deadlocks the test rather than failing it.
	t.Cleanup(func() { close(release) })

	done := make(chan error, 1)
	go func() { done <- v.StartVerification(context.Background(), "081234567890", ChannelSMS) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartVerification hung past its own timeout")
	}
}
