package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const nonceTTL = 120 * time.Second

// NonceStore implements crypto.NonceChecker using Redis SETNX.
// Key: pin_nonce:{nonce}, TTL 120s.
// A nonce that has been seen is rejected — this prevents replay
// of a captured pin_encrypted payload.
type NonceStore struct {
	client *goredis.Client
}

func NewNonceStore(client *goredis.Client) *NonceStore {
	return &NonceStore{client: client}
}

// CheckAndMark returns true if the nonce is fresh (not seen before).
// It atomically marks the nonce as used via SETNX.
func (ns *NonceStore) CheckAndMark(nonce string) (bool, error) {
	key := "pin_nonce:" + nonce
	ok, err := ns.client.SetNX(context.Background(), key, "1", nonceTTL).Result()
	if err != nil {
		return false, err
	}
	return ok, nil // ok=true means fresh (key did not exist)
}