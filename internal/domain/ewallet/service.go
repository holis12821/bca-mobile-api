package ewallet

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
	providers        ProviderRepository
	providerStub     EWalletProviderStub
	executor         EWalletExecutor
	accounts         account.AccountRepository
	inquiryCache     transaction.InquiryCache
	vtokenCache      transaction.VerificationTokenCache
	providerCache    ProviderCache
	cacheInvalidator transaction.TransactionCacheInvalidator
	idemStore        transaction.IdempotencyStore
	notifier         *notify.Notifier
	versions         account.VersionCounter
}

type ServiceConfig struct {
	Providers        ProviderRepository
	ProviderStub     EWalletProviderStub
	Executor         EWalletExecutor
	Accounts         account.AccountRepository
	InquiryCache     transaction.InquiryCache
	VTokenCache      transaction.VerificationTokenCache
	ProviderCache    ProviderCache
	CacheInvalidator transaction.TransactionCacheInvalidator
	IdemStore        transaction.IdempotencyStore
	Notifier         *notify.Notifier
	Versions         account.VersionCounter
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		providers:        cfg.Providers,
		providerStub:     cfg.ProviderStub,
		executor:         cfg.Executor,
		accounts:         cfg.Accounts,
		inquiryCache:     cfg.InquiryCache,
		vtokenCache:      cfg.VTokenCache,
		providerCache:    cfg.ProviderCache,
		cacheInvalidator: cfg.CacheInvalidator,
		idemStore:        cfg.IdemStore,
		notifier:         cfg.Notifier,
		versions:         cfg.Versions,
	}
}

// ListProviders returns active e-wallet providers, cached 1 hour.
func (s *Service) ListProviders(ctx context.Context) ([]ProviderItem, error) {
	// Check cache
	if s.providerCache != nil {
		cached, err := s.providerCache.GetProviders(ctx)
		if err != nil {
			slog.Warn("provider cache get failed", "error", err)
		}
		if cached != nil {
			return toProviderItems(cached), nil
		}
	}

	providers, err := s.providers.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}

	if s.providerCache != nil {
		if err := s.providerCache.SetProviders(ctx, providers); err != nil {
			slog.Warn("provider cache set failed", "error", err)
		}
	}

	return toProviderItems(providers), nil
}

// CreateInquiry validates the phone number via the provider stub and creates an inquiry.
func (s *Service) CreateInquiry(ctx context.Context, userID uuid.UUID, req InquiryRequest) (*InquiryResponse, error) {
	// Validate provider exists
	provider, err := s.providers.FindByID(ctx, req.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("find provider: %w", err)
	}
	if provider == nil || !provider.IsActive {
		return nil, apperr.ValidationError
	}

	amount := decimal.NewFromInt(req.Amount)
	if amount.LessThan(provider.MinAmount) || amount.GreaterThan(provider.MaxAmount) {
		return nil, apperr.Error{
			Status:  422,
			Code:    "VALIDATION_ERROR",
			Message: fmt.Sprintf("Nominal harus antara %s dan %s.", provider.MinAmount.String(), provider.MaxAmount.String()),
		}
	}

	// Validate phone via provider stub
	lookupResult, err := s.providerStub.Lookup(ctx, req.ProviderID, req.PhoneNumber)
	if err != nil {
		return nil, err // propagates EWALLET_PROVIDER_DOWN
	}
	if !lookupResult.Valid {
		return nil, apperr.EWalletAccountNotFound
	}

	sourceAccountID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		return nil, apperr.ValidationError
	}

	// The source account has to belong to the caller. This used to be resolved
	// only for display, and an account that was not theirs simply produced an
	// empty source_account in the response while the top-up still went through.
	sourceAccount, err := s.accounts.FindOwnedByID(ctx, userID, sourceAccountID)
	if err != nil {
		return nil, fmt.Errorf("resolve source account: %w", err)
	}
	if sourceAccount == nil {
		return nil, apperr.SourceAccountForbidden
	}

	adminFee := provider.AdminFee
	totalAmount := amount.Add(adminFee)
	inquiryID := uuid.New()
	expiresAt := time.Now().Add(transaction.InquiryCacheTTL)

	// Store inquiry in Redis (authoritative for liveness)
	inquiry := &transaction.Inquiry{
		ID:                 inquiryID,
		UserID:             userID,
		InquiryType:        "EWALLET",
		DestinationAccount: req.PhoneNumber,
		DestinationName:    &lookupResult.Name,
		ProviderID:         &req.ProviderID,
		Amount:             &amount,
		AdminFee:           &adminFee,
		ExpiresAt:          expiresAt,
		CreatedAt:          time.Now(),
		Metadata: map[string]any{
			"source_account_id": sourceAccountID.String(),
			"provider_name":     provider.Name,
			"total_amount":      totalAmount.String(),
		},
	}

	if s.inquiryCache != nil {
		if err := s.inquiryCache.StoreInquiry(ctx, inquiry); err != nil {
			return nil, fmt.Errorf("store inquiry: %w", err)
		}
	}

	maskedPhone := account.MaskPhone(req.PhoneNumber)

	sourceNumber := sourceAccount.AccountNumber

	return &InquiryResponse{
		InquiryID:        inquiryID.String(),
		Provider:         provider.Name,
		DestinationName:  lookupResult.Name,
		DestinationPhone: maskedPhone,
		Amount:           amount.StringFixed(2),
		AdminFee:         adminFee.StringFixed(2),
		Total:            totalAmount.StringFixed(2),
		SourceAccount:    sourceNumber,
		ExpiresIn:        int(transaction.InquiryCacheTTL.Seconds()),
	}, nil
}

