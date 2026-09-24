package account

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
)

type Service struct {
	accounts      AccountRepository
	profiles      ProfileRepository
	limits        TransactionLimitRepository
	notifications NotificationRepository
	promotions    PromotionRepository
	profileCache  ProfileCache
	balanceCache  BalanceCache
	dashCache     DashboardCache
	notifCache    NotificationCache
	profileOTP    ProfileOTPCache
	sms           SMSGateway
	vtokens       VerificationTokenConsumer
	notifier      *notify.Notifier
	versions      VersionCounter
	devMode       bool
}

type ServiceConfig struct {
	Accounts      AccountRepository
	Profiles      ProfileRepository
	Limits        TransactionLimitRepository
	Notifications NotificationRepository
	Promotions    PromotionRepository
	ProfileCache  ProfileCache
	BalanceCache  BalanceCache
	DashCache     DashboardCache
	NotifCache    NotificationCache
	ProfileOTP    ProfileOTPCache
	SMS           SMSGateway
	VTokens       VerificationTokenConsumer
	Notifier      *notify.Notifier
	Versions      VersionCounter
	DevMode       bool
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		accounts:      cfg.Accounts,
		profiles:      cfg.Profiles,
		limits:        cfg.Limits,
		notifications: cfg.Notifications,
		promotions:    cfg.Promotions,
		profileCache:  cfg.ProfileCache,
		balanceCache:  cfg.BalanceCache,
		dashCache:     cfg.DashCache,
		notifCache:    cfg.NotifCache,
		profileOTP:    cfg.ProfileOTP,
		sms:           cfg.SMS,
		vtokens:       cfg.VTokens,
		notifier:      cfg.Notifier,
		versions:      cfg.Versions,
		devMode:       cfg.DevMode,
	}
}

// SetVerificationTokenConsumer completes the wiring after construction.
//
// account.Service needs transaction.Service (to burn a CHANGE_LIMIT token) and
// transaction.Service needs account.AccountRepository, so one of the two edges
// has to be closed after both exist. This is that edge; the alternative is an
// import cycle.
func (s *Service) SetVerificationTokenConsumer(c VerificationTokenConsumer) {
	s.vtokens = c
}

// profileOTPTTL / profileOTPMaxFail bound the profile-change code the same way
// the onboarding flow bounds its own.
const (
	profileOTPTTL     = 5 * time.Minute
	profileOTPMaxFail = 5
)

// wib is the timezone every "today" in this service is measured in. Daily
// usage rolls over at midnight Jakarta, not midnight UTC.
var wib = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic("failed to load Asia/Jakarta: " + err.Error())
	}
	return loc
}()

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
	total := decimal.Zero
	currency := "IDR"
	for i, a := range accounts {
		b := toAccountBalance(a)
		balances[i] = b
		total = total.Add(a.Balance)
		if a.Currency != "" {
			currency = a.Currency
		}
		if a.IsPrimary {
			p := b
			primary = &p
		}
	}
	// No account is flagged primary → fall back to the first one rather than
	// rendering a Beranda with no balance at all.
	if primary == nil && len(balances) > 0 {
		primary = &balances[0]
	}

	primaryNumber := ""
	if primary != nil {
		primaryNumber = primary.AccountNumber
	}

	dashUser := DashboardUser{
		DisplayName:   profile.DisplayName,
		MaskedAccount: MaskAccountNumber(primaryNumber),
	}
	if profile.LastLoginAt != nil {
		dashUser.LastLoginAt = profile.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	// Promotions are best-effort: the Beranda still renders without them.
	var promos []PromotionItem
	if s.promotions != nil {
		rows, err := s.promotions.ListActive(ctx, 10)
		if err != nil {
			slog.Warn("list promotions failed", "error", err)
		} else {
			promos = make([]PromotionItem, 0, len(rows))
			for _, p := range rows {
				item := PromotionItem{
					ID:         p.ID.String(),
					Title:      p.Title,
					ImageURL:   p.ImageURL,
					ValidUntil: p.ValidUntil.In(wib).Format("2006-01-02"),
				}
				if p.DeepLink != nil {
					item.DeepLink = *p.DeepLink
				}
				promos = append(promos, item)
			}
		}
	}
	if promos == nil {
		promos = []PromotionItem{}
	}

	resp := &DashboardResponse{
		User: dashUser,
		Balance: DashboardBalance{
			Total:          total.StringFixed(2),
			Currency:       currency,
			PrimaryAccount: primaryNumber,
		},
		UnreadCount:  unreadCount,
		Promotions:   promos,
		QuickActions: DefaultQuickActions,

		PrimaryAccount:    primary,
		Accounts:          balances,
		UnreadCountLegacy: unreadCount,
	}

	if s.dashCache != nil {
		if err := s.dashCache.SetDashboard(ctx, userID, ver, resp); err != nil {
			slog.Warn("dashboard cache set failed", "error", err)
		}
	}

	return resp, nil
}

