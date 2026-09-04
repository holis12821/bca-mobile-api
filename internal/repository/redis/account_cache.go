package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
)

// ProfileCache stores profile data in Redis cache.
type ProfileCache struct {
	client *goredis.Client
}

func NewProfileCache(client *goredis.Client) *ProfileCache {
	return &ProfileCache{client: client}
}

func profileCacheKey(userID uuid.UUID, version int64) string {
	return fmt.Sprintf("cache:profile:%s:v%d", userID.String(), version)
}

type cachedProfile struct {
	ID          string     `json:"id"`
	FullName    string     `json:"full_name"`
	DisplayName string     `json:"display_name"`
	Phone       string     `json:"phone"`
	Email       string     `json:"email"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	Accounts    []cachedAccount `json:"accounts"`
}

type cachedAccount struct {
	ID               string `json:"id"`
	UserID           string `json:"user_id"`
	AccountNumber    string `json:"account_number"`
	AccountType      string `json:"account_type"`
	AccountLabel     string `json:"account_label"`
	Currency         string `json:"currency"`
	Balance          string `json:"balance"`
	HoldAmount       string `json:"hold_amount"`
	AvailableBalance string `json:"available_balance"`
	IsPrimary        bool   `json:"is_primary"`
	Status           string `json:"status"`
	OpenedAt         string `json:"opened_at"`
}

func (pc *ProfileCache) GetProfile(ctx context.Context, userID uuid.UUID, version int64) (*account.UserProfile, error) {
	data, err := pc.client.Get(ctx, profileCacheKey(userID, version)).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cp cachedProfile
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}

	profile := &account.UserProfile{
		ID:          uuid.MustParse(cp.ID),
		FullName:    cp.FullName,
		DisplayName: cp.DisplayName,
		Phone:       cp.Phone,
		Email:       cp.Email,
		LastLoginAt: cp.LastLoginAt,
	}

	for _, ca := range cp.Accounts {
		bal, _ := decimal.NewFromString(ca.Balance)
		hold, _ := decimal.NewFromString(ca.HoldAmount)
		avail, _ := decimal.NewFromString(ca.AvailableBalance)
		opened, _ := time.Parse(time.RFC3339, ca.OpenedAt)
		profile.Accounts = append(profile.Accounts, account.Account{
			ID:               uuid.MustParse(ca.ID),
			UserID:           uuid.MustParse(ca.UserID),
			AccountNumber:    ca.AccountNumber,
			AccountType:      ca.AccountType,
			AccountLabel:     ca.AccountLabel,
			Currency:         ca.Currency,
			Balance:          bal,
			HoldAmount:       hold,
			AvailableBalance: avail,
			IsPrimary:        ca.IsPrimary,
			Status:           ca.Status,
			OpenedAt:         opened,
		})
	}

	return profile, nil
}

func (pc *ProfileCache) SetProfile(ctx context.Context, userID uuid.UUID, version int64, profile *account.UserProfile) error {
	cp := cachedProfile{
		ID:          profile.ID.String(),
		FullName:    profile.FullName,
		DisplayName: profile.DisplayName,
		Phone:       profile.Phone,
		Email:       profile.Email,
		LastLoginAt: profile.LastLoginAt,
	}

	for _, a := range profile.Accounts {
		cp.Accounts = append(cp.Accounts, cachedAccount{
			ID:               a.ID.String(),
			UserID:           a.UserID.String(),
			AccountNumber:    a.AccountNumber,
			AccountType:      a.AccountType,
			AccountLabel:     a.AccountLabel,
			Currency:         a.Currency,
			Balance:          a.Balance.String(),
			HoldAmount:       a.HoldAmount.String(),
			AvailableBalance: a.AvailableBalance.String(),
			IsPrimary:        a.IsPrimary,
			Status:           a.Status,
			OpenedAt:         a.OpenedAt.UTC().Format(time.RFC3339),
		})
	}

	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}

	return pc.client.Set(ctx, profileCacheKey(userID, version), data, account.ProfileCacheTTL).Err()
}

// BalanceCache stores balance data in Redis cache.
type BalanceCache struct {
	client *goredis.Client
}

func NewBalanceCache(client *goredis.Client) *BalanceCache {
	return &BalanceCache{client: client}
}

func balanceCacheKey(userID uuid.UUID, version int64) string {
	return fmt.Sprintf("cache:balance:%s:v%d", userID.String(), version)
}

func (bc *BalanceCache) GetBalances(ctx context.Context, userID uuid.UUID, version int64) ([]account.Account, error) {
	data, err := bc.client.Get(ctx, balanceCacheKey(userID, version)).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cached []cachedAccount
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	accounts := make([]account.Account, len(cached))
	for i, ca := range cached {
		bal, _ := decimal.NewFromString(ca.Balance)
		hold, _ := decimal.NewFromString(ca.HoldAmount)
		avail, _ := decimal.NewFromString(ca.AvailableBalance)
		opened, _ := time.Parse(time.RFC3339, ca.OpenedAt)
		accounts[i] = account.Account{
			ID:               uuid.MustParse(ca.ID),
			UserID:           uuid.MustParse(ca.UserID),
			AccountNumber:    ca.AccountNumber,
			AccountType:      ca.AccountType,
			AccountLabel:     ca.AccountLabel,
			Currency:         ca.Currency,
			Balance:          bal,
			HoldAmount:       hold,
			AvailableBalance: avail,
			IsPrimary:        ca.IsPrimary,
			Status:           ca.Status,
			OpenedAt:         opened,
		}
	}

	return accounts, nil
}

func (bc *BalanceCache) SetBalances(ctx context.Context, userID uuid.UUID, version int64, accounts []account.Account) error {
	cached := make([]cachedAccount, len(accounts))
	for i, a := range accounts {
		cached[i] = cachedAccount{
			ID:               a.ID.String(),
			UserID:           a.UserID.String(),
			AccountNumber:    a.AccountNumber,
			AccountType:      a.AccountType,
			AccountLabel:     a.AccountLabel,
			Currency:         a.Currency,
			Balance:          a.Balance.String(),
			HoldAmount:       a.HoldAmount.String(),
			AvailableBalance: a.AvailableBalance.String(),
			IsPrimary:        a.IsPrimary,
			Status:           a.Status,
			OpenedAt:         a.OpenedAt.UTC().Format(time.RFC3339),
		}
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}

	return bc.client.Set(ctx, balanceCacheKey(userID, version), data, account.BalanceCacheTTL).Err()
}

// DashboardCache stores dashboard data in Redis cache.
type DashboardCache struct {
	client *goredis.Client
}

func NewDashboardCache(client *goredis.Client) *DashboardCache {
	return &DashboardCache{client: client}
}

func dashboardCacheKey(userID uuid.UUID, version int64) string {
	return fmt.Sprintf("cache:dashboard:%s:v%d", userID.String(), version)
}

func (dc *DashboardCache) GetDashboard(ctx context.Context, userID uuid.UUID, version int64) (*account.DashboardResponse, error) {
	data, err := dc.client.Get(ctx, dashboardCacheKey(userID, version)).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp account.DashboardResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func (dc *DashboardCache) SetDashboard(ctx context.Context, userID uuid.UUID, version int64, resp *account.DashboardResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	return dc.client.Set(ctx, dashboardCacheKey(userID, version), data, account.DashboardCacheTTL).Err()
}