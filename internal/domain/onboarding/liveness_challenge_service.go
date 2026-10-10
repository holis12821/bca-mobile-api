package onboarding

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// LivenessConfig holds every tunable of the active liveness flow.
//
// All of it is configuration rather than constants because the numbers in the
// Phase 2 decisions (3 failures, 5 minutes, 6 per 24 hours, the score thresholds)
// are policy, and policy changes without a release.
type LivenessConfig struct {
	ChallengeTTL time.Duration

	// ActionCount is how many actions one challenge asks for. BLINK is always one
	// of them, so this must be at least 2 for the head poses to mean anything.
	ActionCount int

	// MaxFailures before a cooldown starts (decision Q6 step 1).
	MaxFailures int

	// CooldownDuration is how long the customer waits after MaxFailures.
	CooldownDuration time.Duration

	// MaxCooldownRounds before self-service liveness stops (decision Q6 step 2).
	MaxCooldownRounds int

	// BlockWindow is the rolling window the rounds are counted in.
	BlockWindow time.Duration

	LivenessThreshold  float64
	FaceMatchThreshold float64

	// MinFrameDistance is the minimum perceptual-hash Hamming distance between
	// any two frames. Identical or near-identical frames mean a still image was
	// submitted for every step.
	MinFrameDistance int

	// MaxClockSkew tolerates a device clock that is slightly off when checking
	// that step timestamps fall inside the challenge window.
	MaxClockSkew time.Duration

	// IntegrityLogOnly records a failing Play Integrity verdict without refusing.
	// Only ever true outside production (decision Q3).
	IntegrityLogOnly bool
}

// DefaultLivenessConfig carries the values from the Phase 2 decisions.
func DefaultLivenessConfig() LivenessConfig {
	return LivenessConfig{
		ChallengeTTL:       60 * time.Second,
		ActionCount:        3,
		MaxFailures:        3,
		CooldownDuration:   5 * time.Minute,
		MaxCooldownRounds:  2,
		BlockWindow:        24 * time.Hour,
		LivenessThreshold:  90.0,
		FaceMatchThreshold: 85.0,
		MinFrameDistance:   6,
		MaxClockSkew:       2 * time.Minute,
		IntegrityLogOnly:   false,
	}
}

// LivenessChallengeService issues the challenges the client must satisfy.
type LivenessChallengeService struct {
	sessions   SessionRepository
	cache      SessionCache
	challenges LivenessChallengeStore
	attempts   LivenessAttemptTracker
	audit      AuditRepository
	cfg        LivenessConfig
}

type LivenessChallengeServiceConfig struct {
	Sessions   SessionRepository
	Cache      SessionCache
	Challenges LivenessChallengeStore
	Attempts   LivenessAttemptTracker
	Audit      AuditRepository
	Liveness   LivenessConfig
}

func NewLivenessChallengeService(cfg LivenessChallengeServiceConfig) *LivenessChallengeService {
	liveness := cfg.Liveness
	if liveness.ActionCount == 0 {
		liveness = DefaultLivenessConfig()
	}
	return &LivenessChallengeService{
		sessions:   cfg.Sessions,
		cache:      cfg.Cache,
		challenges: cfg.Challenges,
		attempts:   cfg.Attempts,
		audit:      cfg.Audit,
		cfg:        liveness,
	}
}

// Config exposes the active policy so the verification path uses the same numbers.
func (s *LivenessChallengeService) Config() LivenessConfig { return s.cfg }

