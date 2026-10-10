package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

const challengeTTL = 60 * time.Second

// BiometricChallengeCache stores challenges in Redis with TTL 60s.
// Key pattern: bio_challenge:{challenge_id}
type BiometricChallengeCache struct {
	client *goredis.Client
}

func NewBiometricChallengeCache(client *goredis.Client) *BiometricChallengeCache {
	return &BiometricChallengeCache{client: client}
}

func challengeKey(challengeID string) string {
	return fmt.Sprintf("bio_challenge:%s", challengeID)
}

// StoreChallenge stores challenge data with TTL 60 seconds.
func (c *BiometricChallengeCache) StoreChallenge(ctx context.Context, challengeID string, data *auth.ChallengeData) error {
	val, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal challenge: %w", err)
	}
	return c.client.Set(ctx, challengeKey(challengeID), val, challengeTTL).Err()
}

// ConsumeChallenge atomically retrieves and deletes the challenge using GETDEL.
// GETDEL is atomic — a challenge read then deleted separately can be replayed
// in the gap between the two operations.
// Returns nil if not found (expired or already consumed).
func (c *BiometricChallengeCache) ConsumeChallenge(ctx context.Context, challengeID string) (*auth.ChallengeData, error) {
	val, err := c.client.GetDel(ctx, challengeKey(challengeID)).Result()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consume challenge: %w", err)
	}

	var data auth.ChallengeData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, fmt.Errorf("unmarshal challenge: %w", err)
	}
	return &data, nil
}

// --- Onboarding liveness ---

// OnboardingLivenessCache stores liveness challenges and the attempt counters.
//
// Both live in Redis rather than Postgres because both are short-lived and
// read on every attempt, and because the single-use guarantee needs an atomic
// read-and-delete, which GETDEL gives for free.
type OnboardingLivenessCache struct {
	client *goredis.Client
	cfg    LivenessPolicy
}

// LivenessPolicy is the subset of the liveness configuration this store needs.
//
// Passed in rather than read from a package-level constant so the numbers come
// from one place — the domain's LivenessConfig — and the counters cannot drift
// from the limits the services enforce.
type LivenessPolicy struct {
	ChallengeTTL      time.Duration
	MaxFailures       int
	CooldownDuration  time.Duration
	MaxCooldownRounds int
	BlockWindow       time.Duration
}

func NewOnboardingLivenessCache(client *goredis.Client, cfg LivenessPolicy) *OnboardingLivenessCache {
	if cfg.ChallengeTTL <= 0 {
		cfg.ChallengeTTL = 60 * time.Second
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 3
	}
	if cfg.CooldownDuration <= 0 {
		cfg.CooldownDuration = 5 * time.Minute
	}
	if cfg.MaxCooldownRounds <= 0 {
		cfg.MaxCooldownRounds = 2
	}
	if cfg.BlockWindow <= 0 {
		cfg.BlockWindow = 24 * time.Hour
	}
	return &OnboardingLivenessCache{client: client, cfg: cfg}
}

func livenessChallengeKey(challengeID string) string {
	return "liveness:challenge:" + challengeID
}

// Counters are keyed by session AND device. Either alone is not enough: a session
// id can be driven from a second phone, and a device can start a fresh session.
func livenessFailKey(sessionID, deviceID string) string {
	return fmt.Sprintf("liveness:fail:%s:%s", sessionID, deviceID)
}

func livenessCooldownKey(sessionID, deviceID string) string {
	return fmt.Sprintf("liveness:cooldown:%s:%s", sessionID, deviceID)
}

func livenessRoundsKey(sessionID, deviceID string) string {
	return fmt.Sprintf("liveness:rounds:%s:%s", sessionID, deviceID)
}

// Store writes the challenge with the TTL that is its lifetime.
//
// The TTL is the expiry: there is no separate sweep, and a challenge that outlives
// its Redis key cannot be consumed, which is the correct outcome.
func (c *OnboardingLivenessCache) Store(ctx context.Context, challenge *onboarding.LivenessChallenge) error {
	val, err := json.Marshal(challenge)
	if err != nil {
		return fmt.Errorf("marshal liveness challenge: %w", err)
	}
	ttl := time.Until(challenge.ExpiresAt)
	if ttl <= 0 {
		ttl = c.cfg.ChallengeTTL
	}
	return c.client.Set(ctx, livenessChallengeKey(challenge.ChallengeID), val, ttl).Err()
}