// UpdateTransactionLimit updates limits, enforcing server-side ceilings.
//
// A valid CHANGE_LIMIT verification token is mandatory: the daily ceiling is
// the last control standing between a stolen access token and the account
// balance, and this endpoint used to raise it on the access token alone.
func (s *Service) UpdateTransactionLimit(ctx context.Context, userID uuid.UUID, req UpdateLimitRequest) (*LimitStatusResponse, error) {
	if s.vtokens == nil {
		return nil, apperr.InternalError
	}
	if err := s.vtokens.ConsumeVerificationToken(ctx, userID, req.VerificationToken, "CHANGE_LIMIT"); err != nil {
		return nil, err
	}

	for limitType, update := range req.Limits {
		ceiling, ok := LimitCeilings[limitType]
		if !ok {
			return nil, apperr.ValidationError
		}

		if update.DailyLimit != nil && update.DailyLimit.LessThan(decimal.Zero) {
			return nil, apperr.ValidationError
		}
		if update.DailyLimit != nil && update.DailyLimit.GreaterThan(ceiling.DailyLimit) {
			return nil, apperr.Error{
				Status:  422,
				Code:    apperr.ValidationError.Code,
				Message: fmt.Sprintf("Limit harian %s melebihi batas maksimum %s.", limitType, ceiling.DailyLimit.String()),
			}
		}
		if ceiling.PerTransactionLimit != nil && update.PerTransactionLimit != nil {
			if update.PerTransactionLimit.GreaterThan(*ceiling.PerTransactionLimit) {
				return nil, apperr.Error{
					Status:  422,
					Code:    apperr.ValidationError.Code,
					Message: fmt.Sprintf("Limit per transaksi %s melebihi batas maksimum %s.", limitType, ceiling.PerTransactionLimit.String()),
				}
			}
		}

		if err := s.limits.UpdateLimit(ctx, userID, limitType, update); err != nil {
			return nil, fmt.Errorf("update limit %s: %w", limitType, err)
		}
	}

	// Invalidate dashboard cache after limit change
	if s.versions != nil {
		_, _ = s.versions.IncrVersion(ctx, "dashboard", userID)
	}

	if s.notifier != nil {
		s.notifier.Security(ctx, userID,
			"Limit transaksi diperbarui",
			"Limit transaksi akun Anda baru saja diubah. Jika ini bukan Anda, segera hubungi Halo BCA.",
		)
	}

	return s.GetTransactionLimits(ctx, userID)
}

// GetTransactionLimits returns the effective limits and today's usage, in the
// flat shape the API spec documents.
func (s *Service) GetTransactionLimits(ctx context.Context, userID uuid.UUID) (*LimitStatusResponse, error) {
	limits, err := s.limits.FindByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find limits: %w", err)
	}

	used, err := s.limits.UsedToday(ctx, userID, time.Now().In(wib).Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("find daily usage: %w", err)
	}

	out := make(map[string]string, len(limits)*2)
	for _, l := range limits {
		key := strings.ToLower(l.LimitType)
		out[key+"_daily"] = l.DailyLimit.StringFixed(2)
		out[key+"_used_today"] = used[l.LimitType].StringFixed(2)
		out[key+"_remaining_today"] = l.DailyLimit.Sub(used[l.LimitType]).StringFixed(2)
		if l.PerTransactionLimit != nil {
			out[key+"_per_transaction"] = l.PerTransactionLimit.StringFixed(2)
		}
	}

	return &LimitStatusResponse{Limits: out}, nil
}

