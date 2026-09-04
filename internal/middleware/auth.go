package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type ctxKey int

const (
	ctxUserID    ctxKey = iota
	ctxSessionID
	ctxDeviceID
)

// UserIDFromCtx returns the authenticated user ID from the request context.
func UserIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

// SessionIDFromCtx returns the session ID from the request context.
func SessionIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxSessionID).(string)
	return v
}

// DeviceIDFromCtx returns the device ID from the request context.
func DeviceIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxDeviceID).(string)
	return v
}

// Auth verifies the Bearer access token and injects user/session/device IDs into context.
func Auth(jwtMgr *crypto.JWTManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			if token == "" {
				response.Err(w, r, apperr.TokenInvalid)
				return
			}

			claims, err := jwtMgr.VerifyToken(token, crypto.TokenTypeAccess)
			if err != nil {
				if strings.Contains(err.Error(), "token is expired") {
					response.Err(w, r, apperr.TokenExpired)
					return
				}
				response.Err(w, r, apperr.TokenInvalid)
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, ctxUserID, claims.Subject)
			ctx = context.WithValue(ctx, ctxSessionID, claims.SessionID)
			ctx = context.WithValue(ctx, ctxDeviceID, claims.DeviceID)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return auth[7:]
	}
	return ""
}