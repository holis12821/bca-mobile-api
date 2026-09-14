package qris

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

var wib = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic("failed to load Asia/Jakarta: " + err.Error())
	}
	return loc
}()

type Service struct {
	executor         QRISExecutor
	accounts         account.AccountRepository
	decodeCache      QRISDecodeCache
	vtokenCache      transaction.VerificationTokenCache
	cacheInvalidator transaction.TransactionCacheInvalidator
	idemStore        transaction.IdempotencyStore
	versions         account.VersionCounter
}

type ServiceConfig struct {
	Executor         QRISExecutor
	Accounts         account.AccountRepository
	DecodeCache      QRISDecodeCache
	VTokenCache      transaction.VerificationTokenCache
	CacheInvalidator transaction.TransactionCacheInvalidator
	IdemStore        transaction.IdempotencyStore
	Versions         account.VersionCounter
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		executor:         cfg.Executor,
		accounts:         cfg.Accounts,
		decodeCache:      cfg.DecodeCache,
		vtokenCache:      cfg.VTokenCache,
		cacheInvalidator: cfg.CacheInvalidator,
		idemStore:        cfg.IdemStore,
		versions:         cfg.Versions,
	}
}

// Decode parses and caches QRIS payload, returns decoded data.
func (s *Service) Decode(ctx context.Context, req DecodeRequest) (*DecodedQRIS, error) {
	decoded, err := ParseEMVCo(req.QRData)
	if err != nil {
		return nil, err
	}

	qrisID := uuid.New().String()
	expiresAt := time.Now().Add(DecodeCacheTTL)

	decoded.QRISID = qrisID
	decoded.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)

	if s.decodeCache != nil {
		if err := s.decodeCache.StoreDecoded(ctx, qrisID, decoded); err != nil {
			return nil, fmt.Errorf("store decoded qris: %w", err)
		}
	}

	return decoded, nil
}

// Pay executes the QRIS payment.
func (s *Service) Pay(ctx context.Context, userID uuid.UUID, req PayRequest, idemKey string) (*PayResponse, bool, error) {
	// Step 1: Idempotency guard
	if s.idemStore != nil {
		result, err := s.idemStore.Claim(ctx, userID.String(), idemKey)
		if err != nil {
			return nil, false, fmt.Errorf("idempotency claim: %w", err)
		}
		if result.AlreadyClaimed {
			if result.StillProcessing {
				return nil, false, apperr.IdempotencyConflict
			}
			var resp PayResponse
			if err := json.Unmarshal([]byte(result.StoredResponse), &resp); err != nil {
				return nil, false, fmt.Errorf("decode idempotent response: %w", err)
			}
			return &resp, true, nil
		}
	}

	releaseIdem := true
	defer func() {
		if releaseIdem && s.idemStore != nil {
			if err := s.idemStore.Release(ctx, userID.String(), idemKey); err != nil {
				slog.Error("release idempotency key failed", "error", err)
			}
		}
	}()

	// Step 2: Consume verification token
	tokenHash := hashVToken(req.VerificationToken)
	vtokenUserID, vtokenPurpose, err := s.vtokenCache.ConsumeToken(ctx, tokenHash)
	if err != nil {
		return nil, false, fmt.Errorf("consume vtoken: %w", err)
	}
	if vtokenUserID == "" || vtokenUserID != userID.String() || vtokenPurpose != "QRIS_PAYMENT" {
		return nil, false, apperr.VerificationTokenInvalid
	}

	// Step 3: Get decoded QRIS data
	if s.decodeCache == nil {
		return nil, false, apperr.QRISInvalidPayload
	}
	decoded, err := s.decodeCache.ConsumeDecoded(ctx, req.QRISID)
	if err != nil {
		return nil, false, fmt.Errorf("consume decoded qris: %w", err)
	}
	if decoded == nil {
		return nil, false, apperr.InquiryExpired
	}

	// Determine amount: use decoded if fixed, otherwise from request
	amount := decoded.Amount
	if !decoded.IsAmountFixed && req.Amount > 0 {
		amount = decimal.NewFromInt(req.Amount)
	}
	if amount.IsZero() || amount.IsNegative() {
		return nil, false, apperr.ValidationError
	}

	sourceAccountID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		return nil, false, apperr.ValidationError
	}

	adminFee := decimal.Zero
	totalAmount := amount.Add(adminFee)

	now := time.Now().In(wib)

	params := ExecutePayParams{
		IdempotencyKey:  idemKey,
		UserID:          userID,
		SourceAccountID: sourceAccountID,
		MerchantName:    decoded.MerchantName,
		MerchantCity:    decoded.MerchantCity,
		Amount:          amount,
		AdminFee:        adminFee,
		TotalAmount:     totalAmount,
		WIBDate:         now,
		WIBTime:         now,
	}

	txn, err := s.executor.ExecutePay(ctx, params)
	if err != nil {
		return nil, false, err
	}

	// Cache invalidation
	parties := []transaction.AffectedParty{
		{UserID: userID.String(), AccountID: sourceAccountID.String()},
	}
	if s.cacheInvalidator != nil {
		if err := s.cacheInvalidator.InvalidateTransactionCaches(ctx, parties); err != nil {
			slog.Error("cache invalidation failed", "error", err)
		}
	}

	// Resolve source info
	sourceName := ""
	sourceNumber := ""
	accounts, err := s.accounts.FindActiveByUserID(ctx, userID)
	if err == nil {
		for _, a := range accounts {
			if a.ID == sourceAccountID {
				sourceNumber = a.AccountNumber
				sourceName = a.AccountLabel
				break
			}
		}
	}

	resp := &PayResponse{
		TransactionID:   txn.ID.String(),
		ReferenceNumber: txn.ReferenceNumber,
		Status:          txn.Status,
		MerchantName:    decoded.MerchantName,
		MerchantCity:    decoded.MerchantCity,
		Amount:          amount.StringFixed(2),
		AdminFee:        adminFee.StringFixed(2),
		Total:           totalAmount.StringFixed(2),
		SourceAccount:   sourceNumber,
		SourceName:      sourceName,
		CreatedAt:       txn.CreatedAt.UTC().Format(time.RFC3339),
	}

	// Persist idempotent response
	releaseIdem = false
	if s.idemStore != nil {
		respJSON, err := json.Marshal(resp)
		if err != nil {
			slog.Error("marshal idempotent response failed", "error", err)
		} else if err := s.idemStore.Persist(ctx, userID.String(), idemKey, string(respJSON)); err != nil {
			slog.Error("persist idempotent response failed", "error", err)
		}
	}

	return resp, false, nil
}

func hashVToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}