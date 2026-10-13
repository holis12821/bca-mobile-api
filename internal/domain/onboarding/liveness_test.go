package onboarding

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"
)

// --- Challenge issuance ---

// The order is the whole defence against a pre-recorded video, so the properties
// asserted here are the ones that make it worth having: BLINK is always present,
// nothing repeats, and the position of each action varies between challenges.
func TestPickActions_AlwaysBlinkNoRepeats(t *testing.T) {
	for i := 0; i < 200; i++ {
		actions, err := pickActions(3)
		if err != nil {
			t.Fatalf("pickActions: %v", err)
		}
		if len(actions) != 3 {
			t.Fatalf("want 3 actions, got %d", len(actions))
		}

		seen := map[LivenessAction]int{}
		blink := 0
		for _, a := range actions {
			if !a.Valid() {
				t.Fatalf("invalid action %q", a)
			}
			seen[a]++
			if seen[a] > 1 {
				t.Fatalf("action %q repeated in one challenge: %v", a, actions)
			}
			if a == ActionBlink {
				blink++
			}
		}
		if blink != 1 {
			t.Fatalf("BLINK must appear exactly once, got %d: %v", blink, actions)
		}
	}
}

// BLINK used to be hardcoded first. If it still were, this would catch it.
func TestPickActions_BlinkPositionVaries(t *testing.T) {
	positions := map[int]bool{}
	for i := 0; i < 300; i++ {
		actions, err := pickActions(3)
		if err != nil {
			t.Fatalf("pickActions: %v", err)
		}
		for idx, a := range actions {
			if a == ActionBlink {
				positions[idx] = true
			}
		}
	}
	if len(positions) < 3 {
		t.Fatalf("BLINK only ever landed at positions %v; the order is not being shuffled", positions)
	}
}

func TestPickActions_HeadPosesVary(t *testing.T) {
	seen := map[LivenessAction]bool{}
	for i := 0; i < 300; i++ {
		actions, _ := pickActions(3)
		for _, a := range actions {
			if a != ActionBlink {
				seen[a] = true
			}
		}
	}
	if len(seen) != len(HeadActions) {
		t.Fatalf("only %d of %d head poses were ever chosen: %v", len(seen), len(HeadActions), seen)
	}
}

func TestIssue_RejectsWrongDeviceAndStep(t *testing.T) {
	svc, sessionRepo, cache, store := setupChallengeService()
	sessionID := seedLivenessSession(sessionRepo, cache, StepBiometric)
	ctx := context.Background()

	if _, err := svc.Issue(ctx, sessionID, "dev_other", "key_1", testPublicKey(t), "", ""); err == nil {
		t.Fatal("a challenge must not be issued to a device that does not own the session")
	}

	sessionRepo.sessions[sessionID].CurrentStep = StepOCR
	cache.data[sessionID].CurrentStep = StepOCR
	if _, err := svc.Issue(ctx, sessionID, "dev_bio", "key_1", testPublicKey(t), "", ""); err == nil {
		t.Fatal("a challenge must not be issued outside the BIOMETRIC step")
	}

	if len(store.stored) != 0 {
		t.Fatalf("no challenge should have been stored, got %d", len(store.stored))
	}
}

func TestIssue_RejectsNonP256Key(t *testing.T) {
	svc, sessionRepo, cache, _ := setupChallengeService()
	sessionID := seedLivenessSession(sessionRepo, cache, StepBiometric)

	// A P-384 key is a perfectly valid EC key and still must be refused: the
	// verifier only implements P-256, and finding out at verification time would
	// cost the customer the whole set of movements.
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p384: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal p384: %v", err)
	}

	_, err = svc.Issue(context.Background(), sessionID, "dev_bio", "key_1",
		base64.StdEncoding.EncodeToString(der), "", "")
	if err == nil {
		t.Fatal("a non-P-256 key must be refused at issuance")
	}
}

func TestIssue_RefusesDuringCooldownAndBlock(t *testing.T) {
	svc, sessionRepo, cache, _ := setupChallengeService()
	sessionID := seedLivenessSession(sessionRepo, cache, StepBiometric)
	tracker := svc.attempts.(*mockAttemptTracker)
	ctx := context.Background()

	tracker.status = &LivenessAttemptStatus{RetryAfter: 5 * time.Minute}
	if _, err := svc.Issue(ctx, sessionID, "dev_bio", "key_1", testPublicKey(t), "", ""); err == nil {
		t.Fatal("a challenge must not be issued during a cooldown")
	}

	tracker.status = &LivenessAttemptStatus{Blocked: true}
	if _, err := svc.Issue(ctx, sessionID, "dev_bio", "key_1", testPublicKey(t), "", ""); err == nil {
		t.Fatal("a challenge must not be issued once liveness is blocked")
	}
}

