package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
	"github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

// RateLimit returns middleware that enforces a per-key rate limit.
// failClosed controls behaviour when Redis is unavailable:
//   - true  → deny (for auth endpoints)
//   - false → allow (for read-only endpoints)
func RateLimit(limiter *redis.RateLimiter, keyFunc func(*http.Request) string, limit int64, window time.Duration, failClosed bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFunc(r)
			result, err := limiter.Allow(r.Context(), key, limit, window)
			if err != nil {
				slog.Error("rate limit check failed",
					"request_id", chimiddleware.GetReqID(r.Context()),
					"key", key,
					"error", err,
				)
				if failClosed {
					// Auth endpoints: Redis down → deny
					response.Err(w, r, apperr.InternalError)
					return
				}
				// Read-only endpoints: Redis down → allow
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))

			if !result.Allowed {
				retryAfter := int(time.Until(result.RetryAt).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				response.Err(w, r, apperr.RateLimitExceeded)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}