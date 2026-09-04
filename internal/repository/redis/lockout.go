package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

const (
	LockoutDuration      = 30 * time.Minute
	LockoutEscalated     = 24 * time.Hour
	LockoutEscalateAfter = 3 // 3 lockouts within 24h → escalate to 24h
)

func lockAccountKey(userID string) string  { return "lock:account:" + userID }
func lockoutCountKey(userID string) string { return "lock:count:" + userID }

// LockoutManager handles account lockout state in Redis.
// Implements auth.LockoutChecker.
type LockoutManager struct {
	client *goredis.Client
}

func NewLockoutManager(client *goredis.Client) *LockoutManager {
	return &LockoutManager{client: client}
}

// IsLocked checks if a user account is currently locked.
func (lm *LockoutManager) IsLocked(ctx context.Context, userID string) (*auth.LockoutStatus, error) {
	ttl, err := lm.client.TTL(ctx, lockAccountKey(userID)).Result()
	if err != nil {
		return nil, fmt.Errorf("check lockout: %w", err)
	}

	if ttl <= 0 {
		return &auth.LockoutStatus{Locked: false}, nil
	}

	return &auth.LockoutStatus{
		Locked:      true,
		LockedUntil: time.Now().Add(ttl),
	}, nil
}

// LockAccount locks a user account. Tracks lockout count within 24h;
// three lockouts → escalate to 24h lock + CS notification needed.
func (lm *LockoutManager) LockAccount(ctx context.Context, userID string) (*auth.LockoutStatus, error) {
	pipe := lm.client.Pipeline()

	// Increment the 24h lockout counter
	incrCmd := pipe.Incr(ctx, lockoutCountKey(userID))
	pipe.Expire(ctx, lockoutCountKey(userID), 24*time.Hour)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("increment lockout count: %w", err)
	}

	count := incrCmd.Val()

	// Three lockouts within 24h → escalate to 24h lock
	duration := LockoutDuration
	if count >= int64(LockoutEscalateAfter) {
		duration = LockoutEscalated
	}

	lockedUntil := time.Now().Add(duration)

	err = lm.client.Set(ctx, lockAccountKey(userID), lockedUntil.Unix(), duration).Err()
	if err != nil {
		return nil, fmt.Errorf("set lock: %w", err)
	}

	return &auth.LockoutStatus{
		Locked:      true,
		LockedUntil: lockedUntil,
	}, nil
}

// Unlock removes the account lock (e.g., admin override or after PIN reset).
func (lm *LockoutManager) Unlock(ctx context.Context, userID string) error {
	return lm.client.Del(ctx, lockAccountKey(userID)).Err()
}

// GetLockoutCount returns how many times the account was locked in the last 24h.
func (lm *LockoutManager) GetLockoutCount(ctx context.Context, userID string) (int64, error) {
	val, err := lm.client.Get(ctx, lockoutCountKey(userID)).Result()
	if err == goredis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get lockout count: %w", err)
	}
	count, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse lockout count: %w", err)
	}
	return count, nil
}