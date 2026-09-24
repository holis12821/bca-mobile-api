package ewallet

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Provider represents an ewallet_providers row.
type Provider struct {
	ID            string
	Name          string
	IconURL       *string
	IsActive      bool
	MinAmount     decimal.Decimal
	MaxAmount     decimal.Decimal
	AdminFee      decimal.Decimal
	PresetAmounts []int64
	SortOrder     int
}

// ProviderItem is a single provider in the list response.
type ProviderItem struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	IconURL       string  `json:"icon_url,omitempty"`
	IsActive      bool    `json:"is_active"`
	MinAmount     string  `json:"min_amount"`
	MaxAmount     string  `json:"max_amount"`
	AdminFee      string  `json:"admin_fee"`
	PresetAmounts []int64 `json:"preset_amounts"`
}

// InquiryRequest is the request for POST /ewallet/inquiry.
type InquiryRequest struct {
	ProviderID      string `json:"provider_id" validate:"required"`
	PhoneNumber     string `json:"phone_number" validate:"required"`
	Amount          int64  `json:"amount" validate:"required"`
	SourceAccountID string `json:"source_account_id" validate:"required"`
}

// InquiryResponse is returned from POST /ewallet/inquiry.
type InquiryResponse struct {
	InquiryID        string `json:"inquiry_id"`
	Provider         string `json:"provider"`
	DestinationName  string `json:"destination_name"`
	DestinationPhone string `json:"destination_phone"`
	Amount           string `json:"amount"`
	AdminFee         string `json:"admin_fee"`
	Total            string `json:"total"`
	SourceAccount    string `json:"source_account"`
	ExpiresIn        int    `json:"expires_in"`
}

// TopUpRequest is the request for POST /ewallet/topup.
type TopUpRequest struct {
	IdempotencyKey    string `json:"idempotency_key"`
	InquiryID         string `json:"inquiry_id" validate:"required"`
	VerificationToken string `json:"verification_token" validate:"required"`
}

// TopUpResponse is returned from POST /ewallet/topup.
type TopUpResponse struct {
	TransactionID    string `json:"transaction_id"`
	ReferenceNumber  string `json:"reference_number"`
	Status           string `json:"status"`
	Provider         string `json:"provider"`
	DestinationPhone string `json:"destination_phone"`
	DestinationName  string `json:"destination_name"`
	Amount           string `json:"amount"`
	AdminFee         string `json:"admin_fee"`
	Total            string `json:"total"`
	SourceAccount    string `json:"source_account"`
	SourceName       string `json:"source_name"`
	CreatedAt        string `json:"created_at"`
}

// EWalletInquiry extends the generic inquiry with e-wallet specific fields.
type EWalletInquiry struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	ProviderID      string
	ProviderName    string
	PhoneNumber     string
	DestinationName string
	Amount          decimal.Decimal
	AdminFee        decimal.Decimal
	TotalAmount     decimal.Decimal
	SourceAccountID uuid.UUID
	ExpiresAt       time.Time
}

// ExecuteTopUpParams holds everything needed for the SERIALIZABLE transaction.
type ExecuteTopUpParams struct {
	IdempotencyKey  string
	UserID          uuid.UUID
	SourceAccountID uuid.UUID
	ProviderID      string
	ProviderName    string
	PhoneNumber     string
	DestinationName string
	Amount          decimal.Decimal
	AdminFee        decimal.Decimal
	TotalAmount     decimal.Decimal
	WIBDate         time.Time
	WIBTime         time.Time
}

// ProviderLookupResult is what the stub provider returns.
type ProviderLookupResult struct {
	Name  string
	Valid bool
}
