package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/config"
)

// newTestRouter builds the real route tree. The repositories are wired to nil
// handles on purpose: every request in this file is answered by middleware
// before any storage is touched, so a route that started reaching the database
// would fail loudly instead of quietly passing.
func newTestRouter(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	return New(Deps{Config: cfg})
}

func productionLikeConfig(internalKey string) *config.Config {
	cfg := &config.Config{InternalAPIKey: internalKey}
	cfg.App.Env = "development"
	return cfg
}

// Internal endpoints are the CS backend's, not the public internet's.
func TestInternalEndpoints_RequireAPIKey(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig("s3cret-from-the-vault"))

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/onboarding/video-call/result"},
		{http.MethodPost, "/v1/onboarding/video-call/agent-token"},
		{http.MethodGet, "/v1/onboarding/sessions/onb_1/audit"},
		{http.MethodGet, "/v1/onboarding/monitoring"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Run("no key", func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Errorf("expected 403 without a key, got %d", rr.Code)
				}
			})

			t.Run("wrong key", func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				req.Header.Set("X-Internal-API-Key", "s3cret-from-the-vaul") // one byte short
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Errorf("expected 403 with a wrong key, got %d", rr.Code)
				}
			})
		})
	}
}

// A service started without INTERNAL_API_KEY must not fall back to a shared
// default — the endpoints simply stay closed.
func TestInternalEndpoints_ClosedWhenKeyUnset(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig(""))

	for _, key := range []string{"", "dev-internal-key"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/monitoring", nil)
		if key != "" {
			req.Header.Set("X-Internal-API-Key", key)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("key %q: expected 403, got %d", key, rr.Code)
		}
	}
}

// The dev-only helpers must not exist outside development: the route is absent,
// so the answer is the ordinary 404 envelope.
func TestDevEndpoints_OnlyInDevelopment(t *testing.T) {
	prod := &config.Config{InternalAPIKey: "k"}
	prod.App.Env = "production"

	r := newTestRouter(t, prod)

	for _, path := range []string{"/v1/dev/pin-public-key", "/v1/dev/encrypt-pin"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected the route to be absent, got %d", path, rr.Code)
		}
	}
}

func TestUnknownRoute_UsesTheStandardEnvelope(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig("k"))

	req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code"`) {
		t.Errorf("404 should use the error envelope, got %s", rr.Body.String())
	}
}

// Protected endpoints stay protected: no token, no access.
func TestProtectedEndpoints_RequireAccessToken(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig("k"))

	req := httptest.NewRequest(http.MethodGet, "/v1/account/balance", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without a token, got %d", rr.Code)
	}
}