// Issue mints a single-use challenge bound to this device.
func (s *LivenessChallengeService) Issue(
	ctx context.Context,
	sessionID, deviceID, deviceKeyID, devicePublicKey string,
	ipAddress, userAgent string,
) (*IssuedChallenge, error) {
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepBiometric {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan BIOMETRIC.", session.CurrentStep),
		}
	}

	// The device the session belongs to is the only device that may be issued a
	// challenge for it. The handler already asserts this, but a challenge is what
	// binds the nonce, so the check is repeated where the binding is written.
	if deviceID == "" || deviceID != session.DeviceID {
		return nil, apperr.OnboardingDeviceMismatch
	}

	if err := validateDeviceKey(devicePublicKey); err != nil {
		return nil, err
	}

	// Cooldown and block are checked before a nonce is minted: issuing one during
	// a cooldown would let the customer burn the whole window collecting frames
	// that are refused at the end anyway.
	if err := s.assertAttemptAllowed(ctx, sessionID, deviceID); err != nil {
		return nil, err
	}

	actions, err := pickActions(s.cfg.ActionCount)
	if err != nil {
		return nil, fmt.Errorf("pick liveness actions: %w", err)
	}

	nonce, err := randomNonce()
	if err != nil {
		return nil, fmt.Errorf("mint liveness nonce: %w", err)
	}

	shortID, err := generateShortID()
	if err != nil {
		return nil, fmt.Errorf("generate challenge id: %w", err)
	}

	now := time.Now().UTC()
	challenge := &LivenessChallenge{
		ChallengeID:     "chl_" + shortID,
		Nonce:           nonce,
		SessionID:       sessionID,
		DeviceID:        deviceID,
		DeviceKeyID:     deviceKeyID,
		DevicePublicKey: devicePublicKey,
		Actions:         actions,
		IssuedAt:        now,
		ExpiresAt:       now.Add(s.cfg.ChallengeTTL),
	}

	if err := s.challenges.Store(ctx, challenge); err != nil {
		return nil, fmt.Errorf("store liveness challenge: %w", err)
	}

	// The action list is audited: without it there is no way to tell afterwards
	// whether a submission answered the challenge it claims to answer.
	s.writeAudit(ctx, sessionID, AuditBiometricUploaded, "system", map[string]any{
		"event":        "liveness_challenge_issued",
		"challenge_id": challenge.ChallengeID,
		"actions":      actionNames(actions),
		"device_id":    deviceID,
	}, ipAddress, userAgent)

	return &IssuedChallenge{
		ChallengeID: challenge.ChallengeID,
		Nonce:       challenge.Nonce,
		Actions:     challenge.Actions,
		ExpiresAt:   challenge.ExpiresAt,
	}, nil
}

// assertAttemptAllowed refuses while a cooldown or block is in force.
func (s *LivenessChallengeService) assertAttemptAllowed(ctx context.Context, sessionID, deviceID string) error {
	if s.attempts == nil {
		return nil
	}
	status, err := s.attempts.Status(ctx, sessionID, deviceID)
	if err != nil {
		// Failing open here would hand back an unlimited number of attempts
		// exactly when the counter store is the thing that is broken.
		slog.Error("liveness attempt status failed", "session_id", sessionID, "error", err)
		return apperr.InternalError
	}
	if status == nil {
		return nil
	}
	if status.Blocked {
		return apperr.LivenessBlocked
	}
	if status.RetryAfter > 0 {
		return livenessCooldownError(status.RetryAfter)
	}
	return nil
}

func (s *LivenessChallengeService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
	if s.cache != nil {
		session, err := s.cache.Get(ctx, sessionID)
		if err != nil {
			slog.Error("cache get session failed", "error", err)
		}
		if session != nil {
			if session.IsExpired() {
				return nil, apperr.OnboardingSessionExpired
			}
			return session, nil
		}
	}
	session, err := s.sessions.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if session == nil {
		return nil, apperr.OnboardingNotFound
	}
	if session.IsExpired() {
		return nil, apperr.OnboardingSessionExpired
	}
	return session, nil
}

