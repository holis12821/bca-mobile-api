package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func TestBiometricChallengeCache_ConsumeWithGETDEL(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewBiometricChallengeCache(client)
	ctx := context.Background()

	challengeID := "test-challenge-001"
	data := &auth.ChallengeData{
		Challenge: []byte("0123456789abcdef0123456789abcdef"),
		DeviceID:  "device-001",
	}

	if err := cache.StoreChallenge(ctx, challengeID, data); err != nil {
		t.Fatalf("store challenge: %v", err)
	}

	// First consume — should return data
	got, err := cache.ConsumeChallenge(ctx, challengeID)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got == nil {
		t.Fatal("expected challenge data, got nil")
	}
	if got.DeviceID != data.DeviceID {
		t.Fatalf("device_id mismatch: %s != %s", got.DeviceID, data.DeviceID)
	}
	if string(got.Challenge) != string(data.Challenge) {
		t.Fatal("challenge bytes mismatch")
	}

	// Second consume — GETDEL already removed it, must return nil
	got2, err := cache.ConsumeChallenge(ctx, challengeID)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if got2 != nil {
		t.Fatal("expected nil on second consume (GETDEL), got data")
	}
}

func TestBiometricChallengeCache_NotFound(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewBiometricChallengeCache(client)
	ctx := context.Background()

	got, err := cache.ConsumeChallenge(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent challenge")
	}
}

// --- Onboarding liveness ---

func livenessTestPolicy() redisrepo.LivenessPolicy {
	// Short durations so the cooldown and the 24-hour window are both observable
	// inside a test, while keeping the SHAPE of the policy identical to production:
	// three failures per round, two rounds, then blocked.
	return redisrepo.LivenessPolicy{
		ChallengeTTL:      2 * time.Second,
		MaxFailures:       3,
		CooldownDuration:  2 * time.Second,
		MaxCooldownRounds: 2,
		BlockWindow:       30 * time.Second,
	}
}

func livenessTestChallenge(id string) *onboarding.LivenessChallenge {
	now := time.Now().UTC()
	return &onboarding.LivenessChallenge{
		ChallengeID:     id,
		Nonce:           "nonce-" + id,
		SessionID:       "onb_test",
		DeviceID:        "dev_test",
		DeviceKeyID:     "key_test",
		DevicePublicKey: "pubkey",
		Actions: []onboarding.LivenessAction{
			onboarding.ActionTurnLeft,
			onboarding.ActionBlink,
			onboarding.ActionLookUp,
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(2 * time.Second),
	}
}

// The single-use guarantee rests entirely on GETDEL being atomic. This is the
// test that proves a replayed challenge_id finds nothing.
func TestOnboardingLivenessCache_ConsumeIsSingleUse(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewOnboardingLivenessCache(client, livenessTestPolicy())
	ctx := context.Background()

	challenge := livenessTestChallenge("chl_001")
	if err := cache.Store(ctx, challenge); err != nil {
		t.Fatalf("store: %v", err)
	}

	got, err := cache.Consume(ctx, challenge.ChallengeID)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got == nil {
		t.Fatal("expected the challenge back on first consume")
	}
	if got.Nonce != challenge.Nonce || got.DeviceID != challenge.DeviceID {
		t.Fatalf("challenge round-tripped wrong: %+v", got)
	}
	if len(got.Actions) != 3 || got.Actions[1] != onboarding.ActionBlink {
		t.Fatalf("actions must survive the round trip in order: %v", got.Actions)
	}

	// Second consume is the replay, and it must come back empty.
	again, err := cache.Consume(ctx, challenge.ChallengeID)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if again != nil {
		t.Fatal("a consumed challenge was returned a second time; replay is possible")
	}
}

func TestOnboardingLivenessCache_UnknownChallengeIsNotAnError(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewOnboardingLivenessCache(client, livenessTestPolicy())

	// nil, nil rather than an error: the caller must not be able to tell an
	// unknown id from an expired or already-spent one.
	got, err := cache.Consume(context.Background(), "chl_never_issued")
	if err != nil {
		t.Fatalf("an unknown challenge must not be an error: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for an unknown challenge")
	}
}

// The TTL is the expiry; there is no sweep. A challenge that outlives its key
// cannot be consumed, which is the correct outcome.
func TestOnboardingLivenessCache_ChallengeExpires(t *testing.T) {
	client := setupTestRedis(t)
	policy := livenessTestPolicy()
	policy.ChallengeTTL = 500 * time.Millisecond
	cache := redisrepo.NewOnboardingLivenessCache(client, policy)
	ctx := context.Background()

	challenge := livenessTestChallenge("chl_expiring")
	challenge.ExpiresAt = time.Now().UTC().Add(400 * time.Millisecond)
	if err := cache.Store(ctx, challenge); err != nil {
		t.Fatalf("store: %v", err)
	}

	time.Sleep(900 * time.Millisecond)

	got, err := cache.Consume(ctx, challenge.ChallengeID)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got != nil {
		t.Fatal("an expired challenge must not be consumable")
	}
}

// Decision Q6 step 1: three failures start a five-minute cooldown.
func TestOnboardingLivenessCache_ThreeFailuresStartCooldown(t *testing.T) {
	client := setupTestRedis(t)
	policy := livenessTestPolicy()
	cache := redisrepo.NewOnboardingLivenessCache(client, policy)
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		status, err := cache.RecordFailure(ctx, "onb_test", "dev_test")
		if err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if status.RetryAfter != 0 {
			t.Fatalf("failure %d must not start a cooldown yet, got %s", i, status.RetryAfter)
		}
		if status.Failures != i {
			t.Fatalf("want %d failures, got %d", i, status.Failures)
		}
	}

	status, err := cache.RecordFailure(ctx, "onb_test", "dev_test")
	if err != nil {
		t.Fatalf("third failure: %v", err)
	}
	if status.RetryAfter != policy.CooldownDuration {
		t.Fatalf("the third failure must start a cooldown, got %s", status.RetryAfter)
	}
	if status.CooldownRounds != 1 {
		t.Fatalf("want 1 completed round, got %d", status.CooldownRounds)
	}
	if status.Blocked {
		t.Fatal("one round must not block; the customer may still retry after waiting")
	}

	// Status reports the remaining time, which is the number the client displays.
	read, err := cache.Status(ctx, "onb_test", "dev_test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if read.RetryAfter <= 0 || read.RetryAfter > policy.CooldownDuration {
		t.Fatalf("want a remaining cooldown within the window, got %s", read.RetryAfter)
	}
	if read.CooldownRounds != 1 {
		t.Fatalf("want 1 round in status, got %d", read.CooldownRounds)
	}
}

