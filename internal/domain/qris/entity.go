package qris

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// DecodedQRIS holds the parsed fields from an EMVCo QRIS payload.
type DecodedQRIS struct {
	MerchantName string          `json:"merchant_name"`
	MerchantCity string          `json:"merchant_city"`
	Amount       decimal.Decimal `json:"amount"`
	IsAmountFixed bool           `json:"is_amount_fixed"`
	QRISID       string          `json:"qris_id"`
	ExpiresAt    string          `json:"expires_at"`
}

// DecodeRequest is the request for POST /qris/decode.
type DecodeRequest struct {
	QRData string `json:"qr_data" validate:"required"`
}

// PayRequest is the request for POST /qris/pay.
type PayRequest struct {
	IdempotencyKey    string  `json:"idempotency_key"`
	QRISID            string  `json:"qris_id" validate:"required"`
	SourceAccountID   string  `json:"source_account_id" validate:"required"`
	Amount            int64   `json:"amount" validate:"required"`
	VerificationToken string  `json:"verification_token" validate:"required"`
}

// PayResponse is returned from POST /qris/pay.
type PayResponse struct {
	TransactionID   string `json:"transaction_id"`
	ReferenceNumber string `json:"reference_number"`
	Status          string `json:"status"`
	MerchantName    string `json:"merchant_name"`
	MerchantCity    string `json:"merchant_city"`
	Amount          string `json:"amount"`
	AdminFee        string `json:"admin_fee"`
	Total           string `json:"total"`
	SourceAccount   string `json:"source_account"`
	SourceName      string `json:"source_name"`
	CreatedAt       string `json:"created_at"`
}

// ExecutePayParams holds everything needed for the SERIALIZABLE transaction.
type ExecutePayParams struct {
	IdempotencyKey  string
	UserID          uuid.UUID
	SourceAccountID uuid.UUID
	MerchantName    string
	MerchantCity    string
	Amount          decimal.Decimal
	AdminFee        decimal.Decimal
	TotalAmount     decimal.Decimal
	WIBDate         time.Time
	WIBTime         time.Time
}