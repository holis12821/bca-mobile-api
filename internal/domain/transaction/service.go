package transaction

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pagination"
)

type Service struct {
	mutations   MutationRepository
	txns        TransactionRepository
	inquiries   InquiryRepository
	vtokens     VerificationTokenRepository
	favorites   FavoriteTransferRepository
	accounts    account.AccountRepository
	executor    TransferExecutor
	inquiryCache     InquiryCache
	vtokenCache      VerificationTokenCache
	recentCache      RecentTransferCache
	receiptCache     ReceiptCache
	cacheInvalidator TransactionCacheInvalidator
	idemStore        IdempotencyStore
	versions         account.VersionCounter
}

type ServiceConfig struct {
	Mutations        MutationRepository
	Transactions     TransactionRepository
	Inquiries        InquiryRepository
	VTokens          VerificationTokenRepository
	Favorites        FavoriteTransferRepository
	Accounts         account.AccountRepository
	Executor         TransferExecutor
	InquiryCache     InquiryCache
	VTokenCache      VerificationTokenCache
	RecentCache      RecentTransferCache
	ReceiptCache     ReceiptCache
	CacheInvalidator TransactionCacheInvalidator
	IdemStore        IdempotencyStore
	Versions         account.VersionCounter
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		mutations:        cfg.Mutations,
		txns:             cfg.Transactions,
		inquiries:        cfg.Inquiries,
		vtokens:          cfg.VTokens,
		favorites:        cfg.Favorites,
		accounts:         cfg.Accounts,
		executor:         cfg.Executor,
		inquiryCache:     cfg.InquiryCache,
		vtokenCache:      cfg.VTokenCache,
		recentCache:      cfg.RecentCache,
		receiptCache:     cfg.ReceiptCache,
		cacheInvalidator: cfg.CacheInvalidator,
		idemStore:        cfg.IdemStore,
		versions:         cfg.Versions,
	}
}

