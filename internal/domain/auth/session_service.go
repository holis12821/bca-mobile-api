package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// RefreshToken implements refresh token rotation with reuse detection.
//
// Flow:
//  1. Parse + verify JWT (enforce typ:refresh)
//  2. Hash the old token
//  3. Check revoked set — if found, this is a replay: revoke ALL sessions
//  4. Find session in DB by hash
//  5. Verify session not expired, user_id + device_id match claims
//  6. Generate new token pair
//  7. Update session with new hash + new expires_at
//  8. Mark old hash revoked with TTL = remaining lifetime
//  9. Update Redis cache
func (s *Service) RefreshToken(ctx context.Context, rawToken, clientIP string) (*LoginResponse, error) {
	// 1. Verify JWT and enforce typ:refresh
	claims, err := s.jwtMgr.VerifyToken(rawToken, crypto.TokenTypeRefresh)
	if err != nil {
		return nil, apperr.TokenInvalid
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, apperr.TokenInvalid
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return nil, apperr.TokenInvalid
	}

	// 2. Hash the old token
	oldHash := hashRefreshToken(rawToken)

	// 3. Check if hash is in revoked set → reuse detection
	if s.tokenRevocation != nil {
		revoked, err := s.tokenRevocation.IsRevoked(ctx, oldHash)
		if err != nil {
			slog.Error("check revoked token failed", "error", err)
			// Fail-closed for security: if we can't check, deny
			return nil, apperr.TokenInvalid
		}
		if revoked {
			// SECURITY: Token replay detected — revoke ALL sessions
			s.handleSuspiciousLogin(ctx, userID, oldHash, clientIP)
			return nil, apperr.TokenInvalid
		}
	}

	// 4. Find session in DB by refresh token hash
	session, err := s.sessions.FindByRefreshTokenHash(ctx, oldHash)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if session == nil {
		return nil, apperr.TokenInvalid
	}

	// 5. Verify session
	if session.ExpiresAt.Before(time.Now()) {
		return nil, apperr.TokenExpired
	}
	if session.UserID != userID || session.ID != sessionID {
		return nil, apperr.TokenInvalid
	}

	// 6. Generate new token pair
	newTokenPair, err := s.jwtMgr.GenerateTokenPair(
		userID.String(), sessionID.String(), claims.DeviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("generate tokens: %w", err)
	}

	newHash := hashRefreshToken(newTokenPair.RefreshToken)
	newExpiresAt := time.Now().Add(s.refreshTTL)

	// 7. Update session in DB with new hash
	if err := s.sessions.UpdateRefreshToken(ctx, sessionID, newHash, newExpiresAt); err != nil {
		return nil, fmt.Errorf("update refresh token: %w", err)
	}

	// 8. Mark old hash as revoked with TTL = remaining lifetime
	remainingTTL := time.Until(session.ExpiresAt)
	if s.tokenRevocation != nil {
		if err := s.tokenRevocation.MarkRevoked(ctx, oldHash, remainingTTL); err != nil {
			slog.Error("mark token revoked failed", "error", err)
			// Non-fatal: rotation succeeded, revocation tracking is best-effort
		}
	}

	// 9. Update Redis session cache. DeviceKey carries the client device id so
	// the cache key matches the one logout deletes.
	if s.sessionCache != nil {
		session.DeviceKey = claims.DeviceID
		if err := s.sessionCache.StoreSession(ctx, session, "", session.AuthMethod); err != nil {
			slog.Error("refresh session cache failed", "error", err)
		}
		// Rotation extends the session, so the live marker is extended too.
		if err := s.sessionCache.MarkActive(ctx, userID, sessionID, s.refreshTTL); err != nil {
			slog.Error("mark session active on refresh failed", "error", err)
		}
	}

	// Audit
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			SessionID:    &sessionID,
			Action:       AuditTokenRefresh,
			ResourceType: "session",
			ResourceID:   sessionID.String(),
			IPAddress:    clientIP,
			Metadata:     map[string]any{"device_id": claims.DeviceID},
		})
	}

	// Resolve user info for response
	user, err := s.users.FindByDeviceID(ctx, claims.DeviceID)
	if err != nil || user == nil {
		// Session is valid so return tokens even without user info
		return &LoginResponse{
			AccessToken:  newTokenPair.AccessToken,
			RefreshToken: newTokenPair.RefreshToken,
			TokenType:    "Bearer",
			ExpiresIn:    int(s.accessTTL.Seconds()),
		}, nil
	}

	return &LoginResponse{
		AccessToken:  newTokenPair.AccessToken,
		RefreshToken: newTokenPair.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.accessTTL.Seconds()),
		User: UserInfo{
			ID:          user.ID.String(),
			DisplayName: user.DisplayName,
			FullName:    user.FullName,
		},
	}, nil
}

