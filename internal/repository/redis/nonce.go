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

// SignalingTokenStore makes a signaling token single-use.
//
// The token is carried in the WebSocket URL's query string, where it ends up in
// proxy logs, crash reports, and anything that records a URL. A five-minute
// window was still five minutes in which a copied URL joined someone else's
// e-KYC call; marking the jti spent on first connect closes it. A reconnect asks
// POST /video-call/queue again, which mints a fresh token for the same queue.
type SignalingTokenStore struct {
	client *goredis.Client
}

func NewSignalingTokenStore(client *goredis.Client) *SignalingTokenStore {
	return &SignalingTokenStore{client: client}
}

// ConsumeSignalingToken reports whether this jti is being used for the first
// time, marking it spent for ttl (the token's own remaining lifetime — there is
// no point remembering a jti that can no longer verify).
//
// A Redis failure returns the error; the caller decides. Signaling is the one
// place where failing open would let a replayed URL into a live video call, so
// the handler refuses the connection instead.
func (s *SignalingTokenStore) ConsumeSignalingToken(ctx context.Context, jti string, ttl time.Duration) (bool, error) {
	if jti == "" {
		// A token without a jti cannot be tracked, so it cannot be trusted.
		return false, nil
	}
	if ttl <= 0 {
		return false, nil
	}
	return s.client.SetNX(ctx, "signal:jti:"+jti, "1", ttl).Result()
}
