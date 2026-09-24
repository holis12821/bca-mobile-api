package account

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// AccountRepository defines data access for accounts.
type AccountRepository interface {
	// FindActiveByUserID returns all active customer accounts for a user.
	FindActiveByUserID(ctx context.Context, userID uuid.UUID) ([]Account, error)

	// FindByAccountNumber finds a CUSTOMER account by number.
	// MUST filter owner_type = 'CUSTOMER' to exclude settlement shards.
	FindByAccountNumber(ctx context.Context, accountNumber string) (*Account, error)

	// FindOwnedByID returns the account only if it belongs to userID and is an
	// active CUSTOMER account. Returns nil, nil otherwise.
	//
	// Every debit path (transfer, e-wallet top-up, QRIS) takes
	// source_account_id straight from the request body. Without this check the
	// executors locked and debited whatever UUID they were handed, and the
	// daily limit consulted was the caller's own — so a logged-in user could
	// spend someone else's balance.
	FindOwnedByID(ctx context.Context, userID, accountID uuid.UUID) (*Account, error)
}

// ProfileRepository defines data access for user profile.
type ProfileRepository interface {
	// FindProfile returns the user profile with decrypted PII.
	// PII decryption happens at this layer.
	FindProfile(ctx context.Context, userID uuid.UUID) (*UserProfile, error)

	// UpdateEmail updates the user's encrypted email.
	UpdateEmail(ctx context.Context, userID uuid.UUID, email string) error

	// UpdateSettings updates the user's settings flags.
	UpdateSettings(ctx context.Context, userID uuid.UUID, req UpdateSettingsRequest) error
}

// TransactionLimitRepository defines data access for transaction limits.
type TransactionLimitRepository interface {
	// FindByUserID returns all limits for a user.
	FindByUserID(ctx context.Context, userID uuid.UUID) ([]TransactionLimit, error)

	// UpdateLimit updates a single limit type for a user.
	UpdateLimit(ctx context.Context, userID uuid.UUID, limitType string, update LimitUpdate) error

	// UsedToday returns today's consumed amount per limit type, keyed by
	// limit_type. The date is a WIB calendar date supplied by the caller —
	// never CURRENT_DATE, which would roll over at the wrong hour.
	UsedToday(ctx context.Context, userID uuid.UUID, wibDate string) (map[string]decimal.Decimal, error)
}

// PromotionRepository reads the promo cards shown on the Beranda.
type PromotionRepository interface {
	// ListActive returns active, in-window promotions ordered by priority.
	ListActive(ctx context.Context, limit int) ([]Promotion, error)
}

// ProfileOTPCache stores the one-time code that authorises a profile change.
type ProfileOTPCache interface {
	// Store saves the hashed code and returns when it expires.
	Store(ctx context.Context, userID uuid.UUID, otpHash string, ttl time.Duration) (time.Time, error)

	// Get returns the stored hash, or "" when there is none.
	Get(ctx context.Context, userID uuid.UUID) (string, error)

	// Delete removes the code (used or burnt).
	Delete(ctx context.Context, userID uuid.UUID) error

	// IncrAttempt counts a failed verification and returns the new total.
	IncrAttempt(ctx context.Context, userID uuid.UUID) (int, error)

	// ResetAttempts clears the failure counter.
	ResetAttempts(ctx context.Context, userID uuid.UUID) error
}

// SMSGateway delivers the profile-change OTP. Satisfied by internal/pkg/sms.
type SMSGateway interface {
	SendOTP(ctx context.Context, phone, otp string) error
}

// VerificationTokenConsumer burns a purpose-bound token issued by
// POST /auth/pin/verify. Satisfied by transaction.Service.
type VerificationTokenConsumer interface {
	ConsumeVerificationToken(ctx context.Context, userID uuid.UUID, rawToken, purpose string) error
}

// NotificationRepository defines data access for notifications.
type NotificationRepository interface {
	// ListByUserID returns paginated notifications for a user.
	// Uses keyset pagination: cursor is the last notification ID.
	// Returns limit+1 rows so caller can compute has_more.
	ListByUserID(ctx context.Context, userID uuid.UUID, cursor *uuid.UUID, limit int) ([]Notification, error)

	// CountUnread returns the number of unread notifications.
	CountUnread(ctx context.Context, userID uuid.UUID) (int, error)

	// MarkRead marks a single notification as read.
	MarkRead(ctx context.Context, userID uuid.UUID, notifID uuid.UUID) error

	// MarkAllRead marks all unread notifications as read for a user.
	MarkAllRead(ctx context.Context, userID uuid.UUID) error
}

// ProfileCache caches profile data in Redis.
type ProfileCache interface {
	GetProfile(ctx context.Context, userID uuid.UUID, version int64) (*UserProfile, error)
	SetProfile(ctx context.Context, userID uuid.UUID, version int64, profile *UserProfile) error
}

// BalanceCache caches balance data in Redis.
type BalanceCache interface {
	GetBalances(ctx context.Context, userID uuid.UUID, version int64) ([]Account, error)
	SetBalances(ctx context.Context, userID uuid.UUID, version int64, accounts []Account) error
}

// DashboardCache caches dashboard data in Redis.
type DashboardCache interface {
	GetDashboard(ctx context.Context, userID uuid.UUID, version int64) (*DashboardResponse, error)
	SetDashboard(ctx context.Context, userID uuid.UUID, version int64, resp *DashboardResponse) error
}

// NotificationCache caches notification data in Redis.
type NotificationCache interface {
	GetNotifications(ctx context.Context, userID uuid.UUID, version int64, cursorHash string) (*NotificationListResponse, error)
	SetNotifications(ctx context.Context, userID uuid.UUID, version int64, cursorHash string, resp *NotificationListResponse) error
}

// VersionCounter manages cache version counters for invalidation.
type VersionCounter interface {
	// GetVersion returns the current version for a resource.
	GetVersion(ctx context.Context, resource string, userID uuid.UUID) (int64, error)

	// IncrVersion increments the version counter and returns the new value.
	IncrVersion(ctx context.Context, resource string, userID uuid.UUID) (int64, error)
}

// CacheTTLs for different resources.
const (
	ProfileCacheTTL      = 10 * time.Minute
	BalanceCacheTTL      = 30 * time.Second
	DashboardCacheTTL    = 60 * time.Second
	NotificationCacheTTL = 60 * time.Second
)
