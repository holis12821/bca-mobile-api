package account

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AccountRepository defines data access for accounts.
type AccountRepository interface {
	// FindActiveByUserID returns all active customer accounts for a user.
	FindActiveByUserID(ctx context.Context, userID uuid.UUID) ([]Account, error)

	// FindByAccountNumber finds a CUSTOMER account by number.
	// MUST filter owner_type = 'CUSTOMER' to exclude settlement shards.
	FindByAccountNumber(ctx context.Context, accountNumber string) (*Account, error)
}

// ProfileRepository defines data access for user profile.
type ProfileRepository interface {
	// FindProfile returns the user profile with decrypted PII.
	// PII decryption happens at this layer.
	FindProfile(ctx context.Context, userID uuid.UUID) (*UserProfile, error)
}

// TransactionLimitRepository defines data access for transaction limits.
type TransactionLimitRepository interface {
	// FindByUserID returns all limits for a user.
	FindByUserID(ctx context.Context, userID uuid.UUID) ([]TransactionLimit, error)

	// UpdateLimit updates a single limit type for a user.
	UpdateLimit(ctx context.Context, userID uuid.UUID, limitType string, update LimitUpdate) error
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