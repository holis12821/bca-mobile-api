package card

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type Service struct {
	cards CardRepository

	// writer is the same Postgres repo as cards, kept as a separate field so
	// that a caller wiring only the read path cannot accidentally expose the
	// write endpoints with a nil repository.
	writer CardWriter

	vtokens VerificationTokenConsumer

	// now is the clock. Injectable because the expiry boundary is behaviour
	// worth testing, and a test cannot wait for December 2029 to arrive.
	now func() time.Time
}

type ServiceConfig struct {
	Cards  CardRepository
	Writer CardWriter

	// VTokens consumes the BLOCK_CARD and REPLACE_CARD verification tokens.
	VTokens VerificationTokenConsumer

	// Now overrides the clock. Leave nil in production wiring.
	Now func() time.Time
}

func NewService(cfg ServiceConfig) *Service {
	clock := cfg.Now
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		cards:   cfg.Cards,
		writer:  cfg.Writer,
		vtokens: cfg.VTokens,
		now:     clock,
	}
}

// SetVerificationTokenConsumer wires the token consumer after construction.
//
// Same shape as account.Service: transaction.Service is built later in the
// router than this one, and reordering the whole wiring block to satisfy one
// dependency is how that block turns into something nobody dares touch.
func (s *Service) SetVerificationTokenConsumer(c VerificationTokenConsumer) {
	s.vtokens = c
}

// ListCards serves GET /v1/account/cards.
//
// Every card returned belongs to userID because the query filters on it — the
// caller never supplies a card id here, so there is no ownership check to
// forget. Endpoints that DO take a card id from the request (settings, block,
// replacement) must look it up scoped by user; that is what
// accounts.FindOwnedByID exists for on the money paths.
func (s *Service) ListCards(ctx context.Context, userID uuid.UUID) (*CardListResponse, error) {
	cards, err := s.cards.ListByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}

	// make(..., 0, n) rather than a nil slice on purpose: encoding/json turns a
	// nil slice into `null`, and the client reads `cards` as a list. The empty
	// state depends on `[]` arriving.
	out := make([]CardResponse, 0, len(cards))
	now := s.now()
	for _, c := range cards {
		out = append(out, c.ToResponse(now))
	}

	return &CardListResponse{Cards: out}, nil
}

