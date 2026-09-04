package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestLockout_NotLockedByDefault(t *testing.T) {
	client := setupTestRedis(t)
	lm := redisrepo.NewLockoutManager(client)
	ctx := context.Background()

	status, err := lm.IsLocked(ctx, "user-not-locked")
	require.NoError(t, err)
	assert.False(t, status.Locked)
}

func TestLockout_LockAccount(t *testing.T) {
	client := setupTestRedis(t)
	lm := redisrepo.NewLockoutManager(client)
	ctx := context.Background()
	userID := "user-lock-" + t.Name()

	status, err := lm.LockAccount(ctx, userID)
	require.NoError(t, err)
	assert.True(t, status.Locked)
	assert.True(t, status.LockedUntil.After(time.Now()))

	// Verify locked
	status, err = lm.IsLocked(ctx, userID)
	require.NoError(t, err)
	assert.True(t, status.Locked)
}

func TestLockout_EscalateAfterThree(t *testing.T) {
	client := setupTestRedis(t)
	lm := redisrepo.NewLockoutManager(client)
	ctx := context.Background()
	userID := "user-escalate-" + t.Name()

	// Lock 3 times
	for i := 0; i < 3; i++ {
		_, err := lm.LockAccount(ctx, userID)
		require.NoError(t, err)
	}

	// Check count
	count, err := lm.GetLockoutCount(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)

	// 3rd lockout should have escalated to 24h
	status, err := lm.IsLocked(ctx, userID)
	require.NoError(t, err)
	assert.True(t, status.Locked)

	// The lock duration should be close to 24h (not 30m)
	remaining := time.Until(status.LockedUntil)
	assert.Greater(t, remaining, 23*time.Hour, "3rd lockout should escalate to ~24h")
}

func TestLockout_Unlock(t *testing.T) {
	client := setupTestRedis(t)
	lm := redisrepo.NewLockoutManager(client)
	ctx := context.Background()
	userID := "user-unlock-" + t.Name()

	_, err := lm.LockAccount(ctx, userID)
	require.NoError(t, err)

	err = lm.Unlock(ctx, userID)
	require.NoError(t, err)

	status, err := lm.IsLocked(ctx, userID)
	require.NoError(t, err)
	assert.False(t, status.Locked)
}