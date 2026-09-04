package account_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
)

// --- Mock implementations ---

type mockAccountRepo struct {
	accounts []account.Account
}

func (r *mockAccountRepo) FindActiveByUserID(_ context.Context, userID uuid.UUID) ([]account.Account, error) {
	var result []account.Account
	for _, a := range r.accounts {
		if a.UserID == userID {
			result = append(result, a)
		}
	}
	return result, nil
}

func (r *mockAccountRepo) FindByAccountNumber(_ context.Context, accountNumber string) (*account.Account, error) {
	for _, a := range r.accounts {
		if a.AccountNumber == accountNumber {
			return &a, nil
		}
	}
	return nil, nil
}

type mockProfileRepo struct {
	profiles map[uuid.UUID]*account.UserProfile
}

func (r *mockProfileRepo) FindProfile(_ context.Context, userID uuid.UUID) (*account.UserProfile, error) {
	p, ok := r.profiles[userID]
	if !ok {
		return nil, nil
	}
	return p, nil
}

type mockLimitRepo struct {
	mu     sync.Mutex
	limits map[uuid.UUID][]account.TransactionLimit
}

func (r *mockLimitRepo) FindByUserID(_ context.Context, userID uuid.UUID) ([]account.TransactionLimit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limits[userID], nil
}

func (r *mockLimitRepo) UpdateLimit(_ context.Context, userID uuid.UUID, limitType string, update account.LimitUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	limits := r.limits[userID]
	for i, l := range limits {
		if l.LimitType == limitType {
			if update.DailyLimit != nil {
				limits[i].DailyLimit = *update.DailyLimit
			}
			if update.MonthlyLimit != nil {
				limits[i].MonthlyLimit = update.MonthlyLimit
			}
			if update.PerTransactionLimit != nil {
				limits[i].PerTransactionLimit = update.PerTransactionLimit
			}
			r.limits[userID] = limits
			return nil
		}
	}
	return fmt.Errorf("not found")
}

type mockNotifRepo struct {
	mu            sync.Mutex
	notifications []account.Notification
}

func (r *mockNotifRepo) ListByUserID(_ context.Context, userID uuid.UUID, cursor *uuid.UUID, limit int) ([]account.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []account.Notification
	pastCursor := cursor == nil
	for _, n := range r.notifications {
		if n.UserID != userID {
			continue
		}
		if !pastCursor {
			if n.ID == *cursor {
				pastCursor = true
			}
			continue
		}
		result = append(result, n)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *mockNotifRepo) CountUnread(_ context.Context, userID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, n := range r.notifications {
		if n.UserID == userID && !n.IsRead {
			count++
		}
	}
	return count, nil
}

func (r *mockNotifRepo) MarkRead(_ context.Context, userID, notifID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, n := range r.notifications {
		if n.ID == notifID && n.UserID == userID {
			r.notifications[i].IsRead = true
			now := time.Now()
			r.notifications[i].ReadAt = &now
			return nil
		}
	}
	return nil
}

func (r *mockNotifRepo) MarkAllRead(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for i, n := range r.notifications {
		if n.UserID == userID && !n.IsRead {
			r.notifications[i].IsRead = true
			r.notifications[i].ReadAt = &now
		}
	}
	return nil
}

type mockVersionCounter struct {
	mu       sync.Mutex
	versions map[string]int64
}

func newMockVersionCounter() *mockVersionCounter {
	return &mockVersionCounter{versions: make(map[string]int64)}
}

func (vc *mockVersionCounter) GetVersion(_ context.Context, resource string, userID uuid.UUID) (int64, error) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.versions[resource+":"+userID.String()], nil
}

func (vc *mockVersionCounter) IncrVersion(_ context.Context, resource string, userID uuid.UUID) (int64, error) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	key := resource + ":" + userID.String()
	vc.versions[key]++
	return vc.versions[key], nil
}

// --- Helper ---

func newTestService(accounts []account.Account, profile *account.UserProfile, limits []account.TransactionLimit, notifs []account.Notification) (*account.Service, *mockVersionCounter) {
	vc := newMockVersionCounter()

	profileMap := make(map[uuid.UUID]*account.UserProfile)
	if profile != nil {
		profileMap[profile.ID] = profile
	}

	limitMap := make(map[uuid.UUID][]account.TransactionLimit)
	if len(limits) > 0 {
		// Group limits by their UserID
		limitMap[limits[0].UserID] = limits
	}

	svc := account.NewService(account.ServiceConfig{
		Accounts:      &mockAccountRepo{accounts: accounts},
		Profiles:      &mockProfileRepo{profiles: profileMap},
		Limits:        &mockLimitRepo{limits: limitMap},
		Notifications: &mockNotifRepo{notifications: notifs},
		Versions:      vc,
	})

	return svc, vc
}

