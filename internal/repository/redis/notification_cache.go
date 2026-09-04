package redis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
)

// NotificationCache stores paginated notification data in Redis cache.
// Key format: cache:notif:{user_id}:v{version}:{cursor_hash}
// The cursor hash MUST be part of the key — otherwise page 2 returns page 1's content.
type NotificationCache struct {
	client *goredis.Client
}

func NewNotificationCache(client *goredis.Client) *NotificationCache {
	return &NotificationCache{client: client}
}

func notifCacheKey(userID uuid.UUID, version int64, cursorHash string) string {
	return fmt.Sprintf("cache:notif:%s:v%d:%s", userID.String(), version, cursorHash)
}

func (nc *NotificationCache) GetNotifications(ctx context.Context, userID uuid.UUID, version int64, cursorHash string) (*account.NotificationListResponse, error) {
	data, err := nc.client.Get(ctx, notifCacheKey(userID, version, cursorHash)).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp account.NotificationListResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func (nc *NotificationCache) SetNotifications(ctx context.Context, userID uuid.UUID, version int64, cursorHash string, resp *account.NotificationListResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	return nc.client.Set(ctx, notifCacheKey(userID, version, cursorHash), data, account.NotificationCacheTTL).Err()
}