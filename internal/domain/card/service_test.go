package card_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/card"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- Mock ---

type mockCardRepo struct {
	cards []card.Card
	err   error
}

func (r *mockCardRepo) ListByUserID(_ context.Context, userID uuid.UUID) ([]card.Card, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]card.Card, 0, len(r.cards))
	for _, c := range r.cards {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}

// wib matches the zone the service measures expiry in.
var wib = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic(err)
	}
	return loc
}()

func newService(cards []card.Card, now time.Time) *card.Service {
	return card.NewService(card.ServiceConfig{
		Cards: &mockCardRepo{cards: cards},
		Now:   func() time.Time { return now },
	})
}

func sampleCard(userID uuid.UUID) card.Card {
	return card.Card{
		ID:                   uuid.New(),
		UserID:               userID,
		AccountID:            uuid.New(),
		CardType:             "PASPOR_GOLD",
		MaskedNumber:         "•••• •••• •••• 7890",
		CardholderName:       "BUDI SANTOSO",
		ValidThruMonth:       8,
		ValidThruYear:        2029,
		Status:               card.StatusActive,
		DebitOnlineEnabled:   true,
		InternationalEnabled: false,
		IsPrimary:            true,
		ProductName:          "Gold Mastercard",
		Network:              "MASTERCARD",
		TierKey:              "DEBIT",
		Style:                "GOLD",
		FeeCardReplacement:   25000,

		// Katalog Gold di migrasi 000022: 3–7 hari, bisa diambil di cabang.
		DeliveryDaysMin:       3,
		DeliveryDaysMax:       7,
		BranchPickupAvailable: true,
	}
}

// A customer without cards must receive [] — the screen's empty state depends
// on it. A nil slice would marshal to null and the client would treat the
// response as malformed.
func TestListCards_EmptyIsArrayNotNull(t *testing.T) {
	svc := newService(nil, time.Now())

	got, err := svc.ListCards(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cards == nil {
		t.Fatal("expected non-nil slice so JSON renders [], got nil")
	}
	if len(got.Cards) != 0 {
		t.Fatalf("expected 0 cards, got %d", len(got.Cards))
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(body) != `{"cards":[]}` {
		t.Fatalf("expected {\"cards\":[]}, got %s", body)
	}
}

func TestListCards_ShapeAndValidThru(t *testing.T) {
	userID := uuid.New()
	svc := newService([]card.Card{sampleCard(userID)}, time.Date(2026, 9, 25, 12, 0, 0, 0, wib))

	got, err := svc.ListCards(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got.Cards))
	}

	c := got.Cards[0]
	if c.ValidThru != "08/29" {
		t.Errorf("valid_thru: expected 08/29, got %q", c.ValidThru)
	}
	if c.Status != card.StatusActive {
		t.Errorf("status: expected ACTIVE, got %q", c.Status)
	}
	if c.ProductName != "Gold Mastercard" || c.Network != "MASTERCARD" || c.Style != "GOLD" {
		t.Errorf("catalog fields not carried through: %+v", c)
	}
	if !c.Settings.DebitOnlineEnabled || c.Settings.InternationalEnabled {
		t.Errorf("settings mismapped: %+v", c.Settings)
	}
	// blocked_reason must be absent for a live card, not an empty string.
	if c.BlockedReason != nil {
		t.Errorf("expected no blocked_reason on an active card, got %q", *c.BlockedReason)
	}
}

// Month 12 must render as "12", not "0" — a single-digit pad bug here would
// print the expiry wrong on every December card.
func TestListCards_ValidThruDoesNotDropDoubleDigitMonth(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.ValidThruMonth = 12
	c.ValidThruYear = 2030
	svc := newService([]card.Card{c}, time.Date(2026, 9, 25, 12, 0, 0, 0, wib))

	got, _ := svc.ListCards(context.Background(), userID)
	if got.Cards[0].ValidThru != "12/30" {
		t.Fatalf("expected 12/30, got %q", got.Cards[0].ValidThru)
	}
}

// Nothing in this service flips a stored status to EXPIRED, so the read path
// has to. Otherwise a card that lapsed in 2024 keeps rendering as the green
// "Aktif & Terhubung" chip.
func TestListCards_LapsedCardIsNotReportedActive(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.ValidThruMonth, c.ValidThruYear = 12, 2024
	c.Status = card.StatusActive

	svc := newService([]card.Card{c}, time.Date(2026, 9, 25, 12, 0, 0, 0, wib))

	got, _ := svc.ListCards(context.Background(), userID)
	if got.Cards[0].Status != card.StatusExpired {
		t.Fatalf("expected EXPIRED for a card valid thru 12/24, got %q", got.Cards[0].Status)
	}
}