// RequestProfileOTP issues a one-time code to the phone number on file.
//
// The code authorises PUT /account/profile. It is sent to the *registered*
// phone, never to an address supplied in the request — otherwise the check
// would be one an attacker could satisfy themselves.
func (s *Service) RequestProfileOTP(ctx context.Context, userID uuid.UUID) (*RequestProfileOTPResponse, error) {
	if s.profileOTP == nil {
		return nil, apperr.InternalError
	}

	profile, err := s.profiles.FindProfile(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find profile: %w", err)
	}
	if profile == nil {
		return nil, apperr.NotFound
	}
	if profile.Phone == "" {
		return nil, apperr.Error{
			Status:  422,
			Code:    "PROFILE_NO_PHONE",
			Message: "Nomor HP belum terdaftar. Hubungi Halo BCA.",
		}
	}

	code, err := generateNumericOTP(6)
	if err != nil {
		return nil, fmt.Errorf("generate otp: %w", err)
	}

	expiresAt, err := s.profileOTP.Store(ctx, userID, hashOTPCode(code), profileOTPTTL)
	if err != nil {
		return nil, fmt.Errorf("store profile otp: %w", err)
	}
	if err := s.profileOTP.ResetAttempts(ctx, userID); err != nil {
		slog.Warn("reset profile otp attempts failed", "error", err)
	}

	if s.sms != nil {
		if err := s.sms.SendOTP(ctx, profile.Phone, code); err != nil {
			slog.Error("send profile otp failed", "user_id", userID, "error", err)
		}
	}

	resp := &RequestProfileOTPResponse{
		SentTo:    MaskPhone(profile.Phone),
		ExpiresIn: int(time.Until(expiresAt).Seconds()),
	}
	if s.devMode {
		resp.OTPDebug = code
	}
	return resp, nil
}

// UpdateProfile updates the user's email after verifying a real OTP.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, req UpdateProfileRequest) error {
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		return apperr.ValidationError
	}
	if s.profileOTP == nil {
		return apperr.InternalError
	}

	storedHash, err := s.profileOTP.Get(ctx, userID)
	if err != nil {
		return fmt.Errorf("get profile otp: %w", err)
	}
	if storedHash == "" {
		return apperr.OTPExpired
	}

	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashOTPCode(req.OTPCode))) != 1 {
		attempts, incErr := s.profileOTP.IncrAttempt(ctx, userID)
		if incErr != nil {
			slog.Error("incr profile otp attempt failed", "error", incErr)
		}
		if attempts >= profileOTPMaxFail {
			// Burn the code instead of leaving a partially-guessed one alive.
			if delErr := s.profileOTP.Delete(ctx, userID); delErr != nil {
				slog.Error("delete profile otp failed", "error", delErr)
			}
			return apperr.OTPBlocked
		}
		return apperr.OTPInvalid
	}

	if err := s.profileOTP.Delete(ctx, userID); err != nil {
		slog.Warn("delete used profile otp failed", "error", err)
	}
	if err := s.profileOTP.ResetAttempts(ctx, userID); err != nil {
		slog.Warn("reset profile otp attempts failed", "error", err)
	}

	if err := s.profiles.UpdateEmail(ctx, userID, req.Email); err != nil {
		return fmt.Errorf("update email: %w", err)
	}

	if s.notifier != nil {
		s.notifier.Security(ctx, userID,
			"Email berhasil diperbarui",
			"Alamat email akun Anda baru saja diubah. Jika ini bukan Anda, segera hubungi Halo BCA.",
		)
	}

	// Invalidate profile and dashboard caches
	if s.versions != nil {
		_, _ = s.versions.IncrVersion(ctx, "profile", userID)
		_, _ = s.versions.IncrVersion(ctx, "dashboard", userID)
	}

	return nil
}

// UpdateSettings updates the user's settings flags.
func (s *Service) UpdateSettings(ctx context.Context, userID uuid.UUID, req UpdateSettingsRequest) error {
	if req.BiometricEnabled == nil && req.PushNotificationEnabled == nil && req.EmailStatementEnabled == nil {
		return apperr.ValidationError
	}

	if err := s.profiles.UpdateSettings(ctx, userID, req); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}

	// Invalidate profile cache
	if s.versions != nil {
		_, _ = s.versions.IncrVersion(ctx, "profile", userID)
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