// ExecuteTopUp consumes the inquiry and verification token, then executes the top-up.
func (s *Service) ExecuteTopUp(ctx context.Context, userID uuid.UUID, req TopUpRequest, idemKey string) (*TopUpResponse, bool, error) {
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
			var resp TopUpResponse
			if err := json.Unmarshal([]byte(result.StoredResponse), &resp); err != nil {
				return nil, false, fmt.Errorf("decode idempotent response: %w", err)
			}
			return &resp, true, nil
		}
	}

	// A failed top-up gives the inquiry and the verification token back — see
	// the equivalent comment in transaction.ExecuteTransfer. Nothing was
	// written, so making the nasabah redo the PIN prompt is pure friction.
	releaseIdem := true
	var restoreInquiry *transaction.Inquiry
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
		if restoreInquiry != nil && s.inquiryCache != nil && time.Now().Before(restoreInquiry.ExpiresAt) {
			if err := s.inquiryCache.StoreInquiry(undoCtx, restoreInquiry); err != nil {
				slog.Warn("restore ewallet inquiry failed", "error", err)
			}
		}
		if restoreVToken != "" && s.vtokenCache != nil {
			if err := s.vtokenCache.StoreToken(undoCtx, restoreVToken, userID.String(), "EWALLET_TOPUP"); err != nil {
				slog.Warn("restore ewallet verification token failed", "error", err)
			}
		}
	}()

	// Step 2: Consume verification token
	tokenHash := hashVToken(req.VerificationToken)
	vtokenUserID, vtokenPurpose, err := s.vtokenCache.ConsumeToken(ctx, tokenHash)
	if err != nil {
		return nil, false, fmt.Errorf("consume vtoken: %w", err)
	}
	if vtokenUserID == "" || vtokenUserID != userID.String() || vtokenPurpose != "EWALLET_TOPUP" {
		return nil, false, apperr.VerificationTokenInvalid
	}
	restoreVToken = tokenHash

	// Step 3: Consume inquiry
	inquiry, err := s.inquiryCache.ConsumeInquiry(ctx, req.InquiryID)
	if err != nil {
		return nil, false, fmt.Errorf("consume inquiry: %w", err)
	}
	if inquiry == nil || inquiry.UserID != userID {
		return nil, false, apperr.InquiryExpired
	}
	restoreInquiry = inquiry

	// Extract fields from inquiry
	providerID := ""
	if inquiry.ProviderID != nil {
		providerID = *inquiry.ProviderID
	}
	providerName := providerID
	destName := inquiry.DestinationAccount
	if inquiry.DestinationName != nil {
		destName = *inquiry.DestinationName
	}
	amount := decimal.Zero
	if inquiry.Amount != nil {
		amount = *inquiry.Amount
	}
	adminFee := decimal.Zero
	if inquiry.AdminFee != nil {
		adminFee = *inquiry.AdminFee
	}
	totalAmount := amount.Add(adminFee)

	// Extract source account from metadata
	var sourceAccountID uuid.UUID
	if m := inquiry.Metadata; m != nil {
		if sid, ok := m["source_account_id"].(string); ok {
			sourceAccountID, _ = uuid.Parse(sid)
		}
		if pn, ok := m["provider_name"].(string); ok {
			providerName = pn
		}
	}
	if sourceAccountID == uuid.Nil {
		return nil, false, apperr.ValidationError
	}

	sourceAccount, err := s.accounts.FindOwnedByID(ctx, userID, sourceAccountID)
	if err != nil {
		return nil, false, fmt.Errorf("resolve source account: %w", err)
	}
	if sourceAccount == nil {
		return nil, false, apperr.SourceAccountForbidden
	}

	now := time.Now().In(wib)

	params := ExecuteTopUpParams{
		IdempotencyKey:  idemKey,
		UserID:          userID,
		SourceAccountID: sourceAccountID,
		ProviderID:      providerID,
		ProviderName:    providerName,
		PhoneNumber:     inquiry.DestinationAccount,
		DestinationName: destName,
		Amount:          amount,
		AdminFee:        adminFee,
		TotalAmount:     totalAmount,
		WIBDate:         now,
		WIBTime:         now,
	}

	txn, err := s.executor.ExecuteTopUp(ctx, params)
	if err != nil {
		return nil, false, err
	}

	// Committed. Follow-up work runs detached so a client that hung up cannot
	// leave the balance cache stale or the idempotent response unwritten.
	postCtx, cancelPost := transaction.PostCommitContext(ctx)
	defer cancelPost()

	// Cache invalidation for source user
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

	maskedPhone := account.MaskPhone(inquiry.DestinationAccount)

	resp := &TopUpResponse{
		TransactionID:    txn.ID.String(),
		ReferenceNumber:  txn.ReferenceNumber,
		Status:           txn.Status,
		Provider:         providerName,
		DestinationPhone: maskedPhone,
		DestinationName:  destName,
		Amount:           amount.StringFixed(2),
		AdminFee:         adminFee.StringFixed(2),
		Total:            totalAmount.StringFixed(2),
		SourceAccount:    sourceNumber,
		SourceName:       sourceName,
		CreatedAt:        txn.CreatedAt.UTC().Format(time.RFC3339),
	}

	releaseIdem = false

	if s.notifier != nil {
		s.notifier.Transaction(postCtx, userID,
			"Top up e-wallet berhasil",
			fmt.Sprintf("Top up %s ke %s (%s) berhasil. Ref: %s",
				formatIDR(totalAmount), providerName, maskedPhone, txn.ReferenceNumber),
			"bcamobile://transaction/"+txn.ID.String(),
			map[string]any{
				"transaction_id":   txn.ID.String(),
				"reference_number": txn.ReferenceNumber,
				"amount":           amount.StringFixed(2),
				"provider":         providerName,
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

func toProviderItems(providers []Provider) []ProviderItem {
	items := make([]ProviderItem, len(providers))
	for i, p := range providers {
		items[i] = ProviderItem{
			ID:            p.ID,
			Name:          p.Name,
			IsActive:      p.IsActive,
			MinAmount:     p.MinAmount.StringFixed(2),
			MaxAmount:     p.MaxAmount.StringFixed(2),
			AdminFee:      p.AdminFee.StringFixed(2),
			PresetAmounts: p.PresetAmounts,
		}
		if p.IconURL != nil {
			items[i].IconURL = *p.IconURL
		}
	}
	return items
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
