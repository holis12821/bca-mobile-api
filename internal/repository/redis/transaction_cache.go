package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
)

// InquiryCache stores transfer inquiries in Redis.
// Key: inquiry:{inquiry_id}, TTL 5m, consumed with GETDEL.
type InquiryCache struct {
	client *goredis.Client
}

func NewInquiryCache(client *goredis.Client) *InquiryCache {
	return &InquiryCache{client: client}
}

type cachedInquiry struct {
	ID                 string  `json:"id"`
	UserID             string  `json:"user_id"`
	InquiryType        string  `json:"inquiry_type"`
	DestinationAccount string  `json:"destination_account"`
	DestinationName    *string `json:"destination_name"`
	DestinationBank    *string `json:"destination_bank"`
	BankCode           *string `json:"bank_code"`
	ProviderID         *string `json:"provider_id"`
	Amount             *string `json:"amount"`
	AdminFee           *string `json:"admin_fee"`
	ExpiresAt          string  `json:"expires_at"`
}

func (ic *InquiryCache) StoreInquiry(ctx context.Context, inquiry *transaction.Inquiry) error {
	ci := cachedInquiry{
		ID:                 inquiry.ID.String(),
		UserID:             inquiry.UserID.String(),
		InquiryType:        inquiry.InquiryType,
		DestinationAccount: inquiry.DestinationAccount,
		DestinationName:    inquiry.DestinationName,
		DestinationBank:    inquiry.DestinationBank,
		BankCode:           inquiry.BankCode,
		ProviderID:         inquiry.ProviderID,
		ExpiresAt:          inquiry.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if inquiry.Amount != nil {
		s := inquiry.Amount.String()
		ci.Amount = &s
	}
	if inquiry.AdminFee != nil {
		s := inquiry.AdminFee.String()
		ci.AdminFee = &s
	}

	data, err := json.Marshal(ci)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("inquiry:%s", inquiry.ID.String())
	return ic.client.Set(ctx, key, data, transaction.InquiryCacheTTL).Err()
}

func (ic *InquiryCache) ConsumeInquiry(ctx context.Context, inquiryID string) (*transaction.Inquiry, error) {
	key := fmt.Sprintf("inquiry:%s", inquiryID)

	// GETDEL: atomic get-and-delete — prevents replay
	data, err := ic.client.GetDel(ctx, key).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var ci cachedInquiry
	if err := json.Unmarshal(data, &ci); err != nil {
		return nil, err
	}

	inquiry := &transaction.Inquiry{
		ID:                 uuid.MustParse(ci.ID),
		UserID:             uuid.MustParse(ci.UserID),
		InquiryType:        ci.InquiryType,
		DestinationAccount: ci.DestinationAccount,
		DestinationName:    ci.DestinationName,
		DestinationBank:    ci.DestinationBank,
		BankCode:           ci.BankCode,
		ProviderID:         ci.ProviderID,
	}
	inquiry.ExpiresAt, _ = time.Parse(time.RFC3339, ci.ExpiresAt)
	if ci.Amount != nil {
		a, _ := decimal.NewFromString(*ci.Amount)
		inquiry.Amount = &a
	}
	if ci.AdminFee != nil {
		f, _ := decimal.NewFromString(*ci.AdminFee)
		inquiry.AdminFee = &f
	}

	return inquiry, nil
}

// VerificationTokenCacheImpl stores verification tokens in Redis.
// Key: vtoken:{hash}, TTL 120s, consumed with GETDEL.
type VerificationTokenCacheImpl struct {
	client *goredis.Client
}

func NewVerificationTokenCache(client *goredis.Client) *VerificationTokenCacheImpl {
	return &VerificationTokenCacheImpl{client: client}
}

type cachedVToken struct {
	UserID  string `json:"user_id"`
	Purpose string `json:"purpose"`
}

func (vc *VerificationTokenCacheImpl) StoreToken(ctx context.Context, tokenHash, userID, purpose string) error {
	data, _ := json.Marshal(cachedVToken{UserID: userID, Purpose: purpose})
	key := fmt.Sprintf("vtoken:%s", tokenHash)
	return vc.client.Set(ctx, key, data, transaction.VerificationTokenTTL).Err()
}

func (vc *VerificationTokenCacheImpl) ConsumeToken(ctx context.Context, tokenHash string) (string, string, error) {
	key := fmt.Sprintf("vtoken:%s", tokenHash)

	// GETDEL: atomic single-use consumption
	data, err := vc.client.GetDel(ctx, key).Bytes()
	if err == goredis.Nil {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}

	var cv cachedVToken
	if err := json.Unmarshal(data, &cv); err != nil {
		return "", "", err
	}

	return cv.UserID, cv.Purpose, nil
}

// RecentTransferCacheImpl caches recent transfers.
// Key: cache:transfers:recent:{user_id}, TTL 5m.
type RecentTransferCacheImpl struct {
	client *goredis.Client
}

func NewRecentTransferCache(client *goredis.Client) *RecentTransferCacheImpl {
	return &RecentTransferCacheImpl{client: client}
}

type cachedFavorite struct {
	DestinationAccount string  `json:"dest_account"`
	DestinationName    string  `json:"dest_name"`
	DestinationBank    string  `json:"dest_bank"`
	BankCode           string  `json:"bank_code"`
	TransferType       string  `json:"type"`
	TransferCount      int     `json:"count"`
	LastTransferAt     *string `json:"last_at,omitempty"`
}

func (rc *RecentTransferCacheImpl) GetRecent(ctx context.Context, userID uuid.UUID) ([]transaction.FavoriteTransfer, error) {
	key := fmt.Sprintf("cache:transfers:recent:%s", userID.String())
	data, err := rc.client.Get(ctx, key).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cached []cachedFavorite
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	favs := make([]transaction.FavoriteTransfer, len(cached))
	for i, c := range cached {
		favs[i] = transaction.FavoriteTransfer{
			DestinationAccount: c.DestinationAccount,
			DestinationName:    c.DestinationName,
			DestinationBank:    c.DestinationBank,
			BankCode:           c.BankCode,
			TransferType:       c.TransferType,
			TransferCount:      c.TransferCount,
		}
		if c.LastTransferAt != nil {
			t, _ := time.Parse(time.RFC3339, *c.LastTransferAt)
			favs[i].LastTransferAt = &t
		}
	}
	return favs, nil
}

func (rc *RecentTransferCacheImpl) SetRecent(ctx context.Context, userID uuid.UUID, transfers []transaction.FavoriteTransfer) error {
	cached := make([]cachedFavorite, len(transfers))
	for i, f := range transfers {
		cached[i] = cachedFavorite{
			DestinationAccount: f.DestinationAccount,
			DestinationName:    f.DestinationName,
			DestinationBank:    f.DestinationBank,
			BankCode:           f.BankCode,
			TransferType:       f.TransferType,
			TransferCount:      f.TransferCount,
		}
		if f.LastTransferAt != nil {
			s := f.LastTransferAt.UTC().Format(time.RFC3339)
			cached[i].LastTransferAt = &s
		}
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("cache:transfers:recent:%s", userID.String())
	return rc.client.Set(ctx, key, data, transaction.RecentTransfersTTL).Err()
}

// TransactionCacheInvalidator invalidates caches for ALL affected parties after a transaction.
// Takes a list — internal transfers affect two different users.
type TransactionCacheInvalidator struct {
	client *goredis.Client
}

func NewTransactionCacheInvalidator(client *goredis.Client) *TransactionCacheInvalidator {
	return &TransactionCacheInvalidator{client: client}
}

// InvalidateTransactionCaches invalidates balance, dashboard, recent, and
// bumps version counters for mutations/history/notifications for every affected party.
// Settlement and fee-income shards are excluded — they have no customer-facing cache.
func (tc *TransactionCacheInvalidator) InvalidateTransactionCaches(ctx context.Context, parties []transaction.AffectedParty) error {
	pipe := tc.client.Pipeline()
	for _, p := range parties {
		pipe.Del(ctx, "cache:balance:"+p.AccountID)
		pipe.Del(ctx, "cache:dashboard:"+p.UserID)
		pipe.Del(ctx, "cache:transfers:recent:"+p.UserID)
		pipe.Del(ctx, "cache:notif:count:"+p.UserID)
		pipe.Incr(ctx, "cachever:mutations:"+p.AccountID)
		pipe.Incr(ctx, "cachever:history:"+p.UserID)
		pipe.Incr(ctx, "cachever:notif:"+p.UserID)
	}
	_, err := pipe.Exec(ctx)
	return err
}