package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type Service struct {
	accounts      AccountRepository
	profiles      ProfileRepository
	limits        TransactionLimitRepository
	notifications NotificationRepository
	profileCache  ProfileCache
	balanceCache  BalanceCache
	dashCache     DashboardCache
	notifCache    NotificationCache
	versions      VersionCounter
}

type ServiceConfig struct {
	Accounts      AccountRepository
	Profiles      ProfileRepository
	Limits        TransactionLimitRepository
	Notifications NotificationRepository
	ProfileCache  ProfileCache
	BalanceCache  BalanceCache
	DashCache     DashboardCache
	NotifCache    NotificationCache
	Versions      VersionCounter
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		accounts:      cfg.Accounts,
		profiles:      cfg.Profiles,
		limits:        cfg.Limits,
		notifications: cfg.Notifications,
		profileCache:  cfg.ProfileCache,
		balanceCache:  cfg.BalanceCache,
		dashCache:     cfg.DashCache,
		notifCache:    cfg.NotifCache,
		versions:      cfg.Versions,
	}
}

// GetProfile returns the user profile, using cache with version counter.
func (s *Service) GetProfile(ctx context.Context, userID uuid.UUID) (*UserProfile, error) {
	ver, _ := s.versions.GetVersion(ctx, "profile", userID)

	if s.profileCache != nil {
		cached, err := s.profileCache.GetProfile(ctx, userID, ver)
		if err != nil {
			slog.Warn("profile cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	profile, err := s.profiles.FindProfile(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find profile: %w", err)
	}
	if profile == nil {
		return nil, apperr.NotFound
	}

	accounts, err := s.accounts.FindActiveByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find accounts: %w", err)
	}
	profile.Accounts = accounts

	if s.profileCache != nil {
		if err := s.profileCache.SetProfile(ctx, userID, ver, profile); err != nil {
			slog.Warn("profile cache set failed", "error", err)
		}
	}

	return profile, nil
}

