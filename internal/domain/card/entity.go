// Package card serves the cards a customer OWNS — the "Manajemen Kartu Paspor"
// section of Profil Saya.
//
// This is deliberately separate from the card catalog in domain/onboarding,
// which answers "which card types can someone pick while opening an account".
// A card here has a lifecycle of its own (blocked, replaced, expired) that the
// catalog has no notion of. Folding it into domain/account was the alternative,
// and that service already carries profile, balance, dashboard, limits and the
// profile-change OTP.
package card

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Card status values. REPLACEMENT_PENDING is set while a replacement is in
// flight; the card still works until the new one is activated.
const (
	StatusActive             = "ACTIVE"
	StatusBlocked            = "BLOCKED"
	StatusExpired            = "EXPIRED"
	StatusReplacementPending = "REPLACEMENT_PENDING"
)

// Block reasons accepted by POST /account/cards/{id}/block.
const (
	BlockReasonLost           = "LOST"
	BlockReasonStolen         = "STOLEN"
	BlockReasonDamaged        = "DAMAGED"
	BlockReasonSuspectedFraud = "SUSPECTED_FRAUD"
)

// wib is the clock every "is this card still valid" question is answered in.
// A card expires at the end of its month in Jakarta, not in UTC.
var wib = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic("failed to load Asia/Jakarta: " + err.Error())
	}
	return loc
}()

// Card is one physical card owned by a customer.
//
// The last five fields are copied from card_products at read time, not stored
// on the card row: the catalog is the single source of truth for what a
// PASPOR_GOLD is called and what replacing one costs.
type Card struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	AccountID uuid.UUID

	CardType string

	// MaskedNumber is the only form of the card number this service ever holds.
	// The full PAN belongs to core banking (docs/04-SECURITY.md), and migration
	// 000021 enforces the masking with a CHECK constraint.
	MaskedNumber   string
	CardholderName string

	ValidThruMonth int
	ValidThruYear  int

	Status        string
	BlockedReason *string
	BlockedAt     *time.Time

	DebitOnlineEnabled   bool
	InternationalEnabled bool
	IsPrimary            bool

	CreatedAt time.Time
	UpdatedAt time.Time

	// From card_products.
	ProductName        string
	Network            string
	TierKey            string
	Style              string
	FeeCardReplacement int64

	// Delivery window promised for a replacement of this card type, also from
	// the catalog. Platinum is printed to order and takes longer, so the
	// estimate cannot be one constant shared by every card.
	DeliveryDaysMin int
	DeliveryDaysMax int

	// BranchPickupAvailable gates the BRANCH_PICKUP delivery method. Offering a
	// pickup the catalog says is unavailable produces a request nobody can
	// fulfil.
	BranchPickupAvailable bool
}

// ValidThru renders the expiry the way the screen prints it: MM/YY.
//
// Formatting lives here rather than in the handler so that both the card list
// and the replacement response cannot drift into two different spellings.
func (c Card) ValidThru() string {
	return fmt.Sprintf("%02d/%02d", c.ValidThruMonth, c.ValidThruYear%100)
}

// EffectiveStatus is the status the customer should be shown.
//
// It is NOT always the stored status. Nothing in this service flips a card to
// EXPIRED when its month passes — there is no scheduled job, and adding one
// would still leave a window where the row lies. Without this, a card valid
// thru 12/24 would keep rendering as the green "Aktif & Terhubung" chip
// forever.
//
// BLOCKED wins over expiry: a card reported lost must keep saying so, because
// that is the state the customer acted on.
func (c Card) EffectiveStatus(now time.Time) string {
	if c.Status == StatusBlocked {
		return StatusBlocked
	}
	if c.IsExpired(now) {
		return StatusExpired
	}
	return c.Status
}

// IsExpired reports whether the card is past the last day of its valid-thru
// month, measured in WIB. A card marked 12/28 is good through 2028-12-31.
func (c Card) IsExpired(now time.Time) bool {
	if c.ValidThruMonth < 1 || c.ValidThruMonth > 12 {
		// A row that violates the migration's CHECK should never reach here.
		// Treating it as expired is the safe direction: it hides a card rather
		// than offering a broken one.
		return true
	}
	// First instant of the month AFTER the valid-thru month.
	expiresAt := time.Date(c.ValidThruYear, time.Month(c.ValidThruMonth), 1, 0, 0, 0, 0, wib).
		AddDate(0, 1, 0)
	return !now.In(wib).Before(expiresAt)
}

