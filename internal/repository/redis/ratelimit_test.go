package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestRateLimiter_AllowWithinLimit(t *testing.T) {
	client := setupTestRedis(t)
	rl := redisrepo.NewRateLimiter(client)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		result, err := rl.Allow(ctx, "test:allow:"+t.Name(), 5, time.Minute)
		require.NoError(t, err)
		assert.True(t, result.Allowed, "request %d should be allowed", i+1)
	}
}

func TestRateLimiter_DenyOverLimit(t *testing.T) {
	client := setupTestRedis(t)
	rl := redisrepo.NewRateLimiter(client)
	ctx := context.Background()
	key := "test:deny:" + t.Name()

	// Use up the limit
	for i := 0; i < 5; i++ {
		_, err := rl.Allow(ctx, key, 5, time.Minute)
		require.NoError(t, err)
	}

	// 6th request should be denied
	result, err := rl.Allow(ctx, key, 5, time.Minute)
	require.NoError(t, err)
	assert.False(t, result.Allowed, "request 6 should be denied")
	assert.Equal(t, int64(0), result.Remaining)
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	client := setupTestRedis(t)
	rl := redisrepo.NewRateLimiter(client)
	ctx := context.Background()
	key := "test:expiry:" + t.Name()

	// Short window for testing
	window := 500 * time.Millisecond

	for i := 0; i < 3; i++ {
		_, err := rl.Allow(ctx, key, 3, window)
		require.NoError(t, err)
	}

	// Should be denied now
	result, err := rl.Allow(ctx, key, 3, window)
	require.NoError(t, err)
	assert.False(t, result.Allowed)

	// Wait for window to expire
	time.Sleep(600 * time.Millisecond)

	// Should be allowed again
	result, err = rl.Allow(ctx, key, 3, window)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
}

func TestRateLimiter_SlidingWindow(t *testing.T) {
	client := setupTestRedis(t)
	rl := redisrepo.NewRateLimiter(client)
	ctx := context.Background()
	key := "test:sliding:" + t.Name()
	window := 1 * time.Second

	// Add 2 entries
	for i := 0; i < 2; i++ {
		_, err := rl.Allow(ctx, key, 3, window)
		require.NoError(t, err)
	}

	// Wait for half the window — old entries start sliding out
	time.Sleep(600 * time.Millisecond)

	// Add 1 more — should still be allowed (old entries partially expired)
	result, err := rl.Allow(ctx, key, 3, window)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
}

func TestRateLimiter_Reset(t *testing.T) {
	client := setupTestRedis(t)
	rl := redisrepo.NewRateLimiter(client)
	ctx := context.Background()
	key := "test:reset:" + t.Name()

	// Fill the limit
	for i := 0; i < 5; i++ {
		_, err := rl.Allow(ctx, key, 5, time.Minute)
		require.NoError(t, err)
	}

	// Reset
	err := rl.Reset(ctx, key)
	require.NoError(t, err)

	// Should be allowed again
	result, err := rl.Allow(ctx, key, 5, time.Minute)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
}

func TestRateLimiter_IndependentKeys(t *testing.T) {
	client := setupTestRedis(t)
	rl := redisrepo.NewRateLimiter(client)
	ctx := context.Background()

	// Fill one key
	for i := 0; i < 5; i++ {
		_, err := rl.Allow(ctx, "test:indep:a:"+t.Name(), 5, time.Minute)
		require.NoError(t, err)
	}

	// Other key should still be allowed
	result, err := rl.Allow(ctx, "test:indep:b:"+t.Name(), 5, time.Minute)
	require.NoError(t, err)
	assert.True(t, result.Allowed, "different keys should have independent counters")
}