// A card reported lost must keep saying BLOCKED even after it lapses: that is
// the state the customer acted on, and the reason belongs in the response.
func TestListCards_BlockedWinsOverExpiry(t *testing.T) {
	userID := uuid.New()
	reason := card.BlockReasonLost
	blockedAt := time.Date(2025, 1, 2, 0, 0, 0, 0, wib)

	c := sampleCard(userID)
	c.ValidThruMonth, c.ValidThruYear = 12, 2024
	c.Status = card.StatusBlocked
	c.BlockedReason = &reason
	c.BlockedAt = &blockedAt

	svc := newService([]card.Card{c}, time.Date(2026, 9, 25, 12, 0, 0, 0, wib))

	got, _ := svc.ListCards(context.Background(), userID)
	if got.Cards[0].Status != card.StatusBlocked {
		t.Fatalf("expected BLOCKED to win over expiry, got %q", got.Cards[0].Status)
	}
	if got.Cards[0].BlockedReason == nil || *got.Cards[0].BlockedReason != card.BlockReasonLost {
		t.Fatalf("expected blocked_reason LOST, got %v", got.Cards[0].BlockedReason)
	}
}

// A card marked 12/28 is good through the LAST day of December 2028, measured
// in Jakarta. Off-by-one here would kill a card a month early, or keep a dead
// one alive for a month.
func TestCard_ExpiryBoundaryIsEndOfMonthInWIB(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.ValidThruMonth, c.ValidThruYear = 12, 2028

	cases := []struct {
		name        string
		now         time.Time
		wantExpired bool
	}{
		{"awal bulan berlaku", time.Date(2028, 12, 1, 0, 0, 0, 0, wib), false},
		{"detik terakhir bulan berlaku", time.Date(2028, 12, 31, 23, 59, 59, 0, wib), false},
		{"tengah malam pertama sesudahnya", time.Date(2029, 1, 1, 0, 0, 0, 0, wib), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.IsExpired(tc.now); got != tc.wantExpired {
				t.Fatalf("IsExpired(%s) = %v, mau %v", tc.now.Format(time.RFC3339), got, tc.wantExpired)
			}
		})
	}
}

// The WIB boundary is not decoration: 2029-01-01 00:30 in Jakarta is still
// 2028-12-31 17:30 UTC. A card checked against the UTC clock would look alive
// for another seven hours.
func TestCard_ExpiryUsesJakartaNotUTC(t *testing.T) {
	c := sampleCard(uuid.New())
	c.ValidThruMonth, c.ValidThruYear = 12, 2028

	justAfterMidnightJakarta := time.Date(2029, 1, 1, 0, 30, 0, 0, wib)
	if !c.IsExpired(justAfterMidnightJakarta) {
		t.Fatal("card should be expired at 00:30 WIB on 2029-01-01")
	}
	if justAfterMidnightJakarta.UTC().Year() != 2028 {
		t.Fatalf("test premise broken: expected the same instant to still be 2028 in UTC, got %s",
			justAfterMidnightJakarta.UTC())
	}
}

// Cards belonging to other users must not leak through the list path.
func TestListCards_OnlyOwnCards(t *testing.T) {
	mine, theirs := uuid.New(), uuid.New()
	svc := newService([]card.Card{sampleCard(mine), sampleCard(theirs)},
		time.Date(2026, 9, 25, 12, 0, 0, 0, wib))

	got, err := svc.ListCards(context.Background(), mine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Cards) != 1 {
		t.Fatalf("expected only the caller's card, got %d", len(got.Cards))
	}
}

func TestListCards_RepoErrorPropagates(t *testing.T) {
	svc := card.NewService(card.ServiceConfig{
		Cards: &mockCardRepo{err: errors.New("boom")},
	})

	if _, err := svc.ListCards(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected the repository error to propagate")
	}
}

// --- Mock jalur tulis ---

type mockCardWriter struct {
	card *card.Card

	findErr        error
	updateErr      error
	blockErr       error
	createErr      error
	existingReplay *card.ReplacementRecord

	// Recorded so a test can assert what the service decided, not just what it
	// answered.
	lastSettings card.CardSettings
	lastReason   string
	lastRecord   card.ReplacementRecord
	auditActions []string
	writes       int
}

