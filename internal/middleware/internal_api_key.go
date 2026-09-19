package middleware

import (
	"net/http"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// InternalAPIKey protects internal/admin endpoints with a shared secret.
// The client must send the key via the X-Internal-API-Key header.
func InternalAPIKey(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-Internal-API-Key")
			if provided == "" || provided != apiKey {
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