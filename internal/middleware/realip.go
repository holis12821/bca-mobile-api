package middleware

import (
	"context"
	"net/http"

	"github.com/holis12821/bca-mobile-api/internal/pkg/clientip"
)

type clientIPKey struct{}

// RealIP resolves the caller's IP once, at the edge, and puts it in the request
// context. Handlers and downstream middleware read it from there instead of
// each re-parsing X-Forwarded-For with slightly different rules — which is how
// two implementations of the same thing ended up disagreeing.
func RealIP(resolver *clientip.Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := resolver.Resolve(r)
			ctx := context.WithValue(r.Context(), clientIPKey{}, ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIP returns the resolved address for this request. It falls back to the
// raw peer address when RealIP is not mounted (tests, direct handler calls).
func ClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey{}).(string); ok && ip != "" {
		return ip
	}
	var resolver *clientip.Resolver // nil resolver trusts nothing
	return resolver.Resolve(r)
}
