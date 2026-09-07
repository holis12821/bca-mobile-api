package transaction_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// sha256Hex mirrors hashVToken in service_execute.go
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// --- Execute-specific mocks ---

type mockIdempotencyStore struct {
	claimed  map[string]string // key → stored response ("" means processing)
	released map[string]bool
}

func newMockIdemStore() *mockIdempotencyStore {
	return &mockIdempotencyStore{
		claimed:  make(map[string]string),
		released: make(map[string]bool),
	}
}

func (m *mockIdempotencyStore) Claim(_ context.Context, userID, idemKey string) (transaction.IdempotencyResult, error) {
	key := userID + ":" + idemKey
	if resp, ok := m.claimed[key]; ok {
		if resp == "" {
			return transaction.IdempotencyResult{AlreadyClaimed: true, StillProcessing: true}, nil
		}
		return transaction.IdempotencyResult{AlreadyClaimed: true, StoredResponse: resp}, nil
	}
	m.claimed[key] = "" // mark as processing
	return transaction.IdempotencyResult{AlreadyClaimed: false}, nil
}

func (m *mockIdempotencyStore) Persist(_ context.Context, userID, idemKey, responseJSON string) error {
	key := userID + ":" + idemKey
	m.claimed[key] = responseJSON
	return nil
}

func (m *mockIdempotencyStore) Release(_ context.Context, userID, idemKey string) error {
	key := userID + ":" + idemKey
	delete(m.claimed, key)
	m.released[key] = true
	return nil
}

type mockVTokenCacheExec struct {
	tokens map[string]vtEntry
}

type vtEntry struct {
	userID  string
	purpose string
}

func newMockVTCache() *mockVTokenCacheExec {
	return &mockVTokenCacheExec{tokens: make(map[string]vtEntry)}
}

func (m *mockVTokenCacheExec) StoreToken(_ context.Context, tokenHash, userID, purpose string) error {
	m.tokens[tokenHash] = vtEntry{userID: userID, purpose: purpose}
	return nil
}

func (m *mockVTokenCacheExec) ConsumeToken(_ context.Context, tokenHash string) (string, string, error) {
	e, ok := m.tokens[tokenHash]
	if !ok {
		return "", "", nil
	}
	delete(m.tokens, tokenHash)
	return e.userID, e.purpose, nil
}

type mockInqCacheExec struct {
	inquiries map[string]*transaction.Inquiry
}

func newMockInqCache() *mockInqCacheExec {
	return &mockInqCacheExec{inquiries: make(map[string]*transaction.Inquiry)}
}

func (m *mockInqCacheExec) StoreInquiry(_ context.Context, inq *transaction.Inquiry) error {
	m.inquiries[inq.ID.String()] = inq
	return nil
}

func (m *mockInqCacheExec) ConsumeInquiry(_ context.Context, inquiryID string) (*transaction.Inquiry, error) {
	inq, ok := m.inquiries[inquiryID]
	if !ok {
		return nil, nil
	}
	delete(m.inquiries, inquiryID)
	return inq, nil
}

type mockCacheInval struct {
	invalidated []transaction.AffectedParty
}

func (m *mockCacheInval) InvalidateTransactionCaches(_ context.Context, parties []transaction.AffectedParty) error {
	m.invalidated = append(m.invalidated, parties...)
	return nil
}

type mockExecutor struct {
	lastParams *transaction.ExecuteParams
	txn        *transaction.Transaction
	err        error
}

func (m *mockExecutor) ExecuteTransfer(_ context.Context, params transaction.ExecuteParams) (*transaction.Transaction, error) {
	m.lastParams = &params
	if m.err != nil {
		return nil, m.err
	}
	if m.txn != nil {
		return m.txn, nil
	}
	now := time.Now().UTC()
	return &transaction.Transaction{
		ID:              uuid.New(),
		UserID:          params.UserID,
		SourceAccountID: &params.SourceAccountID,
		Type:            params.TxnType,
		Status:          "SUCCESS",
		Amount:          params.Amount,
		AdminFee:        params.AdminFee,
		TotalAmount:     params.TotalAmount,
		Currency:        "IDR",
		ReferenceNumber: "REF2026090500000001",
		CreatedAt:       now,
		ProcessedAt:     &now,
		CompletedAt:     &now,
	}, nil
}

