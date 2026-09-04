package redis_test

import (
	"context"
	"testing"
	"time"

	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestTokenRevocationCache(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewTokenRevocationCache(client)
	ctx := context.Background()

	t.Run("fresh hash is not revoked", func(t *testing.T) {
		revoked, err := cache.IsRevoked(ctx, "nonexistent_hash")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if revoked {
			t.Fatal("expected false, got true")
		}
	})

	t.Run("marked hash is revoked", func(t *testing.T) {
		hash := "test_revoked_hash_001"
		if err := cache.MarkRevoked(ctx, hash, 10*time.Second); err != nil {
			t.Fatalf("mark revoked: %v", err)
		}
		revoked, err := cache.IsRevoked(ctx, hash)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !revoked {
			t.Fatal("expected true, got false")
		}
	})

	t.Run("zero TTL is no-op", func(t *testing.T) {
		hash := "test_zero_ttl_hash"
		if err := cache.MarkRevoked(ctx, hash, 0); err != nil {
			t.Fatalf("mark revoked: %v", err)
		}
		revoked, err := cache.IsRevoked(ctx, hash)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if revoked {
			t.Fatal("expected false for zero TTL, got true")
		}
	})
}