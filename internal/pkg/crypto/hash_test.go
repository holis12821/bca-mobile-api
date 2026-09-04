package crypto_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

var testParams = crypto.Argon2Params{
	Time:       1,
	Memory:     64 * 1024,
	Threads:    4,
	KeyLength:  32,
	SaltLength: 16,
}

func TestHashPassword_VerifyCorrectPIN(t *testing.T) {
	ctx := context.Background()
	hash, err := crypto.HashPassword(ctx, "123456", testParams)
	require.NoError(t, err)

	match, err := crypto.VerifyPassword(ctx, "123456", hash)
	require.NoError(t, err)
	assert.True(t, match)
}

func TestHashPassword_VerifyWrongPIN(t *testing.T) {
	ctx := context.Background()
	hash, err := crypto.HashPassword(ctx, "123456", testParams)
	require.NoError(t, err)

	match, err := crypto.VerifyPassword(ctx, "654321", hash)
	require.NoError(t, err)
	assert.False(t, match)
}

func TestHashPassword_DifferentSalts(t *testing.T) {
	ctx := context.Background()
	hash1, err := crypto.HashPassword(ctx, "123456", testParams)
	require.NoError(t, err)

	hash2, err := crypto.HashPassword(ctx, "123456", testParams)
	require.NoError(t, err)

	assert.NotEqual(t, hash1, hash2, "two hashes of same input should differ (random salt)")
}

func TestHashPassword_ConcurrentSemaphore(t *testing.T) {
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := crypto.HashPassword(ctx, "123456", testParams)
			if err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("unexpected error: %v", err)
	}
}