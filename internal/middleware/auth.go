package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
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

// SessionValidator answers whether the session behind an access token is still
// live. §2.1 of docs/04-SECURITY.md lists this as steps 12 and 13 of every
// authenticated request; until it existed, verifying the JWT signature was the
// whole check, so logging out revoked the refresh token while the access token
// kept working — including for transfers — for the rest of its 15 minutes.
type SessionValidator interface {
	// IsSessionActive reports whether sessionID is still usable.
	IsSessionActive(ctx context.Context, sessionID uuid.UUID) (bool, error)
}

// Auth verifies the Bearer access token and injects user/session/device IDs into context.
//
// validator may be nil, which skips the session check — that is only for tests
// that construct a router without a datastore. Production wiring always passes
// one; router.New logs loudly if it cannot.
func Auth(jwtMgr *crypto.JWTManager, validator SessionValidator) func(http.Handler) http.Handler {
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

			if validator != nil {
				sessionID, err := uuid.Parse(claims.SessionID)
				if err != nil {
					response.Err(w, r, apperr.TokenInvalid)
					return
				}

				active, err := validator.IsSessionActive(r.Context(), sessionID)
				if err != nil {
					// Fail closed. A token whose session cannot be confirmed
					// must not move money; the alternative is that a datastore
					// outage silently re-enables every revoked session.
					slog.Error("session validation failed",
						"session_id", claims.SessionID, "error", err)
					response.Err(w, r, apperr.InternalError)
					return
				}
				if !active {
					response.Err(w, r, apperr.SessionRevoked)
					return
				}
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
