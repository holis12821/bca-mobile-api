package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// RateLimitResult holds the outcome of a rate limit check.
type RateLimitResult struct {
	Allowed   bool
	Current   int64
	Limit     int64
	Remaining int64
	RetryAt   time.Time // meaningful only when !Allowed
}

// RateLimiter implements sliding window rate limiting using a Redis sorted set.
// Algorithm: ZREMRANGEBYSCORE → ZCARD → ZADD → EXPIRE in one pipeline.
// The attempt is counted BEFORE knowing the outcome, so a burst of
// successful logins also throttles.
type RateLimiter struct {
	client *goredis.Client
}

func NewRateLimiter(client *goredis.Client) *RateLimiter {
	return &RateLimiter{client: client}
}

// Allow checks whether a request identified by key is within the limit
// for the given window. It always records the attempt (ZADD), even when
// the limit is already exceeded, so that the window slides correctly.
func (rl *RateLimiter) Allow(ctx context.Context, key string, limit int64, window time.Duration) (*RateLimitResult, error) {
	now := time.Now()
	windowStart := now.Add(-window)
	member := fmt.Sprintf("%s:%d", uuid.New().String(), now.UnixNano())

	pipe := rl.client.Pipeline()

	// Remove entries older than the window
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%d", windowStart.UnixNano()))

	// Count current entries in window
	cardCmd := pipe.ZCard(ctx, key)

	// Add the current attempt
	pipe.ZAdd(ctx, key, goredis.Z{Score: float64(now.UnixNano()), Member: member})

	// Set expiry on the key to auto-cleanup
	pipe.Expire(ctx, key, window+time.Second)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("rate limit pipeline: %w", err)
	}

	current := cardCmd.Val() // count BEFORE adding the new entry
	allowed := current < limit

	result := &RateLimitResult{
		Allowed:   allowed,
		Current:   current + 1, // including the one we just added
		Limit:     limit,
		Remaining: limit - current - 1,
	}
	if result.Remaining < 0 {
		result.Remaining = 0
	}
	if !allowed {
		// Estimate when the oldest entry expires out of the window
		result.RetryAt = now.Add(window)
	}

	return result, nil
}

// Reset removes a rate limit key (e.g., on successful login to reset device counter).
func (rl *RateLimiter) Reset(ctx context.Context, key string) error {
	return rl.client.Del(ctx, key).Err()
}