func (w *mockCardWriter) FindOwnedByID(_ context.Context, userID, cardID uuid.UUID) (*card.Card, error) {
	if w.findErr != nil {
		return nil, w.findErr
	}
	if w.card == nil || w.card.ID != cardID || w.card.UserID != userID {
		return nil, apperr.CardNotFound
	}
	c := *w.card
	return &c, nil
}

func (w *mockCardWriter) UpdateSettings(_ context.Context, _ uuid.UUID, settings card.CardSettings, audit card.AuditEntry) error {
	if w.updateErr != nil {
		return w.updateErr
	}
	w.writes++
	w.lastSettings = settings
	w.auditActions = append(w.auditActions, audit.Action)
	return nil
}

func (w *mockCardWriter) Block(_ context.Context, _ uuid.UUID, reason string, _ time.Time, audit card.AuditEntry) error {
	if w.blockErr != nil {
		return w.blockErr
	}
	w.writes++
	w.lastReason = reason
	w.auditActions = append(w.auditActions, audit.Action)
	return nil
}

func (w *mockCardWriter) FindReplacementByIdemKey(_ context.Context, _ uuid.UUID, _ string) (*card.ReplacementRecord, error) {
	return w.existingReplay, nil
}

func (w *mockCardWriter) CreateReplacement(_ context.Context, rec card.ReplacementRecord, audit card.AuditEntry) (*card.ReplacementRecord, error) {
	if w.createErr != nil {
		return nil, w.createErr
	}
	w.writes++
	w.lastRecord = rec
	w.auditActions = append(w.auditActions, audit.Action)
	saved := rec
	saved.ID = uuid.New()
	return &saved, nil
}

type mockVTokens struct {
	err      error
	purposes []string
	calls    int
}

func (m *mockVTokens) ConsumeVerificationToken(_ context.Context, _ uuid.UUID, _, purpose string) error {
	m.calls++
	m.purposes = append(m.purposes, purpose)
	return m.err
}

func newWriteService(w *mockCardWriter, v *mockVTokens, now time.Time) *card.Service {
	return card.NewService(card.ServiceConfig{
		Cards:   &mockCardRepo{},
		Writer:  w,
		VTokens: v,
		Now:     func() time.Time { return now },
	})
}

func boolPtr(b bool) *bool { return &b }

// --- Settings ---

