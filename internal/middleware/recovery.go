package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rv := recover(); rv != nil {
				slog.Error("panic recovered",
					"request_id", chimiddleware.GetReqID(r.Context()),
					"panic", rv,
					"stack", string(debug.Stack()),
				)
				response.Err(w, r, apperr.InternalError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}