func TestIssue_StoresBoundSingleUseChallenge(t *testing.T) {
	svc, sessionRepo, cache, store := setupChallengeService()
	sessionID := seedLivenessSession(sessionRepo, cache, StepBiometric)

	issued, err := svc.Issue(context.Background(), sessionID, "dev_bio", "key_1", testPublicKey(t), "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Nonce == "" || issued.ChallengeID == "" {
		t.Fatal("issued challenge carries no nonce or id")
	}
	if !issued.ExpiresAt.After(time.Now()) {
		t.Fatal("issued challenge is already expired")
	}

	stored := store.stored[issued.ChallengeID]
	if stored == nil {
		t.Fatal("challenge was not stored, so it could never be verified")
	}
	if stored.DeviceID != "dev_bio" || stored.SessionID != sessionID {
		t.Fatalf("challenge is not bound to the session and device: %+v", stored)
	}
	if stored.Nonce != issued.Nonce {
		t.Fatal("stored nonce differs from the one handed to the client")
	}
}

func TestIssue_NoncesAreUnique(t *testing.T) {
	svc, sessionRepo, cache, _ := setupChallengeService()
	sessionID := seedLivenessSession(sessionRepo, cache, StepBiometric)

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		issued, err := svc.Issue(context.Background(), sessionID, "dev_bio", "key_1", testPublicKey(t), "", "")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if seen[issued.Nonce] {
			t.Fatalf("nonce %q was issued twice", issued.Nonce)
		}
		seen[issued.Nonce] = true
	}
}

// --- Canonical payload ---

// The payload is one half of a matched pair with LivenessPayload.kt in the Android
// repo. If this shape drifts, every submission fails signature verification.
func TestLivenessSignedPayload_Shape(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000).UTC()
	payload := livenessSignedPayload("chl_1", "nonce_1", "dev_1", []byte{1, 2, 3}, []LivenessStepFrame{
		{Index: 1, Action: ActionTurnLeft, CapturedAt: at.Add(time.Second), JPEG: []byte{9}},
		{Index: 0, Action: ActionBlink, CapturedAt: at, JPEG: []byte{8}},
	})

	want := "v1\n" +
		"challenge_id=chl_1\n" +
		"nonce=nonce_1\n" +
		"device_id=dev_1\n" +
		"neutral=039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81\n" +
		"step=0:BLINK:1700000000000:" +
		"beead77994cf573341ec17b58bbf7eb34d2711c993c1d976b128b3188dc1829a\n" +
		"step=1:TURN_LEFT:1700000001000:" +
		"2b4c342f5433ebe591a1da77e013d1b72475562d48578dca8b84bac6651c3cb9\n"

	if payload != want {
		t.Fatalf("canonical payload drifted.\n got: %q\nwant: %q", payload, want)
	}
}

// --- Provider: fail-closed ---