// ListMutations returns keyset-paginated mutations for an account.
func (s *Service) ListMutations(ctx context.Context, userID uuid.UUID, accountID uuid.UUID, cursorStr string, limit int, period *DateRange) ([]MutationItem, bool, string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var cursor *MutationCursorValues
	if cursorStr != "" {
		decoded, err := pagination.DecodeMutationCursor(cursorStr)
		if err != nil {
			return nil, false, "", apperr.ValidationError
		}
		d, _ := time.Parse("2006-01-02", decoded.Date)
		c, _ := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
		id, _ := uuid.Parse(decoded.ID)
		cursor = &MutationCursorValues{Date: d, CreatedAt: c, ID: id}
	}

	rows, err := s.mutations.ListByAccountID(ctx, accountID, cursor, limit+1, period)
	if err != nil {
		return nil, false, "", fmt.Errorf("list mutations: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	items := make([]MutationItem, len(rows))
	for i, m := range rows {
		items[i] = toMutationItem(m)
	}

	var nextCursor string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = pagination.MutationCursor{
			Date:      last.TransactionDate.Format("2006-01-02"),
			CreatedAt: last.CreatedAt.Format(time.RFC3339Nano),
			ID:        last.ID.String(),
		}.Encode()
	}

	return items, hasMore, nextCursor, nil
}

// ListHistory returns keyset-paginated transaction history.
func (s *Service) ListHistory(ctx context.Context, userID uuid.UUID, txnType *string, cursorStr string, limit int) ([]TransactionItem, bool, string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var cursor *HistoryCursorValues
	if cursorStr != "" {
		decoded, err := pagination.DecodeHistoryCursor(cursorStr)
		if err != nil {
			return nil, false, "", apperr.ValidationError
		}
		c, _ := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
		id, _ := uuid.Parse(decoded.ID)
		cursor = &HistoryCursorValues{CreatedAt: c, ID: id}
	}

	rows, err := s.txns.ListByUserID(ctx, userID, txnType, cursor, limit+1)
	if err != nil {
		return nil, false, "", fmt.Errorf("list history: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	items := make([]TransactionItem, len(rows))
	for i, t := range rows {
		items[i] = toTransactionItem(t)
	}

	var nextCursor string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = pagination.HistoryCursor{
			CreatedAt: last.CreatedAt.Format(time.RFC3339Nano),
			ID:        last.ID.String(),
		}.Encode()
	}

	return items, hasMore, nextCursor, nil
}

// ListRecentTransfers returns recent transfer destinations.
func (s *Service) ListRecentTransfers(ctx context.Context, userID uuid.UUID) ([]RecentTransferItem, error) {
	if s.recentCache != nil {
		cached, err := s.recentCache.GetRecent(ctx, userID)
		if err != nil {
			slog.Warn("recent cache get failed", "error", err)
		}
		if cached != nil {
			return toRecentItems(cached), nil
		}
	}

	favorites, err := s.favorites.ListByUserID(ctx, userID, 10)
	if err != nil {
		return nil, fmt.Errorf("list recent: %w", err)
	}

	if s.recentCache != nil {
		if err := s.recentCache.SetRecent(ctx, userID, favorites); err != nil {
			slog.Warn("recent cache set failed", "error", err)
		}
	}

	return toRecentItems(favorites), nil
}

// CreateInquiry validates the destination and creates a transfer inquiry.
func (s *Service) CreateInquiry(ctx context.Context, userID uuid.UUID, req InquiryRequest) (*InquiryResponse, error) {
	if !ValidTransferTypes[req.TransferType] {
		return nil, apperr.ValidationError
	}

	transferType := "INTERNAL"
	if req.TransferType == "EXTERNAL" || req.TransferType == "VIRTUAL_ACCOUNT" {
		transferType = "EXTERNAL"
	}

	var destName string
	var destBank string
	bankCode := "014" // BCA default
	var adminFee decimal.Decimal

	if transferType == "INTERNAL" {
		// Lookup destination account — MUST filter owner_type='CUSTOMER'
		destAccount, err := s.accounts.FindByAccountNumber(ctx, req.DestinationAccount)
		if err != nil {
			return nil, fmt.Errorf("find destination: %w", err)
		}
		if destAccount == nil {
			return nil, apperr.TransferAccountNotFound
		}

		// Self-transfer check
		if destAccount.UserID == userID {
			return nil, apperr.SelfTransfer
		}

		destName = destAccount.AccountLabel
		destBank = "BCA"
		adminFee = decimal.Zero
	} else {
		// External transfer — use provided bank info
		if req.BankCode == "" {
			return nil, apperr.ValidationError
		}
		destName = req.DestinationAccount // will be resolved by provider in real impl
		destBank = req.DestinationBank
		bankCode = req.BankCode
		adminFee = decimal.NewFromInt(6500) // standard inter-bank fee
	}

	inquiryID := uuid.New()
	expiresAt := time.Now().Add(InquiryCacheTTL)

	var amount *decimal.Decimal
	if req.Amount > 0 {
		a := decimal.NewFromInt(req.Amount)
		amount = &a
	}

	inquiry := &Inquiry{
		ID:                 inquiryID,
		UserID:             userID,
		InquiryType:        "TRANSFER",
		DestinationAccount: req.DestinationAccount,
		DestinationName:    &destName,
		DestinationBank:    &destBank,
		BankCode:           &bankCode,
		Amount:             amount,
		AdminFee:           &adminFee,
		ExpiresAt:          expiresAt,
		CreatedAt:          time.Now(),
	}

	// Store in PostgreSQL (audit record)
	if err := s.inquiries.Create(ctx, inquiry); err != nil {
		return nil, fmt.Errorf("create inquiry: %w", err)
	}

	// Store in Redis (authoritative for liveness)
	if s.inquiryCache != nil {
		if err := s.inquiryCache.StoreInquiry(ctx, inquiry); err != nil {
			slog.Error("store inquiry cache failed", "error", err)
		}
	}

	return &InquiryResponse{
		InquiryID:          inquiryID.String(),
		DestinationAccount: req.DestinationAccount,
		DestinationName:    destName,
		DestinationBank:    destBank,
		BankCode:           bankCode,
		TransferType:       req.TransferType,
		AdminFee:           adminFee.String(),
		ExpiresIn:          int(InquiryCacheTTL.Seconds()),
	}, nil
}

// CreateVerificationToken issues a purpose-bound verification token after PIN verify.
func (s *Service) CreateVerificationToken(ctx context.Context, userID uuid.UUID, purpose string) (*PINVerifyResponse, error) {
	if !ValidPurposes[purpose] {
		return nil, apperr.ValidationError
	}

	// Generate random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	rawToken := hex.EncodeToString(tokenBytes)
	tokenHash := hashToken(rawToken)

	expiresAt := time.Now().Add(VerificationTokenTTL)

	// Store in PostgreSQL (audit record)
	vtoken := &VerificationToken{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: tokenHash,
		Purpose:   purpose,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
	if err := s.vtokens.Create(ctx, vtoken); err != nil {
		return nil, fmt.Errorf("create vtoken: %w", err)
	}

	// Store in Redis (authoritative for liveness)
	if s.vtokenCache != nil {
		if err := s.vtokenCache.StoreToken(ctx, tokenHash, userID.String(), purpose); err != nil {
			slog.Error("store vtoken cache failed", "error", err)
		}
	}

	return &PINVerifyResponse{
		VerificationToken: rawToken,
		ExpiresIn:         int(VerificationTokenTTL.Seconds()),
	}, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func toMutationItem(m Mutation) MutationItem {
	item := MutationItem{
		ID:              m.ID.String(),
		MutationType:    m.MutationType,
		Amount:          m.Amount.String(),
		BalanceBefore:   m.BalanceBefore.String(),
		BalanceAfter:    m.BalanceAfter.String(),
		Description:     m.Description,
		TransactionDate: m.TransactionDate.Format("2006-01-02"),
		TransactionTime: m.TransactionTime.Format("15:04:05"),
		CreatedAt:       m.CreatedAt.UTC().Format(time.RFC3339),
	}
	if m.Detail != nil {
		item.Detail = *m.Detail
	}
	if m.Category != nil {
		item.Category = *m.Category
	}
	if m.ReferenceNumber != nil {
		item.ReferenceNumber = *m.ReferenceNumber
	}
	return item
}

func toTransactionItem(t Transaction) TransactionItem {
	item := TransactionItem{
		ID:              t.ID.String(),
		Type:            t.Type,
		Status:          t.Status,
		Amount:          t.Amount.String(),
		AdminFee:        t.AdminFee.String(),
		TotalAmount:     t.TotalAmount.String(),
		Currency:        t.Currency,
		ReferenceNumber: t.ReferenceNumber,
		CreatedAt:       t.CreatedAt.UTC().Format(time.RFC3339),
	}
	if t.Description != nil {
		item.Description = *t.Description
	}
	if t.Notes != nil {
		item.Notes = *t.Notes
	}
	if t.DestinationAccount != nil {
		item.DestinationAccount = *t.DestinationAccount
	}
	if t.DestinationName != nil {
		item.DestinationName = *t.DestinationName
	}
	if t.DestinationBank != nil {
		item.DestinationBank = *t.DestinationBank
	}
	return item
}

// GetReceipt returns a formatted receipt for a transaction.
func (s *Service) GetReceipt(ctx context.Context, userID, txnID uuid.UUID) (*ReceiptResponse, error) {
	txnIDStr := txnID.String()

	// Check cache first (receipts are immutable, 24h TTL)
	if s.receiptCache != nil {
		cached, err := s.receiptCache.GetReceipt(ctx, txnIDStr)
		if err != nil {
			slog.Warn("receipt cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	txn, err := s.txns.FindByID(ctx, userID, txnID)
	if err != nil {
		return nil, fmt.Errorf("find transaction: %w", err)
	}
	if txn == nil {
		return nil, apperr.NotFound
	}

	// Resolve source account info
	var sourceNumber, sourceName string
	if txn.SourceAccountID != nil {
		accounts, err := s.accounts.FindActiveByUserID(ctx, userID)
		if err == nil {
			for _, a := range accounts {
				if a.ID == *txn.SourceAccountID {
					sourceNumber = a.AccountNumber
					sourceName = a.AccountLabel
					break
				}
			}
		}
	}

	wibLoc, _ := time.LoadLocation("Asia/Jakarta")
	createdWIB := txn.CreatedAt.In(wibLoc)

	receipt := &ReceiptResponse{
		TransactionID:   txn.ID.String(),
		Type:            txn.Type,
		Status:          txn.Status,
		Date:            createdWIB.Format("02 January 2006"),
		Time:            createdWIB.Format("15:04 WIB"),
		ReferenceNumber: txn.ReferenceNumber,
		SourceAccount:   sourceNumber,
		SourceName:      sourceName,
		Amount:          txn.Amount.StringFixed(2),
		AdminFee:        txn.AdminFee.StringFixed(2),
		Total:           txn.TotalAmount.StringFixed(2),
		Currency:        txn.Currency,
	}

	if txn.DestinationAccount != nil {
		receipt.DestinationAccount = *txn.DestinationAccount
	}
	if txn.DestinationName != nil {
		receipt.DestinationName = *txn.DestinationName
	}
	if txn.DestinationBank != nil {
		receipt.DestinationBank = *txn.DestinationBank
	}
	if txn.ProviderName != nil {
		receipt.ProviderName = *txn.ProviderName
	}
	if txn.Notes != nil {
		receipt.Notes = *txn.Notes
	}

	if s.receiptCache != nil {
		if err := s.receiptCache.SetReceipt(ctx, txnIDStr, receipt); err != nil {
			slog.Warn("receipt cache set failed", "error", err)
		}
	}

	return receipt, nil
}

func toRecentItems(favs []FavoriteTransfer) []RecentTransferItem {
	items := make([]RecentTransferItem, len(favs))
	for i, f := range favs {
		items[i] = RecentTransferItem{
			DestinationAccount: f.DestinationAccount,
			DestinationName:    f.DestinationName,
			DestinationBank:    f.DestinationBank,
			BankCode:           f.BankCode,
			TransferType:       f.TransferType,
			TransferCount:      f.TransferCount,
		}
		if f.LastTransferAt != nil {
			items[i].LastTransferAt = f.LastTransferAt.UTC().Format(time.RFC3339)
		}
	}
	return items
}