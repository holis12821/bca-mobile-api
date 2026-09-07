package transaction_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pagination"
)

// --- Mocks ---

type mockMutationRepo struct {
	mutations []transaction.Mutation
}

func (r *mockMutationRepo) ListByAccountID(_ context.Context, accountID uuid.UUID, cursor *transaction.MutationCursorValues, limit int, period *transaction.DateRange) ([]transaction.Mutation, error) {
	var result []transaction.Mutation
	pastCursor := cursor == nil
	for _, m := range r.mutations {
		if m.AccountID != accountID {
			continue
		}
		if period != nil {
			if m.TransactionDate.Before(period.From) || m.TransactionDate.After(period.To) {
				continue
			}
		}
		if !pastCursor {
			// Simple cursor: skip until we find the cursor ID
			if m.ID == cursor.ID {
				pastCursor = true
			}
			continue
		}
		result = append(result, m)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

type mockTransactionRepo struct {
	transactions []transaction.Transaction
}

func (r *mockTransactionRepo) ListByUserID(_ context.Context, userID uuid.UUID, txnType *string, cursor *transaction.HistoryCursorValues, limit int) ([]transaction.Transaction, error) {
	var result []transaction.Transaction
	pastCursor := cursor == nil
	for _, t := range r.transactions {
		if t.UserID != userID {
			continue
		}
		if txnType != nil && t.Type != *txnType {
			continue
		}
		if !pastCursor {
			if t.ID == cursor.ID {
				pastCursor = true
			}
			continue
		}
		result = append(result, t)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *mockTransactionRepo) FindByID(_ context.Context, userID, txnID uuid.UUID) (*transaction.Transaction, error) {
	for _, t := range r.transactions {
		if t.ID == txnID && t.UserID == userID {
			return &t, nil
		}
	}
	return nil, nil
}

func (r *mockTransactionRepo) FindByIdempotencyKey(_ context.Context, userID uuid.UUID, key string) (*transaction.Transaction, error) {
	for _, t := range r.transactions {
		if t.UserID == userID && t.IdempotencyKey != nil && *t.IdempotencyKey == key {
			return &t, nil
		}
	}
	return nil, nil
}

type mockInquiryRepo struct {
	inquiries []transaction.Inquiry
}

func (r *mockInquiryRepo) Create(_ context.Context, inquiry *transaction.Inquiry) error {
	r.inquiries = append(r.inquiries, *inquiry)
	return nil
}

func (r *mockInquiryRepo) FindByID(_ context.Context, userID, inquiryID uuid.UUID) (*transaction.Inquiry, error) {
	for _, inq := range r.inquiries {
		if inq.ID == inquiryID && inq.UserID == userID {
			return &inq, nil
		}
	}
	return nil, nil
}

func (r *mockInquiryRepo) MarkUsed(_ context.Context, inquiryID uuid.UUID) (bool, error) {
	for i, inq := range r.inquiries {
		if inq.ID == inquiryID && inq.UsedAt == nil {
			now := time.Now()
			r.inquiries[i].UsedAt = &now
			return true, nil
		}
	}
	return false, nil
}

type mockVTokenRepo struct {
	tokens []transaction.VerificationToken
}

func (r *mockVTokenRepo) Create(_ context.Context, token *transaction.VerificationToken) error {
	r.tokens = append(r.tokens, *token)
	return nil
}

func (r *mockVTokenRepo) MarkUsed(_ context.Context, tokenHash string) error {
	return nil
}

type mockFavoriteRepo struct {
	favorites []transaction.FavoriteTransfer
}

func (r *mockFavoriteRepo) ListByUserID(_ context.Context, userID uuid.UUID, limit int) ([]transaction.FavoriteTransfer, error) {
	var result []transaction.FavoriteTransfer
	for _, f := range r.favorites {
		if f.UserID == userID {
			result = append(result, f)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (r *mockFavoriteRepo) Upsert(_ context.Context, fav *transaction.FavoriteTransfer) error {
	return nil
}

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

func (r *mockAccountRepo) FindByAccountNumber(_ context.Context, number string) (*account.Account, error) {
	for _, a := range r.accounts {
		if a.AccountNumber == number {
			return &a, nil
		}
	}
	return nil, nil
}

type mockVersionCounter struct {
	versions map[string]int64
}

func (vc *mockVersionCounter) GetVersion(_ context.Context, resource string, userID uuid.UUID) (int64, error) {
	return vc.versions[resource+":"+userID.String()], nil
}

func (vc *mockVersionCounter) IncrVersion(_ context.Context, resource string, userID uuid.UUID) (int64, error) {
	key := resource + ":" + userID.String()
	vc.versions[key]++
	return vc.versions[key], nil
}

// --- Helper ---

func newTestService(opts ...func(*transaction.ServiceConfig)) *transaction.Service {
	cfg := transaction.ServiceConfig{
		Mutations:    &mockMutationRepo{},
		Transactions: &mockTransactionRepo{},
		Inquiries:    &mockInquiryRepo{},
		VTokens:      &mockVTokenRepo{},
		Favorites:    &mockFavoriteRepo{},
		Accounts:     &mockAccountRepo{},
		Versions:     &mockVersionCounter{versions: make(map[string]int64)},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return transaction.NewService(cfg)
}

// --- Tests ---

func TestListMutations_HappyPath(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mutations := make([]transaction.Mutation, 5)
	for i := range 5 {
		mutations[i] = transaction.Mutation{
			ID:              uuid.New(),
			AccountID:       accountID,
			MutationType:    "DEBIT",
			Amount:          decimal.NewFromInt(100000),
			BalanceBefore:   decimal.NewFromInt(int64(500000 - i*100000)),
			BalanceAfter:    decimal.NewFromInt(int64(400000 - i*100000)),
			Description:     fmt.Sprintf("Transfer %d", i),
			TransactionDate: now.AddDate(0, 0, -i),
			TransactionTime: now,
			CreatedAt:       now.Add(-time.Duration(i) * time.Hour),
		}
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Mutations = &mockMutationRepo{mutations: mutations}
	})

	items, hasMore, nextCursor, err := svc.ListMutations(context.Background(), userID, accountID, "", 3, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if !hasMore {
		t.Fatal("expected has_more=true")
	}
	if nextCursor == "" {
		t.Fatal("expected non-empty cursor")
	}
}

func TestListMutations_Page2DifferentFromPage1(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mutations := make([]transaction.Mutation, 5)
	for i := range 5 {
		mutations[i] = transaction.Mutation{
			ID:              uuid.New(),
			AccountID:       accountID,
			MutationType:    "DEBIT",
			Amount:          decimal.NewFromInt(int64((i + 1) * 10000)),
			BalanceBefore:   decimal.NewFromInt(500000),
			BalanceAfter:    decimal.NewFromInt(490000),
			Description:     fmt.Sprintf("Txn %d", i),
			TransactionDate: now.AddDate(0, 0, -i),
			TransactionTime: now,
			CreatedAt:       now.Add(-time.Duration(i) * time.Minute),
		}
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Mutations = &mockMutationRepo{mutations: mutations}
	})

	// Page 1
	page1, _, cursor1, err := svc.ListMutations(context.Background(), userID, accountID, "", 2, nil)
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}

	// Page 2
	page2, _, _, err := svc.ListMutations(context.Background(), userID, accountID, cursor1, 2, nil)
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}

	// No overlap
	page1IDs := map[string]bool{}
	for _, m := range page1 {
		page1IDs[m.ID] = true
	}
	for _, m := range page2 {
		if page1IDs[m.ID] {
			t.Fatalf("page 2 contains page 1 item: %s", m.ID)
		}
	}
}

func TestListMutations_SharedDatesNotDropped(t *testing.T) {
	accountID := uuid.New()
	sameDate := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// 4 mutations on the same date — cursor must not drop any
	mutations := make([]transaction.Mutation, 4)
	for i := range 4 {
		mutations[i] = transaction.Mutation{
			ID:              uuid.New(),
			AccountID:       accountID,
			MutationType:    "CREDIT",
			Amount:          decimal.NewFromInt(50000),
			BalanceBefore:   decimal.NewFromInt(100000),
			BalanceAfter:    decimal.NewFromInt(150000),
			Description:     fmt.Sprintf("Same date %d", i),
			TransactionDate: sameDate,
			TransactionTime: sameDate.Add(time.Duration(i) * time.Hour),
			CreatedAt:       sameDate.Add(time.Duration(i) * time.Minute),
		}
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Mutations = &mockMutationRepo{mutations: mutations}
	})

	// Get all 4
	items, _, _, err := svc.ListMutations(context.Background(), uuid.New(), accountID, "", 10, nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 items on same date, got %d — cursor may have dropped rows", len(items))
	}
}

func TestListHistory_FilterByType(t *testing.T) {
	userID := uuid.New()
	txns := []transaction.Transaction{
		{ID: uuid.New(), UserID: userID, Type: "TRANSFER_INTERNAL", Status: "SUCCESS", Amount: decimal.NewFromInt(100000), TotalAmount: decimal.NewFromInt(100000), Currency: "IDR", ReferenceNumber: "REF1", CreatedAt: time.Now()},
		{ID: uuid.New(), UserID: userID, Type: "EWALLET_TOPUP", Status: "SUCCESS", Amount: decimal.NewFromInt(50000), TotalAmount: decimal.NewFromInt(50000), Currency: "IDR", ReferenceNumber: "REF2", CreatedAt: time.Now()},
		{ID: uuid.New(), UserID: userID, Type: "TRANSFER_INTERNAL", Status: "SUCCESS", Amount: decimal.NewFromInt(200000), TotalAmount: decimal.NewFromInt(200000), Currency: "IDR", ReferenceNumber: "REF3", CreatedAt: time.Now()},
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Transactions = &mockTransactionRepo{transactions: txns}
	})

	filterType := "TRANSFER_INTERNAL"
	items, _, _, err := svc.ListHistory(context.Background(), userID, &filterType, "", 20)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 TRANSFER_INTERNAL, got %d", len(items))
	}
}

func TestListRecentTransfers_HappyPath(t *testing.T) {
	userID := uuid.New()
	now := time.Now()
	favs := []transaction.FavoriteTransfer{
		{ID: uuid.New(), UserID: userID, DestinationAccount: "1234567890", DestinationName: "John", DestinationBank: "BCA", BankCode: "014", TransferType: "INTERNAL", TransferCount: 5, LastTransferAt: &now},
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Favorites = &mockFavoriteRepo{favorites: favs}
	})

	items, err := svc.ListRecentTransfers(context.Background(), userID)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1, got %d", len(items))
	}
	if items[0].TransferCount != 5 {
		t.Fatalf("expected count 5, got %d", items[0].TransferCount)
	}
}

