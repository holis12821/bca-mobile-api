package middleware

import (
	"net/http"
	"strings"
)

// BodyLimit caps the size of a request body.
//
// Handlers decoded JSON straight off r.Body with no ceiling, so a single
// request could ask the process to allocate as much memory as the client cared
// to send. Multipart endpoints (KTP photo, document upload) set their own
// larger limit with http.MaxBytesReader and are skipped here so this does not
// truncate them.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || maxBytes <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
				next.ServeHTTP(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
