package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestProfileCache_SetGet(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewProfileCache(client)
	ctx := context.Background()

	userID := uuid.New()
	profile := &account.UserProfile{
		ID:          userID,
		FullName:    "John Doe",
		DisplayName: "John",
		Phone:       "081234567890",
		Email:       "john@example.com",
		Accounts: []account.Account{
			{
				ID:               uuid.New(),
				UserID:           userID,
				AccountNumber:    "1234567890",
				AccountType:      "TAHAPAN",
				AccountLabel:     "Tabungan",
				Currency:         "IDR",
				Balance:          decimal.NewFromInt(1000000),
				HoldAmount:       decimal.NewFromInt(0),
				AvailableBalance: decimal.NewFromInt(1000000),
				IsPrimary:        true,
				Status:           "ACTIVE",
				OpenedAt:         time.Now().UTC().Truncate(time.Second),
			},
		},
	}

	if err := cache.SetProfile(ctx, userID, 1, profile); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	got, err := cache.GetProfile(ctx, userID, 1)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if got == nil {
		t.Fatal("expected profile, got nil")
	}
	if got.FullName != "John Doe" {
		t.Fatalf("expected John Doe, got %s", got.FullName)
	}
	if len(got.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(got.Accounts))
	}
	if !got.Accounts[0].Balance.Equal(decimal.NewFromInt(1000000)) {
		t.Fatalf("expected balance 1000000, got %s", got.Accounts[0].Balance)
	}
}

func TestProfileCache_VersionMismatch(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewProfileCache(client)
	ctx := context.Background()

	userID := uuid.New()
	profile := &account.UserProfile{
		ID:          userID,
		FullName:    "John",
		DisplayName: "J",
	}

	if err := cache.SetProfile(ctx, userID, 1, profile); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Different version should miss
	got, err := cache.GetProfile(ctx, userID, 2)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for different version, got data")
	}
}

func TestBalanceCache_SetGet(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewBalanceCache(client)
	ctx := context.Background()

	userID := uuid.New()
	accounts := []account.Account{
		{
			ID:               uuid.New(),
			UserID:           userID,
			AccountNumber:    "1234567890",
			Balance:          decimal.NewFromInt(5000000),
			AvailableBalance: decimal.NewFromInt(4500000),
			HoldAmount:       decimal.NewFromInt(500000),
			Currency:         "IDR",
			IsPrimary:        true,
			Status:           "ACTIVE",
			OpenedAt:         time.Now().UTC().Truncate(time.Second),
		},
	}

	if err := cache.SetBalances(ctx, userID, 0, accounts); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := cache.GetBalances(ctx, userID, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if !got[0].Balance.Equal(decimal.NewFromInt(5000000)) {
		t.Fatalf("expected 5000000, got %s", got[0].Balance)
	}
}

func TestDashboardCache_SetGet(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewDashboardCache(client)
	ctx := context.Background()

	userID := uuid.New()
	resp := &account.DashboardResponse{
		User:        account.DashboardUser{DisplayName: "Test"},
		Accounts:    []account.AccountBalance{{AccountID: uuid.New().String(), Balance: "100"}},
		UnreadCount: 3,
	}

	if err := cache.SetDashboard(ctx, userID, 0, resp); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := cache.GetDashboard(ctx, userID, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected data, got nil")
	}
	if got.UnreadCount != 3 {
		t.Fatalf("expected 3 unread, got %d", got.UnreadCount)
	}
}

func TestNotificationCache_CursorInKey(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewNotificationCache(client)
	ctx := context.Background()

	userID := uuid.New()
	page1 := &account.NotificationListResponse{
		Notifications: []account.NotificationItem{
			{ID: uuid.New().String(), Title: "Page 1"},
		},
	}
	page2 := &account.NotificationListResponse{
		Notifications: []account.NotificationItem{
			{ID: uuid.New().String(), Title: "Page 2"},
		},
	}

	// Store page 1 with "first" cursor hash
	if err := cache.SetNotifications(ctx, userID, 0, "first", page1); err != nil {
		t.Fatalf("set page1: %v", err)
	}

	// Store page 2 with different cursor hash
	if err := cache.SetNotifications(ctx, userID, 0, "abc123", page2); err != nil {
		t.Fatalf("set page2: %v", err)
	}

	// Get page 1 — must NOT return page 2's data
	got1, err := cache.GetNotifications(ctx, userID, 0, "first")
	if err != nil {
		t.Fatalf("get page1: %v", err)
	}
	if got1.Notifications[0].Title != "Page 1" {
		t.Fatalf("expected 'Page 1', got '%s'", got1.Notifications[0].Title)
	}

	// Get page 2 — must return page 2's data
	got2, err := cache.GetNotifications(ctx, userID, 0, "abc123")
	if err != nil {
		t.Fatalf("get page2: %v", err)
	}
	if got2.Notifications[0].Title != "Page 2" {
		t.Fatalf("expected 'Page 2', got '%s'", got2.Notifications[0].Title)
	}
}

func TestVersionCounter_IncrAndGet(t *testing.T) {
	client := setupTestRedis(t)
	vc := redisrepo.NewVersionCounter(client)
	ctx := context.Background()

	userID := uuid.New()

	// Initial version is 0
	v, err := vc.GetVersion(ctx, "profile", userID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != 0 {
		t.Fatalf("expected 0, got %d", v)
	}

	// Increment
	v, err = vc.IncrVersion(ctx, "profile", userID)
	if err != nil {
		t.Fatalf("incr: %v", err)
	}
	if v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}

	// Get again
	v, err = vc.GetVersion(ctx, "profile", userID)
	if err != nil {
		t.Fatalf("get after incr: %v", err)
	}
	if v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}
}