// The single most important property of the whole backend change: with no ML
// layer available, the provider must refuse. A provider that cannot judge and
// passes anyway is worse than no provider at all.
func TestInternalProvider_FailsClosedWithoutAnalyzer(t *testing.T) {
	cfg := DefaultLivenessConfig()
	provider := NewInternalLivenessProvider(nil, cfg)
	challenge, submission := validPair(t, cfg)

	verdict, err := provider.Verify(context.Background(), challenge, submission, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verdict.Passed {
		t.Fatal("the provider passed a submission it could not actually judge")
	}
	if !verdict.Unavailable {
		t.Fatalf("an unavailable ML layer must be reported as unavailable, got reason %q", verdict.Reason)
	}
}

func TestUnconfiguredProvider_Refuses(t *testing.T) {
	verdict, err := unconfiguredLivenessProvider{}.Verify(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verdict.Passed {
		t.Fatal("the unconfigured provider must never pass")
	}
}

// The stub is for local development, but it must not be a way around the checks
// that need no model at all.
func TestStubProvider_StillEnforcesDeterministicLayer(t *testing.T) {
	cfg := DefaultLivenessConfig()
	provider := NewStubLivenessProvider(cfg)
	challenge, submission := validPair(t, cfg)

	verdict, err := provider.Verify(context.Background(), challenge, submission, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verdict.Passed {
		t.Fatalf("stub should accept a well-formed submission, refused with %q", verdict.Reason)
	}

	// Same still image for every step: the classic replay, and it must fail even
	// under the stub.
	replay := cloneSubmission(submission)
	for i := range replay.StepFrames {
		replay.StepFrames[i].JPEG = replay.NeutralFrame
	}
	verdict, err = provider.Verify(context.Background(), challenge, replay, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verdict.Passed {
		t.Fatal("the stub passed a submission that reused one still image for every step")
	}
}

// --- Provider: deterministic layer ---

func TestDeterministicLayer_Refusals(t *testing.T) {
	cfg := DefaultLivenessConfig()
	provider := &internalLivenessProvider{cfg: cfg}

	tests := []struct {
		name   string
		mutate func(c *LivenessChallenge, s *LivenessSubmission)
		reason string
	}{
		{
			name:   "nonce does not match the challenge",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) { s.Nonce = "someone-elses" },
			reason: "challenge_mismatch",
		},
		{
			name:   "submission from another device",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) { s.DeviceID = "dev_other" },
			reason: "device_mismatch",
		},
		{
			name:   "signed by a different registered key",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) { s.DeviceKeyID = "key_other" },
			reason: "device_key_mismatch",
		},
		{
			name: "fewer frames than actions",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) {
				s.StepFrames = s.StepFrames[:1]
			},
			reason: "frame_count_mismatch",
		},
		{
			name: "frame claims an action the challenge did not ask for",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) {
				s.StepFrames[1].Action = ActionLookDown
			},
			reason: "step_action_mismatch",
		},
		{
			name: "timestamps run backwards",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) {
				s.StepFrames[0].CapturedAt = s.StepFrames[2].CapturedAt.Add(time.Second)
			},
			reason: "timestamp_not_monotonic",
		},
		{
			name: "frames captured long before the challenge existed",
			mutate: func(c *LivenessChallenge, s *LivenessSubmission) {
				for i := range s.StepFrames {
					s.StepFrames[i].CapturedAt = c.IssuedAt.Add(-time.Hour)
				}
			},
			reason: "timestamp_outside_window",
		},
		{
			name:   "no neutral frame",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) { s.NeutralFrame = nil },
			reason: "missing_neutral_frame",
		},
		{
			name: "one still image reused for every step",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) {
				for i := range s.StepFrames {
					s.StepFrames[i].JPEG = s.NeutralFrame
				}
			},
			reason: "duplicate_frame",
		},
		{
			name: "frames are not decodable images",
			mutate: func(_ *LivenessChallenge, s *LivenessSubmission) {
				s.StepFrames[0].JPEG = []byte("not-a-jpeg")
			},
			reason: "step_frame_undecodable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			challenge, submission := validPair(t, cfg)
			tc.mutate(challenge, submission)

			if got := provider.checkDeterministic(challenge, submission); got != tc.reason {
				t.Fatalf("want refusal %q, got %q", tc.reason, got)
			}
		})
	}
}

// A head turn changes the picture; a frame that barely differs from neutral did
// not involve a head turn. The blink frame is exempt, and that exemption is the
// part most likely to be broken by accident.
func TestFrameDistinctness_BlinkFrameIsExemptFromTheMargin(t *testing.T) {
	cfg := DefaultLivenessConfig()
	provider := &internalLivenessProvider{cfg: cfg}
	challenge, submission := validPair(t, cfg)

	// A blink frame that is only slightly different from neutral is exactly what
	// an eyes-closed capture looks like, and must be accepted.
	for i, step := range submission.StepFrames {
		if step.Action == ActionBlink {
			submission.StepFrames[i].JPEG = testJPEG(t, 40, 41)
		}
	}
	submission.NeutralFrame = testJPEG(t, 40, 40)

	if reason := provider.checkDeterministic(challenge, submission); reason != "" {
		t.Fatalf("a near-neutral blink frame must not be refused, got %q", reason)
	}

	// The same near-identical frame for a head pose must be refused.
	for i, step := range submission.StepFrames {
		if step.Action.IsHeadPose() {
			submission.StepFrames[i].JPEG = testJPEG(t, 40, 41)
			break
		}
	}
	if reason := provider.checkDeterministic(challenge, submission); reason == "" {
		t.Fatal("a head-pose frame nearly identical to neutral must be refused")
	}
}

func TestPoseMatches_BlinkRequiresClosedEyes(t *testing.T) {
	if poseMatches(ActionBlink, &FrameFaceAnalysis{EyeOpenProbability: 0.95}) {
		t.Fatal("an open-eyed frame must not satisfy BLINK")
	}
	if !poseMatches(ActionBlink, &FrameFaceAnalysis{EyeOpenProbability: 0.05}) {
		t.Fatal("a closed-eyed frame must satisfy BLINK")
	}
}