func TestCreateInquiry_HappyPath(t *testing.T) {
	userID := uuid.New()
	destUserID := uuid.New()

	accounts := []account.Account{
		{ID: uuid.New(), UserID: destUserID, AccountNumber: "1234567890", AccountLabel: "John Doe", Status: "ACTIVE"},
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Accounts = &mockAccountRepo{accounts: accounts}
	})

	resp, err := svc.CreateInquiry(context.Background(), userID, transaction.InquiryRequest{
		DestinationAccount: "1234567890",
		TransferType:       "INTERNAL",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if resp.DestinationName != "John Doe" {
		t.Fatalf("expected John Doe, got %s", resp.DestinationName)
	}
	if resp.AdminFee != "0" {
		t.Fatalf("expected 0 admin fee for internal, got %s", resp.AdminFee)
	}
}

func TestCreateInquiry_SettlementAccountRejected(t *testing.T) {
	// Settlement accounts must not be findable — they have owner_type='INTERNAL'
	// which is filtered by FindByAccountNumber
	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Accounts = &mockAccountRepo{accounts: nil} // no CUSTOMER accounts
	})

	_, err := svc.CreateInquiry(context.Background(), uuid.New(), transaction.InquiryRequest{
		DestinationAccount: "9902000000", // settlement shard
		TransferType:       "INTERNAL",
	})
	if err == nil {
		t.Fatal("expected error for settlement account")
	}
}

