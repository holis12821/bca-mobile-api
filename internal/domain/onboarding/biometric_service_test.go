package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- in-memory mocks for the biometric service ---

type mockBiometricRepo struct {
	data map[string]*BiometricResult
}

func newMockBiometricRepo() *mockBiometricRepo {
	return &mockBiometricRepo{data: make(map[string]*BiometricResult)}
}

func (m *mockBiometricRepo) Create(_ context.Context, r *BiometricResult) error {
	m.data[r.SessionID] = r
	return nil
}

func (m *mockBiometricRepo) FindBySessionID(_ context.Context, sessionID string) (*BiometricResult, error) {
	return m.data[sessionID], nil
}

// passingProvider stands in for a configured provider. It exists so the
// orchestration can be tested without pretending a model is present: the point of
// these tests is the order of the gates, not the ML.
type passingProvider struct {
	calls   int
	verdict *LivenessVerdict
	err     error
}

func (p *passingProvider) Name() string { return "test" }

func (p *passingProvider) Verify(
	_ context.Context, _ *LivenessChallenge, _ *LivenessSubmission, reference []byte,
) (*LivenessVerdict, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.verdict != nil {
		return p.verdict, nil
	}
	return &LivenessVerdict{
		Passed:          true,
		LivenessScore:   96,
		FaceMatchPassed: true,
		FaceMatchScore:  93,
		Reason:          "passed",
	}, nil
}

type bioHarness struct {
	svc        *BiometricService
	sessions   *mockSessionRepo
	cache      *mockSessionCache
	store      *mockLivenessChallengeStore
	tracker    *mockAttemptTracker
	attemptLog *mockLivenessAttemptRepo
	integrity  *mockIntegrityVerifier
	provider   *passingProvider
	biometrics *mockBiometricRepo
	sessionID  string
}

func setupBioService(t *testing.T) *bioHarness {
	t.Helper()
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	ocrRepo := newMockOCRResultRepo()
	bioRepo := newMockBiometricRepo()
	store := newMockLivenessChallengeStore()
	tracker := &mockAttemptTracker{}
	attemptLog := &mockLivenessAttemptRepo{}
	integrity := &mockIntegrityVerifier{}
	provider := &passingProvider{}

	sessionID := "onb_liveness_test"
	ocrRepo.data[sessionID] = &OCRResult{
		OCRID:     "ocr_test",
		SessionID: sessionID,
		PhotoPath: "ktp.jpg",
		Extracted: KTPData{NIK: "3174082104950001", NamaLengkap: "TEST USER"},
	}

	svc := NewBiometricService(BiometricServiceConfig{
		Sessions:   sessionRepo,
		Cache:      cache,
		OCRResults: ocrRepo,
		Biometrics: bioRepo,
		Provider:   provider,
		Integrity:  integrity,
		Challenges: store,
		Attempts:   tracker,
		AttemptLog: attemptLog,
		Storage:    NewMockObjectStorage(),
		Audit:      &mockAuditRepo{},
		Liveness:   DefaultLivenessConfig(),
	})

	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_bio",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepBiometric,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session

	return &bioHarness{
		svc: svc, sessions: sessionRepo, cache: cache, store: store,
		tracker: tracker, attemptLog: attemptLog, integrity: integrity,
		provider: provider, biometrics: bioRepo, sessionID: sessionID,
	}
}

// issue stores a challenge and returns it with a matching signed submission.
func (h *bioHarness) issue(t *testing.T) (*LivenessChallenge, *LivenessSubmission) {
	t.Helper()
	challenge, submission := validPair(t, DefaultLivenessConfig())
	if err := h.store.Store(context.Background(), challenge); err != nil {
		t.Fatalf("store challenge: %v", err)
	}
	return challenge, submission
}

// --- The pass ---

func TestProcessBiometric_PassAdvancesStep(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)

	resp, err := h.svc.ProcessBiometric(context.Background(), submission, "1.1.1.1", "test-agent")
	if err != nil {
		t.Fatalf("ProcessBiometric: %v", err)
	}
	if !resp.LivenessVerified {
		t.Fatal("a passing verdict must report liveness as verified")
	}
	if resp.CurrentStep != StepVideoCall {
		t.Fatalf("want next step %s, got %s", StepVideoCall, resp.CurrentStep)
	}
	if h.sessions.sessions[h.sessionID].CurrentStep != StepVideoCall {
		t.Fatal("the session step was not advanced")
	}
	if h.tracker.resets != 1 {
		t.Fatalf("the failure counters must be cleared after a pass, resets=%d", h.tracker.resets)
	}
	if got := h.attemptLog.outcomes(); len(got) != 1 || got[0] != LivenessOutcomePassed {
		t.Fatalf("want one PASSED audit row, got %v", got)
	}
}

