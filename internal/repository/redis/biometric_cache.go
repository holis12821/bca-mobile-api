package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

const challengeTTL = 60 * time.Second

// BiometricChallengeCache stores challenges in Redis with TTL 60s.
// Key pattern: bio_challenge:{challenge_id}
type BiometricChallengeCache struct {
	client *goredis.Client
}

func NewBiometricChallengeCache(client *goredis.Client) *BiometricChallengeCache {
	return &BiometricChallengeCache{client: client}
}

func challengeKey(challengeID string) string {
	return fmt.Sprintf("bio_challenge:%s", challengeID)
}

// StoreChallenge stores challenge data with TTL 60 seconds.
func (c *BiometricChallengeCache) StoreChallenge(ctx context.Context, challengeID string, data *auth.ChallengeData) error {
	val, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal challenge: %w", err)
	}
	return c.client.Set(ctx, challengeKey(challengeID), val, challengeTTL).Err()
}

// ConsumeChallenge atomically retrieves and deletes the challenge using GETDEL.
// GETDEL is atomic — a challenge read then deleted separately can be replayed
// in the gap between the two operations.
// Returns nil if not found (expired or already consumed).
func (c *BiometricChallengeCache) ConsumeChallenge(ctx context.Context, challengeID string) (*auth.ChallengeData, error) {
	val, err := c.client.GetDel(ctx, challengeKey(challengeID)).Result()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consume challenge: %w", err)
	}

	var data auth.ChallengeData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, fmt.Errorf("unmarshal challenge: %w", err)
	}
	return &data, nil
}