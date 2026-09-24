package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

const onboardingSessionTTL = 24 * time.Hour

type OnboardingSessionCache struct {
	client *goredis.Client
}

func NewOnboardingSessionCache(client *goredis.Client) *OnboardingSessionCache {
	return &OnboardingSessionCache{client: client}
}

func onboardingSessionKey(sessionID string) string {
	return fmt.Sprintf("onboarding:session:%s", sessionID)
}

// cachedSession is the on-the-wire shape of a cached onboarding session.
//
// onboarding.Session is shaped for API responses: device_id, the internal id,
// the TNC version and the timestamps all carry `json:"-"`, so marshalling the
// domain struct straight into Redis silently dropped them. Every audit entry
// written from a cache hit then recorded an empty actor. This struct spells
// out what the cache stores, so the round trip is lossless by construction.
type cachedSession struct {
	ID             uuid.UUID                 `json:"id"`
	SessionID      string                    `json:"session_id"`
	DeviceID       string                    `json:"device_id"`
	ProductType    onboarding.ProductType    `json:"product_type"`
	CurrentStep    onboarding.Step           `json:"current_step"`
	TNCVersion     string                    `json:"tnc_version"`
	StepsCompleted onboarding.StepsCompleted `json:"steps_completed"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	ExpiresAt      time.Time                 `json:"expires_at"`
	DeletedAt      *time.Time                `json:"deleted_at,omitempty"`

	// Pilihan kartu ikut disimpan. Tanpa ketiga field ini, sesi yang dibaca
	// dari cache kehilangan kartunya — dan karena pembacaan MENDAHULUKAN cache,
	// itu berarti kartu yang baru dipilih nasabah hilang dari respons sampai
	// entri cache kedaluwarsa. Persis jenis kebocoran yang komentar di atas
	// dituliskan untuk dicegah.
	CardType           string     `json:"card_type,omitempty"`
	CardSelectedAt     *time.Time `json:"card_selected_at,omitempty"`
	CardCatalogVersion string     `json:"card_catalog_version,omitempty"`
}

func toCached(s *onboarding.Session) cachedSession {
	return cachedSession{
		ID:             s.ID,
		SessionID:      s.SessionID,
		DeviceID:       s.DeviceID,
		ProductType:    s.ProductType,
		CurrentStep:    s.CurrentStep,
		TNCVersion:     s.TNCVersion,
		StepsCompleted: s.StepsCompleted,
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
		ExpiresAt:      s.ExpiresAt,
		DeletedAt:      s.DeletedAt,

		CardType:           s.CardType,
		CardSelectedAt:     s.CardSelectedAt,
		CardCatalogVersion: s.CardCatalogVersion,
	}
}

func (c cachedSession) toDomain() *onboarding.Session {
	return &onboarding.Session{
		ID:             c.ID,
		SessionID:      c.SessionID,
		DeviceID:       c.DeviceID,
		ProductType:    c.ProductType,
		CurrentStep:    c.CurrentStep,
		TNCVersion:     c.TNCVersion,
		StepsCompleted: c.StepsCompleted,
		CreatedAt:      c.CreatedAt,
		UpdatedAt:      c.UpdatedAt,
		ExpiresAt:      c.ExpiresAt,
		DeletedAt:      c.DeletedAt,

		CardType:           c.CardType,
		CardSelectedAt:     c.CardSelectedAt,
		CardCatalogVersion: c.CardCatalogVersion,
	}
}

func (c *OnboardingSessionCache) Store(ctx context.Context, session *onboarding.Session) error {
	data, err := json.Marshal(toCached(session))
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	// TTL = remaining time until expiry, capped at 24h
	ttl := time.Until(session.ExpiresAt)
	if ttl <= 0 {
		return nil // already expired, don't cache
	}
	if ttl > onboardingSessionTTL {
		ttl = onboardingSessionTTL
	}

	key := onboardingSessionKey(session.SessionID)
	return c.client.Set(ctx, key, data, ttl).Err()
}

func (c *OnboardingSessionCache) Get(ctx context.Context, sessionID string) (*onboarding.Session, error) {
	key := onboardingSessionKey(sessionID)

	data, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get onboarding session: %w", err)
	}

	var cached cachedSession
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	// A session soft-deleted while its cache entry was still warm must not
	// keep working off that entry.
	if cached.DeletedAt != nil {
		return nil, nil
	}
	return cached.toDomain(), nil
}

func (c *OnboardingSessionCache) Delete(ctx context.Context, sessionID string) error {
	key := onboardingSessionKey(sessionID)
	return c.client.Del(ctx, key).Err()
}

// OCRRateLimiter implements sliding window rate limiting for OCR attempts.
const (
	ocrRateLimitMax    = 10
	ocrRateLimitWindow = 1 * time.Hour
)

type OCRRateLimiter struct {
	client *goredis.Client
}

func NewOCRRateLimiter(client *goredis.Client) *OCRRateLimiter {
	return &OCRRateLimiter{client: client}
}

func ocrRateLimitKey(sessionID string) string {
	return fmt.Sprintf("onboarding:ocr_rate:%s", sessionID)
}

func (rl *OCRRateLimiter) CheckOCRAttempt(ctx context.Context, sessionID string) (bool, error) {
	key := ocrRateLimitKey(sessionID)

	count, err := rl.client.Incr(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("incr ocr rate: %w", err)
	}

	// Set TTL on first increment. A counter without a TTL never resets, so a
	// failure here is repaired rather than ignored.
	if count == 1 {
		if err := rl.client.Expire(ctx, key, ocrRateLimitWindow).Err(); err != nil {
			slog.Error("set ocr rate limit ttl failed", "key", key, "error", err)
			if delErr := rl.client.Del(ctx, key).Err(); delErr != nil {
				slog.Error("drop ocr rate limit key failed", "key", key, "error", delErr)
			}
			return false, fmt.Errorf("set ocr rate ttl: %w", err)
		}
	}

	return count <= ocrRateLimitMax, nil
}

// OnboardingOTPCache manages OTP storage and attempt tracking.
type OnboardingOTPCache struct {
	client *goredis.Client
}

func NewOnboardingOTPCache(client *goredis.Client) *OnboardingOTPCache {
	return &OnboardingOTPCache{client: client}
}

func otpKey(sessionID string) string {
	return fmt.Sprintf("onboarding:otp:%s", sessionID)
}

func otpAttemptKey(sessionID string) string {
	return fmt.Sprintf("onboarding:otp_attempt:%s", sessionID)
}

func otpBlockKey(sessionID string) string {
	return fmt.Sprintf("onboarding:otp_block:%s", sessionID)
}

func otpResendKey(sessionID string) string {
	return fmt.Sprintf("onboarding:otp_resend:%s", sessionID)
}

func (c *OnboardingOTPCache) StoreOTP(ctx context.Context, sessionID, otpHash string, ttl time.Duration) (time.Time, error) {
	key := otpKey(sessionID)
	expiresAt := time.Now().UTC().Add(ttl)
	if err := c.client.Set(ctx, key, otpHash, ttl).Err(); err != nil {
		return time.Time{}, fmt.Errorf("store otp: %w", err)
	}
	return expiresAt, nil
}

func (c *OnboardingOTPCache) GetOTP(ctx context.Context, sessionID string) (string, error) {
	key := otpKey(sessionID)
	hash, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if err == goredis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("get otp: %w", err)
	}
	return hash, nil
}

func (c *OnboardingOTPCache) DeleteOTP(ctx context.Context, sessionID string) error {
	return c.client.Del(ctx, otpKey(sessionID)).Err()
}

// otpAttemptWindow must be at least as long as the lockout it feeds
// (otpBlockTime, 30m). A shorter window would let an attacker refill their
// attempt budget simply by waiting.
const otpAttemptWindow = 30 * time.Minute

func (c *OnboardingOTPCache) IncrAttempt(ctx context.Context, sessionID string) (int64, error) {
	key := otpAttemptKey(sessionID)
	count, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("incr otp attempt: %w", err)
	}
	if count == 1 {
		if err := c.client.Expire(ctx, key, otpAttemptWindow).Err(); err != nil {
			// Fail loud: a counter with no expiry would lock the session out
			// of OTP verification permanently.
			slog.Error("set otp attempt ttl failed", "session_id", sessionID, "error", err)
			return count, fmt.Errorf("set otp attempt ttl: %w", err)
		}
	}
	return count, nil
}

// ResetAttempts clears the failure counter once a code verifies.
func (c *OnboardingOTPCache) ResetAttempts(ctx context.Context, sessionID string) error {
	return c.client.Del(ctx, otpAttemptKey(sessionID)).Err()
}

func (c *OnboardingOTPCache) IsBlocked(ctx context.Context, sessionID string) (bool, error) {
	key := otpBlockKey(sessionID)
	exists, err := c.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("check otp block: %w", err)
	}
	return exists > 0, nil
}

func (c *OnboardingOTPCache) Block(ctx context.Context, sessionID string, duration time.Duration) error {
	key := otpBlockKey(sessionID)
	return c.client.Set(ctx, key, "1", duration).Err()
}

func (c *OnboardingOTPCache) BlockRemaining(ctx context.Context, sessionID string) (time.Duration, error) {
	key := otpBlockKey(sessionID)
	ttl, err := c.client.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("ttl otp block: %w", err)
	}
	if ttl <= 0 {
		return 0, nil
	}
	return ttl, nil
}

// otpResendWindow is the fixed hour a session's resend quota lives in. It
// starts at the first resend, not at the first OTP: the counter is cleared
// when personal-data issues the opening code, so every entry into OTP_VERIFY
// begins with a full quota.
const otpResendWindow = time.Hour

// IncrResend counts one resend against the session's hourly quota and returns
// the new total. Counting before the SMS goes out is deliberate — the quota
// exists to cap SMS cost and abuse, and a check-then-increment pair would let
// two parallel requests both pass on the last remaining slot.
func (c *OnboardingOTPCache) IncrResend(ctx context.Context, sessionID string) (int64, error) {
	key := otpResendKey(sessionID)
	count, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("incr otp resend: %w", err)
	}
	if count == 1 {
		if err := c.client.Expire(ctx, key, otpResendWindow).Err(); err != nil {
			// Fail loud: a quota counter with no expiry would deny the
			// nasabah a resend for the rest of the session's life.
			slog.Error("set otp resend ttl failed", "session_id", sessionID, "error", err)
			return count, fmt.Errorf("set otp resend ttl: %w", err)
		}
	}
	return count, nil
}

// ResendWindowRemaining is how long until the quota refills. Feeds
// details.retry_after_seconds on RATE_LIMIT_EXCEEDED.
func (c *OnboardingOTPCache) ResendWindowRemaining(ctx context.Context, sessionID string) (time.Duration, error) {
	ttl, err := c.client.TTL(ctx, otpResendKey(sessionID)).Result()
	if err != nil {
		return 0, fmt.Errorf("ttl otp resend: %w", err)
	}
	if ttl <= 0 {
		return 0, nil
	}
	return ttl, nil
}

// ResetResend clears the quota. Called when personal-data issues the first
// OTP of a step, so a resumed or re-submitted flow is not charged for the
// previous one's resends.
func (c *OnboardingOTPCache) ResetResend(ctx context.Context, sessionID string) error {
	return c.client.Del(ctx, otpResendKey(sessionID)).Err()
}

// BiometricRateLimiter rate limits biometric attempts per session.
const (
	bioRateLimitMax    int64 = 5
	bioRateLimitWindow       = 1 * time.Hour
)

type BiometricRateLimiter struct {
	client *goredis.Client
}

func NewBiometricRateLimiter(client *goredis.Client) *BiometricRateLimiter {
	return &BiometricRateLimiter{client: client}
}

func (rl *BiometricRateLimiter) CheckBiometricAttempt(ctx context.Context, sessionID string) (bool, error) {
	key := fmt.Sprintf("onboarding:bio_rate:%s", sessionID)
	count, err := rl.client.Incr(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("incr bio rate: %w", err)
	}
	if count == 1 {
		if err := rl.client.Expire(ctx, key, bioRateLimitWindow).Err(); err != nil {
			slog.Error("set biometric rate limit ttl failed", "key", key, "error", err)
			if delErr := rl.client.Del(ctx, key).Err(); delErr != nil {
				slog.Error("drop biometric rate limit key failed", "key", key, "error", delErr)
			}
			return false, fmt.Errorf("set bio rate ttl: %w", err)
		}
	}
	return count <= bioRateLimitMax, nil
}

// OnboardingIdempotencyCache guards against duplicate onboarding submissions.
//
// The key is namespaced by session_id. Idempotency-Key is client-chosen, so
// two sessions picking the same string is ordinary, not adversarial — an
// unscoped key would replay one nasabah's account details to another.
const (
	onboardingIdemTTL      = 24 * time.Hour
	onboardingIdemClaimTTL = 60 * time.Second
	onboardingIdemRunning  = "PROCESSING"
)

type OnboardingIdempotencyCache struct {
	client *goredis.Client
}

func NewOnboardingIdempotencyCache(client *goredis.Client) *OnboardingIdempotencyCache {
	return &OnboardingIdempotencyCache{client: client}
}

func onboardingIdemKey(sessionID, key string) string {
	return fmt.Sprintf("onboarding:idem:%s:%s", sessionID, key)
}

// Claim reserves the slot with SETNX — never GET-then-SET, which would let two
// concurrent submits both reach core banking and open two accounts.
func (c *OnboardingIdempotencyCache) Claim(ctx context.Context, sessionID, key string) (onboarding.IdempotencyClaim, error) {
	redisKey := onboardingIdemKey(sessionID, key)

	ok, err := c.client.SetNX(ctx, redisKey, onboardingIdemRunning, onboardingIdemClaimTTL).Result()
	if err != nil {
		return onboarding.IdempotencyClaim{}, fmt.Errorf("claim idempotency: %w", err)
	}
	if ok {
		return onboarding.IdempotencyClaim{}, nil
	}

	val, err := c.client.Get(ctx, redisKey).Result()
	if err == goredis.Nil {
		// Expired between SETNX and GET — treat as still running; the client retries.
		return onboarding.IdempotencyClaim{AlreadyClaimed: true, StillProcessing: true}, nil
	}
	if err != nil {
		return onboarding.IdempotencyClaim{}, fmt.Errorf("read idempotency slot: %w", err)
	}
	if val == onboardingIdemRunning {
		return onboarding.IdempotencyClaim{AlreadyClaimed: true, StillProcessing: true}, nil
	}
	return onboarding.IdempotencyClaim{AlreadyClaimed: true, StoredResponse: val}, nil
}

// Persist replaces the claim with the response, extending it to the full TTL.
func (c *OnboardingIdempotencyCache) Persist(ctx context.Context, sessionID, key, responseJSON string) error {
	return c.client.Set(ctx, onboardingIdemKey(sessionID, key), responseJSON, onboardingIdemTTL).Err()
}

// Release drops the claim so a failed submit can be retried.
func (c *OnboardingIdempotencyCache) Release(ctx context.Context, sessionID, key string) error {
	return c.client.Del(ctx, onboardingIdemKey(sessionID, key)).Err()
}

// VideoCallQueueCache manages the video call queue using a Redis sorted set.
const videoCallQueueKey = "onboarding:queue:active"

type VideoCallQueueCache struct {
	client *goredis.Client
}

func NewVideoCallQueueCache(client *goredis.Client) *VideoCallQueueCache {
	return &VideoCallQueueCache{client: client}
}

func (c *VideoCallQueueCache) Add(ctx context.Context, queueID string, score float64) error {
	return c.client.ZAdd(ctx, videoCallQueueKey, goredis.Z{
		Score:  score,
		Member: queueID,
	}).Err()
}

func (c *VideoCallQueueCache) Remove(ctx context.Context, queueID string) error {
	return c.client.ZRem(ctx, videoCallQueueKey, queueID).Err()
}

func (c *VideoCallQueueCache) Position(ctx context.Context, queueID string) (int64, error) {
	rank, err := c.client.ZRank(ctx, videoCallQueueKey, queueID).Result()
	if err != nil {
		if err == goredis.Nil {
			return 0, nil
		}
		return 0, fmt.Errorf("zrank: %w", err)
	}
	return rank + 1, nil // 1-based
}

func (c *VideoCallQueueCache) Length(ctx context.Context) (int64, error) {
	return c.client.ZCard(ctx, videoCallQueueKey).Result()
}

func (c *VideoCallQueueCache) IncrDailyCounter(ctx context.Context) (int64, error) {
	today := time.Now().Format("2006-01-02")
	key := fmt.Sprintf("onboarding:queue:counter:%s", today)
	count, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("incr daily counter: %w", err)
	}
	if count == 1 {
		// Expire at end of day + 1 hour buffer
		if err := c.client.Expire(ctx, key, 25*time.Hour).Err(); err != nil {
			slog.Error("set queue counter ttl failed", "key", key, "error", err)
		}
	}
	return count, nil
}
