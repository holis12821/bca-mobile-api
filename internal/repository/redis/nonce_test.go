package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestNonceStore_FreshNonce(t *testing.T) {
	client := setupTestRedis(t)
	ns := redisrepo.NewNonceStore(client)

	fresh, err := ns.CheckAndMark("nonce-fresh-" + t.Name())
	require.NoError(t, err)
	assert.True(t, fresh, "first use of nonce should be fresh")
}

func TestNonceStore_ReplayedNonce(t *testing.T) {
	client := setupTestRedis(t)
	ns := redisrepo.NewNonceStore(client)

	nonce := "nonce-replay-" + t.Name()

	fresh, err := ns.CheckAndMark(nonce)
	require.NoError(t, err)
	assert.True(t, fresh)

	// Second use → replayed
	fresh, err = ns.CheckAndMark(nonce)
	require.NoError(t, err)
	assert.False(t, fresh, "reused nonce should be rejected")
}

func TestSignalingTokenStore_SingleUse(t *testing.T) {
	client := setupTestRedis(t)
	store := redisrepo.NewSignalingTokenStore(client)
	ctx := context.Background()

	jti := "signal-jti-" + t.Name()

	fresh, err := store.ConsumeSignalingToken(ctx, jti, time.Minute)
	require.NoError(t, err)
	assert.True(t, fresh, "first connect with a signaling token should be accepted")

	// The URL carrying this token is a credential until the jti is spent.
	fresh, err = store.ConsumeSignalingToken(ctx, jti, time.Minute)
	require.NoError(t, err)
	assert.False(t, fresh, "a replayed signaling token must be rejected")
}

func TestSignalingTokenStore_UntrackableToken(t *testing.T) {
	client := setupTestRedis(t)
	store := redisrepo.NewSignalingTokenStore(client)
	ctx := context.Background()

	// No jti, or an already-expired token: neither can be tracked, so neither
	// is treated as a fresh first use.
	fresh, err := store.ConsumeSignalingToken(ctx, "", time.Minute)
	require.NoError(t, err)
	assert.False(t, fresh)

	fresh, err = store.ConsumeSignalingToken(ctx, "expired-jti", 0)
	require.NoError(t, err)
	assert.False(t, fresh)
}
