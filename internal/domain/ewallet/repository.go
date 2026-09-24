package ewallet

import (
	"context"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
)

// ProviderRepository reads e-wallet providers from the database.
type ProviderRepository interface {
	ListActive(ctx context.Context) ([]Provider, error)
	FindByID(ctx context.Context, id string) (*Provider, error)
}

// EWalletProviderStub validates phone numbers and returns owner names.
type EWalletProviderStub interface {
	Lookup(ctx context.Context, providerID, phoneNumber string) (*ProviderLookupResult, error)
}

// EWalletExecutor runs the entire top-up inside a SERIALIZABLE transaction
// with retry on 40001. Identical to transfer executor but with EWALLET rail.
type EWalletExecutor interface {
	ExecuteTopUp(ctx context.Context, params ExecuteTopUpParams) (*transaction.Transaction, error)
}

// ProviderCache caches the provider list in Redis.
type ProviderCache interface {
	GetProviders(ctx context.Context) ([]Provider, error)
	SetProviders(ctx context.Context, providers []Provider) error
}

// CacheTTLs for e-wallet caches.
const (
	ProviderCacheTTL = 1 * time.Hour
)