// Logout revokes a single session.
func (s *Service) Logout(ctx context.Context, sessionID, userID uuid.UUID, deviceID string) error {
	if err := s.sessions.RevokeByID(ctx, sessionID); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}

	if s.sessionCache != nil {
		if err := s.sessionCache.DeleteSession(ctx, userID, deviceID); err != nil {
			slog.Error("delete session cache failed", "error", err)
		}
		// Clearing the live marker is what actually makes the access token
		// stop working. Revoking the row alone only stopped the refresh.
		if err := s.sessionCache.RevokeActive(ctx, userID, sessionID); err != nil {
			slog.Error("revoke active session failed", "error", err)
		}
	}

	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			SessionID:    &sessionID,
			Action:       AuditLogout,
			ResourceType: "session",
			ResourceID:   sessionID.String(),
			Metadata:     map[string]any{"device_id": deviceID},
		})
	}

	return nil
}

// LogoutAll revokes all sessions for a user.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID, currentSessionID *uuid.UUID) error {
	if err := s.sessions.RevokeByUserID(ctx, userID); err != nil {
		return fmt.Errorf("revoke all sessions: %w", err)
	}

	if s.sessionCache != nil {
		if err := s.sessionCache.InvalidateAllUserSessions(ctx, userID); err != nil {
			slog.Error("invalidate all sessions failed", "error", err)
		}
		if err := s.sessionCache.RevokeAllActive(ctx, userID); err != nil {
			slog.Error("revoke all active sessions failed", "error", err)
		}
	}

	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			SessionID:    currentSessionID,
			Action:       AuditLogoutAll,
			ResourceType: "session",
			Metadata:     map[string]any{"all_devices": true},
		})
	}

	return nil
}

// handleSuspiciousLogin revokes ALL sessions for the user and logs an audit event.
// Called when a revoked refresh token is replayed (reuse detection).
func (s *Service) handleSuspiciousLogin(ctx context.Context, userID uuid.UUID, reusedHash, clientIP string) {
	slog.Warn("refresh token reuse detected — revoking all sessions",
		"user_id", userID,
		"reused_hash", reusedHash,
		"client_ip", clientIP,
	)

	// Revoke all sessions in DB
	if err := s.sessions.RevokeByUserID(ctx, userID); err != nil {
		slog.Error("revoke all sessions on suspicious login failed", "error", err)
	}

	// Invalidate all Redis sessions
	if s.sessionCache != nil {
		if err := s.sessionCache.InvalidateAllUserSessions(ctx, userID); err != nil {
			slog.Error("invalidate redis sessions on suspicious login failed", "error", err)
		}
		if err := s.sessionCache.RevokeAllActive(ctx, userID); err != nil {
			slog.Error("revoke active sessions on suspicious login failed", "error", err)
		}
	}

	// Audit
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			Action:       AuditSuspiciousLogin,
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata: map[string]any{
				"reason":      "refresh_token_reuse",
				"reused_hash": reusedHash,
			},
		})
	}

	// The nasabah is told about this the next time the app polls: a SECURITY
	// notification row is written by the caller-visible audit trail above and
	// surfaced through GET /notifications. Push delivery rides on the same
	// row once a provider is configured (see internal/pkg/push).
	if s.notifier != nil {
		s.notifier.Security(ctx, userID,
			"Aktivitas mencurigakan terdeteksi",
			"Semua sesi Anda telah diakhiri karena terdeteksi penggunaan token yang tidak wajar. Silakan login kembali.",
		)
	}
}

func hashRefreshToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// IsSessionActive implements middleware.SessionValidator.
//
// Redis is the fast path and Postgres is the authority. A missing marker is
// not evidence of revocation — keys get evicted and instances get restarted —
// so it falls through to the sessions table and re-warms the marker on the way
// back. A revoked or expired row is the only thing that returns false, and a
// datastore error returns an error so the middleware can fail closed.
func (s *Service) IsSessionActive(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	if s.sessionCache != nil {
		active, err := s.sessionCache.IsActive(ctx, sessionID)
		if err != nil {
			slog.Warn("session cache lookup failed; falling back to postgres",
				"session_id", sessionID, "error", err)
		} else if active {
			return true, nil
		}

		// A cache miss is not proof of revocation, so the Postgres fallback
		// below has to run — but its ANSWER is worth remembering. A client that
		// keeps calling with a dead token otherwise buys one database round trip
		// per request, and authenticated routes carry no rate limiter.
		known, err := s.sessionCache.IsKnownInactive(ctx, sessionID)
		if err != nil {
			slog.Warn("session inactive-marker lookup failed",
				"session_id", sessionID, "error", err)
		} else if known {
			return false, nil
		}
	}

	session, err := s.sessions.FindActiveByID(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("find active session: %w", err)
	}
	if session == nil {
		if s.sessionCache != nil {
			if err := s.sessionCache.MarkInactive(ctx, sessionID, 0); err != nil {
				slog.Warn("mark session inactive failed", "session_id", sessionID, "error", err)
			}
		}
		return false, nil
	}

	if s.sessionCache != nil {
		ttl := time.Until(session.ExpiresAt)
		if ttl > 0 {
			if err := s.sessionCache.MarkActive(ctx, session.UserID, session.ID, ttl); err != nil {
				slog.Warn("re-warm session marker failed", "session_id", sessionID, "error", err)
			}
		}
	}

	return true, nil
}
