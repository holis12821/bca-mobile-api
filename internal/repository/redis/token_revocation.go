package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// TokenRevocationCache tracks revoked refresh token hashes.
// Key pattern: refresh:revoked:{hash}
// TTL = remaining lifetime of the rotated-out token.
//
// Why not just delete? A deleted key is indistinguishable from an expired key.
// Without the revoked marker, reuse detection is impossible.
type TokenRevocationCache struct {
	client *goredis.Client
}

func NewTokenRevocationCache(client *goredis.Client) *TokenRevocationCache {
	return &TokenRevocationCache{client: client}
}

func revokedKey(hash string) string {
	return fmt.Sprintf("refresh:revoked:%s", hash)
}

// MarkRevoked stores the hash with the given TTL (remaining lifetime of the old token).
func (c *TokenRevocationCache) MarkRevoked(ctx context.Context, hash string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil // token already expired, no need to track
	}
	return c.client.Set(ctx, revokedKey(hash), "1", ttl).Err()
}

// IsRevoked checks whether the refresh token hash has been revoked.
func (c *TokenRevocationCache) IsRevoked(ctx context.Context, hash string) (bool, error) {
	n, err := c.client.Exists(ctx, revokedKey(hash)).Result()
	if err != nil {
		return false, fmt.Errorf("check revoked token: %w", err)
	}
	return n > 0, nil
}