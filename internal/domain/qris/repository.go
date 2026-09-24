package qris

import (
	"context"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
)

// QRISExecutor runs the QRIS payment inside a SERIALIZABLE transaction.
type QRISExecutor interface {
	ExecutePay(ctx context.Context, params ExecutePayParams) (*transaction.Transaction, error)
}

// QRISDecodeCache caches decoded QRIS payloads so the pay step can reference them.
type QRISDecodeCache interface {
	StoreDecoded(ctx context.Context, qrisID string, decoded *DecodedQRIS) error
	GetDecoded(ctx context.Context, qrisID string) (*DecodedQRIS, error)
	ConsumeDecoded(ctx context.Context, qrisID string) (*DecodedQRIS, error)
}

const (
	DecodeCacheTTL = 5 * time.Minute
	QRISAdminFee   = 0 // QRIS typically has no admin fee for payer
)
