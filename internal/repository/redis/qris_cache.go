package redis

import (
	"context"
	"encoding/json"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/qris"
)

type QRISDecodeCache struct {
	client *goredis.Client
}

func NewQRISDecodeCache(client *goredis.Client) *QRISDecodeCache {
	return &QRISDecodeCache{client: client}
}

func (c *QRISDecodeCache) StoreDecoded(ctx context.Context, qrisID string, decoded *qris.DecodedQRIS) error {
	data, err := json.Marshal(decoded)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, "cache:qris:decoded:"+qrisID, data, qris.DecodeCacheTTL).Err()
}

func (c *QRISDecodeCache) GetDecoded(ctx context.Context, qrisID string) (*qris.DecodedQRIS, error) {
	data, err := c.client.Get(ctx, "cache:qris:decoded:"+qrisID).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var decoded qris.DecodedQRIS
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func (c *QRISDecodeCache) ConsumeDecoded(ctx context.Context, qrisID string) (*qris.DecodedQRIS, error) {
	decoded, err := c.GetDecoded(ctx, qrisID)
	if err != nil || decoded == nil {
		return decoded, err
	}
	c.client.Del(ctx, "cache:qris:decoded:"+qrisID)
	return decoded, nil
}
