package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/pkg/clientip"
)

func TestOnboardingAudit_CapturesContext(t *testing.T) {
	var captured *OnboardingAuditContext

	handler := OnboardingAudit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetOnboardingAuditCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// The request arrives from 192.0.2.1 (httptest's default RemoteAddr), which
	// the resolver is told to trust as a proxy — so its forwarding header is
	// believed. Without that trust the header is ignored; see the tests below.
	resolver, _ := clientip.NewResolver([]string{"192.0.2.1"})
	handler = RealIP(resolver)(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/onboarding/sessions", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.Header.Set("User-Agent", "BCA-Mobile/1.0")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if captured == nil {
		t.Fatal("audit context should be captured for onboarding routes")
	}
	if captured.IPAddress != "203.0.113.50" {
		t.Errorf("expected IP 203.0.113.50, got %s", captured.IPAddress)
	}
	if captured.UserAgent != "BCA-Mobile/1.0" {
		t.Errorf("expected user agent BCA-Mobile/1.0, got %s", captured.UserAgent)
	}
	if captured.Method != "POST" {
		t.Errorf("expected POST, got %s", captured.Method)
	}
	if captured.Path != "/v1/onboarding/sessions" {
		t.Errorf("expected /v1/onboarding/sessions, got %s", captured.Path)
	}
}

func TestOnboardingAudit_AlwaysCapturesWhenMounted(t *testing.T) {
	// Since the middleware is now scoped to onboarding routes via the router,
	// it always captures context for any request that reaches it.
	var captured *OnboardingAuditContext

	handler := OnboardingAudit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetOnboardingAuditCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/ocr/onb_test123", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if captured == nil {
		t.Fatal("audit context should always be set when middleware is mounted")
	}
	if captured.Method != "GET" {
		t.Errorf("expected GET, got %s", captured.Method)
	}
}

// A forwarding header from an untrusted peer is spoofing, not information:
// every one of these addresses lands in an audit log.
func TestClientIP_IgnoresHeadersFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	req.Header.Set("X-Real-Ip", "10.0.0.1")

	if ip := ClientIP(req); ip != "192.0.2.1" {
		t.Errorf("expected the peer address 192.0.2.1, got %s", ip)
	}
}

func TestClientIP_TrustedProxyXForwardedFor(t *testing.T) {
	resolver, bad := clientip.NewResolver([]string{"192.0.2.0/24"})
	if len(bad) != 0 {
		t.Fatalf("unexpected unparseable entries: %v", bad)
	}

	var got string
	handler := RealIP(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIP(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Client, then two proxy hops. The right-most non-trusted entry is the
	// real client as far as our infrastructure can vouch for it.
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "5.6.7.8" {
		t.Errorf("expected 5.6.7.8, got %s", got)
	}
}

func TestClientIP_TrustedProxyXRealIP(t *testing.T) {
	resolver, _ := clientip.NewResolver([]string{"192.0.2.1"})

	var got string
	handler := RealIP(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIP(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-Ip", "10.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %s", got)
	}
}

func TestClientIP_TrustedProxyRejectsJunkHeader(t *testing.T) {
	resolver, _ := clientip.NewResolver([]string{"192.0.2.1"})

	var got string
	handler := RealIP(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIP(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "192.0.2.1" {
		t.Errorf("expected fallback to peer address, got %s", got)
	}
}
