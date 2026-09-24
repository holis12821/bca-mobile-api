package redis

import (
	"context"
	"encoding/json"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/ewallet"
)

type EWalletProviderCache struct {
	client *goredis.Client
}

func NewEWalletProviderCache(client *goredis.Client) *EWalletProviderCache {
	return &EWalletProviderCache{client: client}
}

func (c *EWalletProviderCache) GetProviders(ctx context.Context) ([]ewallet.Provider, error) {
	data, err := c.client.Get(ctx, "cache:ewallet:providers").Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var providers []ewallet.Provider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, err
	}
	return providers, nil
}

func (c *EWalletProviderCache) SetProviders(ctx context.Context, providers []ewallet.Provider) error {
	data, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, "cache:ewallet:providers", data, ewallet.ProviderCacheTTL).Err()
}