func TestPoseMatches_DirectionsAreDistinct(t *testing.T) {
	left := &FrameFaceAnalysis{YawDegrees: 30}
	if !poseMatches(ActionTurnLeft, left) {
		t.Fatal("a left turn must satisfy TURN_LEFT")
	}
	if poseMatches(ActionTurnRight, left) {
		t.Fatal("a left turn must not satisfy TURN_RIGHT")
	}

	up := &FrameFaceAnalysis{PitchDegrees: 25}
	if !poseMatches(ActionLookUp, up) {
		t.Fatal("looking up must satisfy LOOK_UP")
	}
	if poseMatches(ActionLookDown, up) {
		t.Fatal("looking up must not satisfy LOOK_DOWN")
	}
}

// --- Play Integrity verdict policy ---

func TestIntegrityVerdict_Acceptable(t *testing.T) {
	full := &IntegrityVerdict{MeetsDeviceIntegrity: true, PackageMatches: true, NonceMatches: true}
	if !full.Acceptable() {
		t.Fatal("a complete verdict must be acceptable")
	}

	// Each one alone is enough to refuse; a nonce mismatch in particular means a
	// token minted for a different attempt.
	for _, missing := range []*IntegrityVerdict{
		{PackageMatches: true, NonceMatches: true},
		{MeetsDeviceIntegrity: true, NonceMatches: true},
		{MeetsDeviceIntegrity: true, PackageMatches: true},
		nil,
	} {
		if missing.Acceptable() {
			t.Fatalf("verdict %+v must not be acceptable", missing)
		}
	}
}

func TestNoopIntegrityVerifier_IsNeverAcceptable(t *testing.T) {
	verdict, err := NewNoopIntegrityVerifier().Verify(context.Background(), "token", "nonce")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verdict.Acceptable() {
		t.Fatal("an unconfigured Play Integrity verifier must not produce an acceptable verdict")
	}
}

// --- Helpers ---

type mockLivenessChallengeStore struct {
	stored   map[string]*LivenessChallenge
	consumed map[string]bool
}

func newMockLivenessChallengeStore() *mockLivenessChallengeStore {
	return &mockLivenessChallengeStore{
		stored:   map[string]*LivenessChallenge{},
		consumed: map[string]bool{},
	}
}

func (m *mockLivenessChallengeStore) Store(_ context.Context, c *LivenessChallenge) error {
	m.stored[c.ChallengeID] = c
	return nil
}

// Consume mirrors GETDEL: the second call for the same id finds nothing.
func (m *mockLivenessChallengeStore) Consume(_ context.Context, id string) (*LivenessChallenge, error) {
	if m.consumed[id] {
		return nil, nil
	}
	c, ok := m.stored[id]
	if !ok {
		return nil, nil
	}
	m.consumed[id] = true
	return c, nil
}

type mockAttemptTracker struct {
	status   *LivenessAttemptStatus
	failures int
	resets   int
	onFail   func(failures int) *LivenessAttemptStatus
}

func (m *mockAttemptTracker) Status(_ context.Context, _, _ string) (*LivenessAttemptStatus, error) {
	return m.status, nil
}

func (m *mockAttemptTracker) RecordFailure(_ context.Context, _, _ string) (*LivenessAttemptStatus, error) {
	m.failures++
	if m.onFail != nil {
		return m.onFail(m.failures), nil
	}
	return &LivenessAttemptStatus{Failures: m.failures}, nil
}

func (m *mockAttemptTracker) Reset(_ context.Context, _, _ string) error {
	m.resets++
	return nil
}

type mockLivenessAttemptRepo struct {
	rows []*LivenessAttempt
}

func (m *mockLivenessAttemptRepo) Insert(_ context.Context, a *LivenessAttempt) error {
	m.rows = append(m.rows, a)
	return nil
}

func (m *mockLivenessAttemptRepo) CountFailuresSince(_ context.Context, _ string, _ time.Time) (int, error) {
	return len(m.rows), nil
}

func (m *mockLivenessAttemptRepo) outcomes() []LivenessAttemptOutcome {
	out := make([]LivenessAttemptOutcome, len(m.rows))
	for i, r := range m.rows {
		out[i] = r.Outcome
	}
	return out
}

type mockIntegrityVerifier struct {
	verdict *IntegrityVerdict
	err     error
	nonces  []string
}