// The response must not carry an ISO/IEC 30107-3 claim any more: the field was
// filled by a mock and shown to the customer as a certification (decision Q4).
func TestProcessBiometric_ResponseCarriesNoCertificationClaim(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)

	resp, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err != nil {
		t.Fatalf("ProcessBiometric: %v", err)
	}

	// The KEY must be gone, not merely false. An earlier version of this test only
	// checked the value, and the field went on being serialised as
	// "iso_30107_compliant": false — still part of the contract, still readable by
	// a client, still the concept Q4 asked to remove.
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if bytes.Contains(encoded, []byte("iso_30107")) {
		t.Fatalf("the response still carries an ISO/IEC 30107-3 field: %s", encoded)
	}

	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, present := fields["iso_30107_compliant"]; present {
		t.Fatal("iso_30107_compliant is still a response field")
	}
}

// --- Replay ---

// The single most important rejection: the same payload sent twice.
func TestProcessBiometric_NonceIsSingleUse(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	ctx := context.Background()

	if _, err := h.svc.ProcessBiometric(ctx, submission, "", ""); err != nil {
		t.Fatalf("first submission should pass: %v", err)
	}

	// Reset the step so the replay is refused by the nonce and not by the step
	// check — otherwise this test would pass for the wrong reason.
	h.sessions.sessions[h.sessionID].CurrentStep = StepBiometric
	h.cache.data[h.sessionID].CurrentStep = StepBiometric

	_, err := h.svc.ProcessBiometric(ctx, submission, "", "")
	if err == nil {
		t.Fatal("replaying the same submission must be refused")
	}
	if apperr.From(err).Code != apperr.LivenessChallengeInvalid.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessChallengeInvalid.Code, apperr.From(err).Code)
	}
}

func TestProcessBiometric_UnknownChallengeRefused(t *testing.T) {
	h := setupBioService(t)
	_, submission := validPair(t, DefaultLivenessConfig())
	// Deliberately not stored: a fabricated challenge_id must look exactly like
	// an expired or replayed one from the outside.

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("a challenge the server never issued must be refused")
	}
	if apperr.From(err).Code != apperr.LivenessChallengeInvalid.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessChallengeInvalid.Code, apperr.From(err).Code)
	}
	if h.provider.calls != 0 {
		t.Fatal("the provider must not be reached without a valid challenge")
	}
}

func TestProcessBiometric_ExpiredChallengeRefused(t *testing.T) {
	h := setupBioService(t)
	challenge, submission := h.issue(t)
	challenge.ExpiresAt = time.Now().UTC().Add(-time.Second)

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("an expired challenge must be refused")
	}
	if h.provider.calls != 0 {
		t.Fatal("the provider must not be reached for an expired challenge")
	}
}

// --- Binding and signature ---

func TestProcessBiometric_ChallengeFromAnotherSessionRefused(t *testing.T) {
	h := setupBioService(t)
	challenge, submission := h.issue(t)
	challenge.SessionID = "onb_someone_else"

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("a challenge issued for another session must be refused")
	}
}

func TestProcessBiometric_DeviceMismatchRefused(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	submission.DeviceID = "dev_other"

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("a submission from a device that does not own the session must be refused")
	}
	if apperr.From(err).Code != apperr.OnboardingDeviceMismatch.Code {
		t.Fatalf("want %s, got %s", apperr.OnboardingDeviceMismatch.Code, apperr.From(err).Code)
	}
}

// The tampered-client case: a payload with a signature that does not cover it.
func TestProcessBiometric_BadSignatureRefused(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	submission.Signature = "bm90LWEtc2lnbmF0dXJl"

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("an invalid signature must be refused")
	}
	if apperr.From(err).Code != apperr.LivenessSignatureInvalid.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessSignatureInvalid.Code, apperr.From(err).Code)
	}
	if h.provider.calls != 0 {
		t.Fatal("the provider must not be reached with an invalid signature")
	}
}

// Swapping a frame after signing must invalidate the signature — that is why the
// payload covers a digest of every frame rather than only metadata.
func TestProcessBiometric_SwappedFrameInvalidatesSignature(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	submission.StepFrames[1].JPEG = testJPEG(t, 7, 11)

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("replacing a frame after signing must be refused")
	}
	if apperr.From(err).Code != apperr.LivenessSignatureInvalid.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessSignatureInvalid.Code, apperr.From(err).Code)
	}
}

// The client's own public key must not be the one the signature is checked
// against; otherwise an attacker signs with a key they generated themselves.
func TestProcessBiometric_SubmissionKeyIsIgnored(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	submission.DevicePublicKey = "obviously-not-the-registered-key"

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err != nil {
		t.Fatalf("verification must use the registered key, not the submitted one: %v", err)
	}
}