// UpdateSettings serves PUT /v1/account/cards/{card_id}/settings.
//
// No verification token: both switches are things the customer can undo
// themselves, and demanding a PIN to turn off overseas purchases would train
// people to enter their PIN for trivia.
func (s *Service) UpdateSettings(ctx context.Context, userID, cardID uuid.UUID, req UpdateSettingsRequest, ip string) (*CardDetailResponse, error) {
	if req.IsEmpty() {
		// Nothing asked for. Answering 200 would tell the screen a write
		// happened when the body was empty or misspelled — the same trap that
		// made PUT /account/settings reject an all-nil request.
		return nil, apperr.ValidationError
	}
	if s.writer == nil {
		return nil, apperr.InternalError
	}

	c, err := s.writer.FindOwnedByID(ctx, userID, cardID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	if c.EffectiveStatus(now) == StatusBlocked {
		return nil, apperr.CardBlocked
	}

	current := CardSettings{
		DebitOnlineEnabled:   c.DebitOnlineEnabled,
		InternationalEnabled: c.InternationalEnabled,
	}
	next := req.Apply(current)
	if next == current {
		// Idempotent by nature: the switches already read that way. Skipping
		// the write keeps the audit log free of rows that record no change.
		resp := c.ToResponse(now)
		return &CardDetailResponse{Card: resp}, nil
	}

	audit := AuditEntry{
		CardID:   c.ID,
		UserID:   userID,
		Action:   AuditSettingsUpdated,
		OldValue: current,
		NewValue: next,
		IP:       ip,
	}
	if err := s.writer.UpdateSettings(ctx, c.ID, next, audit); err != nil {
		return nil, fmt.Errorf("update card settings: %w", err)
	}

	c.DebitOnlineEnabled = next.DebitOnlineEnabled
	c.InternationalEnabled = next.InternationalEnabled
	resp := c.ToResponse(now)
	return &CardDetailResponse{Card: resp}, nil
}

// Block serves POST /v1/account/cards/{card_id}/block.
//
// Unblocking is deliberately absent. Reinstating a card reported lost is a
// branch decision, not a button in the app.
func (s *Service) Block(ctx context.Context, userID, cardID uuid.UUID, req BlockRequest, ip string) (*CardDetailResponse, error) {
	if !IsValidBlockReason(req.Reason) {
		return nil, apperr.ValidationError
	}
	if req.VerificationToken == "" {
		return nil, apperr.ValidationError
	}
	if s.writer == nil || s.vtokens == nil {
		return nil, apperr.InternalError
	}

	c, err := s.writer.FindOwnedByID(ctx, userID, cardID)
	if err != nil {
		return nil, err
	}

	// The token is consumed even when the card turns out to be blocked
	// already. Verifying first and asking questions second is what makes
	// "token required" a property of the endpoint rather than of one branch.
	if err := s.vtokens.ConsumeVerificationToken(ctx, userID, req.VerificationToken, PurposeBlockCard); err != nil {
		return nil, err
	}

	now := s.now()
	if c.Status == StatusBlocked {
		// Already blocked: answer with the same state, not an error. A customer
		// tapping twice in a panic must not be told something went wrong.
		resp := c.ToResponse(now)
		return &CardDetailResponse{Card: resp}, nil
	}

	audit := AuditEntry{
		CardID:   c.ID,
		UserID:   userID,
		Action:   AuditCardBlocked,
		OldValue: map[string]any{"status": c.Status},
		NewValue: map[string]any{"status": StatusBlocked, "blocked_reason": req.Reason},
		IP:       ip,
	}
	if err := s.writer.Block(ctx, c.ID, req.Reason, now, audit); err != nil {
		return nil, fmt.Errorf("block card: %w", err)
	}

	c.Status = StatusBlocked
	c.BlockedReason = &req.Reason
	c.BlockedAt = &now
	resp := c.ToResponse(now)
	return &CardDetailResponse{Card: resp}, nil
}

// RequestReplacement serves POST /v1/account/cards/{card_id}/replacement.
//
// Returns the response, whether it was replayed from an earlier identical
// request, and an error.
func (s *Service) RequestReplacement(ctx context.Context, userID, cardID uuid.UUID, req ReplacementRequest, ip string) (*ReplacementResponse, bool, error) {
	if !IsValidReplacementReason(req.Reason) {
		return nil, false, apperr.ValidationError
	}
	if req.DeliveryMethod == "" {
		req.DeliveryMethod = DeliveryCourier
	}
	if !IsValidDeliveryMethod(req.DeliveryMethod) {
		return nil, false, apperr.ValidationError
	}
	if req.VerificationToken == "" {
		return nil, false, apperr.ValidationError
	}
	if req.IdempotencyKey == "" {
		// Mandatory, exactly as on /transfer/execute: a replacement is charged,
		// and a retried request without a key would print and bill a second
		// card.
		return nil, false, apperr.Error{
			Status:  apperr.ValidationError.Status,
			Code:    apperr.ValidationError.Code,
			Message: apperr.ValidationError.Message,
			Details: map[string]any{"missing_header": "X-Idempotency-Key"},
		}
	}
	if s.writer == nil || s.vtokens == nil {
		return nil, false, apperr.InternalError
	}

	c, err := s.writer.FindOwnedByID(ctx, userID, cardID)
	if err != nil {
		return nil, false, err
	}
	if req.DeliveryMethod == DeliveryBranchPickup && !c.BranchPickupAvailable {
		return nil, false, apperr.CardDeliveryUnavailable
	}

	// Replay check BEFORE the token is consumed. The first attempt burned the
	// token; a retry carrying the same idempotency key must still get its
	// answer instead of a token error for a request that already succeeded.
	if existing, err := s.writer.FindReplacementByIdemKey(ctx, userID, req.IdempotencyKey); err != nil {
		return nil, false, fmt.Errorf("lookup replacement idempotency: %w", err)
	} else if existing != nil {
		resp := existing.ToResponse()
		return &resp, true, nil
	}

	if err := s.vtokens.ConsumeVerificationToken(ctx, userID, req.VerificationToken, PurposeReplaceCard); err != nil {
		return nil, false, err
	}

	now := s.now()
	from, to := estimatedArrival(now, c.DeliveryDaysMin, c.DeliveryDaysMax)

	rec := ReplacementRecord{
		CardID:         c.ID,
		UserID:         userID,
		IdempotencyKey: req.IdempotencyKey,
		Reason:         req.Reason,
		DeliveryMethod: req.DeliveryMethod,

		// Frozen from the catalog now. Re-reading the fee later would let a
		// catalog change restate what the customer was told they would pay.
		Fee:                  c.FeeCardReplacement,
		Status:               ReplacementStatusNew,
		MaskedNumber:         c.MaskedNumber,
		EstimatedArrivalFrom: from,
		EstimatedArrivalTo:   to,
	}

	audit := AuditEntry{
		CardID: c.ID,
		UserID: userID,
		Action: AuditReplacementRequested,
		OldValue: map[string]any{
			"status": c.Status,
		},
		NewValue: map[string]any{
			"status":          StatusReplacementPending,
			"reason":          req.Reason,
			"delivery_method": req.DeliveryMethod,
			"fee":             c.FeeCardReplacement,
		},
		IP: ip,
	}

	saved, err := s.writer.CreateReplacement(ctx, rec, audit)
	if err != nil {
		return nil, false, err
	}
	resp := saved.ToResponse()
	return &resp, saved.Replayed, nil
}

// estimatedArrival turns the catalog's delivery window into two WIB dates.
//
// Falls back to 3–7 days when the catalog leaves the window unset, so a card
// type added without one still answers with a range the screen can print
// rather than 01 January year one.
func estimatedArrival(now time.Time, minDays, maxDays int) (time.Time, time.Time) {
	if minDays <= 0 {
		minDays = 3
	}
	if maxDays < minDays {
		maxDays = minDays + 4
	}
	base := now.In(wib)
	day := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, wib)
	return day.AddDate(0, 0, minDays), day.AddDate(0, 0, maxDays)
}
