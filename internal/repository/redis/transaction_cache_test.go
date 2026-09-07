package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestInquiryCache_StoreAndConsume(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewInquiryCache(client)
	ctx := context.Background()

	fee := decimal.NewFromInt(6500)
	inquiry := &transaction.Inquiry{
		ID:                 uuid.New(),
		UserID:             uuid.New(),
		InquiryType:        "TRANSFER",
		DestinationAccount: "1234567890",
		DestinationName:    strPtr("John Doe"),
		DestinationBank:    strPtr("BCA"),
		BankCode:           strPtr("014"),
		AdminFee:           &fee,
		ExpiresAt:          time.Now().Add(5 * time.Minute),
	}

	if err := cache.StoreInquiry(ctx, inquiry); err != nil {
		t.Fatalf("store: %v", err)
	}

	// First consume — should return data
	got, err := cache.ConsumeInquiry(ctx, inquiry.ID.String())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got == nil {
		t.Fatal("expected inquiry data, got nil")
	}
	if got.DestinationAccount != "1234567890" {
		t.Fatalf("expected 1234567890, got %s", got.DestinationAccount)
	}
	if got.AdminFee == nil || !got.AdminFee.Equal(decimal.NewFromInt(6500)) {
		t.Fatalf("admin fee mismatch")
	}

	// Second consume — GETDEL already removed it
	got2, err := cache.ConsumeInquiry(ctx, inquiry.ID.String())
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if got2 != nil {
		t.Fatal("expected nil on second consume (GETDEL)")
	}
}

func TestVerificationTokenCache_StoreAndConsume(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewVerificationTokenCache(client)
	ctx := context.Background()

	tokenHash := "abc123hash"
	userID := uuid.New().String()

	if err := cache.StoreToken(ctx, tokenHash, userID, "TRANSFER"); err != nil {
		t.Fatalf("store: %v", err)
	}

	// First consume
	gotUID, gotPurpose, err := cache.ConsumeToken(ctx, tokenHash)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if gotUID != userID {
		t.Fatalf("user_id mismatch: %s != %s", gotUID, userID)
	}
	if gotPurpose != "TRANSFER" {
		t.Fatalf("purpose mismatch: %s", gotPurpose)
	}

	// Second consume — gone (GETDEL)
	gotUID2, _, err := cache.ConsumeToken(ctx, tokenHash)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if gotUID2 != "" {
		t.Fatal("expected empty on second consume")
	}
}

func TestVerificationTokenCache_PurposeMismatchPrevention(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewVerificationTokenCache(client)
	ctx := context.Background()

	tokenHash := "purpose-test-hash"

	if err := cache.StoreToken(ctx, tokenHash, uuid.New().String(), "EWALLET_TOPUP"); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Consume — should return the stored purpose
	_, purpose, err := cache.ConsumeToken(ctx, tokenHash)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if purpose != "EWALLET_TOPUP" {
		t.Fatalf("expected EWALLET_TOPUP, got %s", purpose)
	}
}

func TestRecentTransferCache_SetGet(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewRecentTransferCache(client)
	ctx := context.Background()

	userID := uuid.New()
	now := time.Now()
	transfers := []transaction.FavoriteTransfer{
		{DestinationAccount: "1234567890", DestinationName: "John", DestinationBank: "BCA", BankCode: "014", TransferType: "INTERNAL", TransferCount: 3, LastTransferAt: &now},
	}

	if err := cache.SetRecent(ctx, userID, transfers); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := cache.GetRecent(ctx, userID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].TransferCount != 3 {
		t.Fatalf("expected count 3, got %d", got[0].TransferCount)
	}
}

func TestTransactionCacheInvalidator(t *testing.T) {
	client := setupTestRedis(t)
	invalidator := redisrepo.NewTransactionCacheInvalidator(client)
	ctx := context.Background()

	userID := uuid.New().String()
	accountID := uuid.New().String()

	// Set some cache keys first
	client.Set(ctx, "cache:balance:"+accountID, "cached", 0)
	client.Set(ctx, "cache:dashboard:"+userID, "cached", 0)

	// Invalidate
	err := invalidator.InvalidateTransactionCaches(ctx, []transaction.AffectedParty{
		{UserID: userID, AccountID: accountID},
	})
	if err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	// Keys should be deleted
	exists, _ := client.Exists(ctx, "cache:balance:"+accountID).Result()
	if exists != 0 {
		t.Fatal("expected balance cache to be invalidated")
	}
	exists, _ = client.Exists(ctx, "cache:dashboard:"+userID).Result()
	if exists != 0 {
		t.Fatal("expected dashboard cache to be invalidated")
	}

	// Version counter should be bumped
	ver, _ := client.Get(ctx, "cachever:mutations:"+accountID).Int64()
	if ver != 1 {
		t.Fatalf("expected mutation version 1, got %d", ver)
	}
}

func strPtr(s string) *string {
	return &s
}