// Consume reads and deletes in one operation.
//
// GETDEL, not GET then DEL: a challenge read and deleted separately can be
// replayed in the window between the two, which is exactly the replay this is
// here to prevent. Returns nil, nil when unknown, expired, or already spent —
// the caller must not be able to tell those apart.
func (c *OnboardingLivenessCache) Consume(
	ctx context.Context,
	challengeID string,
) (*onboarding.LivenessChallenge, error) {
	val, err := c.client.GetDel(ctx, livenessChallengeKey(challengeID)).Result()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consume liveness challenge: %w", err)
	}

	var challenge onboarding.LivenessChallenge
	if err := json.Unmarshal([]byte(val), &challenge); err != nil {
		return nil, fmt.Errorf("unmarshal liveness challenge: %w", err)
	}
	return &challenge, nil
}

// Status reports the current cooldown and block state.
func (c *OnboardingLivenessCache) Status(
	ctx context.Context,
	sessionID, deviceID string,
) (*onboarding.LivenessAttemptStatus, error) {
	pipe := c.client.Pipeline()
	failures := pipe.Get(ctx, livenessFailKey(sessionID, deviceID))
	cooldown := pipe.TTL(ctx, livenessCooldownKey(sessionID, deviceID))
	rounds := pipe.Get(ctx, livenessRoundsKey(sessionID, deviceID))
	if _, err := pipe.Exec(ctx); err != nil && err != goredis.Nil {
		return nil, fmt.Errorf("read liveness attempt status: %w", err)
	}

	status := &onboarding.LivenessAttemptStatus{
		Failures:       intOrZero(failures.Val()),
		CooldownRounds: intOrZero(rounds.Val()),
	}
	if ttl := cooldown.Val(); ttl > 0 {
		status.RetryAfter = ttl
	}
	// Blocked outlives the cooldown: once the rounds are used up, waiting does not
	// help any more, and the client must stop offering a retry.
	status.Blocked = status.CooldownRounds >= c.cfg.MaxCooldownRounds
	return status, nil
}

// RecordFailure increments the counter and starts a cooldown when it is reached.
func (c *OnboardingLivenessCache) RecordFailure(
	ctx context.Context,
	sessionID, deviceID string,
) (*onboarding.LivenessAttemptStatus, error) {
	failKey := livenessFailKey(sessionID, deviceID)

	failures, err := c.client.Incr(ctx, failKey).Result()
	if err != nil {
		return nil, fmt.Errorf("increment liveness failures: %w", err)
	}
	if failures == 1 {
		// The expiry is set on first failure only, so the window does not slide
		// forward with every attempt — otherwise a steady drip of failures never
		// expires the counter.
		if err := c.client.Expire(ctx, failKey, c.cfg.BlockWindow).Err(); err != nil {
			return nil, fmt.Errorf("expire liveness failures: %w", err)
		}
	}

	status := &onboarding.LivenessAttemptStatus{Failures: int(failures)}

	if int(failures) < c.cfg.MaxFailures {
		return status, nil
	}

	// The round is complete: reset the per-round counter, count the round, and
	// start the cooldown.
	rounds, err := c.client.Incr(ctx, livenessRoundsKey(sessionID, deviceID)).Result()
	if err != nil {
		return nil, fmt.Errorf("increment liveness rounds: %w", err)
	}
	if rounds == 1 {
		if err := c.client.Expire(
			ctx, livenessRoundsKey(sessionID, deviceID), c.cfg.BlockWindow,
		).Err(); err != nil {
			return nil, fmt.Errorf("expire liveness rounds: %w", err)
		}
	}
	if err := c.client.Del(ctx, failKey).Err(); err != nil {
		return nil, fmt.Errorf("reset liveness failures: %w", err)
	}

	status.Failures = 0
	status.CooldownRounds = int(rounds)
	status.Blocked = int(rounds) >= c.cfg.MaxCooldownRounds

	if status.Blocked {
		// No cooldown is offered once blocked: the recovery is agent verification,
		// not waiting, and showing a countdown would promise something untrue.
		return status, nil
	}

	cooldownTTL := c.cfg.CooldownDuration
	if err := c.client.Set(
		ctx, livenessCooldownKey(sessionID, deviceID), "1", cooldownTTL,
	).Err(); err != nil {
		return nil, fmt.Errorf("start liveness cooldown: %w", err)
	}
	status.RetryAfter = cooldownTTL
	return status, nil
}

// Reset clears the counters after a pass.
//
// The round counter is cleared too: the 24-hour window exists to stop repeated
// failed attempts, and a successful verification ends that story.
func (c *OnboardingLivenessCache) Reset(ctx context.Context, sessionID, deviceID string) error {
	return c.client.Del(ctx,
		livenessFailKey(sessionID, deviceID),
		livenessCooldownKey(sessionID, deviceID),
		livenessRoundsKey(sessionID, deviceID),
	).Err()
}

func intOrZero(raw string) int {
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}
