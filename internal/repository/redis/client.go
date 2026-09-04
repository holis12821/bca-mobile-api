package redis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/config"
)

type Clients struct {
	Session *redis.Client
	Cache   *redis.Client
}

func NewClients(ctx context.Context, session, cache config.Redis) (*Clients, error) {
	sessClient, err := connect(ctx, session, "redis-session")
	if err != nil {
		return nil, err
	}

	cacheClient, err := connect(ctx, cache, "redis-cache")
	if err != nil {
		sessClient.Close()
		return nil, err
	}

	return &Clients{Session: sessClient, Cache: cacheClient}, nil
}

func (c *Clients) Close() error {
	var firstErr error
	if err := c.Session.Close(); err != nil {
		firstErr = err
	}
	if err := c.Cache.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func connect(ctx context.Context, cfg config.Redis, name string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping %s: %w", name, err)
	}

	slog.Info(name+" connected", "addr", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	return client, nil
}