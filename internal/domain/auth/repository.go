package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// UserRepository defines data access for users during authentication.
type UserRepository interface {
	// FindByDeviceID resolves the user from an active device_id.
	// Returns nil, nil if no active device row exists.
	FindByDeviceID(ctx context.Context, deviceID string) (*User, error)

	// IncrementFailedAttempts atomically increments failed_pin_attempts
	// and returns the NEW value (UPDATE ... RETURNING).
	// Branching on a stale in-memory value is a race — always use the returned value.
	IncrementFailedAttempts(ctx context.Context, userID uuid.UUID) (newCount int, err error)

	// ResetFailedAttempts sets failed_pin_attempts to 0 and updates last_login_at.
	ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error

	// SetLockedUntil sets the locked_until timestamp on the user row.
	SetLockedUntil(ctx context.Context, userID uuid.UUID, lockedUntil *time.Time) error
}

// DeviceRepository defines data access for devices.
type DeviceRepository interface {
	// FindActiveByDeviceID finds the active (non-revoked) device row.
	// Returns nil, nil if not found.
	FindActiveByDeviceID(ctx context.Context, deviceID string) (*Device, error)

	// UpdateLastActive updates last_active_at on the device.
	UpdateLastActive(ctx context.Context, deviceID uuid.UUID) error
}

// SessionRepository defines data access for sessions in PostgreSQL.
type SessionRepository interface {
	// Create inserts a new session row.
	Create(ctx context.Context, session *Session) error

	// FindByRefreshTokenHash finds an active (non-revoked) session by its refresh token hash.
	FindByRefreshTokenHash(ctx context.Context, hash string) (*Session, error)

	// UpdateRefreshToken atomically swaps the refresh token hash and resets expires_at.
	UpdateRefreshToken(ctx context.Context, sessionID uuid.UUID, newHash string, expiresAt time.Time) error

	// RevokeByID revokes a single session.
	RevokeByID(ctx context.Context, sessionID uuid.UUID) error

	// RevokeByUserID revokes all sessions for a user.
	RevokeByUserID(ctx context.Context, userID uuid.UUID) error
}

// SessionCache defines session storage in Redis.
type SessionCache interface {
	// StoreSession stores session data in Redis with TTL.
	StoreSession(ctx context.Context, session *Session, displayName string, authMethod string) error

	// AddToUserSessions adds a device to the user's session set.
	AddToUserSessions(ctx context.Context, userID uuid.UUID, deviceID string) error

	// DeleteSession removes a single session from Redis.
	DeleteSession(ctx context.Context, userID uuid.UUID, deviceID string) error

	// InvalidateAllUserSessions removes all cached sessions for a user
	// using the sessions:user:{id} Set (no SCAN).
	InvalidateAllUserSessions(ctx context.Context, userID uuid.UUID) error
}

// TokenRevocationCache tracks revoked refresh token hashes in Redis.
type TokenRevocationCache interface {
	// MarkRevoked stores refresh:revoked:{hash} with the given TTL.
	MarkRevoked(ctx context.Context, hash string, ttl time.Duration) error

	// IsRevoked checks whether the hash exists in the revoked set.
	IsRevoked(ctx context.Context, hash string) (bool, error)
}

// BiometricKeyRepository defines data access for biometric keys.
type BiometricKeyRepository interface {
	// FindActiveByKeyID finds an active biometric key by its key_id.
	FindActiveByKeyID(ctx context.Context, keyID string) (*BiometricKey, error)

	// Create inserts a new biometric key row.
	Create(ctx context.Context, key *BiometricKey) error
}

// BiometricChallengeCache stores and consumes biometric challenges in Redis.
type BiometricChallengeCache interface {
	// StoreChallenge stores a challenge with TTL 60s.
	StoreChallenge(ctx context.Context, challengeID string, data *ChallengeData) error

	// ConsumeChallenge atomically retrieves and deletes the challenge (GETDEL).
	// Returns nil if not found or expired.
	ConsumeChallenge(ctx context.Context, challengeID string) (*ChallengeData, error)
}

// AuditRepository inserts audit log entries into PostgreSQL.
type AuditRepository interface {
	Insert(ctx context.Context, entry *AuditEntry) error
}

// RateLimitResult holds the outcome of a rate limit check.
type RateLimitResult struct {
	Allowed   bool
	Current   int64
	Limit     int64
	Remaining int64
	RetryAt   time.Time // meaningful only when !Allowed
}

// RateLimiter checks rate limits for authentication endpoints.
// Redis failure → fail-closed (deny).
type RateLimiter interface {
	CheckLoginDevice(ctx context.Context, deviceID string) (*RateLimitResult, error)
	CheckLoginIP(ctx context.Context, ip string) (*RateLimitResult, error)
	CheckPINVerify(ctx context.Context, userID string) (*RateLimitResult, error)
	ResetLoginDevice(ctx context.Context, deviceID string) error
}

// LockoutStatus represents the current lockout state of an account.
type LockoutStatus struct {
	Locked      bool
	LockedUntil time.Time
}

// LockoutChecker manages account lockout state.
type LockoutChecker interface {
	IsLocked(ctx context.Context, userID string) (*LockoutStatus, error)
	LockAccount(ctx context.Context, userID string) (*LockoutStatus, error)
	Unlock(ctx context.Context, userID string) error
}