// --- Play Integrity policy ---

func TestProcessBiometric_FailedIntegrityRefusedInProduction(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.integrity.verdict = &IntegrityVerdict{
		PackageMatches: true,
		NonceMatches:   true,
		Verdicts:       []string{"MEETS_BASIC_INTEGRITY"},
	}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("a failing device verdict must refuse the attempt")
	}
	if apperr.From(err).Code != apperr.LivenessIntegrityFailed.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessIntegrityFailed.Code, apperr.From(err).Code)
	}
}

func TestProcessBiometric_IntegrityLogOnlyAllowsAttempt(t *testing.T) {
	h := setupBioService(t)
	cfg := DefaultLivenessConfig()
	cfg.IntegrityLogOnly = true
	h.svc.cfg = cfg

	_, submission := h.issue(t)
	h.integrity.verdict = &IntegrityVerdict{Verdicts: []string{"MEETS_BASIC_INTEGRITY"}}

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err != nil {
		t.Fatalf("log-only mode must not refuse the attempt: %v", err)
	}

	// Log-only changes the POLICY, not the facts. The verdict failed, and the audit
	// row has to say so — otherwise the one mode that exists to record failures
	// without refusing them records them as passes.
	if len(h.attemptLog.rows) != 1 {
		t.Fatalf("want one audit row, got %d", len(h.attemptLog.rows))
	}
	if h.attemptLog.rows[0].IntegrityOK {
		t.Fatal("log-only recorded integrity_ok=true for a verdict that failed")
	}
	if h.attemptLog.rows[0].Outcome != LivenessOutcomePassed {
		t.Fatalf("the attempt itself was allowed, so want PASSED, got %s",
			h.attemptLog.rows[0].Outcome)
	}
}

// A provider error after integrity passed must not record integrity as failed.
func TestProcessBiometric_ProviderErrorKeepsIntegrityResult(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.err = errors.New("inference service unreachable")

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err == nil {
		t.Fatal("expected a refusal")
	}
	if len(h.attemptLog.rows) != 1 {
		t.Fatalf("want one audit row, got %d", len(h.attemptLog.rows))
	}
	row := h.attemptLog.rows[0]
	if row.Reason != "provider_error" {
		t.Fatalf("want reason provider_error, got %q", row.Reason)
	}
	if !row.IntegrityOK {
		t.Fatal("integrity passed on this attempt; the audit row says it did not")
	}
}

// The token is bound to this challenge's nonce, so one minted for an earlier
// attempt does not satisfy a later one.
func TestProcessBiometric_IntegrityCheckedAgainstChallengeNonce(t *testing.T) {
	h := setupBioService(t)
	challenge, submission := h.issue(t)

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err != nil {
		t.Fatalf("ProcessBiometric: %v", err)
	}
	if len(h.integrity.nonces) != 1 || h.integrity.nonces[0] != challenge.Nonce {
		t.Fatalf("want integrity checked against %q, got %v", challenge.Nonce, h.integrity.nonces)
	}
}

// --- Provider outcomes ---

func TestProcessBiometric_UnavailableProviderRefuses(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.verdict = &LivenessVerdict{Unavailable: true, Reason: "ml_layer_unavailable"}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("an unavailable provider must refuse, not pass")
	}
	if apperr.From(err).Code != apperr.LivenessProviderUnavailable.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessProviderUnavailable.Code, apperr.From(err).Code)
	}
	if h.sessions.sessions[h.sessionID].CurrentStep != StepBiometric {
		t.Fatal("the step must not advance when the provider could not judge")
	}
}

// A provider that errored did not reach a verdict, so there is nothing to act on
// except refusal.
func TestProcessBiometric_ProviderErrorFailsClosed(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.err = errors.New("inference service unreachable")

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("a provider error must refuse the attempt")
	}
	if h.sessions.sessions[h.sessionID].CurrentStep != StepBiometric {
		t.Fatal("the step must not advance after a provider error")
	}
}

func TestProcessBiometric_FaceMismatchMapsToItsOwnCode(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.verdict = &LivenessVerdict{
		LivenessScore:  95,
		FaceMatchScore: 40,
		Reason:         "face_match_below_threshold",
	}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if apperr.From(err).Code != apperr.BioFaceNotMatch.Code {
		t.Fatalf("want %s, got %s", apperr.BioFaceNotMatch.Code, apperr.From(err).Code)
	}
}

// The reason label is for the audit trail only. Returning it would tell whoever
// is probing which check to work around next.
func TestProcessBiometric_ReasonIsAuditedNotReturned(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.verdict = &LivenessVerdict{Reason: "step_pose_mismatch"}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if msg := apperr.From(err).Message; msg == "step_pose_mismatch" {
		t.Fatal("the internal reason leaked into the customer-facing message")
	}
	if len(h.attemptLog.rows) != 1 || h.attemptLog.rows[0].Reason != "step_pose_mismatch" {
		t.Fatalf("the reason must be recorded in the audit row, got %+v", h.attemptLog.rows)
	}
}

