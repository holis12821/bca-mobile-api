package middleware

import (
	"net/http"

	"github.com/go-chi/cors"
)

// CORS trusts only the origins passed in. An empty list means no browser
// origin is allowed at all — the correct default for a mobile-only API
// (§11), and harmless for Postman or curl, which send no Origin header.
//
// A web frontend must be named explicitly via CORS_ALLOWED_ORIGINS. Note that
// `AllowCredentials` stays false: tokens travel in the Authorization header,
// never in a cookie, so there is nothing for a browser to attach implicitly.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	if allowedOrigins == nil {
		allowedOrigins = []string{}
	}

	return cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Device-ID", "X-Request-ID", "X-Idempotency-Key"},
		ExposedHeaders:   []string{"X-Request-ID", "X-Idempotent-Replayed", "X-RateLimit-Limit", "X-RateLimit-Remaining", "Retry-After"},
		AllowCredentials: false,
		MaxAge:           300,
	})
}