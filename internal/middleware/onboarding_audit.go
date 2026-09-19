package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type onboardingAuditKey struct{}

// OnboardingAuditContext holds request metadata captured by the audit middleware.
type OnboardingAuditContext struct {
	RequestID string
	IPAddress string
	UserAgent string
	Method    string
	Path      string
	StartTime time.Time
}

// GetOnboardingAuditCtx retrieves audit context from the request context.
func GetOnboardingAuditCtx(ctx context.Context) *OnboardingAuditContext {
	if v, ok := ctx.Value(onboardingAuditKey{}).(*OnboardingAuditContext); ok {
		return v
	}
	return nil
}

// OnboardingAudit middleware captures request metadata for onboarding audit trail.
// Mount this only on /v1/onboarding routes. It enriches the context with
// request_id, ip, user_agent, method, and path for downstream audit logging.
func OnboardingAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auditCtx := &OnboardingAuditContext{
			RequestID: chimiddleware.GetReqID(r.Context()),
			IPAddress: extractClientIP(r),
			UserAgent: r.UserAgent(),
			Method:    r.Method,
			Path:      r.URL.Path,
			StartTime: time.Now(),
		}

		ctx := context.WithValue(r.Context(), onboardingAuditKey{}, auditCtx)

		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		// Log completed onboarding request
		duration := time.Since(auditCtx.StartTime)
		status := ww.Status()

		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}

		slog.Log(r.Context(), level, "onboarding request",
			"request_id", auditCtx.RequestID,
			"method", auditCtx.Method,
			"path", auditCtx.Path,
			"status", status,
			"duration_ms", duration.Milliseconds(),
			"ip", auditCtx.IPAddress,
		)
	})
}

func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	// Strip port from RemoteAddr
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}