// The audit row must record the VERIFIED integrity result, not merely that a
// token was attached. The earlier version produced rows reading
// reason=integrity_failed alongside integrity_ok=true.
func TestProcessBiometric_AuditRecordsRealIntegrityResult(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.integrity.verdict = &IntegrityVerdict{
		PackageMatches: true,
		NonceMatches:   true,
		Verdicts:       []string{"MEETS_BASIC_INTEGRITY"},
	}

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err == nil {
		t.Fatal("expected the attempt to be refused")
	}

	if len(h.attemptLog.rows) != 1 {
		t.Fatalf("want one audit row, got %d", len(h.attemptLog.rows))
	}
	row := h.attemptLog.rows[0]
	if row.Reason != "integrity_failed" {
		t.Fatalf("want reason integrity_failed, got %q", row.Reason)
	}
	if row.IntegrityOK {
		t.Fatal("the audit row says integrity_ok while the reason says it failed")
	}
}

func TestProcessBiometric_AuditRecordsIntegrityPass(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err != nil {
		t.Fatalf("ProcessBiometric: %v", err)
	}
	if len(h.attemptLog.rows) != 1 || !h.attemptLog.rows[0].IntegrityOK {
		t.Fatalf("a passing attempt must record integrity_ok=true, got %+v", h.attemptLog.rows)
	}
}

// --- Cooldown and escalation (decision Q6) ---

func TestProcessBiometric_CooldownIsReportedWithRetryAfter(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.verdict = &LivenessVerdict{Reason: "pad_below_threshold"}
	h.tracker.onFail = func(int) *LivenessAttemptStatus {
		return &LivenessAttemptStatus{RetryAfter: 5 * time.Minute}
	}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	appErr := apperr.From(err)
	if appErr.Code != apperr.LivenessCooldown.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessCooldown.Code, appErr.Code)
	}
	// The client displays this number; without it the retry button has nothing
	// to count down from.
	details, ok := appErr.Details.(map[string]any)
	if !ok {
		t.Fatalf("want details carrying retry_after_seconds, got %T", appErr.Details)
	}
	if seconds, _ := details["retry_after_seconds"].(int); seconds != 300 {
		t.Fatalf("want retry_after_seconds=300, got %v", details)
	}
}

func TestProcessBiometric_BlockedEscalatesToVideoCall(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.provider.verdict = &LivenessVerdict{Reason: "pad_below_threshold"}
	h.tracker.onFail = func(int) *LivenessAttemptStatus {
		return &LivenessAttemptStatus{Blocked: true, CooldownRounds: 2}
	}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if apperr.From(err).Code != apperr.LivenessEscalated.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessEscalated.Code, apperr.From(err).Code)
	}

	// Routed to the queue that already exists — no new feature is built for this.
	if h.sessions.sessions[h.sessionID].CurrentStep != StepVideoCall {
		t.Fatal("a blocked applicant must be routed to the video-call step")
	}
	// But NOT recorded as biometrically verified: an agent still has to confirm.
	if h.sessions.sessions[h.sessionID].StepsCompleted.BiometricVerified {
		t.Fatal("escalation must not mark the biometric step as verified")
	}
	if got := h.attemptLog.outcomes(); len(got) != 1 || got[0] != LivenessOutcomeEscalated {
		t.Fatalf("want one ESCALATED audit row, got %v", got)
	}
}

func TestProcessBiometric_RefusesWhileAlreadyBlocked(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.tracker.status = &LivenessAttemptStatus{Blocked: true}

	_, err := h.svc.ProcessBiometric(context.Background(), submission, "", "")
	if apperr.From(err).Code != apperr.LivenessBlocked.Code {
		t.Fatalf("want %s, got %s", apperr.LivenessBlocked.Code, apperr.From(err).Code)
	}
	// The nonce must survive: it is refused before being spent, so the customer
	// does not lose a challenge they will need after the block is lifted.
	if h.store.consumed[submission.ChallengeID] {
		t.Fatal("the challenge was consumed by a request that was refused up front")
	}
}

func TestProcessBiometric_RefusesOutsideBiometricStep(t *testing.T) {
	h := setupBioService(t)
	_, submission := h.issue(t)
	h.sessions.sessions[h.sessionID].CurrentStep = StepOCR
	h.cache.data[h.sessionID].CurrentStep = StepOCR

	if _, err := h.svc.ProcessBiometric(context.Background(), submission, "", ""); err == nil {
		t.Fatal("a submission outside the BIOMETRIC step must be refused")
	}
}
