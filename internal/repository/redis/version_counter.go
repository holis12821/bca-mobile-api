package redis

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// VersionCounter manages cache version counters for invalidation.
// Pattern: cachever:{resource}:{user_id} — INCR to invalidate, no SCAN.
type VersionCounter struct {
	client *goredis.Client
}

func NewVersionCounter(client *goredis.Client) *VersionCounter {
	return &VersionCounter{client: client}
}

func versionKey(resource string, userID uuid.UUID) string {
	return fmt.Sprintf("cachever:%s:%s", resource, userID.String())
}

// GetVersion returns the current version for a resource.
// Returns 0 if the key doesn't exist (no versions yet).
func (vc *VersionCounter) GetVersion(ctx context.Context, resource string, userID uuid.UUID) (int64, error) {
	val, err := vc.client.Get(ctx, versionKey(resource, userID)).Int64()
	if err == goredis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get version: %w", err)
	}
	return val, nil
}

// IncrVersion increments the version counter and returns the new value.
func (vc *VersionCounter) IncrVersion(ctx context.Context, resource string, userID uuid.UUID) (int64, error) {
	val, err := vc.client.Incr(ctx, versionKey(resource, userID)).Result()
	if err != nil {
		return 0, fmt.Errorf("incr version: %w", err)
	}
	return val, nil
}