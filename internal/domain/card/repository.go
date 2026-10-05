package card

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CardRepository is the data access this domain needs. Implemented by
// repository/postgres.AccountCardRepo — the domain knows no SQL.
type CardRepository interface {
	// ListByUserID returns every card owned by userID, joined with the catalog
	// row that names the product, primary card first.
	//
	// A customer with no cards returns an empty slice and a nil error — never
	// a not-found. The screen has an empty state for exactly that case, and
	// answering 404 would make it render an error instead.
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]Card, error)
}

// FindOwnedByID, UpdateSettings, Block and CreateReplacement are the write
// path. Every one of them is scoped by user_id — a card id arrives from the
// request, and the executor must never act on a UUID it was merely handed.
// This is the same rule accounts.FindOwnedByID enforces on the money paths,
// where skipping it once let one customer debit another's account.
type CardWriter interface {
	// FindOwnedByID returns the card only if userID owns it. A card that exists
	// but belongs to someone else is reported exactly like one that does not
	// exist (apperr.CardNotFound): telling them apart would confirm that a
	// guessed card id is real.
	FindOwnedByID(ctx context.Context, userID, cardID uuid.UUID) (*Card, error)

	// UpdateSettings writes the two channel switches and the audit row in one
	// transaction.
	UpdateSettings(ctx context.Context, cardID uuid.UUID, settings CardSettings, audit AuditEntry) error

	// Block marks the card blocked and records why, with the audit row in the
	// same transaction.
	Block(ctx context.Context, cardID uuid.UUID, reason string, blockedAt time.Time, audit AuditEntry) error

	// FindReplacementByIdemKey returns an earlier request made with the same
	// idempotency key, or nil when there is none.
	//
	// It is consulted BEFORE the verification token is consumed, which is the
	// same order ExecuteTransfer uses: a retry must replay the stored answer,
	// and a token burned by the first attempt can never be presented again.
	FindReplacementByIdemKey(ctx context.Context, userID uuid.UUID, idemKey string) (*ReplacementRecord, error)

	// CreateReplacement inserts a replacement request, or returns the existing
	// one when this user has already used the same idempotency key. The
	// returned record carries Replayed=true in that case.
	//
	// It returns apperr.CardReplacementInProgress when another request for the
	// same card is still open — the partial unique index in migration 000021 is
	// what enforces that, so two concurrent requests cannot both pass a check
	// and both insert.
	CreateReplacement(ctx context.Context, rec ReplacementRecord, audit AuditEntry) (*ReplacementRecord, error)
}

// VerificationTokenConsumer burns a purpose-bound token issued by
// POST /auth/pin/verify. Implemented by transaction.Service, the same one
// account.Service uses for CHANGE_LIMIT.
type VerificationTokenConsumer interface {
	ConsumeVerificationToken(ctx context.Context, userID uuid.UUID, rawToken, purpose string) error
}
