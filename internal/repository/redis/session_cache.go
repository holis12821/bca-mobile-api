package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

const (
	sessionTTL     = 15 * time.Minute
	userSessionTTL = 7 * 24 * time.Hour // 7 days, matches refresh token TTL
)

// SessionCache stores sessions in Redis for fast lookup.
type SessionCache struct {
	client *goredis.Client
}

func NewSessionCache(client *goredis.Client) *SessionCache {
	return &SessionCache{client: client}
}

func sessionKey(userID uuid.UUID, deviceID string) string {
	return fmt.Sprintf("session:%s:%s", userID.String(), deviceID)
}

func userSessionsKey(userID uuid.UUID) string {
	return fmt.Sprintf("sessions:user:%s", userID.String())
}

// deviceKeyFor prefers the client-supplied device id — the value the JWT
// carries and every delete path passes — and falls back to the devices row
// UUID only for a caller that has not set it.
func deviceKeyFor(session *auth.Session) string {
	if session.DeviceKey != "" {
		return session.DeviceKey
	}
	return session.DeviceID.String()
}

// activeSessionKey marks a session id as live. The access token carries sid,
// so this is what the Auth middleware can check on every request without
// knowing anything else about the caller.
func activeSessionKey(sessionID uuid.UUID) string {
	return "session:active:" + sessionID.String()
}

// userActiveSessionsKey is the set of live session ids for a user, so
// logout-all does not need SCAN.
func userActiveSessionsKey(userID uuid.UUID) string {
	return "sessions:active:user:" + userID.String()
}

// StoreSession stores session data as a Redis Hash with TTL 15 minutes.
func (sc *SessionCache) StoreSession(ctx context.Context, session *auth.Session, displayName string, authMethod string) error {
	key := sessionKey(session.UserID, deviceKeyFor(session))

	pipe := sc.client.Pipeline()
	pipe.HSet(ctx, key, map[string]any{
		"session_id":    session.ID.String(),
		"user_id":       session.UserID.String(),
		"device_id":     session.DeviceID.String(),
		"display_name":  displayName,
		"auth_method":   authMethod,
		"ip":            session.IPAddress,
		"created_at":    session.CreatedAt.UTC().Format(time.RFC3339),
		"last_activity": time.Now().UTC().Format(time.RFC3339),
	})
	pipe.Expire(ctx, key, sessionTTL)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	return nil
}

// MarkActive records the session as live for ttl (the refresh token lifetime).
// Auth middleware reads this; Revoke* clears it.
func (sc *SessionCache) MarkActive(ctx context.Context, userID, sessionID uuid.UUID, ttl time.Duration) error {
	pipe := sc.client.Pipeline()
	pipe.Set(ctx, activeSessionKey(sessionID), userID.String(), ttl)
	pipe.SAdd(ctx, userActiveSessionsKey(userID), sessionID.String())
	pipe.Expire(ctx, userActiveSessionsKey(userID), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("mark session active: %w", err)
	}
	return nil
}

// IsActive reports whether the session id is marked live.
//
// A miss is not proof of revocation — the key can be evicted or the instance
// restarted — so the caller falls back to Postgres. found=false means "ask the
// database", never "reject".
func (sc *SessionCache) IsActive(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	n, err := sc.client.Exists(ctx, activeSessionKey(sessionID)).Result()
	if err != nil {
		return false, fmt.Errorf("check session active: %w", err)
	}
	return n > 0, nil
}

// inactiveSessionKey marks a session id that Postgres has confirmed is dead.
func inactiveSessionKey(sessionID uuid.UUID) string {
	return "session:inactive:" + sessionID.String()
}

// negativeSessionTTL is how long a confirmed-dead session is remembered.
//
// Short on purpose. A revoked session never becomes live again, so a longer TTL
// would also be correct, but a minute is already enough to absorb a client that
// keeps retrying with a dead token — which is the whole point — while keeping the
// blast radius small if this marker is ever written for the wrong reason.
const negativeSessionTTL = time.Minute

// MarkInactive records that Postgres confirmed this session is unusable.
//
// Without it, every request carrying a revoked-but-unexpired access token ran a
// Postgres query: the live marker is gone, so IsActive misses and the fallback
// fires again and again. Authenticated routes have no rate limiter, so one
// logged-out client could keep a database round trip per request going for the
// remaining 15 minutes of its token.
func (sc *SessionCache) MarkInactive(ctx context.Context, sessionID uuid.UUID, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = negativeSessionTTL
	}
	if err := sc.client.Set(ctx, inactiveSessionKey(sessionID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("mark session inactive: %w", err)
	}
	return nil
}

// IsKnownInactive reports whether a confirmed-dead marker exists.
func (sc *SessionCache) IsKnownInactive(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	n, err := sc.client.Exists(ctx, inactiveSessionKey(sessionID)).Result()
	if err != nil {
		return false, fmt.Errorf("check session inactive: %w", err)
	}
	return n > 0, nil
}

// RevokeActive clears the live marker for a single session.
func (sc *SessionCache) RevokeActive(ctx context.Context, userID, sessionID uuid.UUID) error {
	pipe := sc.client.Pipeline()
	pipe.Del(ctx, activeSessionKey(sessionID))
	pipe.SRem(ctx, userActiveSessionsKey(userID), sessionID.String())
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("revoke active session: %w", err)
	}
	return nil
}

// RevokeAllActive clears every live marker for a user.
func (sc *SessionCache) RevokeAllActive(ctx context.Context, userID uuid.UUID) error {
	setKey := userActiveSessionsKey(userID)
	ids, err := sc.client.SMembers(ctx, setKey).Result()
	if err != nil {
		return fmt.Errorf("list active sessions: %w", err)
	}

	keys := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		keys = append(keys, "session:active:"+id)
	}
	keys = append(keys, setKey)

	return sc.client.Del(ctx, keys...).Err()
}

// AddToUserSessions adds a device to the user's session set.
// Used for logout-all instead of SCAN.
func (sc *SessionCache) AddToUserSessions(ctx context.Context, userID uuid.UUID, deviceID string) error {
	key := userSessionsKey(userID)
	pipe := sc.client.Pipeline()
	pipe.SAdd(ctx, key, deviceID)
	pipe.Expire(ctx, key, userSessionTTL)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("add to user sessions: %w", err)
	}
	return nil
}

// DeleteSession removes a single session key from Redis.
func (sc *SessionCache) DeleteSession(ctx context.Context, userID uuid.UUID, deviceID string) error {
	key := sessionKey(userID, deviceID)
	return sc.client.Del(ctx, key).Err()
}

// InvalidateAllUserSessions removes all cached sessions for a user
// using the sessions:user:{id} Set — no SCAN needed.
func (sc *SessionCache) InvalidateAllUserSessions(ctx context.Context, userID uuid.UUID) error {
	setKey := userSessionsKey(userID)

	// Get all device IDs from the Set
	deviceIDs, err := sc.client.SMembers(ctx, setKey).Result()
	if err != nil {
		return fmt.Errorf("get user sessions: %w", err)
	}

	if len(deviceIDs) == 0 {
		return nil
	}

	// Build keys to delete: each session:{user_id}:{device_id} + the set itself
	keys := make([]string, 0, len(deviceIDs)+1)
	for _, did := range deviceIDs {
		keys = append(keys, sessionKey(userID, did))
	}
	keys = append(keys, setKey)

	return sc.client.Del(ctx, keys...).Err()
}
