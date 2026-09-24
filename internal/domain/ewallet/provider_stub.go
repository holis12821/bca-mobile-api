package ewallet

import (
	"context"
	"strings"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// FakeProvider is a stub e-wallet provider for portfolio/demo purposes.
// - Valid phone numbers return a fake owner name.
// - Phone numbers ending in "999" trigger EWALLET_PROVIDER_DOWN (demo error path).
// - Phone numbers ending in "000" return not-found.
type FakeProvider struct{}

func NewFakeProvider() *FakeProvider {
	return &FakeProvider{}
}

func (fp *FakeProvider) Lookup(_ context.Context, providerID, phoneNumber string) (*ProviderLookupResult, error) {
	// Error path: provider down (for demo)
	if strings.HasSuffix(phoneNumber, "999") {
		return nil, apperr.EWalletProviderDown
	}

	// Not found path
	if strings.HasSuffix(phoneNumber, "000") {
		return &ProviderLookupResult{Valid: false}, nil
	}

	// Happy path: generate a name from the phone number
	name := "USER " + strings.ToUpper(providerID) + " " + phoneNumber[len(phoneNumber)-4:]
	return &ProviderLookupResult{
		Name:  name,
		Valid: true,
	}, nil
}