func TestCreateInquiry_SelfTransfer(t *testing.T) {
	userID := uuid.New()
	accounts := []account.Account{
		{ID: uuid.New(), UserID: userID, AccountNumber: "1234567890", AccountLabel: "My Account", Status: "ACTIVE"},
	}

	svc := newTestService(func(cfg *transaction.ServiceConfig) {
		cfg.Accounts = &mockAccountRepo{accounts: accounts}
	})

	_, err := svc.CreateInquiry(context.Background(), userID, transaction.InquiryRequest{
		DestinationAccount: "1234567890",
		TransferType:       "INTERNAL",
	})
	if err == nil {
		t.Fatal("expected error for self-transfer")
	}
}

func TestCreateVerificationToken(t *testing.T) {
	svc := newTestService()

	resp, err := svc.CreateVerificationToken(context.Background(), uuid.New(), "TRANSFER")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if resp.VerificationToken == "" {
		t.Fatal("expected non-empty token")
	}
	if resp.ExpiresIn != 120 {
		t.Fatalf("expected 120s, got %d", resp.ExpiresIn)
	}
}

func TestCreateVerificationToken_InvalidPurpose(t *testing.T) {
	svc := newTestService()

	_, err := svc.CreateVerificationToken(context.Background(), uuid.New(), "INVALID")
	if err == nil {
		t.Fatal("expected error for invalid purpose")
	}
}

func TestCursorEncodeDecodeRoundtrip(t *testing.T) {
	now := time.Now().UTC()
	id := uuid.New()

	c := pagination.MutationCursor{
		Date:      now.Format("2006-01-02"),
		CreatedAt: now.Format(time.RFC3339Nano),
		ID:        id.String(),
	}
	encoded := c.Encode()
	decoded, err := pagination.DecodeMutationCursor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ID != id.String() {
		t.Fatal("id mismatch")
	}
}