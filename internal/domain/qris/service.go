package qris

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
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
	notifier         *notify.Notifier
	versions         account.VersionCounter
}

type ServiceConfig struct {
	Executor         QRISExecutor
	Accounts         account.AccountRepository
	DecodeCache      QRISDecodeCache
	VTokenCache      transaction.VerificationTokenCache
	CacheInvalidator transaction.TransactionCacheInvalidator
	IdemStore        transaction.IdempotencyStore
	Notifier         *notify.Notifier
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
		notifier:         cfg.Notifier,
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

	// A failed payment hands the decoded QR and the verification token back —
	// re-scanning a merchant code and re-entering the PIN for a payment that
	// never happened is friction with no safety benefit.
	releaseIdem := true
	var restoreDecoded *DecodedQRIS
	restoreVToken := ""
	defer func() {
		if !releaseIdem {
			return
		}
		// Detached: landing here with a cancelled context usually means the
		// client gave up, and that is precisely when the key has to be released
		// so their retry is not answered with a 409.
		undoCtx, cancelUndo := transaction.PostCommitContext(ctx)
		defer cancelUndo()

		if s.idemStore != nil {
			if err := s.idemStore.Release(undoCtx, userID.String(), idemKey); err != nil {
				slog.Error("release idempotency key failed", "error", err)
			}
		}
		if restoreDecoded != nil && s.decodeCache != nil {
			if err := s.decodeCache.StoreDecoded(undoCtx, restoreDecoded.QRISID, restoreDecoded); err != nil {
				slog.Warn("restore decoded qris failed", "error", err)
			}
		}
		if restoreVToken != "" && s.vtokenCache != nil {
			if err := s.vtokenCache.StoreToken(undoCtx, restoreVToken, userID.String(), "QRIS_PAYMENT"); err != nil {
				slog.Warn("restore qris verification token failed", "error", err)
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
	restoreVToken = tokenHash

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
	restoreDecoded = decoded

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

	// Ownership gate — the executor debits this UUID with no user filter.
	sourceAccount, err := s.accounts.FindOwnedByID(ctx, userID, sourceAccountID)
	if err != nil {
		return nil, false, fmt.Errorf("resolve source account: %w", err)
	}
	if sourceAccount == nil {
		return nil, false, apperr.SourceAccountForbidden
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

	// Committed. Follow-up work runs detached so a client that hung up cannot
	// leave the balance cache stale or the idempotent response unwritten.
	postCtx, cancelPost := transaction.PostCommitContext(ctx)
	defer cancelPost()

	// Cache invalidation
	parties := []transaction.AffectedParty{
		{UserID: userID.String(), AccountID: sourceAccountID.String()},
	}
	if s.cacheInvalidator != nil {
		if err := s.cacheInvalidator.InvalidateTransactionCaches(postCtx, parties); err != nil {
			slog.Error("cache invalidation failed", "error", err)
		}
	}

	// Source info — already resolved by the ownership check above.
	sourceName := sourceAccount.AccountLabel
	sourceNumber := sourceAccount.AccountNumber

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

	releaseIdem = false

	if s.notifier != nil {
		s.notifier.Transaction(postCtx, userID,
			"Pembayaran QRIS berhasil",
			fmt.Sprintf("Pembayaran %s ke %s berhasil. Ref: %s",
				formatIDR(totalAmount), decoded.MerchantName, txn.ReferenceNumber),
			"bcamobile://transaction/"+txn.ID.String(),
			map[string]any{
				"transaction_id":   txn.ID.String(),
				"reference_number": txn.ReferenceNumber,
				"amount":           amount.StringFixed(2),
				"merchant":         decoded.MerchantName,
			},
		)
	}

	// Persist idempotent response
	if s.idemStore != nil {
		respJSON, err := json.Marshal(resp)
		if err != nil {
			slog.Error("marshal idempotent response failed", "error", err)
		} else if err := s.idemStore.Persist(postCtx, userID.String(), idemKey, string(respJSON)); err != nil {
			slog.Error("persist idempotent response failed", "error", err)
		}
	}

	return resp, false, nil
}

func hashVToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}

// formatIDR renders a decimal as "Rp1.234.567".
func formatIDR(d decimal.Decimal) string {
	digits := d.Truncate(0).Abs().String()

	var b strings.Builder
	b.Grow(len(digits) + len(digits)/3 + 3)
	b.WriteString("Rp")
	if d.IsNegative() {
		b.WriteString("-")
	}
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(".")
		}
		b.WriteRune(r)
	}
	return b.String()
}
