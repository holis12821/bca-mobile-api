package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

// Rate limit constants for authentication endpoints.
// Three mandatory layers — see §2.2 runbook and §10 skill.
const (
	LoginDeviceLimit  int64         = 5
	LoginDeviceWindow time.Duration = 15 * time.Minute

	// Per-IP rate limit is NOT optional. Because login resolves the user
	// from device_id alone (migration 000009), an attacker who guesses a
	// victim's device_id can lock their account by failing PIN five times.
	// Per-IP limiting caps the damage: a single IP can attempt at most 20
	// logins in 15 minutes, regardless of how many device_ids it targets.
	LoginIPLimit  int64         = 20
	LoginIPWindow time.Duration = 15 * time.Minute

	// PIN verify shares the lockout counter with login.
	// 5 failures (combined login + PIN verify) within 15 minutes → lockout.
	PINVerifyUserLimit  int64         = 5
	PINVerifyUserWindow time.Duration = 15 * time.Minute
)

// Rate limit key builders
func loginDeviceKey(deviceID string) string { return "rate:login:dev:" + deviceID }
func loginIPKey(ip string) string           { return "rate:login:ip:" + ip }
func pinVerifyUserKey(userID string) string  { return "rate:pin:" + userID }

// AuthRateLimiter provides rate limiting for authentication endpoints.
// Implements auth.RateLimiter.
// Redis failure → fail-closed (deny). This is a deliberate choice:
// allowing unauthenticated traffic when rate state is unknown is worse
// than a brief service disruption.
type AuthRateLimiter struct {
	limiter *RateLimiter
}

func NewAuthRateLimiter(limiter *RateLimiter) *AuthRateLimiter {
	return &AuthRateLimiter{limiter: limiter}
}

// CheckLoginDevice checks the per-device login rate limit (5 / 15 min).
func (a *AuthRateLimiter) CheckLoginDevice(ctx context.Context, deviceID string) (*auth.RateLimitResult, error) {
	result, err := a.limiter.Allow(ctx, loginDeviceKey(deviceID), LoginDeviceLimit, LoginDeviceWindow)
	if err != nil {
		return nil, fmt.Errorf("check login device rate: %w", err)
	}
	return toAuthResult(result), nil
}

// CheckLoginIP checks the per-IP login rate limit (20 / 15 min).
func (a *AuthRateLimiter) CheckLoginIP(ctx context.Context, ip string) (*auth.RateLimitResult, error) {
	result, err := a.limiter.Allow(ctx, loginIPKey(ip), LoginIPLimit, LoginIPWindow)
	if err != nil {
		return nil, fmt.Errorf("check login ip rate: %w", err)
	}
	return toAuthResult(result), nil
}

// CheckPINVerify checks the per-user PIN verify rate limit (5 / 15 min).
func (a *AuthRateLimiter) CheckPINVerify(ctx context.Context, userID string) (*auth.RateLimitResult, error) {
	result, err := a.limiter.Allow(ctx, pinVerifyUserKey(userID), PINVerifyUserLimit, PINVerifyUserWindow)
	if err != nil {
		return nil, fmt.Errorf("check pin verify rate: %w", err)
	}
	return toAuthResult(result), nil
}

// ResetLoginDevice clears the device rate limit (e.g., after successful login).
func (a *AuthRateLimiter) ResetLoginDevice(ctx context.Context, deviceID string) error {
	return a.limiter.Reset(ctx, loginDeviceKey(deviceID))
}

func toAuthResult(r *RateLimitResult) *auth.RateLimitResult {
	return &auth.RateLimitResult{
		Allowed:   r.Allowed,
		Current:   r.Current,
		Limit:     r.Limit,
		Remaining: r.Remaining,
		RetryAt:   r.RetryAt,
	}
}