// CardSettings carries the two channel switches on the screen.
type CardSettings struct {
	DebitOnlineEnabled   bool `json:"debit_online_enabled"`
	InternationalEnabled bool `json:"international_enabled"`
}

// CardResponse is one card as the Android client receives it.
//
// There is deliberately no colour, no hex value and no image URL: the client
// maps Style to its own design token. Same rule as the onboarding catalog.
type CardResponse struct {
	CardID         string       `json:"card_id"`
	MaskedNumber   string       `json:"masked_number"`
	CardholderName string       `json:"cardholder_name"`
	CardType       string       `json:"card_type"`
	ProductName    string       `json:"product_name"`
	Network        string       `json:"network"`
	TierKey        string       `json:"tier_key"`
	Style          string       `json:"style"`
	ValidThru      string       `json:"valid_thru"`
	Status         string       `json:"status"`
	IsPrimary      bool         `json:"is_primary"`
	Settings       CardSettings `json:"settings"`

	// BlockedReason is present only for a blocked card, so the screen can say
	// why instead of just showing a dead card.
	BlockedReason *string `json:"blocked_reason,omitempty"`
}

// CardListResponse is the body of GET /v1/account/cards.
type CardListResponse struct {
	Cards []CardResponse `json:"cards"`
}

// ToResponse projects a card onto the wire shape.
func (c Card) ToResponse(now time.Time) CardResponse {
	resp := CardResponse{
		CardID:         c.ID.String(),
		MaskedNumber:   c.MaskedNumber,
		CardholderName: c.CardholderName,
		CardType:       c.CardType,
		ProductName:    c.ProductName,
		Network:        c.Network,
		TierKey:        c.TierKey,
		Style:          c.Style,
		ValidThru:      c.ValidThru(),
		Status:         c.EffectiveStatus(now),
		IsPrimary:      c.IsPrimary,
		Settings: CardSettings{
			DebitOnlineEnabled:   c.DebitOnlineEnabled,
			InternationalEnabled: c.InternationalEnabled,
		},
	}
	if resp.Status == StatusBlocked {
		resp.BlockedReason = c.BlockedReason
	}
	return resp
}

// --- Permintaan yang mengubah kartu -------------------------------------
//
// Three endpoints write: settings, block, replacement. Only the first is a
// plain preference; the other two are security actions and carry a
// verification_token, the same control /account/transaction-limit uses.

// Replacement reasons accepted by POST /account/cards/{id}/replacement. UPGRADE
// is here and not in the block list: wanting a better card is not an incident.
const (
	ReplacementReasonDamaged = "DAMAGED"
	ReplacementReasonLost    = "LOST"
	ReplacementReasonUpgrade = "UPGRADE"
)

// Delivery methods, mirroring onboarding_card_issuance so a customer sees the
// same two choices whether the card comes from opening an account or from
// replacing one.
const (
	DeliveryCourier       = "COURIER"
	DeliveryBranchPickup  = "BRANCH_PICKUP"
	ReplacementStatusNew  = "REQUESTED"
	ReplacementStatusDone = "SHIPPED"
)

// Verification token purposes. Both are new; PIN verify issues them.
const (
	PurposeBlockCard   = "BLOCK_CARD"
	PurposeReplaceCard = "REPLACE_CARD"
)

// Audit actions, matching the CHECK constraint in migration 000021. A value
// that is not one of these three makes the INSERT fail, which is the point:
// the constraint is the list, and this is the copy the code reads.
const (
	AuditSettingsUpdated      = "CARD_SETTINGS_UPDATED"
	AuditCardBlocked          = "CARD_BLOCKED"
	AuditReplacementRequested = "CARD_REPLACEMENT_REQUESTED"
)

// IsValidBlockReason reports whether reason is one the screen offers.
func IsValidBlockReason(reason string) bool {
	switch reason {
	case BlockReasonLost, BlockReasonStolen, BlockReasonDamaged, BlockReasonSuspectedFraud:
		return true
	}
	return false
}

// IsValidReplacementReason reports whether reason is accepted by the
// replacement endpoint.
func IsValidReplacementReason(reason string) bool {
	switch reason {
	case ReplacementReasonDamaged, ReplacementReasonLost, ReplacementReasonUpgrade:
		return true
	}
	return false
}

// IsValidDeliveryMethod reports whether method is one of the two the card can
// arrive by.
func IsValidDeliveryMethod(method string) bool {
	return method == DeliveryCourier || method == DeliveryBranchPickup
}