func TestUpdateSettings_OnlyTouchesFieldsThatWereSent(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.DebitOnlineEnabled = true
	c.InternationalEnabled = false

	w := &mockCardWriter{card: &c}
	svc := newWriteService(w, &mockVTokens{}, time.Now())

	resp, err := svc.UpdateSettings(context.Background(), userID, c.ID,
		card.UpdateSettingsRequest{InternationalEnabled: boolPtr(true)}, "203.0.113.7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Debit online was not mentioned, so it must survive untouched. A plain
	// bool would have reset it to false here.
	if !w.lastSettings.DebitOnlineEnabled {
		t.Errorf("an omitted field must keep its old value")
	}
	if !w.lastSettings.InternationalEnabled {
		t.Errorf("international_enabled should be on")
	}
	if !resp.Card.Settings.InternationalEnabled {
		t.Errorf("the response must show the new state, not the old one")
	}
	if len(w.auditActions) != 1 || w.auditActions[0] != card.AuditSettingsUpdated {
		t.Errorf("expected one CARD_SETTINGS_UPDATED audit row, got %v", w.auditActions)
	}
}

func TestUpdateSettings_EmptyBodyRejected(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	w := &mockCardWriter{card: &c}
	svc := newWriteService(w, &mockVTokens{}, time.Now())

	_, err := svc.UpdateSettings(context.Background(), userID, c.ID, card.UpdateSettingsRequest{}, "")
	if err == nil {
		t.Fatal("expected a validation error for a request that asks for nothing")
	}
	if w.writes != 0 {
		t.Errorf("nothing should have been written")
	}
}

// A blocked card cannot have its channels toggled: unblocking is a branch
// decision, and letting the switches move would suggest otherwise.
func TestUpdateSettings_BlockedCardRejected(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.Status = card.StatusBlocked
	w := &mockCardWriter{card: &c}
	svc := newWriteService(w, &mockVTokens{}, time.Now())

	_, err := svc.UpdateSettings(context.Background(), userID, c.ID,
		card.UpdateSettingsRequest{DebitOnlineEnabled: boolPtr(false)}, "")
	if apperr.From(err).Code != apperr.CardBlocked.Code {
		t.Fatalf("expected CARD_BLOCKED, got %v", err)
	}
	if w.writes != 0 {
		t.Errorf("nothing should have been written")
	}
}

// Setting a switch to the value it already holds writes nothing — the audit log
// is for changes, and a row recording no change is noise that hides the rest.
func TestUpdateSettings_NoOpWritesNothing(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.DebitOnlineEnabled = true
	w := &mockCardWriter{card: &c}
	svc := newWriteService(w, &mockVTokens{}, time.Now())

	if _, err := svc.UpdateSettings(context.Background(), userID, c.ID,
		card.UpdateSettingsRequest{DebitOnlineEnabled: boolPtr(true)}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.writes != 0 {
		t.Errorf("a no-op must not write, got %d writes", w.writes)
	}
}

// --- Block ---

func TestBlock_RequiresVerificationTokenWithBlockCardPurpose(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	w := &mockCardWriter{card: &c}
	v := &mockVTokens{}
	svc := newWriteService(w, v, time.Now())

	resp, err := svc.Block(context.Background(), userID, c.ID,
		card.BlockRequest{Reason: card.BlockReasonStolen, VerificationToken: "vt"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v.purposes) != 1 || v.purposes[0] != card.PurposeBlockCard {
		t.Fatalf("expected the token to be consumed with purpose BLOCK_CARD, got %v", v.purposes)
	}
	if resp.Card.Status != card.StatusBlocked {
		t.Errorf("expected the card to come back blocked")
	}
	if w.lastReason != card.BlockReasonStolen {
		t.Errorf("expected STOLEN to reach the repository, got %q", w.lastReason)
	}
}

func TestBlock_WithoutTokenIsRejectedBeforeAnyLookup(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	w := &mockCardWriter{card: &c}
	v := &mockVTokens{}
	svc := newWriteService(w, v, time.Now())

	_, err := svc.Block(context.Background(), userID, c.ID,
		card.BlockRequest{Reason: card.BlockReasonLost}, "")
	if err == nil {
		t.Fatal("expected a validation error without a verification token")
	}
	if v.calls != 0 || w.writes != 0 {
		t.Errorf("nothing should have been consumed or written")
	}
}

func TestBlock_UnknownReasonRejected(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	svc := newWriteService(&mockCardWriter{card: &c}, &mockVTokens{}, time.Now())

	if _, err := svc.Block(context.Background(), userID, c.ID,
		card.BlockRequest{Reason: "HILANG", VerificationToken: "vt"}, ""); err == nil {
		t.Fatal("expected an unknown reason to be rejected")
	}
}

// Blocking an already blocked card answers with the same state. A customer
// tapping twice in a panic must not be told something went wrong.
func TestBlock_AlreadyBlockedIsIdempotent(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.Status = card.StatusBlocked
	reason := card.BlockReasonLost
	c.BlockedReason = &reason

	w := &mockCardWriter{card: &c}
	svc := newWriteService(w, &mockVTokens{}, time.Now())

	resp, err := svc.Block(context.Background(), userID, c.ID,
		card.BlockRequest{Reason: card.BlockReasonStolen, VerificationToken: "vt"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Card.Status != card.StatusBlocked {
		t.Errorf("expected BLOCKED")
	}
	if w.writes != 0 {
		t.Errorf("an already blocked card must not be written again")
	}
}

// A card id belonging to somebody else is reported as not found, and the token
// is never consumed for it.
func TestBlock_OtherCustomersCardIsNotFound(t *testing.T) {
	owner := uuid.New()
	c := sampleCard(owner)
	w := &mockCardWriter{card: &c}
	v := &mockVTokens{}
	svc := newWriteService(w, v, time.Now())

	_, err := svc.Block(context.Background(), uuid.New(), c.ID,
		card.BlockRequest{Reason: card.BlockReasonLost, VerificationToken: "vt"}, "")
	if apperr.From(err).Code != apperr.CardNotFound.Code {
		t.Fatalf("expected CARD_NOT_FOUND, got %v", err)
	}
	if v.calls != 0 {
		t.Errorf("no token should be burned for a card the caller does not own")
	}
}

// --- Replacement ---

func TestRequestReplacement_FreezesFeeAndComputesArrival(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	c.FeeCardReplacement = 50000
	c.DeliveryDaysMin = 5
	c.DeliveryDaysMax = 10

	w := &mockCardWriter{card: &c}
	v := &mockVTokens{}
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, wib)
	svc := newWriteService(w, v, now)

	resp, replayed, err := svc.RequestReplacement(context.Background(), userID, c.ID,
		card.ReplacementRequest{
			Reason:            card.ReplacementReasonDamaged,
			DeliveryMethod:    card.DeliveryCourier,
			VerificationToken: "vt",
			IdempotencyKey:    "idem-1",
		}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if replayed {
		t.Errorf("a first request is not a replay")
	}
	if len(v.purposes) != 1 || v.purposes[0] != card.PurposeReplaceCard {
		t.Fatalf("expected purpose REPLACE_CARD, got %v", v.purposes)
	}
	// The fee is copied from the catalog now, so a later catalog change cannot
	// restate what the customer was told.
	if resp.Fee != 50000 {
		t.Errorf("expected the catalog fee 50000, got %d", resp.Fee)
	}
	if resp.EstimatedArrivalFrom != "2026-09-30" || resp.EstimatedArrivalTo != "2026-10-05" {
		t.Errorf("arrival window should follow the catalog's 5–10 days, got %s..%s",
			resp.EstimatedArrivalFrom, resp.EstimatedArrivalTo)
	}
}

// A retry with the same key replays the stored answer WITHOUT consuming a
// token: the first attempt already burned it, and a replay that demanded a
// fresh one could never succeed.
func TestRequestReplacement_ReplayDoesNotConsumeToken(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	existing := card.ReplacementRecord{
		ID:                   uuid.New(),
		CardID:               c.ID,
		UserID:               userID,
		Reason:               card.ReplacementReasonLost,
		DeliveryMethod:       card.DeliveryCourier,
		Fee:                  25000,
		Status:               card.ReplacementStatusNew,
		EstimatedArrivalFrom: time.Date(2026, 9, 28, 0, 0, 0, 0, wib),
		EstimatedArrivalTo:   time.Date(2026, 10, 2, 0, 0, 0, 0, wib),
	}
	w := &mockCardWriter{card: &c, existingReplay: &existing}
	v := &mockVTokens{err: errors.New("token already used")}
	svc := newWriteService(w, v, time.Now())

	resp, replayed, err := svc.RequestReplacement(context.Background(), userID, c.ID,
		card.ReplacementRequest{
			Reason:            card.ReplacementReasonLost,
			VerificationToken: "vt-already-burned",
			IdempotencyKey:    "idem-1",
		}, "")
	if err != nil {
		t.Fatalf("a replay must succeed, got %v", err)
	}
	if !replayed {
		t.Errorf("expected the response to be marked as a replay")
	}
	if v.calls != 0 {
		t.Errorf("a replay must not touch the verification token")
	}
	if w.writes != 0 {
		t.Errorf("a replay must not write a second request")
	}
	if resp.RequestID != existing.ID.String() {
		t.Errorf("a replay must return the original request id")
	}
}

func TestRequestReplacement_MissingIdempotencyKeyRejected(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	w := &mockCardWriter{card: &c}
	v := &mockVTokens{}
	svc := newWriteService(w, v, time.Now())

	_, _, err := svc.RequestReplacement(context.Background(), userID, c.ID,
		card.ReplacementRequest{
			Reason:            card.ReplacementReasonUpgrade,
			VerificationToken: "vt",
		}, "")
	if err == nil {
		t.Fatal("expected a request without X-Idempotency-Key to be rejected")
	}
	if v.calls != 0 || w.writes != 0 {
		t.Errorf("nothing should have been consumed or written")
	}
}

// Branch pickup is refused when the catalog says this card type cannot be
// collected at a branch — otherwise the request is one nobody can fulfil.
func TestRequestReplacement_BranchPickupUnavailable(t *testing.T) {
	userID := uuid.New()
	c := sampleCard(userID)
	// Katalog untuk kartu ini menutup pengambilan di cabang.
	c.BranchPickupAvailable = false
	w := &mockCardWriter{card: &c}
	svc := newWriteService(w, &mockVTokens{}, time.Now())

	_, _, err := svc.RequestReplacement(context.Background(), userID, c.ID,
		card.ReplacementRequest{
			Reason:            card.ReplacementReasonDamaged,
			DeliveryMethod:    card.DeliveryBranchPickup,
			VerificationToken: "vt",
			IdempotencyKey:    "idem-2",
		}, "")
	if apperr.From(err).Code != apperr.CardDeliveryUnavailable.Code {
		t.Fatalf("expected CARD_DELIVERY_UNAVAILABLE, got %v", err)
	}
}
