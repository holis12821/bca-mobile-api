package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// ProfileOTPCache holds the one-time code that authorises a profile change.
//
// It lives on the session instance (noeviction), not the cache instance: an
// evicted OTP would look to the verifier exactly like an expired one, and the
// nasabah would be told their correct code was wrong.
type ProfileOTPCache struct {
	client *goredis.Client
}

func NewProfileOTPCache(client *goredis.Client) *ProfileOTPCache {
	return &ProfileOTPCache{client: client}
}

func profileOTPKey(userID uuid.UUID) string {
	return "otp:profile:" + userID.String()
}

func profileOTPAttemptsKey(userID uuid.UUID) string {
	return "otp:profile:attempts:" + userID.String()
}

// Store saves the hashed code and returns its expiry.
func (c *ProfileOTPCache) Store(ctx context.Context, userID uuid.UUID, otpHash string, ttl time.Duration) (time.Time, error) {
	if err := c.client.Set(ctx, profileOTPKey(userID), otpHash, ttl).Err(); err != nil {
		return time.Time{}, fmt.Errorf("store profile otp: %w", err)
	}
	return time.Now().Add(ttl), nil
}

// Get returns the stored hash, or "" when there is none.
func (c *ProfileOTPCache) Get(ctx context.Context, userID uuid.UUID) (string, error) {
	v, err := c.client.Get(ctx, profileOTPKey(userID)).Result()
	if err == goredis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get profile otp: %w", err)
	}
	return v, nil
}

// Delete removes the code.
func (c *ProfileOTPCache) Delete(ctx context.Context, userID uuid.UUID) error {
	return c.client.Del(ctx, profileOTPKey(userID)).Err()
}

// IncrAttempt counts a failed verification and returns the new total.
// The counter outlives the code so a fresh code cannot reset the budget on
// its own — only a successful verification or a new request does.
func (c *ProfileOTPCache) IncrAttempt(ctx context.Context, userID uuid.UUID) (int, error) {
	key := profileOTPAttemptsKey(userID)
	pipe := c.client.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 30*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("incr profile otp attempt: %w", err)
	}
	return int(incr.Val()), nil
}

// ResetAttempts clears the failure counter.
func (c *ProfileOTPCache) ResetAttempts(ctx context.Context, userID uuid.UUID) error {
	return c.client.Del(ctx, profileOTPAttemptsKey(userID)).Err()
}