type execDeps struct {
	idemStore *mockIdempotencyStore
	vtCache   *mockVTokenCacheExec
	inqCache  *mockInqCacheExec
	inval     *mockCacheInval
	executor  *mockExecutor
	accounts  *mockAccountRepo
}

func setupExecService(opts ...func(*execDeps)) (*transaction.Service, *execDeps) {
	d := &execDeps{
		idemStore: newMockIdemStore(),
		vtCache:   newMockVTCache(),
		inqCache:  newMockInqCache(),
		inval:     &mockCacheInval{},
		executor:  &mockExecutor{},
		accounts:  &mockAccountRepo{},
	}
	for _, opt := range opts {
		opt(d)
	}
	svc := transaction.NewService(transaction.ServiceConfig{
		Mutations:        &mockMutationRepo{},
		Transactions:     &mockTransactionRepo{},
		Inquiries:        &mockInquiryRepo{},
		VTokens:          &mockVTokenRepo{},
		Favorites:        &mockFavoriteRepo{},
		Accounts:         d.accounts,
		Executor:         d.executor,
		InquiryCache:     d.inqCache,
		VTokenCache:      d.vtCache,
		CacheInvalidator: d.inval,
		IdemStore:        d.idemStore,
		Versions:         &mockVersionCounter{versions: make(map[string]int64)},
	})
	return svc, d
}

func makeTestInquiry(userID uuid.UUID) *transaction.Inquiry {
	fee := decimal.Zero
	amount := decimal.NewFromInt(100000)
	name := "John Doe"
	bank := "BCA"
	code := "014"
	return &transaction.Inquiry{
		ID:                 uuid.New(),
		UserID:             userID,
		InquiryType:        "TRANSFER",
		DestinationAccount: "9876543210",
		DestinationName:    &name,
		DestinationBank:    &bank,
		BankCode:           &code,
		Amount:             &amount,
		AdminFee:           &fee,
		ExpiresAt:          time.Now().Add(5 * time.Minute),
		CreatedAt:          time.Now(),
	}
}

func storeVToken(d *execDeps, rawToken string, userID uuid.UUID, purpose string) {
	d.vtCache.tokens[sha256Hex(rawToken)] = vtEntry{userID: userID.String(), purpose: purpose}
}

// --- Tests ---

func TestExecuteTransfer_HappyPath(t *testing.T) {
	userID := uuid.New()
	sourceAccID := uuid.New()
	inquiry := makeTestInquiry(userID)

	svc, deps := setupExecService(func(d *execDeps) {
		d.accounts = &mockAccountRepo{
			accounts: []account.Account{
				{ID: sourceAccID, UserID: userID, AccountNumber: "1234567890", AccountLabel: "NURHOLIS"},
			},
		}
	})

	rawToken := "happy-path-token"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	deps.inqCache.inquiries[inquiry.ID.String()] = inquiry

	resp, replayed, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         inquiry.ID.String(),
		SourceAccountID:   sourceAccID.String(),
		VerificationToken: rawToken,
		Notes:             "Test",
	}, "key-1")

	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if replayed {
		t.Fatal("should not be replayed")
	}
	if resp.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s", resp.Status)
	}
	if resp.Amount != "100000.00" {
		t.Fatalf("expected 100000.00, got %s", resp.Amount)
	}
	if resp.Source.AccountNumber != "1234567890" {
		t.Fatalf("expected source 1234567890, got %s", resp.Source.AccountNumber)
	}
	if resp.Destination.Name != "John Doe" {
		t.Fatalf("expected John Doe, got %s", resp.Destination.Name)
	}
	if deps.executor.lastParams == nil {
		t.Fatal("executor should have been called")
	}
}

