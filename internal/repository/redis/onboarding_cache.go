package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

func (c *OnboardingSessionCache) Store(ctx context.Context, session *onboarding.Session) error {
	data, err := json.Marshal(session)
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

	var session onboarding.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &session, nil
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

	// Set TTL on first increment
	if count == 1 {
		rl.client.Expire(ctx, key, ocrRateLimitWindow)
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

const otpAttemptWindow = 5 * time.Minute

func (c *OnboardingOTPCache) IncrAttempt(ctx context.Context, sessionID string) (int64, error) {
	key := otpAttemptKey(sessionID)
	count, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("incr otp attempt: %w", err)
	}
	if count == 1 {
		c.client.Expire(ctx, key, otpAttemptWindow)
	}
	return count, nil
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
		rl.client.Expire(ctx, key, bioRateLimitWindow)
	}
	return count <= bioRateLimitMax, nil
}

// OnboardingIdempotencyCache guards against duplicate onboarding submissions.
const onboardingIdemTTL = 24 * time.Hour

type OnboardingIdempotencyCache struct {
	client *goredis.Client
}

func NewOnboardingIdempotencyCache(client *goredis.Client) *OnboardingIdempotencyCache {
	return &OnboardingIdempotencyCache{client: client}
}

func onboardingIdemKey(key string) string {
	return fmt.Sprintf("onboarding:idem:%s", key)
}

func (c *OnboardingIdempotencyCache) Check(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, onboardingIdemKey(key)).Result()
	if err != nil {
		if err == goredis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("check idempotency: %w", err)
	}
	return val, nil
}

func (c *OnboardingIdempotencyCache) Store(ctx context.Context, key, responseJSON string) error {
	return c.client.Set(ctx, onboardingIdemKey(key), responseJSON, onboardingIdemTTL).Err()
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
		c.client.Expire(ctx, key, 25*time.Hour)
	}
	return count, nil
}
