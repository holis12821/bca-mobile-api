package ewallet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

func TestFakeProvider_HappyPath(t *testing.T) {
	fp := NewFakeProvider()
	result, err := fp.Lookup(context.Background(), "gopay", "081234567890")
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Contains(t, result.Name, "GOPAY")
	assert.Contains(t, result.Name, "7890")
}

func TestFakeProvider_PhoneEnding999_ProviderDown(t *testing.T) {
	fp := NewFakeProvider()
	_, err := fp.Lookup(context.Background(), "gopay", "081234567999")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperr.EWalletProviderDown)
}

func TestFakeProvider_PhoneEnding000_NotFound(t *testing.T) {
	fp := NewFakeProvider()
	result, err := fp.Lookup(context.Background(), "ovo", "081234567000")
	require.NoError(t, err)
	assert.False(t, result.Valid)
}

func TestFakeProvider_DifferentProviders(t *testing.T) {
	fp := NewFakeProvider()
	providers := []string{"gopay", "ovo", "dana", "shopeepay"}
	for _, p := range providers {
		result, err := fp.Lookup(context.Background(), p, "081234561234")
		require.NoError(t, err)
		assert.True(t, result.Valid)
		assert.Contains(t, result.Name, "1234")
	}
}