func TestExecuteTransfer_InvalidVToken(t *testing.T) {
	svc, _ := setupExecService()

	_, _, err := svc.ExecuteTransfer(context.Background(), uuid.New(), transaction.ExecuteTransferRequest{
		InquiryID:         uuid.New().String(),
		SourceAccountID:   uuid.New().String(),
		VerificationToken: "nonexistent",
	}, "key-2")

	if err == nil {
		t.Fatal("expected error")
	}
	if apperr.From(err).Code != "VERIFICATION_TOKEN_INVALID" {
		t.Fatalf("expected VERIFICATION_TOKEN_INVALID, got %s", apperr.From(err).Code)
	}
}

func TestExecuteTransfer_WrongPurpose(t *testing.T) {
	userID := uuid.New()
	svc, deps := setupExecService()

	rawToken := "wrong-purpose"
	storeVToken(deps, rawToken, userID, "EWALLET_TOPUP")

	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         uuid.New().String(),
		SourceAccountID:   uuid.New().String(),
		VerificationToken: rawToken,
	}, "key-3")

	if err == nil {
		t.Fatal("expected error")
	}
	if apperr.From(err).Code != "VERIFICATION_TOKEN_INVALID" {
		t.Fatalf("got %s", apperr.From(err).Code)
	}
}

func TestExecuteTransfer_ExpiredInquiry(t *testing.T) {
	userID := uuid.New()
	svc, deps := setupExecService()

	rawToken := "expired-inq"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	// No inquiry stored — simulates expired

	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         uuid.New().String(),
		SourceAccountID:   uuid.New().String(),
		VerificationToken: rawToken,
	}, "key-4")

	if err == nil {
		t.Fatal("expected error")
	}
	if apperr.From(err).Code != "INQUIRY_EXPIRED" {
		t.Fatalf("got %s", apperr.From(err).Code)
	}
}

func TestExecuteTransfer_InquiryMismatch(t *testing.T) {
	userID := uuid.New()
	inquiry := makeTestInquiry(userID)
	svc, deps := setupExecService()

	rawToken := "mismatch"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	deps.inqCache.inquiries[inquiry.ID.String()] = inquiry

	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:          inquiry.ID.String(),
		SourceAccountID:    uuid.New().String(),
		DestinationAccount: "WRONG",
		VerificationToken:  rawToken,
	}, "key-5")

	if err == nil {
		t.Fatal("expected error")
	}
	if apperr.From(err).Code != "INQUIRY_MISMATCH" {
		t.Fatalf("got %s", apperr.From(err).Code)
	}
}

func TestExecuteTransfer_InsufficientBalance(t *testing.T) {
	userID := uuid.New()
	inquiry := makeTestInquiry(userID)

	svc, deps := setupExecService(func(d *execDeps) {
		d.executor = &mockExecutor{err: apperr.InsufficientBalance}
	})

	rawToken := "insuf"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	deps.inqCache.inquiries[inquiry.ID.String()] = inquiry

	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         inquiry.ID.String(),
		SourceAccountID:   uuid.New().String(),
		VerificationToken: rawToken,
	}, "key-6")

	if err == nil {
		t.Fatal("expected error")
	}
	if apperr.From(err).Code != "TRANSFER_INSUFFICIENT_BALANCE" {
		t.Fatalf("got %s", apperr.From(err).Code)
	}
}

func TestExecuteTransfer_DailyLimitExceeded(t *testing.T) {
	userID := uuid.New()
	inquiry := makeTestInquiry(userID)

	svc, deps := setupExecService(func(d *execDeps) {
		d.executor = &mockExecutor{err: apperr.TransferLimitExceeded}
	})

	rawToken := "limit"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	deps.inqCache.inquiries[inquiry.ID.String()] = inquiry

	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         inquiry.ID.String(),
		SourceAccountID:   uuid.New().String(),
		VerificationToken: rawToken,
	}, "key-7")

	if err == nil {
		t.Fatal("expected error")
	}
	if apperr.From(err).Code != "TRANSFER_LIMIT_EXCEEDED" {
		t.Fatalf("got %s", apperr.From(err).Code)
	}
}

