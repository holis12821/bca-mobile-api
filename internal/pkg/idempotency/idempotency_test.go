package idempotency_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	redistc "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/holis12821/bca-mobile-api/internal/pkg/idempotency"
)

func setupTestRedis(t *testing.T) *goredis.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := redistc.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { container.Terminate(context.Background()) })

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "6379")
	client := goredis.NewClient(&goredis.Options{
		Addr: fmt.Sprintf("%s:%s", host, port.Port()),
	})
	t.Cleanup(func() { client.Close() })
	return client
}

func TestIdempotencyStore_ClaimAndPersist(t *testing.T) {
	client := setupTestRedis(t)
	store := idempotency.NewStore(client)
	ctx := context.Background()

	// First claim — should succeed
	result, err := store.Claim(ctx, "user1", "key1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if result.AlreadyClaimed {
		t.Fatal("first claim should not be already claimed")
	}

	// Second claim — should report already claimed + processing
	result2, err := store.Claim(ctx, "user1", "key1")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if !result2.AlreadyClaimed {
		t.Fatal("second claim should be already claimed")
	}
	if !result2.StillProcessing {
		t.Fatal("should still be processing")
	}

	// Persist response
	if err := store.Persist(ctx, "user1", "key1", `{"txn_id":"abc"}`); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// Third claim — should return stored response
	result3, err := store.Claim(ctx, "user1", "key1")
	if err != nil {
		t.Fatalf("third claim: %v", err)
	}
	if !result3.AlreadyClaimed {
		t.Fatal("third claim should be already claimed")
	}
	if result3.StillProcessing {
		t.Fatal("should not be processing after persist")
	}
	if result3.StoredResponse != `{"txn_id":"abc"}` {
		t.Fatalf("expected stored response, got %s", result3.StoredResponse)
	}
}

func TestIdempotencyStore_Release(t *testing.T) {
	client := setupTestRedis(t)
	store := idempotency.NewStore(client)
	ctx := context.Background()

	// Claim
	_, err := store.Claim(ctx, "user2", "key2")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Release
	if err := store.Release(ctx, "user2", "key2"); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Should be claimable again
	result, err := store.Claim(ctx, "user2", "key2")
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if result.AlreadyClaimed {
		t.Fatal("should be claimable after release")
	}
}