package account

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Account represents a customer bank account.
type Account struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	AccountNumber    string
	AccountType      string
	AccountLabel     string
	Currency         string
	Balance          decimal.Decimal
	HoldAmount       decimal.Decimal
	AvailableBalance decimal.Decimal
	IsPrimary        bool
	Status           string
	OpenedAt         time.Time
}

// UserProfile is the profile data returned by GET /account/profile.
type UserProfile struct {
	ID          uuid.UUID
	FullName    string
	DisplayName string
	Phone       string // decrypted at repo layer, masked at handler
	Email       string // decrypted at repo layer, masked at handler
	LastLoginAt *time.Time
	Accounts    []Account
}

// BalanceResponse is the response for GET /account/balance.
type BalanceResponse struct {
	Accounts []AccountBalance `json:"accounts"`
}

// AccountBalance is a single account's balance info.
type AccountBalance struct {
	AccountID        string `json:"account_id"`
	AccountNumber    string `json:"account_number"`
	AccountType      string `json:"account_type"`
	AccountLabel     string `json:"account_label"`
	Currency         string `json:"currency"`
	Balance          string `json:"balance"`
	AvailableBalance string `json:"available_balance"`
	HoldAmount       string `json:"hold_amount"`
	IsPrimary        bool   `json:"is_primary"`
}

// DashboardResponse is the response for GET /account/dashboard.
type DashboardResponse struct {
	User          DashboardUser      `json:"user"`
	PrimaryAccount *AccountBalance   `json:"primary_account"`
	Accounts      []AccountBalance   `json:"accounts"`
	UnreadCount   int                `json:"unread_notification_count"`
}

// DashboardUser is the user subset for dashboard.
type DashboardUser struct {
	DisplayName string `json:"display_name"`
	LastLoginAt string `json:"last_login_at,omitempty"`
}

// TransactionLimit represents a user's transaction limit.
type TransactionLimit struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	LimitType           string
	DailyLimit          decimal.Decimal
	MonthlyLimit        *decimal.Decimal
	PerTransactionLimit *decimal.Decimal
}

// UpdateLimitRequest is the request to update transaction limits.
type UpdateLimitRequest struct {
	Limits map[string]LimitUpdate `json:"limits" validate:"required"`
}

// LimitUpdate is a single limit type update.
type LimitUpdate struct {
	DailyLimit          *decimal.Decimal `json:"daily_limit,omitempty"`
	MonthlyLimit        *decimal.Decimal `json:"monthly_limit,omitempty"`
	PerTransactionLimit *decimal.Decimal `json:"per_transaction_limit,omitempty"`
}

// Notification represents a single notification.
type Notification struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Type      string
	Title     string
	Body      string
	DeepLink  *string
	IsRead    bool
	ReadAt    *time.Time
	Metadata  map[string]any
	CreatedAt time.Time
}

// NotificationListResponse is the paginated notification list.
type NotificationListResponse struct {
	Notifications []NotificationItem `json:"notifications"`
}

// NotificationItem is a single notification in the list response.
type NotificationItem struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	DeepLink  string         `json:"deep_link,omitempty"`
	IsRead    bool           `json:"is_read"`
	ReadAt    string         `json:"read_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt string         `json:"created_at"`
}

// LimitCeilings are the server-side maximums from the DB constraint.
var LimitCeilings = map[string]LimitCeiling{
	"TRANSFER_INTERNAL": {DailyLimit: decimal.NewFromInt(100_000_000)},
	"TRANSFER_EXTERNAL": {DailyLimit: decimal.NewFromInt(100_000_000)},
	"EWALLET":           {DailyLimit: decimal.NewFromInt(20_000_000)},
	"QRIS":              {DailyLimit: decimal.NewFromInt(20_000_000), PerTransactionLimit: decPtr(decimal.NewFromInt(5_000_000))},
	"PAYMENT":           {DailyLimit: decimal.NewFromInt(100_000_000)},
}

// LimitCeiling defines the maximum allowed value for a limit type.
type LimitCeiling struct {
	DailyLimit          decimal.Decimal
	PerTransactionLimit *decimal.Decimal
}

func decPtr(d decimal.Decimal) *decimal.Decimal {
	return &d
}