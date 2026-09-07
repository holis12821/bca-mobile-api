package transaction

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Mutation represents a single account_mutations row (bank statement entry).
type Mutation struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	TransactionID   *uuid.UUID
	MutationType    string // DEBIT or CREDIT
	Amount          decimal.Decimal
	BalanceBefore   decimal.Decimal
	BalanceAfter    decimal.Decimal
	Description     string
	Detail          *string
	Category        *string
	ReferenceNumber *string
	TransactionDate time.Time // WIB date
	TransactionTime time.Time // WIB time
	CreatedAt       time.Time
}

// Transaction represents a transactions row.
type Transaction struct {
	ID                   uuid.UUID
	IdempotencyKey       *string
	UserID               uuid.UUID
	SourceAccountID      *uuid.UUID
	DestinationAccountID *uuid.UUID
	Type                 string
	Status               string
	Amount               decimal.Decimal
	AdminFee             decimal.Decimal
	TotalAmount          decimal.Decimal
	Currency             string
	ReferenceNumber      string
	Description          *string
	Notes                *string

	// Destination info (denormalized)
	DestinationAccount  *string
	DestinationName     *string
	DestinationBank     *string
	DestinationBankCode *string

	// Provider info
	ProviderID   *string
	ProviderName *string
	ProviderRef  *string

	CreatedAt   time.Time
	ProcessedAt *time.Time
	CompletedAt *time.Time
	ExpiredAt   *time.Time
}

// Inquiry represents a transfer_inquiries row.
type Inquiry struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	InquiryType        string // TRANSFER or EWALLET
	DestinationAccount string
	DestinationName    *string
	DestinationBank    *string
	BankCode           *string
	ProviderID         *string
	Amount             *decimal.Decimal
	AdminFee           *decimal.Decimal
	Metadata           map[string]any
	ExpiresAt          time.Time
	UsedAt             *time.Time
	CreatedAt          time.Time
}

// VerificationToken represents a verification_tokens row.
type VerificationToken struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	TokenHash     string
	Purpose       string // TRANSFER, EWALLET_TOPUP, QRIS_PAYMENT, CHANGE_LIMIT, CHANGE_PIN
	TransactionID *uuid.UUID
	ExpiresAt     time.Time
	UsedAt        *time.Time
	CreatedAt     time.Time
}

// FavoriteTransfer represents a favorite_transfers row.
type FavoriteTransfer struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	DestinationAccount string
	DestinationName    string
	DestinationBank    string
	BankCode           string
	TransferType       string
	TransferCount      int
	LastTransferAt     *time.Time
}

// --- Request/Response types ---

// InquiryRequest is the request body for POST /transfer/inquiry.
type InquiryRequest struct {
	DestinationAccount string `json:"destination_account" validate:"required"`
	DestinationBank    string `json:"destination_bank,omitempty"`
	BankCode           string `json:"bank_code,omitempty"`
	TransferType       string `json:"transfer_type" validate:"required"`
	Amount             int64  `json:"amount,omitempty"` // minor units (sen)
	Notes              string `json:"notes,omitempty"`
}

// InquiryResponse is returned from POST /transfer/inquiry.
type InquiryResponse struct {
	InquiryID          string `json:"inquiry_id"`
	DestinationAccount string `json:"destination_account"`
	DestinationName    string `json:"destination_name"`
	DestinationBank    string `json:"destination_bank"`
	BankCode           string `json:"bank_code"`
	TransferType       string `json:"transfer_type"`
	AdminFee           string `json:"admin_fee"`
	ExpiresIn          int    `json:"expires_in"` // seconds
}

// MutationItem is a single item in the mutations list response.
type MutationItem struct {
	ID              string         `json:"id"`
	MutationType    string         `json:"mutation_type"`
	Amount          string         `json:"amount"`
	BalanceBefore   string         `json:"balance_before"`
	BalanceAfter    string         `json:"balance_after"`
	Description     string         `json:"description"`
	Detail          string         `json:"detail,omitempty"`
	Category        string         `json:"category,omitempty"`
	ReferenceNumber string         `json:"reference_number,omitempty"`
	TransactionDate string         `json:"transaction_date"`
	TransactionTime string         `json:"transaction_time"`
	CreatedAt       string         `json:"created_at"`
}

