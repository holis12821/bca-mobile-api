package transaction

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

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

var wib = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic("failed to load Asia/Jakarta: " + err.Error())
	}
	return loc
}()

// postCommitTimeout caps follow-up work that runs after money has already moved.
const postCommitTimeout = 5 * time.Second

// PostCommitContext returns a context for work that must still finish when the
// caller's request is already gone.
//
// Everything after COMMIT — cache invalidation, marking the inquiry and the
// verification token used, storing the idempotent response — used to run on the
// request context. A nasabah on mobile data whose signal dropped the instant the
// transfer committed (or any request that reached the 30s timeout) therefore
// left the balance cache stale and the idempotent response unwritten, so the
// app's retry met IDEMPOTENCY_CONFLICT for the next 30 seconds on a transfer
// that had in fact succeeded. Money that has moved is a fact; recording it must
// not depend on the client still listening.
//
// The same applies to the failure path: a cancelled context meant the
// idempotency key was never released, so a retry of a transfer that never
// happened was rejected too.
func PostCommitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), postCommitTimeout)
}

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

	// On ANY failure below, release the idempotency key and hand the nasabah
	// back their inquiry.
	//
	// The verification token and the inquiry are consumed (GETDEL) before the
	// database transaction runs, which is correct for double-spend safety but
	// meant that a perfectly ordinary rejection — insufficient balance, daily
	// limit — burned both. The app then had to walk the customer through the
	// destination screen and the PIN prompt again to retry a transfer that had
	// never happened. restoreOnFailure puts them back when, and only when, no
	// transaction was written.
	releaseIdem := true
	var restoreInquiry *Inquiry
	restoreVToken := ""
	defer func() {
		if !releaseIdem {
			return
		}
		// Detached: the most common reason to land here with a cancelled
		// context is the client giving up, and that is exactly when the key
		// must be released so their retry is not met with a 409.
		undoCtx, cancelUndo := PostCommitContext(ctx)
		defer cancelUndo()

		if s.idemStore != nil {
			if err := s.idemStore.Release(undoCtx, userID.String(), idemKey); err != nil {
				slog.Error("release idempotency key failed", "error", err)
			}
		}
		if restoreInquiry != nil && s.inquiryCache != nil && time.Now().Before(restoreInquiry.ExpiresAt) {
			if err := s.inquiryCache.StoreInquiry(undoCtx, restoreInquiry); err != nil {
				slog.Warn("restore inquiry after failed transfer failed", "error", err)
			}
		}
		if restoreVToken != "" && s.vtokenCache != nil {
			if err := s.vtokenCache.StoreToken(undoCtx, restoreVToken, userID.String(), "TRANSFER"); err != nil {
				slog.Warn("restore verification token after failed transfer failed", "error", err)
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

	restoreVToken = tokenHash

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

	restoreInquiry = inquiry

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

	// Step 5: Source account ownership + self-transfer check.
	//
	// source_account_id arrives in the request body and the executor debits it
	// under FOR UPDATE without any user filter, so this is the only thing
	// standing between a logged-in user and someone else's balance.
	sourceAccountID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		return nil, false, apperr.ValidationError
	}

	sourceAccount, err := s.accounts.FindOwnedByID(ctx, userID, sourceAccountID)
	if err != nil {
		return nil, false, fmt.Errorf("resolve source account: %w", err)
	}
	if sourceAccount == nil {
		return nil, false, apperr.SourceAccountForbidden
	}
	if sourceAccount.AccountNumber == inquiry.DestinationAccount {
		return nil, false, apperr.SelfTransfer
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

	// The transfer is committed. Everything below runs on a detached context so
	// a client that hung up cannot leave the caches stale or the idempotent
	// response unwritten. See PostCommitContext.
	postCtx, cancelPost := PostCommitContext(ctx)
	defer cancelPost()

	// Step 15: Cache invalidation for EVERY affected party
	parties := []AffectedParty{
		{UserID: userID.String(), AccountID: sourceAccountID.String()},
	}
	// For internal transfers, also invalidate the destination
	if txn.DestinationAccountID != nil {
		destAcct, err := s.accounts.FindByAccountNumber(postCtx, inquiry.DestinationAccount)
		if err == nil && destAcct != nil {
			parties = append(parties, AffectedParty{
				UserID:    destAcct.UserID.String(),
				AccountID: destAcct.ID.String(),
			})
		}
	}

	if s.cacheInvalidator != nil {
		if err := s.cacheInvalidator.InvalidateTransactionCaches(postCtx, parties); err != nil {
			slog.Error("cache invalidation failed", "error", err)
		}
	}

	// Build response — the source account was already resolved above.
	sourceName := sourceAccount.AccountLabel
	sourceNumber := sourceAccount.AccountNumber

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
		Notes:     req.Notes,
		CreatedAt: txn.CreatedAt.UTC().Format(time.RFC3339),
	}

	// Step 16: Persist idempotent response.
	// The transfer is committed: stop restoring, and only now mark the inquiry
	// and the token used in Postgres, which is the audit record of what was
	// actually spent.
	releaseIdem = false
	if err := s.vtokens.MarkUsed(postCtx, tokenHash); err != nil {
		slog.Warn("mark vtoken used in pg failed", "error", err)
	}
	if _, err := s.inquiries.MarkUsed(postCtx, inquiry.ID); err != nil {
		slog.Warn("mark inquiry used in pg failed", "error", err)
	}
	if s.idemStore != nil {
		respJSON, err := json.Marshal(resp)
		if err != nil {
			slog.Error("marshal idempotent response failed", "error", err)
		} else if err := s.idemStore.Persist(postCtx, userID.String(), idemKey, string(respJSON)); err != nil {
			slog.Error("persist idempotent response failed", "error", err)
		}
	}

	// Notify the nasabah. Best effort — a notification must never fail a
	// transfer that has already committed.
	if s.notifier != nil {
		s.notifier.Transaction(postCtx, userID,
			"Transfer berhasil",
			fmt.Sprintf("Transfer %s ke %s berhasil. Ref: %s",
				formatIDR(totalAmount), destName, txn.ReferenceNumber),
			"bcamobile://transaction/"+txn.ID.String(),
			map[string]any{
				"transaction_id":   txn.ID.String(),
				"reference_number": txn.ReferenceNumber,
				"amount":           amount.StringFixed(2),
				"type":             txnType,
			},
		)
	}

	// Step 17: Upsert favorite (async-safe, best effort)
	if err := s.favorites.Upsert(postCtx, &FavoriteTransfer{
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

// formatIDR renders a decimal as "Rp1.234.567" — thousands separated with a
// dot, as Indonesian currency is written.
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
