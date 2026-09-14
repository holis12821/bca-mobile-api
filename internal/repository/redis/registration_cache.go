package redis

import (
	"context"
	"encoding/json"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
)

type RegistrationCache struct {
	client *goredis.Client
}

func NewRegistrationCache(client *goredis.Client) *RegistrationCache {
	return &RegistrationCache{client: client}
}

func (c *RegistrationCache) Store(ctx context.Context, reg *registration.Registration) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, "registration:"+reg.ID.String(), data, registration.RegistrationTTL).Err()
}

func (c *RegistrationCache) Get(ctx context.Context, id string) (*registration.Registration, error) {
	data, err := c.client.Get(ctx, "registration:"+id).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var reg registration.Registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func (c *RegistrationCache) Update(ctx context.Context, reg *registration.Registration) error {
	return c.Store(ctx, reg)
}

func (c *RegistrationCache) Delete(ctx context.Context, id string) error {
	return c.client.Del(ctx, "registration:"+id).Err()
}