// --- Tests ---

func TestGetProfile_HappyPath(t *testing.T) {
	userID := uuid.New()
	accts := []account.Account{
		{ID: uuid.New(), UserID: userID, AccountNumber: "1234567890", AccountType: "TAHAPAN", AccountLabel: "Tabungan", Currency: "IDR", Balance: decimal.NewFromInt(1000000), IsPrimary: true, Status: "ACTIVE"},
	}
	profile := &account.UserProfile{
		ID:          userID,
		FullName:    "John Doe",
		DisplayName: "John",
		Phone:       "081234567890",
		Email:       "john@example.com",
	}

	svc, _ := newTestService(accts, profile, nil, nil)

	got, err := svc.GetProfile(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.FullName != "John Doe" {
		t.Fatalf("expected John Doe, got %s", got.FullName)
	}
	if len(got.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(got.Accounts))
	}
}

func TestGetProfile_NotFound(t *testing.T) {
	svc, _ := newTestService(nil, nil, nil, nil)

	_, err := svc.GetProfile(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for non-existent user")
	}
}

func TestGetBalances_HappyPath(t *testing.T) {
	userID := uuid.New()
	accts := []account.Account{
		{ID: uuid.New(), UserID: userID, AccountNumber: "1234567890", Balance: decimal.NewFromInt(5000000), AvailableBalance: decimal.NewFromInt(5000000), Currency: "IDR", IsPrimary: true, Status: "ACTIVE"},
		{ID: uuid.New(), UserID: userID, AccountNumber: "0987654321", Balance: decimal.NewFromInt(2000000), AvailableBalance: decimal.NewFromInt(2000000), Currency: "IDR", Status: "ACTIVE"},
	}

	svc, _ := newTestService(accts, nil, nil, nil)

	got, err := svc.GetBalances(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(got))
	}
}

