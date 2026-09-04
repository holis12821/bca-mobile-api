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

// StoreSession stores session data as a Redis Hash with TTL 15 minutes.
func (sc *SessionCache) StoreSession(ctx context.Context, session *auth.Session, displayName string, authMethod string) error {
	key := sessionKey(session.UserID, session.DeviceID.String())

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