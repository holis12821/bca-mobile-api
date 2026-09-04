package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestSessionCache_DeleteSession(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewSessionCache(client)
	ctx := context.Background()

	userID := uuid.New()
	deviceID := uuid.New()

	session := &auth.Session{
		ID:       uuid.New(),
		UserID:   userID,
		DeviceID: deviceID,
		CreatedAt: time.Now(),
	}

	if err := cache.StoreSession(ctx, session, "Test User", "PIN"); err != nil {
		t.Fatalf("store session: %v", err)
	}

	// Verify it exists
	key := "session:" + userID.String() + ":" + deviceID.String()
	exists, _ := client.Exists(ctx, key).Result()
	if exists != 1 {
		t.Fatal("session should exist before delete")
	}

	if err := cache.DeleteSession(ctx, userID, deviceID.String()); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	exists, _ = client.Exists(ctx, key).Result()
	if exists != 0 {
		t.Fatal("session should not exist after delete")
	}
}

func TestSessionCache_InvalidateAllUserSessions(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewSessionCache(client)
	ctx := context.Background()

	userID := uuid.New()
	devices := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	// Create 3 sessions
	for _, did := range devices {
		session := &auth.Session{
			ID:        uuid.New(),
			UserID:    userID,
			DeviceID:  did,
			CreatedAt: time.Now(),
		}
		if err := cache.StoreSession(ctx, session, "Test", "PIN"); err != nil {
			t.Fatalf("store session: %v", err)
		}
		if err := cache.AddToUserSessions(ctx, userID, did.String()); err != nil {
			t.Fatalf("add to user sessions: %v", err)
		}
	}

	// Verify set has 3 members
	count, _ := client.SCard(ctx, "sessions:user:"+userID.String()).Result()
	if count != 3 {
		t.Fatalf("expected 3 session members, got %d", count)
	}

	// Invalidate all
	if err := cache.InvalidateAllUserSessions(ctx, userID); err != nil {
		t.Fatalf("invalidate all: %v", err)
	}

	// All session keys and the set should be gone
	for _, did := range devices {
		key := "session:" + userID.String() + ":" + did.String()
		exists, _ := client.Exists(ctx, key).Result()
		if exists != 0 {
			t.Errorf("session %s should not exist", key)
		}
	}

	setExists, _ := client.Exists(ctx, "sessions:user:"+userID.String()).Result()
	if setExists != 0 {
		t.Error("user sessions set should not exist")
	}
}