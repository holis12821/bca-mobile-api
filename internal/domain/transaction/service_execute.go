package transaction

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

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

var wib = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic("failed to load Asia/Jakarta: " + err.Error())
	}
	return loc
}()

// ExecuteTransfer implements the 17-step transfer flow from §5.
func (s *Service) ExecuteTransfer(ctx context.Context, userID uuid.UUID, req ExecuteTransferRequest, idemKey string) (*ExecuteTransferResponse, bool, error) {
	// Step 1: Idempotency guard (§6)
	if s.idemStore != nil {
		result, err := s.idemStore.Claim(ctx, userID.String(), idemKey)
		if err != nil {
			return nil, false, fmt.Errorf("idempotency claim: %w", err)
		}
		if result.AlreadyClaimed {
			if result.StillProcessing {
				return nil, false, apperr.IdempotencyConflict
			}
			// Replay stored response
			var resp ExecuteTransferResponse
			if err := json.Unmarshal([]byte(result.StoredResponse), &resp); err != nil {
				return nil, false, fmt.Errorf("decode idempotent response: %w", err)
			}
			return &resp, true, nil
		}
	}

	// On ANY failure below, release the idempotency key.
	releaseIdem := true
	defer func() {
		if releaseIdem && s.idemStore != nil {
			if err := s.idemStore.Release(ctx, userID.String(), idemKey); err != nil {
				slog.Error("release idempotency key failed", "error", err)
			}
		}
	}()

	// Step 2: Consume verification_token (atomic, purpose-matched, user-matched)
	tokenHash := hashVToken(req.VerificationToken)
	vtokenUserID, vtokenPurpose, err := s.vtokenCache.ConsumeToken(ctx, tokenHash)
	if err != nil {
		return nil, false, fmt.Errorf("consume vtoken: %w", err)
	}
	if vtokenUserID == "" {
		return nil, false, apperr.VerificationTokenInvalid
	}
	if vtokenUserID != userID.String() {
		return nil, false, apperr.VerificationTokenInvalid
	}
	if vtokenPurpose != "TRANSFER" {
		return nil, false, apperr.VerificationTokenInvalid
	}

	// Mark used in PG (audit)
	if err := s.vtokens.MarkUsed(ctx, tokenHash); err != nil {
		slog.Warn("mark vtoken used in pg failed", "error", err)
	}

	// Step 3: Consume inquiry (atomic)
	inquiry, err := s.inquiryCache.ConsumeInquiry(ctx, req.InquiryID)
	if err != nil {
		return nil, false, fmt.Errorf("consume inquiry: %w", err)
	}
	if inquiry == nil {
		return nil, false, apperr.InquiryExpired
	}

	// Verify inquiry belongs to user
	if inquiry.UserID != userID {
		return nil, false, apperr.InquiryExpired
	}

	// Mark used in PG (audit)
	if _, err := s.inquiries.MarkUsed(ctx, inquiry.ID); err != nil {
		slog.Warn("mark inquiry used in pg failed", "error", err)
	}

	// Step 4: Cross-check body against inquiry
	if req.DestinationAccount != "" && req.DestinationAccount != inquiry.DestinationAccount {
		return nil, false, apperr.InquiryMismatch
	}
	if req.BankCode != "" && inquiry.BankCode != nil && req.BankCode != *inquiry.BankCode {
		return nil, false, apperr.InquiryMismatch
	}

	// Derive amount from inquiry or request
	var amount decimal.Decimal
	if inquiry.Amount != nil && !inquiry.Amount.IsZero() {
		amount = *inquiry.Amount
	} else if req.Amount > 0 {
		amount = decimal.NewFromInt(req.Amount)
	} else {
		return nil, false, apperr.ValidationError
	}

	adminFee := decimal.Zero
	if inquiry.AdminFee != nil {
		adminFee = *inquiry.AdminFee
	}
	totalAmount := amount.Add(adminFee)

	// Step 5: Self-transfer check
	sourceAccountID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		return nil, false, apperr.ValidationError
	}

	// Determine transfer type from inquiry
	transferType := "INTERNAL"
	if inquiry.BankCode != nil && *inquiry.BankCode != "014" {
		transferType = "EXTERNAL"
	}
	if req.TransferType != "" {
		if req.TransferType == "EXTERNAL" || req.TransferType == "VIRTUAL_ACCOUNT" {
			transferType = "EXTERNAL"
		}
	}

	txnType := TransferTypeTxnTypeMap[transferType]
	if txnType == "" {
		txnType = "TRANSFER_INTERNAL"
	}
	limitType := TransferTypeLimitMap[transferType]
	if limitType == "" {
		limitType = "TRANSFER_INTERNAL"
	}

	// WIB date from application — never CURRENT_DATE
	now := time.Now().In(wib)

	// Steps 6-14: SERIALIZABLE transaction (handled by TransferExecutor)
	params := ExecuteParams{
		IdempotencyKey:  idemKey,
		UserID:          userID,
		SourceAccountID: sourceAccountID,
		Inquiry:         inquiry,
		TransferType:    transferType,
		TxnType:         txnType,
		LimitType:       limitType,
		Amount:          amount,
		AdminFee:        adminFee,
		TotalAmount:     totalAmount,
		Notes:           req.Notes,
		WIBDate:         now,
		WIBTime:         now,
	}

	txn, err := s.executor.ExecuteTransfer(ctx, params)
	if err != nil {
		return nil, false, err
	}

	// Step 15: Cache invalidation for EVERY affected party
	parties := []AffectedParty{
		{UserID: userID.String(), AccountID: sourceAccountID.String()},
	}
	// For internal transfers, also invalidate the destination
	if txn.DestinationAccountID != nil {
		destAcct, err := s.accounts.FindByAccountNumber(ctx, inquiry.DestinationAccount)
		if err == nil && destAcct != nil {
			parties = append(parties, AffectedParty{
				UserID:    destAcct.UserID.String(),
				AccountID: destAcct.ID.String(),
			})
		}
	}

	if s.cacheInvalidator != nil {
		if err := s.cacheInvalidator.InvalidateTransactionCaches(ctx, parties); err != nil {
			slog.Error("cache invalidation failed", "error", err)
		}
	}

	// Build response
	sourceName := ""
	sourceNumber := ""
	srcAccounts, err := s.accounts.FindActiveByUserID(ctx, userID)
	if err == nil {
		for _, a := range srcAccounts {
			if a.ID == sourceAccountID {
				sourceNumber = a.AccountNumber
				sourceName = a.AccountLabel
				break
			}
		}
	}

	destName := inquiry.DestinationAccount
	if inquiry.DestinationName != nil {
		destName = *inquiry.DestinationName
	}
	destBank := "BCA"
	if inquiry.DestinationBank != nil {
		destBank = *inquiry.DestinationBank
	}

	resp := &ExecuteTransferResponse{
		TransactionID:   txn.ID.String(),
		ReferenceNumber: txn.ReferenceNumber,
		Status:          txn.Status,
		Amount:          amount.StringFixed(2),
		AdminFee:        adminFee.StringFixed(2),
		Total:           totalAmount.StringFixed(2),
		Source: TransferParty{
			AccountNumber: sourceNumber,
			Name:          sourceName,
		},
		Destination: TransferDestination{
			AccountNumber: inquiry.DestinationAccount,
			Name:          destName,
			Bank:          destBank,
		},
		Notes:    req.Notes,
		CreatedAt: txn.CreatedAt.UTC().Format(time.RFC3339),
	}

	// Step 16: Persist idempotent response
	releaseIdem = false // success — don't release
	if s.idemStore != nil {
		respJSON, err := json.Marshal(resp)
		if err != nil {
			slog.Error("marshal idempotent response failed", "error", err)
		} else if err := s.idemStore.Persist(ctx, userID.String(), idemKey, string(respJSON)); err != nil {
			slog.Error("persist idempotent response failed", "error", err)
		}
	}

	// Step 17: Upsert favorite (async-safe, best effort)
	if err := s.favorites.Upsert(ctx, &FavoriteTransfer{
		UserID:             userID,
		DestinationAccount: inquiry.DestinationAccount,
		DestinationName:    destName,
		DestinationBank:    destBank,
		BankCode:           derefStr(inquiry.BankCode, "014"),
		TransferType:       transferType,
	}); err != nil {
		slog.Warn("upsert favorite failed", "error", err)
	}

	return resp, false, nil
}

func hashVToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}

func derefStr(s *string, fallback string) string {
	if s != nil {
		return *s
	}
	return fallback
}