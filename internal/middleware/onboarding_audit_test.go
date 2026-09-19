package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOnboardingAudit_CapturesContext(t *testing.T) {
	var captured *OnboardingAuditContext

	handler := OnboardingAudit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetOnboardingAuditCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

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

func TestExtractClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	ip := extractClientIP(req)
	if ip != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %s", ip)
	}
}

func TestExtractClientIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-Ip", "10.0.0.1")
	ip := extractClientIP(req)
	if ip != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %s", ip)
	}
}