// GetBalances returns all active account balances.
func (s *Service) GetBalances(ctx context.Context, userID uuid.UUID) ([]Account, error) {
	ver, _ := s.versions.GetVersion(ctx, "balance", userID)

	if s.balanceCache != nil {
		cached, err := s.balanceCache.GetBalances(ctx, userID, ver)
		if err != nil {
			slog.Warn("balance cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	accounts, err := s.accounts.FindActiveByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find accounts: %w", err)
	}

	if s.balanceCache != nil {
		if err := s.balanceCache.SetBalances(ctx, userID, ver, accounts); err != nil {
			slog.Warn("balance cache set failed", "error", err)
		}
	}

	return accounts, nil
}

// GetDashboard aggregates balance + unread count for the dashboard.
func (s *Service) GetDashboard(ctx context.Context, userID uuid.UUID) (*DashboardResponse, error) {
	ver, _ := s.versions.GetVersion(ctx, "dashboard", userID)

	if s.dashCache != nil {
		cached, err := s.dashCache.GetDashboard(ctx, userID, ver)
		if err != nil {
			slog.Warn("dashboard cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	profile, err := s.profiles.FindProfile(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find profile: %w", err)
	}
	if profile == nil {
		return nil, apperr.NotFound
	}

	accounts, err := s.accounts.FindActiveByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find accounts: %w", err)
	}

	unreadCount, err := s.notifications.CountUnread(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("count unread: %w", err)
	}

	balances := make([]AccountBalance, len(accounts))
	var primary *AccountBalance
	for i, a := range accounts {
		b := toAccountBalance(a)
		balances[i] = b
		if a.IsPrimary {
			p := b
			primary = &p
		}
	}

	dashUser := DashboardUser{
		DisplayName: profile.DisplayName,
	}
	if profile.LastLoginAt != nil {
		dashUser.LastLoginAt = profile.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	resp := &DashboardResponse{
		User:           dashUser,
		PrimaryAccount: primary,
		Accounts:       balances,
		UnreadCount:    unreadCount,
	}

	if s.dashCache != nil {
		if err := s.dashCache.SetDashboard(ctx, userID, ver, resp); err != nil {
			slog.Warn("dashboard cache set failed", "error", err)
		}
	}

	return resp, nil
}

// UpdateTransactionLimit updates limits, enforcing server-side ceilings.
func (s *Service) UpdateTransactionLimit(ctx context.Context, userID uuid.UUID, req UpdateLimitRequest) error {
	for limitType, update := range req.Limits {
		ceiling, ok := LimitCeilings[limitType]
		if !ok {
			return apperr.ValidationError
		}

		if update.DailyLimit != nil && update.DailyLimit.LessThan(decimal.Zero) {
			return apperr.ValidationError
		}
		if update.DailyLimit != nil && update.DailyLimit.GreaterThan(ceiling.DailyLimit) {
			return apperr.Error{
				Status:  422,
				Code:    apperr.ValidationError.Code,
				Message: fmt.Sprintf("Limit harian %s melebihi batas maksimum %s.", limitType, ceiling.DailyLimit.String()),
			}
		}
		if ceiling.PerTransactionLimit != nil && update.PerTransactionLimit != nil {
			if update.PerTransactionLimit.GreaterThan(*ceiling.PerTransactionLimit) {
				return apperr.Error{
					Status:  422,
					Code:    apperr.ValidationError.Code,
					Message: fmt.Sprintf("Limit per transaksi %s melebihi batas maksimum %s.", limitType, ceiling.PerTransactionLimit.String()),
				}
			}
		}

		if err := s.limits.UpdateLimit(ctx, userID, limitType, update); err != nil {
			return fmt.Errorf("update limit %s: %w", limitType, err)
		}
	}

	// Invalidate dashboard cache after limit change
	if s.versions != nil {
		_, _ = s.versions.IncrVersion(ctx, "dashboard", userID)
	}

	return nil
}

// ListNotifications returns paginated notifications.
func (s *Service) ListNotifications(ctx context.Context, userID uuid.UUID, cursor *uuid.UUID, limit int) (*NotificationListResponse, bool, string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	ver, _ := s.versions.GetVersion(ctx, "notif", userID)
	cursorHash := cursorToHash(cursor)

	if s.notifCache != nil {
		cached, err := s.notifCache.GetNotifications(ctx, userID, ver, cursorHash)
		if err != nil {
			slog.Warn("notif cache get failed", "error", err)
		}
		if cached != nil {
			// Reconstruct has_more and next cursor from cached data
			hasMore := len(cached.Notifications) > limit
			var nextCursor string
			items := cached.Notifications
			if hasMore {
				items = items[:limit]
				nextCursor = items[limit-1].ID
			}
			cached.Notifications = items
			return cached, hasMore, nextCursor, nil
		}
	}

	// Fetch limit+1 to determine has_more
	rows, err := s.notifications.ListByUserID(ctx, userID, cursor, limit+1)
	if err != nil {
		return nil, false, "", fmt.Errorf("list notifications: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	items := make([]NotificationItem, len(rows))
	for i, n := range rows {
		items[i] = toNotificationItem(n)
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}

	resp := &NotificationListResponse{Notifications: items}

	if s.notifCache != nil {
		if err := s.notifCache.SetNotifications(ctx, userID, ver, cursorHash, resp); err != nil {
			slog.Warn("notif cache set failed", "error", err)
		}
	}

	return resp, hasMore, nextCursor, nil
}

// MarkNotificationRead marks a single notification as read.
func (s *Service) MarkNotificationRead(ctx context.Context, userID, notifID uuid.UUID) error {
	if err := s.notifications.MarkRead(ctx, userID, notifID); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}

	// Invalidate notification + dashboard caches
	if s.versions != nil {
		_, _ = s.versions.IncrVersion(ctx, "notif", userID)
		_, _ = s.versions.IncrVersion(ctx, "dashboard", userID)
	}

	return nil
}

// MarkAllNotificationsRead marks all notifications as read.
func (s *Service) MarkAllNotificationsRead(ctx context.Context, userID uuid.UUID) error {
	if err := s.notifications.MarkAllRead(ctx, userID); err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}

	if s.versions != nil {
		_, _ = s.versions.IncrVersion(ctx, "notif", userID)
		_, _ = s.versions.IncrVersion(ctx, "dashboard", userID)
	}

	return nil
}

func toAccountBalance(a Account) AccountBalance {
	return AccountBalance{
		AccountID:        a.ID.String(),
		AccountNumber:    a.AccountNumber,
		AccountType:      a.AccountType,
		AccountLabel:     a.AccountLabel,
		Currency:         a.Currency,
		Balance:          a.Balance.String(),
		AvailableBalance: a.AvailableBalance.String(),
		HoldAmount:       a.HoldAmount.String(),
		IsPrimary:        a.IsPrimary,
	}
}

func toNotificationItem(n Notification) NotificationItem {
	item := NotificationItem{
		ID:        n.ID.String(),
		Type:      n.Type,
		Title:     n.Title,
		Body:      n.Body,
		IsRead:    n.IsRead,
		Metadata:  n.Metadata,
		CreatedAt: n.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if n.DeepLink != nil {
		item.DeepLink = *n.DeepLink
	}
	if n.ReadAt != nil {
		item.ReadAt = n.ReadAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return item
}

func cursorToHash(cursor *uuid.UUID) string {
	if cursor == nil {
		return "first"
	}
	h := sha256.Sum256([]byte(cursor.String()))
	return hex.EncodeToString(h[:8])
}