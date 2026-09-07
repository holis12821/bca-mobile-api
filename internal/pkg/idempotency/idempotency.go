package idempotency

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
)

const (
	statusProcessing = "PROCESSING"
	claimTTL         = 30 * time.Second
	resultTTL        = 24 * time.Hour
)

// Store provides idempotency guard operations backed by Redis.
// Implements transaction.IdempotencyStore.
type Store struct {
	client *goredis.Client
}

func NewStore(client *goredis.Client) *Store {
	return &Store{client: client}
}

// Claim attempts to atomically claim the idempotency slot (SETNX first, never GET first).
func (s *Store) Claim(ctx context.Context, userID, idemKey string) (transaction.IdempotencyResult, error) {
	key := "idem:" + userID + ":" + idemKey

	ok, err := s.client.SetNX(ctx, key, statusProcessing, claimTTL).Result()
	if err != nil {
		return transaction.IdempotencyResult{}, err
	}

	if ok {
		return transaction.IdempotencyResult{AlreadyClaimed: false}, nil
	}

	// Slot exists — check if still processing or has a stored response.
	v, err := s.client.Get(ctx, key).Result()
	if err == goredis.Nil {
		return transaction.IdempotencyResult{AlreadyClaimed: true, StillProcessing: true}, nil
	}
	if err != nil {
		return transaction.IdempotencyResult{}, err
	}

	if v == statusProcessing {
		return transaction.IdempotencyResult{AlreadyClaimed: true, StillProcessing: true}, nil
	}

	return transaction.IdempotencyResult{AlreadyClaimed: true, StoredResponse: v}, nil
}

// Persist stores the successful response JSON with 24h TTL.
func (s *Store) Persist(ctx context.Context, userID, idemKey, responseJSON string) error {
	key := "idem:" + userID + ":" + idemKey
	return s.client.Set(ctx, key, responseJSON, resultTTL).Err()
}

// Release deletes the idempotency key on failure so the client can retry.
func (s *Store) Release(ctx context.Context, userID, idemKey string) error {
	key := "idem:" + userID + ":" + idemKey
	return s.client.Del(ctx, key).Err()
}