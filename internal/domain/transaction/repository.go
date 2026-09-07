package transaction

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// MutationRepository defines data access for account mutations.
type MutationRepository interface {
	// ListByAccountID returns keyset-paginated mutations for an account.
	// Cursor is (transaction_date, created_at, id). Returns limit+1 rows for has_more.
	ListByAccountID(ctx context.Context, accountID uuid.UUID, cursor *MutationCursorValues, limit int, period *DateRange) ([]Mutation, error)
}

// MutationCursorValues holds the decoded cursor fields for keyset pagination.
type MutationCursorValues struct {
	Date      time.Time
	CreatedAt time.Time
	ID        uuid.UUID
}

// DateRange represents a date range for filtering mutations.
type DateRange struct {
	From time.Time
	To   time.Time
}

// TransactionRepository defines data access for transactions.
type TransactionRepository interface {
	// ListByUserID returns keyset-paginated transaction history.
	// Optionally filtered by type.
	ListByUserID(ctx context.Context, userID uuid.UUID, txnType *string, cursor *HistoryCursorValues, limit int) ([]Transaction, error)

	// FindByID finds a transaction by ID, scoped to user_id.
	FindByID(ctx context.Context, userID, txnID uuid.UUID) (*Transaction, error)

	// FindByIdempotencyKey finds a transaction by user + idempotency key.
	FindByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (*Transaction, error)
}

// TransferExecutor runs the entire transfer inside a SERIALIZABLE transaction
// with retry on 40001. This is the PostgreSQL layer for the execute flow.
type TransferExecutor interface {
	// ExecuteTransfer runs the SERIALIZABLE transaction for a transfer.
	// Handles: lock accounts, check balance, check daily limit, update balances,
	// insert transaction + mutations, upsert daily_usage, upsert favorite.
	// Returns the created Transaction on success.
	ExecuteTransfer(ctx context.Context, params ExecuteParams) (*Transaction, error)
}

// ExecuteParams holds everything needed to execute a transfer inside the DB transaction.
type ExecuteParams struct {
	IdempotencyKey     string
	UserID             uuid.UUID
	SourceAccountID    uuid.UUID
	Inquiry            *Inquiry
	TransferType       string // INTERNAL, EXTERNAL, VIRTUAL_ACCOUNT
	TxnType            string // TRANSFER_INTERNAL, TRANSFER_EXTERNAL
	LimitType          string // TRANSFER_INTERNAL, TRANSFER_EXTERNAL
	Amount             decimal.Decimal
	AdminFee           decimal.Decimal
	TotalAmount        decimal.Decimal
	Notes              string
	WIBDate            time.Time // application-supplied WIB date
	WIBTime            time.Time // application-supplied WIB time
}

// LimitCheckResult is returned when a daily limit is exceeded.
type LimitCheckResult struct {
	DailyLimit decimal.Decimal
	UsedToday  decimal.Decimal
	Remaining  decimal.Decimal
}

// HistoryCursorValues holds the decoded cursor fields for history pagination.
type HistoryCursorValues struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// InquiryRepository defines data access for transfer inquiries.
type InquiryRepository interface {
	// Create inserts a new inquiry row.
	Create(ctx context.Context, inquiry *Inquiry) error

	// FindByID finds an inquiry by ID, scoped to user_id.
	FindByID(ctx context.Context, userID, inquiryID uuid.UUID) (*Inquiry, error)

	// MarkUsed marks the inquiry as used (sets used_at).
	// Returns false if already used or not found.
	MarkUsed(ctx context.Context, inquiryID uuid.UUID) (bool, error)
}

// VerificationTokenRepository defines data access for verification tokens in PostgreSQL (audit record).
type VerificationTokenRepository interface {
	// Create inserts a new verification token row.
	Create(ctx context.Context, token *VerificationToken) error

	// MarkUsed marks the token as used.
	MarkUsed(ctx context.Context, tokenHash string) error
}

// FavoriteTransferRepository defines data access for favorite transfers.
type FavoriteTransferRepository interface {
	// ListByUserID returns recent transfers for a user, ordered by last_transfer_at DESC.
	ListByUserID(ctx context.Context, userID uuid.UUID, limit int) ([]FavoriteTransfer, error)

	// Upsert inserts or updates a favorite transfer, incrementing transfer_count.
	Upsert(ctx context.Context, fav *FavoriteTransfer) error
}

// InquiryCache stores transfer inquiries in Redis (authoritative for liveness).
type InquiryCache interface {
	// StoreInquiry stores inquiry with TTL 5m.
	StoreInquiry(ctx context.Context, inquiry *Inquiry) error

	// ConsumeInquiry atomically retrieves and deletes the inquiry (GETDEL).
	// Returns nil if not found or expired.
	ConsumeInquiry(ctx context.Context, inquiryID string) (*Inquiry, error)
}

// VerificationTokenCache stores verification tokens in Redis (authoritative for liveness).
type VerificationTokenCache interface {
	// StoreToken stores a verification token with TTL 120s.
	StoreToken(ctx context.Context, tokenHash, userID, purpose string) error

	// ConsumeToken atomically retrieves and deletes the token (GETDEL).
	// Returns (userID, purpose) or empty strings if not found.
	ConsumeToken(ctx context.Context, tokenHash string) (userID, purpose string, err error)
}

// TransactionCacheInvalidator invalidates caches after a transaction.
type TransactionCacheInvalidator interface {
	// InvalidateTransactionCaches invalidates caches for ALL affected parties.
	InvalidateTransactionCaches(ctx context.Context, parties []AffectedParty) error
}

// AffectedParty identifies a user+account whose caches need invalidation.
type AffectedParty struct {
	UserID    string
	AccountID string
}

// RecentTransferCache caches recent transfers.
type RecentTransferCache interface {
	GetRecent(ctx context.Context, userID uuid.UUID) ([]FavoriteTransfer, error)
	SetRecent(ctx context.Context, userID uuid.UUID, transfers []FavoriteTransfer) error
}

// IdempotencyStore provides idempotency guard operations.
type IdempotencyStore interface {
	// Claim atomically tries to claim the idempotency slot (SETNX).
	Claim(ctx context.Context, userID, idemKey string) (IdempotencyResult, error)
	// Persist stores the successful response with 24h TTL.
	Persist(ctx context.Context, userID, idemKey, responseJSON string) error
	// Release deletes the key on failure so client can retry.
	Release(ctx context.Context, userID, idemKey string) error
}

// IdempotencyResult indicates the state of an idempotency check.
type IdempotencyResult struct {
	AlreadyClaimed  bool
	StoredResponse  string
	StillProcessing bool
}

// CacheTTLs for transaction-related caches.
const (
	InquiryCacheTTL       = 5 * time.Minute
	VerificationTokenTTL  = 120 * time.Second
	RecentTransfersTTL    = 5 * time.Minute
	MutationsCacheTTL     = 60 * time.Second
	HistoryCacheTTL       = 60 * time.Second
	ReceiptCacheTTL       = 24 * time.Hour
)