// TransactionItem is a single item in the transaction history response.
type TransactionItem struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Status             string `json:"status"`
	Amount             string `json:"amount"`
	AdminFee           string `json:"admin_fee"`
	TotalAmount        string `json:"total_amount"`
	Currency           string `json:"currency"`
	ReferenceNumber    string `json:"reference_number"`
	Description        string `json:"description,omitempty"`
	Notes              string `json:"notes,omitempty"`
	DestinationAccount string `json:"destination_account,omitempty"`
	DestinationName    string `json:"destination_name,omitempty"`
	DestinationBank    string `json:"destination_bank,omitempty"`
	CreatedAt          string `json:"created_at"`
}

// RecentTransferItem is a single item in the recent transfers response.
type RecentTransferItem struct {
	DestinationAccount string `json:"destination_account"`
	DestinationName    string `json:"destination_name"`
	DestinationBank    string `json:"destination_bank"`
	BankCode           string `json:"bank_code"`
	TransferType       string `json:"transfer_type"`
	TransferCount      int    `json:"transfer_count"`
	LastTransferAt     string `json:"last_transfer_at,omitempty"`
}

// PINVerifyRequest is the request body for POST /auth/pin/verify.
type PINVerifyRequest struct {
	PINEncrypted string `json:"pin_encrypted" validate:"required"`
	Purpose      string `json:"purpose" validate:"required"`
}

// PINVerifyResponse is returned from POST /auth/pin/verify.
type PINVerifyResponse struct {
	VerificationToken string `json:"verification_token"`
	ExpiresIn         int    `json:"expires_in"` // seconds
}

// Valid purposes for verification tokens.
var ValidPurposes = map[string]bool{
	"TRANSFER":      true,
	"EWALLET_TOPUP": true,
	"QRIS_PAYMENT":  true,
	"CHANGE_LIMIT":  true,
	"CHANGE_PIN":    true,
}

// ExecuteTransferRequest is the request body for POST /transfer/execute.
type ExecuteTransferRequest struct {
	InquiryID          string `json:"inquiry_id" validate:"required"`
	SourceAccountID    string `json:"source_account_id" validate:"required"`
	DestinationAccount string `json:"destination_account"`
	BankCode           string `json:"bank_code"`
	TransferType       string `json:"transfer_type"`
	Amount             int64  `json:"amount"`
	Notes              string `json:"notes,omitempty"`
	VerificationToken  string `json:"verification_token" validate:"required"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"` // cross-check only
}

// ExecuteTransferResponse is returned from POST /transfer/execute.
type ExecuteTransferResponse struct {
	TransactionID   string              `json:"transaction_id"`
	ReferenceNumber string              `json:"reference_number"`
	Status          string              `json:"status"`
	Amount          string              `json:"amount"`
	AdminFee        string              `json:"admin_fee"`
	Total           string              `json:"total"`
	Source          TransferParty       `json:"source"`
	Destination     TransferDestination `json:"destination"`
	Notes           string              `json:"notes,omitempty"`
	CreatedAt       string              `json:"created_at"`
}

// TransferParty identifies the source account in a transfer response.
type TransferParty struct {
	AccountNumber string `json:"account_number"`
	Name          string `json:"name"`
}

// TransferDestination identifies the destination in a transfer response.
type TransferDestination struct {
	AccountNumber string `json:"account_number"`
	Name          string `json:"name"`
	Bank          string `json:"bank"`
}

// Valid transfer types.
var ValidTransferTypes = map[string]bool{
	"INTERNAL":        true,
	"EXTERNAL":        true,
	"VIRTUAL_ACCOUNT": true,
}

// TransferType to limit_type mapping.
var TransferTypeLimitMap = map[string]string{
	"INTERNAL":        "TRANSFER_INTERNAL",
	"EXTERNAL":        "TRANSFER_EXTERNAL",
	"VIRTUAL_ACCOUNT": "TRANSFER_EXTERNAL",
}

// TransferType to transaction type mapping.
var TransferTypeTxnTypeMap = map[string]string{
	"INTERNAL":        "TRANSFER_INTERNAL",
	"EXTERNAL":        "TRANSFER_EXTERNAL",
	"VIRTUAL_ACCOUNT": "TRANSFER_EXTERNAL",
}