// UpdateSettingsRequest is the body of PUT /account/cards/{card_id}/settings.
//
// Both fields are pointers so that "switch international off" is
// distinguishable from "do not touch international". A plain bool would make
// every partial update silently reset the other switch to false — the same
// trap PUT /account/settings already avoids this way.
type UpdateSettingsRequest struct {
	DebitOnlineEnabled   *bool `json:"debit_online_enabled"`
	InternationalEnabled *bool `json:"international_enabled"`
}

// Apply folds the request onto the card's current switches, leaving absent
// fields untouched, and reports the result.
func (r UpdateSettingsRequest) Apply(current CardSettings) CardSettings {
	next := current
	if r.DebitOnlineEnabled != nil {
		next.DebitOnlineEnabled = *r.DebitOnlineEnabled
	}
	if r.InternationalEnabled != nil {
		next.InternationalEnabled = *r.InternationalEnabled
	}
	return next
}

// IsEmpty reports whether the request asks for nothing at all.
func (r UpdateSettingsRequest) IsEmpty() bool {
	return r.DebitOnlineEnabled == nil && r.InternationalEnabled == nil
}

// BlockRequest is the body of POST /account/cards/{card_id}/block.
type BlockRequest struct {
	Reason string `json:"reason"`

	// VerificationToken is mandatory. Blocking a card is a security action, and
	// without it anyone holding an unlocked phone could kill the card.
	VerificationToken string `json:"verification_token"`
}

// ReplacementRequest is the body of POST /account/cards/{card_id}/replacement.
type ReplacementRequest struct {
	Reason            string `json:"reason"`
	DeliveryMethod    string `json:"delivery_method"`
	VerificationToken string `json:"verification_token"`

	// IdempotencyKey comes from the X-Idempotency-Key header, never from the
	// body. Replacing a card costs money (card_products.fee_card_replacement),
	// so a retried request must not print two cards.
	IdempotencyKey string `json:"-"`
}

// ReplacementRecord is one row of card_replacement_requests.
type ReplacementRecord struct {
	ID             uuid.UUID
	CardID         uuid.UUID
	UserID         uuid.UUID
	IdempotencyKey string
	Reason         string
	DeliveryMethod string
	Fee            int64
	Status         string
	MaskedNumber   string

	EstimatedArrivalFrom time.Time
	EstimatedArrivalTo   time.Time

	CreatedAt time.Time

	// Replayed is true when the row came back from an earlier identical request
	// rather than being inserted now. The response body is the same either way
	// — that is what idempotent means — but the service must not charge or
	// audit twice.
	Replayed bool
}

// ReplacementResponse is the body returned by the replacement endpoint.
type ReplacementResponse struct {
	RequestID      string `json:"request_id"`
	CardID         string `json:"card_id"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	DeliveryMethod string `json:"delivery_method"`

	// Fee in rupiah, taken from the catalog at request time and frozen onto the
	// row. A later catalog change must not silently restate what the customer
	// was told they would pay.
	Fee int64 `json:"fee"`

	EstimatedArrivalFrom string `json:"estimated_arrival_from"`
	EstimatedArrivalTo   string `json:"estimated_arrival_to"`
	MaskedNumber         string `json:"masked_number"`
}

// ToResponse projects a replacement row onto the wire shape. Dates are plain
// YYYY-MM-DD: the screen prints a range, not a timestamp.
func (r ReplacementRecord) ToResponse() ReplacementResponse {
	return ReplacementResponse{
		RequestID:            r.ID.String(),
		CardID:               r.CardID.String(),
		Status:               r.Status,
		Reason:               r.Reason,
		DeliveryMethod:       r.DeliveryMethod,
		Fee:                  r.Fee,
		EstimatedArrivalFrom: r.EstimatedArrivalFrom.Format("2006-01-02"),
		EstimatedArrivalTo:   r.EstimatedArrivalTo.Format("2006-01-02"),
		MaskedNumber:         r.MaskedNumber,
	}
}

// CardDetailResponse wraps a single card. Settings and block both answer with
// it so the client can repaint from the response instead of re-fetching the
// whole list.
type CardDetailResponse struct {
	Card CardResponse `json:"card"`
}

// AuditEntry is one row of account_card_audit_log, written in the same
// transaction as the change it records. Written separately it could be lost
// exactly when it matters: a status change with no trace of who made it.
type AuditEntry struct {
	CardID   uuid.UUID
	UserID   uuid.UUID
	Action   string
	OldValue any
	NewValue any
	IP       string
}
