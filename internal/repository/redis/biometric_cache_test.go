package redis_test

import (
	"context"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestBiometricChallengeCache_ConsumeWithGETDEL(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewBiometricChallengeCache(client)
	ctx := context.Background()

	challengeID := "test-challenge-001"
	data := &auth.ChallengeData{
		Challenge: []byte("0123456789abcdef0123456789abcdef"),
		DeviceID:  "device-001",
	}

	if err := cache.StoreChallenge(ctx, challengeID, data); err != nil {
		t.Fatalf("store challenge: %v", err)
	}

	// First consume — should return data
	got, err := cache.ConsumeChallenge(ctx, challengeID)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got == nil {
		t.Fatal("expected challenge data, got nil")
	}
	if got.DeviceID != data.DeviceID {
		t.Fatalf("device_id mismatch: %s != %s", got.DeviceID, data.DeviceID)
	}
	if string(got.Challenge) != string(data.Challenge) {
		t.Fatal("challenge bytes mismatch")
	}

	// Second consume — GETDEL already removed it, must return nil
	got2, err := cache.ConsumeChallenge(ctx, challengeID)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if got2 != nil {
		t.Fatal("expected nil on second consume (GETDEL), got data")
	}
}

func TestBiometricChallengeCache_NotFound(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewBiometricChallengeCache(client)
	ctx := context.Background()

	got, err := cache.ConsumeChallenge(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent challenge")
	}
}