func (m *mockIntegrityVerifier) Verify(_ context.Context, _, nonce string) (*IntegrityVerdict, error) {
	m.nonces = append(m.nonces, nonce)
	if m.err != nil {
		return nil, m.err
	}
	if m.verdict != nil {
		return m.verdict, nil
	}
	return &IntegrityVerdict{MeetsDeviceIntegrity: true, PackageMatches: true, NonceMatches: true}, nil
}

func setupChallengeService() (
	*LivenessChallengeService,
	*mockSessionRepo,
	*mockSessionCache,
	*mockLivenessChallengeStore,
) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	store := newMockLivenessChallengeStore()

	svc := NewLivenessChallengeService(LivenessChallengeServiceConfig{
		Sessions:   sessionRepo,
		Cache:      cache,
		Challenges: store,
		Attempts:   &mockAttemptTracker{},
		Audit:      &mockAuditRepo{},
		Liveness:   DefaultLivenessConfig(),
	})
	return svc, sessionRepo, cache, store
}

func seedLivenessSession(repo *mockSessionRepo, cache *mockSessionCache, step Step) string {
	sessionID := "onb_liveness_test"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_bio",
		ProductType: ProductTahapanBCA,
		CurrentStep: step,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	repo.sessions[sessionID] = session
	cache.data[sessionID] = session
	return sessionID
}

// testKey is one EC P-256 key reused across the package's liveness tests.
var testKey *ecdsa.PrivateKey

func livenessTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	if testKey == nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate p256: %v", err)
		}
		testKey = key
	}
	return testKey
}

func testPublicKey(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&livenessTestKey(t).PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// testJPEG renders a small gradient so each frame is a genuinely different image.
//
// Real JPEG bytes are needed rather than a placeholder: the frame-distinctness
// check decodes them, and a fake would exercise the error path instead of the
// comparison.
func testJPEG(t *testing.T, base, step int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((base + x*step + y*step/2) % 256)})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// validPair builds a challenge and a matching, correctly signed submission.
func validPair(t *testing.T, cfg LivenessConfig) (*LivenessChallenge, *LivenessSubmission) {
	t.Helper()
	now := time.Now().UTC()
	actions := []LivenessAction{ActionTurnLeft, ActionBlink, ActionLookUp}

	challenge := &LivenessChallenge{
		ChallengeID:     "chl_test",
		Nonce:           "nonce_test",
		SessionID:       "onb_liveness_test",
		DeviceID:        "dev_bio",
		DeviceKeyID:     "key_1",
		DevicePublicKey: testPublicKey(t),
		Actions:         actions,
		IssuedAt:        now,
		ExpiresAt:       now.Add(cfg.ChallengeTTL),
	}

	steps := make([]LivenessStepFrame, len(actions))
	for i, action := range actions {
		steps[i] = LivenessStepFrame{
			Index:      i,
			Action:     action,
			CapturedAt: now.Add(time.Duration(i+1) * time.Second),
			// A different gradient per step, so the frames are genuinely distinct.
			JPEG: testJPEG(t, 10+i*60, 3+i*4),
		}
	}

	submission := &LivenessSubmission{
		SessionID:          challenge.SessionID,
		ChallengeID:        challenge.ChallengeID,
		Nonce:              challenge.Nonce,
		DeviceID:           challenge.DeviceID,
		DeviceKeyID:        challenge.DeviceKeyID,
		DevicePublicKey:    challenge.DevicePublicKey,
		SignatureAlgorithm: "EC-P256",
		NeutralFrame:       testJPEG(t, 200, 2),
		StepFrames:         steps,
		IntegrityToken:     "integrity-token",
	}

	submission.Signature = signLivenessPayload(t, challenge, submission)
	return challenge, submission
}

// signLivenessPayload signs the canonical payload the way the client does.
func signLivenessPayload(t *testing.T, c *LivenessChallenge, s *LivenessSubmission) string {
	t.Helper()
	payload := livenessSignedPayload(c.ChallengeID, c.Nonce, c.DeviceID, s.NeutralFrame, s.StepFrames)
	digest := sha256Of(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, livenessTestKey(t), digest)
	if err != nil {
		t.Fatalf("sign payload: %v", err)
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func sha256Of(payload string) []byte {
	sum := sha256.Sum256([]byte(payload))
	return sum[:]
}

func cloneSubmission(s *LivenessSubmission) *LivenessSubmission {
	clone := *s
	clone.StepFrames = make([]LivenessStepFrame, len(s.StepFrames))
	copy(clone.StepFrames, s.StepFrames)
	return &clone
}