func TestExecuteTransfer_Idempotent(t *testing.T) {
	userID := uuid.New()
	sourceAccID := uuid.New()
	inquiry := makeTestInquiry(userID)

	svc, deps := setupExecService(func(d *execDeps) {
		d.accounts = &mockAccountRepo{
			accounts: []account.Account{
				{ID: sourceAccID, UserID: userID, AccountNumber: "1234567890", AccountLabel: "NURHOLIS"},
			},
		}
	})

	rawToken := "idem-token"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	deps.inqCache.inquiries[inquiry.ID.String()] = inquiry

	req := transaction.ExecuteTransferRequest{
		InquiryID:         inquiry.ID.String(),
		SourceAccountID:   sourceAccID.String(),
		VerificationToken: rawToken,
	}

	// First call
	resp1, replayed1, err := svc.ExecuteTransfer(context.Background(), userID, req, "idem-key")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if replayed1 {
		t.Fatal("first should not be replayed")
	}

	// Second call — replayed
	resp2, replayed2, err := svc.ExecuteTransfer(context.Background(), userID, req, "idem-key")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !replayed2 {
		t.Fatal("second should be replayed")
	}
	if resp2.TransactionID != resp1.TransactionID {
		t.Fatal("replayed response should match")
	}
}

func TestExecuteTransfer_IdempotencyCleanupOnFailure(t *testing.T) {
	userID := uuid.New()
	svc, deps := setupExecService()

	// No vtoken stored — will fail at step 2
	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         uuid.New().String(),
		SourceAccountID:   uuid.New().String(),
		VerificationToken: "fail",
	}, "cleanup-key")

	if err == nil {
		t.Fatal("expected error")
	}

	key := userID.String() + ":cleanup-key"
	if !deps.idemStore.released[key] {
		t.Fatal("idempotency key should be released on failure")
	}
}

func TestExecuteTransfer_CacheInvalidationBothParties(t *testing.T) {
	userID := uuid.New()
	sourceAccID := uuid.New()
	destUserID := uuid.New()
	destAccID := uuid.New()
	inquiry := makeTestInquiry(userID)

	now := time.Now().UTC()
	svc, deps := setupExecService(func(d *execDeps) {
		d.accounts = &mockAccountRepo{
			accounts: []account.Account{
				{ID: sourceAccID, UserID: userID, AccountNumber: "1234567890", AccountLabel: "NURHOLIS"},
				{ID: destAccID, UserID: destUserID, AccountNumber: "9876543210", AccountLabel: "John Doe"},
			},
		}
		d.executor = &mockExecutor{
			txn: &transaction.Transaction{
				ID:                   uuid.New(),
				UserID:               userID,
				SourceAccountID:      &sourceAccID,
				DestinationAccountID: &destAccID,
				Type:                 "TRANSFER_INTERNAL",
				Status:               "SUCCESS",
				Amount:               decimal.NewFromInt(100000),
				TotalAmount:          decimal.NewFromInt(100000),
				Currency:             "IDR",
				ReferenceNumber:      "REF20260905TEST",
				CreatedAt:            now,
			},
		}
	})

	rawToken := "cache-inval"
	storeVToken(deps, rawToken, userID, "TRANSFER")
	deps.inqCache.inquiries[inquiry.ID.String()] = inquiry

	_, _, err := svc.ExecuteTransfer(context.Background(), userID, transaction.ExecuteTransferRequest{
		InquiryID:         inquiry.ID.String(),
		SourceAccountID:   sourceAccID.String(),
		VerificationToken: rawToken,
	}, "cache-key")

	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(deps.inval.invalidated) < 2 {
		t.Fatalf("expected >=2 invalidated parties, got %d", len(deps.inval.invalidated))
	}

	foundSrc, foundDst := false, false
	for _, p := range deps.inval.invalidated {
		if p.UserID == userID.String() {
			foundSrc = true
		}
		if p.UserID == destUserID.String() {
			foundDst = true
		}
	}
	if !foundSrc {
		t.Fatal("source not invalidated")
	}
	if !foundDst {
		t.Fatal("destination not invalidated")
	}
}