func TestGetDashboard_HappyPath(t *testing.T) {
	userID := uuid.New()
	now := time.Now()
	accts := []account.Account{
		{ID: uuid.New(), UserID: userID, AccountNumber: "1234567890", Balance: decimal.NewFromInt(5000000), AvailableBalance: decimal.NewFromInt(5000000), Currency: "IDR", IsPrimary: true, Status: "ACTIVE"},
	}
	profile := &account.UserProfile{
		ID:          userID,
		FullName:    "John Doe",
		DisplayName: "John",
		LastLoginAt: &now,
	}
	notifs := []account.Notification{
		{ID: uuid.New(), UserID: userID, Type: "TRANSACTION", Title: "Transfer", Body: "Berhasil", IsRead: false, CreatedAt: now},
		{ID: uuid.New(), UserID: userID, Type: "PROMO", Title: "Promo", Body: "Diskon", IsRead: false, CreatedAt: now},
	}

	svc, _ := newTestService(accts, profile, nil, notifs)

	dash, err := svc.GetDashboard(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dash.User.DisplayName != "John" {
		t.Fatalf("expected John, got %s", dash.User.DisplayName)
	}
	if dash.PrimaryAccount == nil {
		t.Fatal("expected primary account, got nil")
	}
	if dash.UnreadCount != 2 {
		t.Fatalf("expected 2 unread, got %d", dash.UnreadCount)
	}
}

func TestUpdateTransactionLimit_HappyPath(t *testing.T) {
	userID := uuid.New()
	limits := []account.TransactionLimit{
		{ID: uuid.New(), UserID: userID, LimitType: "TRANSFER_INTERNAL", DailyLimit: decimal.NewFromInt(50000000)},
		{ID: uuid.New(), UserID: userID, LimitType: "EWALLET", DailyLimit: decimal.NewFromInt(10000000)},
	}

	svc, _ := newTestService(nil, nil, limits, nil)

	newLimit := decimal.NewFromInt(75000000)
	err := svc.UpdateTransactionLimit(context.Background(), userID, account.UpdateLimitRequest{
		Limits: map[string]account.LimitUpdate{
			"TRANSFER_INTERNAL": {DailyLimit: &newLimit},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateTransactionLimit_ExceedsCeiling(t *testing.T) {
	userID := uuid.New()
	limits := []account.TransactionLimit{
		{ID: uuid.New(), UserID: userID, LimitType: "EWALLET", DailyLimit: decimal.NewFromInt(10000000)},
	}

	svc, _ := newTestService(nil, nil, limits, nil)

	overCeiling := decimal.NewFromInt(999000000) // exceeds 20M ceiling
	err := svc.UpdateTransactionLimit(context.Background(), userID, account.UpdateLimitRequest{
		Limits: map[string]account.LimitUpdate{
			"EWALLET": {DailyLimit: &overCeiling},
		},
	})
	if err == nil {
		t.Fatal("expected error for exceeding ceiling")
	}
}

func TestUpdateTransactionLimit_InvalidType(t *testing.T) {
	svc, _ := newTestService(nil, nil, nil, nil)

	newLimit := decimal.NewFromInt(1000)
	err := svc.UpdateTransactionLimit(context.Background(), uuid.New(), account.UpdateLimitRequest{
		Limits: map[string]account.LimitUpdate{
			"INVALID_TYPE": {DailyLimit: &newLimit},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid limit type")
	}
}

func TestListNotifications_Pagination(t *testing.T) {
	userID := uuid.New()
	var notifs []account.Notification
	for i := 0; i < 5; i++ {
		notifs = append(notifs, account.Notification{
			ID:        uuid.New(),
			UserID:    userID,
			Type:      "TRANSACTION",
			Title:     fmt.Sprintf("Notif %d", i),
			Body:      "Body",
			IsRead:    false,
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}

	svc, _ := newTestService(nil, nil, nil, notifs)

	// Page 1: limit 2
	resp, hasMore, nextCursor, err := svc.ListNotifications(context.Background(), userID, nil, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(resp.Notifications))
	}
	if !hasMore {
		t.Fatal("expected has_more=true")
	}
	if nextCursor == "" {
		t.Fatal("expected non-empty cursor")
	}

	// Page 2: use cursor from page 1
	cursorID, _ := uuid.Parse(nextCursor)
	resp2, _, _, err := svc.ListNotifications(context.Background(), userID, &cursorID, 2)
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}

	// Page 2 must not contain any items from page 1
	page1IDs := map[string]bool{}
	for _, n := range resp.Notifications {
		page1IDs[n.ID] = true
	}
	for _, n := range resp2.Notifications {
		if page1IDs[n.ID] {
			t.Fatalf("page 2 contains page 1 item: %s", n.ID)
		}
	}
}

func TestMarkNotificationRead(t *testing.T) {
	userID := uuid.New()
	notifID := uuid.New()
	notifs := []account.Notification{
		{ID: notifID, UserID: userID, Type: "SYSTEM", Title: "Test", Body: "Body", IsRead: false, CreatedAt: time.Now()},
	}

	svc, vc := newTestService(nil, nil, nil, notifs)

	err := svc.MarkNotificationRead(context.Background(), userID, notifID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Version counter should have been incremented for notif + dashboard
	notifVer, _ := vc.GetVersion(context.Background(), "notif", userID)
	dashVer, _ := vc.GetVersion(context.Background(), "dashboard", userID)
	if notifVer != 1 {
		t.Fatalf("expected notif version 1, got %d", notifVer)
	}
	if dashVer != 1 {
		t.Fatalf("expected dashboard version 1, got %d", dashVer)
	}
}

func TestMarkAllNotificationsRead(t *testing.T) {
	userID := uuid.New()
	notifs := []account.Notification{
		{ID: uuid.New(), UserID: userID, Type: "SYSTEM", Title: "Test 1", Body: "Body", IsRead: false, CreatedAt: time.Now()},
		{ID: uuid.New(), UserID: userID, Type: "PROMO", Title: "Test 2", Body: "Body", IsRead: false, CreatedAt: time.Now()},
	}

	svc, vc := newTestService(nil, nil, nil, notifs)

	err := svc.MarkAllNotificationsRead(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both version counters incremented
	notifVer, _ := vc.GetVersion(context.Background(), "notif", userID)
	dashVer, _ := vc.GetVersion(context.Background(), "dashboard", userID)
	if notifVer != 1 {
		t.Fatalf("expected notif version 1, got %d", notifVer)
	}
	if dashVer != 1 {
		t.Fatalf("expected dashboard version 1, got %d", dashVer)
	}
}

func TestVersionCounter_Invalidation(t *testing.T) {
	userID := uuid.New()
	limits := []account.TransactionLimit{
		{ID: uuid.New(), UserID: userID, LimitType: "TRANSFER_INTERNAL", DailyLimit: decimal.NewFromInt(50000000)},
	}

	svc, vc := newTestService(nil, nil, limits, nil)

	newLimit := decimal.NewFromInt(60000000)
	err := svc.UpdateTransactionLimit(context.Background(), userID, account.UpdateLimitRequest{
		Limits: map[string]account.LimitUpdate{
			"TRANSFER_INTERNAL": {DailyLimit: &newLimit},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dashboard version should be incremented
	dashVer, _ := vc.GetVersion(context.Background(), "dashboard", userID)
	if dashVer != 1 {
		t.Fatalf("expected dashboard version 1 after limit update, got %d", dashVer)
	}
}