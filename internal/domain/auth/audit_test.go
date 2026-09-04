package auth_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

func TestAuditService_HappyPath(t *testing.T) {
	repo := &mockAuditRepo{}
	svc := auth.NewAuditService(repo)
	defer svc.Close()

	userID := uuid.New()
	svc.Log(&auth.AuditEntry{
		UserID:       &userID,
		Action:       auth.AuditLoginSuccess,
		ResourceType: "auth",
		IPAddress:    "10.0.0.1",
		Metadata:     map[string]any{"device_id": "dev-001"},
	})

	// Wait for worker to process
	time.Sleep(50 * time.Millisecond)

	entries := repo.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Action != auth.AuditLoginSuccess {
		t.Fatalf("expected %s, got %s", auth.AuditLoginSuccess, entries[0].Action)
	}
	if entries[0].IPAddress != "10.0.0.1" {
		t.Fatalf("expected IP 10.0.0.1, got %s", entries[0].IPAddress)
	}
}

func TestAuditService_DBFailure_FallbackToSlog(t *testing.T) {
	// Audit insert failure must NOT propagate — just log via slog.
	failingRepo := &failingAuditRepo{}
	svc := auth.NewAuditService(failingRepo)
	defer svc.Close()

	userID := uuid.New()
	svc.Log(&auth.AuditEntry{
		UserID:       &userID,
		Action:       auth.AuditLoginFailed,
		ResourceType: "auth",
	})

	// Should not panic or block — worker handles error gracefully.
	time.Sleep(50 * time.Millisecond)
	// If we reach here without panic/deadlock, the test passes.
}

func TestAuditService_BufferFull_FallbackToSlog(t *testing.T) {
	// Use a blocking repo so the buffer fills up.
	blockingRepo := &blockingAuditRepo{block: make(chan struct{})}
	svc := auth.NewAuditService(blockingRepo)
	defer func() {
		close(blockingRepo.block) // unblock workers so Close() can drain
		svc.Close()
	}()

	userID := uuid.New()

	// Fill the buffer (256) + workers (4) = 260 should saturate.
	// Then one more should trigger the slog fallback.
	for i := 0; i < 300; i++ {
		svc.Log(&auth.AuditEntry{
			UserID:       &userID,
			Action:       "FILL_BUFFER",
			ResourceType: "test",
		})
	}

	// If we reach here without blocking the caller, the non-blocking
	// fallback to slog is working correctly.
}

func TestAuditService_Close_DrainsBuffer(t *testing.T) {
	repo := &mockAuditRepo{}
	svc := auth.NewAuditService(repo)

	userID := uuid.New()
	for i := 0; i < 10; i++ {
		svc.Log(&auth.AuditEntry{
			UserID:       &userID,
			Action:       fmt.Sprintf("ACTION_%d", i),
			ResourceType: "test",
		})
	}

	svc.Close() // should drain all 10 entries

	entries := repo.getEntries()
	if len(entries) != 10 {
		t.Fatalf("expected 10 entries after Close(), got %d", len(entries))
	}
}

func TestAuditService_ConcurrentWrites(t *testing.T) {
	repo := &mockAuditRepo{}
	svc := auth.NewAuditService(repo)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			userID := uuid.New()
			svc.Log(&auth.AuditEntry{
				UserID:       &userID,
				Action:       fmt.Sprintf("CONCURRENT_%d", n),
				ResourceType: "test",
			})
		}(i)
	}

	wg.Wait()
	svc.Close()

	entries := repo.getEntries()
	if len(entries) != 50 {
		t.Fatalf("expected 50 entries, got %d", len(entries))
	}
}

// --- Additional mocks ---

type failingAuditRepo struct{}

func (r *failingAuditRepo) Insert(_ context.Context, _ *auth.AuditEntry) error {
	return fmt.Errorf("simulated DB failure")
}

type blockingAuditRepo struct {
	block chan struct{}
}

func (r *blockingAuditRepo) Insert(_ context.Context, _ *auth.AuditEntry) error {
	<-r.block // blocks until channel is closed
	return nil
}