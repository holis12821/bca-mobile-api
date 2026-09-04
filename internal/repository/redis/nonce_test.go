package redis_test

import (
	"testing"

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