func (s *LivenessChallengeService) writeAudit(
	ctx context.Context,
	sessionID string,
	eventType AuditEventType,
	actor string,
	details map[string]any,
	ip, ua string,
) {
	if s.audit == nil {
		return
	}
	entry := &AuditLog{
		ID:        uuid.New(),
		SessionID: sessionID,
		EventType: eventType,
		Actor:     actor,
		Details:   details,
		IPAddress: ip,
		UserAgent: ua,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.audit.Insert(ctx, entry); err != nil {
		slog.Error("liveness audit insert failed", "session_id", sessionID, "error", err)
	}
}

// --- Action selection ---

// pickActions returns BLINK plus distinct head poses, all in random order.
//
// Every draw goes through crypto/rand. math/rand would be a real weakness and not
// a theoretical one: the order is the whole defence against a pre-recorded video,
// and a predictable PRNG makes the order predictable to whoever is recording.
func pickActions(count int) ([]LivenessAction, error) {
	if count < 2 {
		count = 2
	}
	headCount := count - 1
	if headCount > len(HeadActions) {
		headCount = len(HeadActions)
	}

	pool := make([]LivenessAction, len(HeadActions))
	copy(pool, HeadActions)
	if err := secureShuffle(len(pool), func(i, j int) {
		pool[i], pool[j] = pool[j], pool[i]
	}); err != nil {
		return nil, err
	}

	// BLINK is always included; the rest are distinct head poses, so no action
	// repeats inside one challenge.
	actions := append([]LivenessAction{ActionBlink}, pool[:headCount]...)

	// Shuffling again is what moves BLINK out of a fixed slot. Without this it
	// would always be first, which is exactly the predictability being removed.
	if err := secureShuffle(len(actions), func(i, j int) {
		actions[i], actions[j] = actions[j], actions[i]
	}); err != nil {
		return nil, err
	}
	return actions, nil
}

// secureShuffle is Fisher-Yates driven by crypto/rand.
func secureShuffle(n int, swap func(i, j int)) error {
	for i := n - 1; i > 0; i-- {
		j, err := secureIntn(i + 1)
		if err != nil {
			return err
		}
		swap(i, j)
	}
	return nil
}

func secureIntn(max int) (int, error) {
	if max <= 0 {
		return 0, fmt.Errorf("secureIntn: max must be positive, got %d", max)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}

func randomNonce() (string, error) {
	buf := make([]byte, nonceBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func actionNames(actions []LivenessAction) []string {
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = string(a)
	}
	return names
}

// --- Device key ---

// validateDeviceKey refuses anything that is not an EC P-256 public key.
//
// Checked at issuance rather than at verification so a device that cannot produce
// a usable key finds out before the customer performs the movements.
func validateDeviceKey(publicKey string) error {
	if strings.TrimSpace(publicKey) == "" {
		return apperr.LivenessDeviceKeyInvalid
	}
	parsed, err := crypto.ParseECDSAPublicKey(publicKey)
	if err != nil {
		return apperr.LivenessDeviceKeyInvalid
	}
	if parsed.Curve != elliptic.P256() {
		return apperr.LivenessDeviceKeyInvalid
	}
	var _ *ecdsa.PublicKey = parsed
	return nil
}

// --- Canonical signed payload ---

// LivenessPayloadVersion must match LivenessPayload.VERSION in the Android client.
const LivenessPayloadVersion = "v1"

// livenessSignedPayload rebuilds, byte for byte, the string the client signed.
//
// Text rather than JSON on purpose: both sides must produce the *identical* string
// and no JSON library guarantees key order. One different space means the
// signature is rejected, so this function and
// `core/liveness/LivenessPayload.kt` in the Android repo are a matched pair —
// changing either without the other breaks every submission.
//
// The per-frame digests are the point. A payload that covered only metadata could
// be re-paired with different frames and still verify.
func livenessSignedPayload(
	challengeID, nonce, deviceID string,
	neutralFrame []byte,
	steps []LivenessStepFrame,
) string {
	ordered := make([]LivenessStepFrame, len(steps))
	copy(ordered, steps)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })

	var b strings.Builder
	b.WriteString(LivenessPayloadVersion + "\n")
	b.WriteString("challenge_id=" + challengeID + "\n")
	b.WriteString("nonce=" + nonce + "\n")
	b.WriteString("device_id=" + deviceID + "\n")
	b.WriteString("neutral=" + crypto.SHA256Hex(neutralFrame) + "\n")
	for _, step := range ordered {
		b.WriteString(fmt.Sprintf(
			"step=%d:%s:%d:%s\n",
			step.Index,
			step.Action,
			step.CapturedAt.UnixMilli(),
			crypto.SHA256Hex(step.JPEG),
		))
	}
	return b.String()
}

// --- Errors that carry a wait ---

// livenessCooldownError returns 429 with the remaining seconds in details, the
// same shape the OTP flow uses so the client reads it with code it already has.
func livenessCooldownError(retryAfter time.Duration) error {
	seconds := int(retryAfter.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return apperr.Error{
		Status:  apperr.LivenessCooldown.Status,
		Code:    apperr.LivenessCooldown.Code,
		Message: fmt.Sprintf("Terlalu banyak percobaan. Coba lagi dalam %d detik.", seconds),
		Details: map[string]any{"retry_after_seconds": seconds},
	}
}

const nonceBytes = 32