// Decision Q6 step 2: two rounds (six failures) in the window stop self-service.
func TestOnboardingLivenessCache_TwoRoundsBlock(t *testing.T) {
	client := setupTestRedis(t)
	policy := livenessTestPolicy()
	cache := redisrepo.NewOnboardingLivenessCache(client, policy)
	ctx := context.Background()

	var status *onboarding.LivenessAttemptStatus
	for i := 0; i < 6; i++ {
		var err error
		status, err = cache.RecordFailure(ctx, "onb_test", "dev_test")
		if err != nil {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}

	if !status.Blocked {
		t.Fatalf("six failures must block self-service liveness, got %+v", status)
	}
	if status.CooldownRounds != 2 {
		t.Fatalf("want 2 rounds, got %d", status.CooldownRounds)
	}
	// No cooldown is offered once blocked: waiting does not help any more, and a
	// countdown would promise something untrue.
	if status.RetryAfter != 0 {
		t.Fatalf("a blocked customer must not be shown a countdown, got %s", status.RetryAfter)
	}

	read, err := cache.Status(ctx, "onb_test", "dev_test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !read.Blocked {
		t.Fatal("Status must keep reporting Blocked after the cooldown key expires")
	}
}

// Blocked must outlive the cooldown. If it did not, waiting out the last cooldown
// would quietly hand back unlimited attempts.
func TestOnboardingLivenessCache_BlockOutlivesCooldown(t *testing.T) {
	client := setupTestRedis(t)
	policy := livenessTestPolicy()
	policy.CooldownDuration = 500 * time.Millisecond
	cache := redisrepo.NewOnboardingLivenessCache(client, policy)
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		if _, err := cache.RecordFailure(ctx, "onb_test", "dev_test"); err != nil {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}

	time.Sleep(900 * time.Millisecond)

	read, err := cache.Status(ctx, "onb_test", "dev_test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !read.Blocked {
		t.Fatal("the block was lifted once the cooldown expired")
	}
}

// Counters are keyed by session AND device. Either alone is not enough: a session
// id can be driven from a second phone, and a device can start a fresh session.
func TestOnboardingLivenessCache_CountersAreScopedPerSessionAndDevice(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewOnboardingLivenessCache(client, livenessTestPolicy())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := cache.RecordFailure(ctx, "onb_a", "dev_a"); err != nil {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}

	for _, pair := range [][2]string{{"onb_a", "dev_b"}, {"onb_b", "dev_a"}} {
		status, err := cache.Status(ctx, pair[0], pair[1])
		if err != nil {
			t.Fatalf("status %v: %v", pair, err)
		}
		if status.RetryAfter != 0 || status.CooldownRounds != 0 {
			t.Fatalf("%v must not inherit another pair's counters: %+v", pair, status)
		}
	}
}

// A pass ends the story, including the 24-hour round counter.
func TestOnboardingLivenessCache_ResetClearsEverything(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewOnboardingLivenessCache(client, livenessTestPolicy())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := cache.RecordFailure(ctx, "onb_test", "dev_test"); err != nil {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	if err := cache.Reset(ctx, "onb_test", "dev_test"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	status, err := cache.Status(ctx, "onb_test", "dev_test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Failures != 0 || status.CooldownRounds != 0 ||
		status.RetryAfter != 0 || status.Blocked {
		t.Fatalf("reset must clear every counter, got %+v", status)
	}
}

// The failure window must not slide forward with every attempt: the expiry is set
// on the FIRST failure only. Otherwise a steady drip of failures never expires.
func TestOnboardingLivenessCache_FailureWindowDoesNotSlide(t *testing.T) {
	client := setupTestRedis(t)
	policy := livenessTestPolicy()
	policy.BlockWindow = 2 * time.Second
	cache := redisrepo.NewOnboardingLivenessCache(client, policy)
	ctx := context.Background()

	if _, err := cache.RecordFailure(ctx, "onb_test", "dev_test"); err != nil {
		t.Fatalf("first failure: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := cache.RecordFailure(ctx, "onb_test", "dev_test"); err != nil {
		t.Fatalf("second failure: %v", err)
	}

	// Past the original window, the counter must be gone even though a failure
	// landed in between.
	time.Sleep(1200 * time.Millisecond)
	status, err := cache.Status(ctx, "onb_test", "dev_test")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Failures != 0 {
		t.Fatalf("the window slid forward; want 0 failures after it passed, got %d", status.Failures)
	}
}
