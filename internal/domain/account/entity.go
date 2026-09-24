package account

import (
	"encoding/json"
	"fmt"
	"strings"
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
//
// The shape follows docs/01-API-SPECIFICATION.md §3: user.masked_account, an
// aggregate balance block, promotions and quick_actions. The older
// primary_account / accounts / unread_notification_count keys are still
// emitted so an app built against the previous response keeps working through
// one release.
type DashboardResponse struct {
	User         DashboardUser    `json:"user"`
	Balance      DashboardBalance `json:"balance"`
	UnreadCount  int              `json:"unread_notifications"`
	Promotions   []PromotionItem  `json:"promotions"`
	QuickActions []QuickAction    `json:"quick_actions"`

	// Deprecated: kept for the previous client contract.
	PrimaryAccount    *AccountBalance  `json:"primary_account"`
	Accounts          []AccountBalance `json:"accounts"`
	UnreadCountLegacy int              `json:"unread_notification_count"`
}

// DashboardUser is the user subset for dashboard.
type DashboardUser struct {
	DisplayName   string `json:"display_name"`
	MaskedAccount string `json:"masked_account"`
	LastLoginAt   string `json:"last_login_at,omitempty"`
}

// DashboardBalance is the aggregate across every active account.
type DashboardBalance struct {
	Total          string `json:"total"`
	Currency       string `json:"currency"`
	PrimaryAccount string `json:"primary_account"`
}

// PromotionItem is one promo card on the Beranda carousel.
type PromotionItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ImageURL   string `json:"image_url"`
	DeepLink   string `json:"deep_link,omitempty"`
	ValidUntil string `json:"valid_until"`
}

// Promotion is a promotions row.
type Promotion struct {
	ID         uuid.UUID
	Title      string
	ImageURL   string
	DeepLink   *string
	ValidUntil time.Time
}

// QuickAction is one shortcut tile on the Beranda.
type QuickAction struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Icon    string `json:"icon"`
	Enabled bool   `json:"enabled"`
}

// DefaultQuickActions is the Beranda shortcut row. Static for now — the app
// needs the list to render, and a server-driven version can replace this
// without changing the contract.
var DefaultQuickActions = []QuickAction{
	{ID: "M_INFO", Label: "m-Info", Icon: "ic_info", Enabled: true},
	{ID: "TRANSFER", Label: "Transfer", Icon: "ic_transfer", Enabled: true},
	{ID: "E_WALLET", Label: "e-Wallet", Icon: "ic_ewallet", Enabled: true},
	{ID: "QRIS", Label: "QRIS", Icon: "ic_qris", Enabled: true},
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
//
// VerificationToken is required: raising a daily ceiling is a security
// decision, and the endpoint used to accept an access token alone.
type UpdateLimitRequest struct {
	VerificationToken string                 `json:"verification_token"`
	Limits            map[string]LimitUpdate `json:"limits"`
}

// LimitUpdate is a single limit type update.
type LimitUpdate struct {
	DailyLimit          *decimal.Decimal `json:"daily_limit,omitempty"`
	MonthlyLimit        *decimal.Decimal `json:"monthly_limit,omitempty"`
	PerTransactionLimit *decimal.Decimal `json:"per_transaction_limit,omitempty"`
}

// flatLimitKeys maps the flat request shape the API spec documents
// ("transfer_internal_daily": 50000000) onto the canonical limit_type plus the
// field it sets. Both shapes are accepted — the spec's flat one and the
// nested {"TRANSFER_INTERNAL": {"daily_limit": ...}} the handler grew — so an
// Android build written against either keeps working.
var flatLimitKeys = map[string]struct {
	LimitType string
	Field     string // daily | monthly | per_transaction
}{
	"transfer_internal_daily":           {"TRANSFER_INTERNAL", "daily"},
	"transfer_external_daily":           {"TRANSFER_EXTERNAL", "daily"},
	"ewallet_daily":                     {"EWALLET", "daily"},
	"qris_daily":                        {"QRIS", "daily"},
	"payment_daily":                     {"PAYMENT", "daily"},
	"transfer_internal_monthly":         {"TRANSFER_INTERNAL", "monthly"},
	"transfer_external_monthly":         {"TRANSFER_EXTERNAL", "monthly"},
	"qris_per_transaction":              {"QRIS", "per_transaction"},
	"transfer_internal_per_transaction": {"TRANSFER_INTERNAL", "per_transaction"},
	"transfer_external_per_transaction": {"TRANSFER_EXTERNAL", "per_transaction"},
}

// UnmarshalJSON accepts both documented shapes for "limits".
func (r *UpdateLimitRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		VerificationToken string                     `json:"verification_token"`
		Limits            map[string]json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	r.VerificationToken = raw.VerificationToken
	r.Limits = make(map[string]LimitUpdate, len(raw.Limits))

	for key, value := range raw.Limits {
		// Nested form: {"TRANSFER_INTERNAL": {"daily_limit": 50000000}}
		var nested LimitUpdate
		if err := json.Unmarshal(value, &nested); err == nil {
			existing := r.Limits[key]
			mergeLimit(&existing, nested)
			r.Limits[key] = existing
			continue
		}

		// Flat form: {"transfer_internal_daily": 50000000}
		var amount decimal.Decimal
		if err := json.Unmarshal(value, &amount); err != nil {
			return fmt.Errorf("limit %q: expected a number or an object", key)
		}
		mapping, ok := flatLimitKeys[strings.ToLower(key)]
		if !ok {
			return fmt.Errorf("limit %q is not a recognised limit key", key)
		}
		entry := r.Limits[mapping.LimitType]
		switch mapping.Field {
		case "daily":
			entry.DailyLimit = &amount
		case "monthly":
			entry.MonthlyLimit = &amount
		case "per_transaction":
			entry.PerTransactionLimit = &amount
		}
		r.Limits[mapping.LimitType] = entry
	}

	return nil
}

func mergeLimit(dst *LimitUpdate, src LimitUpdate) {
	if src.DailyLimit != nil {
		dst.DailyLimit = src.DailyLimit
	}
	if src.MonthlyLimit != nil {
		dst.MonthlyLimit = src.MonthlyLimit
	}
	if src.PerTransactionLimit != nil {
		dst.PerTransactionLimit = src.PerTransactionLimit
	}
}

// LimitStatusResponse is returned by PUT /account/transaction-limit and shows
// the effective limits together with today's usage, as the spec documents.
type LimitStatusResponse struct {
	Limits map[string]string `json:"limits"`
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

// UpdateProfileRequest is the request for PUT /account/profile.
//
// OTPCode is verified against a real one-time code issued by
// POST /account/profile/otp. It used to be checked only for being six
// characters long, so any six digits changed the address that receives
// statements and recovery mail.
type UpdateProfileRequest struct {
	Email   string `json:"email" validate:"required,email"`
	OTPCode string `json:"otp_code" validate:"required"`
}

// RequestProfileOTPResponse is returned by POST /account/profile/otp.
type RequestProfileOTPResponse struct {
	SentTo    string `json:"sent_to"`
	ExpiresIn int    `json:"expires_in"`
	// OTPDebug is populated only when APP_ENV=development — the SMS gateway
	// is a stub there and the app would otherwise have nothing to type.
	OTPDebug string `json:"otp_debug,omitempty"`
}

// UpdateSettingsRequest is the request for PUT /account/settings.
type UpdateSettingsRequest struct {
	BiometricEnabled        *bool `json:"biometric_enabled"`
	PushNotificationEnabled *bool `json:"push_notification_enabled"`
	EmailStatementEnabled   *bool `json:"email_statement_enabled"`
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
