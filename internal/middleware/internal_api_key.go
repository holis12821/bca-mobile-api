package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// InternalAPIKey protects internal/admin endpoints with a shared secret.
// The client must send the key via the X-Internal-API-Key header.
//
// The comparison is constant-time: this is a bearer secret, and a byte-by-byte
// string compare leaks its prefix through timing. An empty configured key
// denies every request rather than accepting an empty header — a service
// started without INTERNAL_API_KEY must not expose the CS endpoints.
func InternalAPIKey(apiKey string) func(http.Handler) http.Handler {
	expected := []byte(apiKey)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(expected) == 0 {
				slog.Error("internal api key is not configured; denying internal endpoint",
					"path", r.URL.Path,
				)
				response.Err(w, r, apperr.Error{
					Status:  http.StatusForbidden,
					Code:    "FORBIDDEN",
					Message: "Akses ditolak.",
				})
				return
			}

			provided := []byte(r.Header.Get("X-Internal-API-Key"))
			if subtle.ConstantTimeCompare(provided, expected) != 1 {
				response.Err(w, r, apperr.Error{
					Status:  http.StatusForbidden,
					Code:    "FORBIDDEN",
					Message: "Akses ditolak.",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
