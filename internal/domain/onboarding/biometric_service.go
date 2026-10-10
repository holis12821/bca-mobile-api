package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

const (
	biometricPhotoBucket   = "onboarding-biometric"
	biometricAutoDeleteTTL = 7 * 24 * time.Hour // 7 days (UU PDP)
)

// BiometricService verifies one liveness submission and decides the outcome.
//
// The decision used to be the client's. `LivenessDetector.isComplete` on the phone
// triggered the upload, `liveness_meta` carried `completed_actions = 3` as a
// constant, and this service's only real check was that between three and five
// frames arrived. Everything that mattered — is this a live face, is it the right
// person, did the movements actually happen, is this submission fresh — is decided
// here now, and only here.
type BiometricService struct {
	sessions   SessionRepository
	cache      SessionCache
	ocrResults OCRResultRepository
	biometrics BiometricRepository
	provider   LivenessProvider
	integrity  IntegrityVerifier
	challenges LivenessChallengeStore
	attempts   LivenessAttemptTracker
	attemptLog LivenessAttemptRepository
	storage    ObjectStorage
	aes        *crypto.AES
	audit      AuditRepository
	cfg        LivenessConfig
}

type BiometricServiceConfig struct {
	Sessions   SessionRepository
	Cache      SessionCache
	OCRResults OCRResultRepository
	Biometrics BiometricRepository
	Provider   LivenessProvider
	Integrity  IntegrityVerifier
	Challenges LivenessChallengeStore
	Attempts   LivenessAttemptTracker
	AttemptLog LivenessAttemptRepository
	Storage    ObjectStorage
	AES        *crypto.AES
	Audit      AuditRepository
	Liveness   LivenessConfig
}

func NewBiometricService(cfg BiometricServiceConfig) *BiometricService {
	liveness := cfg.Liveness
	if liveness.ActionCount == 0 {
		liveness = DefaultLivenessConfig()
	}
	return &BiometricService{
		sessions:   cfg.Sessions,
		cache:      cfg.Cache,
		ocrResults: cfg.OCRResults,
		biometrics: cfg.Biometrics,
		provider:   cfg.Provider,
		integrity:  cfg.Integrity,
		challenges: cfg.Challenges,
		attempts:   cfg.Attempts,
		attemptLog: cfg.AttemptLog,
		storage:    cfg.Storage,
		aes:        cfg.AES,
		audit:      cfg.Audit,
		cfg:        liveness,
	}
}

// ProcessBiometric verifies a submission and advances the step only on a pass.
func (s *BiometricService) ProcessBiometric(
	ctx context.Context,
	submission *LivenessSubmission,
	ipAddress, userAgent string,
) (*BiometricResponse, error) {
	if submission == nil {
		return nil, apperr.ValidationError
	}

	session, err := s.resolveSession(ctx, submission.SessionID)
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
	if submission.DeviceID == "" || submission.DeviceID != session.DeviceID {
		return nil, apperr.OnboardingDeviceMismatch
	}

	// Cooldown and block are checked before anything expensive happens, and before
	// the nonce is consumed: a submission arriving during a cooldown must not burn
	// a challenge the customer will need afterwards.
	if err := s.assertAttemptAllowed(ctx, submission.SessionID, submission.DeviceID); err != nil {
		return nil, err
	}

	// Consume is atomic. Anything unknown, expired, or already used comes back nil,
	// and that single answer covers replay, expiry and a fabricated challenge_id —
	// which of the three it was is not told to the caller.
	challenge, err := s.challenges.Consume(ctx, submission.ChallengeID)
	if err != nil {
		slog.Error("consume liveness challenge failed",
			"session_id", submission.SessionID, "error", err)
		return nil, apperr.InternalError
	}
	if challenge == nil {
		return nil, s.refuse(ctx, submission, nil, "challenge_unknown_or_spent",
			apperr.LivenessChallengeInvalid, ipAddress, userAgent)
	}

	// The challenge must belong to this session, this device, and carry this nonce.
	// Each of these is a separate way a valid-looking challenge_id could be paired
	// with someone else's submission.
	if challenge.SessionID != submission.SessionID ||
		challenge.DeviceID != submission.DeviceID ||
		challenge.Nonce != submission.Nonce {
		return nil, s.refuse(ctx, submission, challenge, "challenge_binding_mismatch",
			apperr.LivenessChallengeInvalid, ipAddress, userAgent)
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return nil, s.refuse(ctx, submission, challenge, "challenge_expired",
			apperr.LivenessChallengeInvalid, ipAddress, userAgent)
	}

	// The signature is checked against the key registered when the challenge was
	// issued, never against the key in the submission — otherwise an attacker
	// signs with a key of their own and sends it along.
	payload := livenessSignedPayload(
		challenge.ChallengeID,
		challenge.Nonce,
		challenge.DeviceID,
		submission.NeutralFrame,
		submission.StepFrames,
	)
	if err := crypto.VerifyECDSASignatureBase64(
		challenge.DevicePublicKey, payload, submission.Signature,
	); err != nil {
		return nil, s.refuse(ctx, submission, challenge, "signature_invalid",
			apperr.LivenessSignatureInvalid, ipAddress, userAgent)
	}

	integrity, err := s.checkIntegrity(ctx, submission, challenge)
	if err != nil {
		return nil, err
	}
	if !integrity.Allowed {
		return nil, s.refuseVerdict(ctx, submission, challenge,
			&LivenessVerdict{Reason: "integrity_failed"},
			apperr.LivenessIntegrityFailed, ipAddress, userAgent)
	}

	reference := s.loadReference(ctx, submission.SessionID)

	verdict, err := s.provider.Verify(ctx, challenge, submission, reference)
	if verdict != nil {
		verdict.IntegrityOK = integrity.Verified
	}
	if err != nil {
		slog.Error("liveness provider error",
			"provider", s.provider.Name(), "session_id", submission.SessionID, "error", err)
		// A provider that errored did not judge, so there is no verdict to act on.
		// Fail closed: refuse, and say it was unavailable rather than failed.
		return nil, s.refuseVerdict(ctx, submission, challenge,
			&LivenessVerdict{Reason: "provider_error", IntegrityOK: integrity.Verified},
			apperr.LivenessProviderUnavailable, ipAddress, userAgent)
	}
	if verdict == nil {
		return nil, s.refuseVerdict(ctx, submission, challenge,
			&LivenessVerdict{Reason: "provider_no_verdict", IntegrityOK: integrity.Verified},
			apperr.LivenessProviderUnavailable, ipAddress, userAgent)
	}

	if !verdict.Passed {
		var appErr apperr.Error
		switch {
		case verdict.Unavailable:
			appErr = apperr.LivenessProviderUnavailable
		case verdict.Reason == "face_match_below_threshold":
			appErr = apperr.BioFaceNotMatch
		case verdict.Reason == "step_frame_face_count",
			verdict.Reason == "neutral_frame_face_count":
			appErr = apperr.BioMultipleFaces
		default:
			appErr = apperr.BioLivenessFailed
		}
		return nil, s.refuseVerdict(ctx, submission, challenge, verdict, appErr, ipAddress, userAgent)
	}

	return s.recordPass(ctx, session, submission, challenge, verdict, ipAddress, userAgent)
}

// checkIntegrity applies the Play Integrity policy for this environment.
// integrityOutcome separates what the verdict WAS from what policy does about it.
//
// Collapsing the two into one boolean is a mistake that has now been made twice in
// this file: in log-only mode a failed verdict came back as "true", and the audit
// row then recorded integrity_ok=true for an attempt whose device verdict had in
// fact failed. The audit trail must record the verdict; only the handler cares
// about the policy.
type integrityOutcome struct {
	// Verified is the real result: did the device verdict actually pass.
	Verified bool

	// Allowed is the policy decision, which differs from Verified only outside
	// production, where IntegrityLogOnly records a failure without refusing.
	Allowed bool
}

func (s *BiometricService) checkIntegrity(
	ctx context.Context,
	submission *LivenessSubmission,
	challenge *LivenessChallenge,
) (integrityOutcome, error) {
	if s.integrity == nil {
		return integrityOutcome{Verified: false, Allowed: s.cfg.IntegrityLogOnly}, nil
	}

	// The nonce is the challenge's own nonce, so a token minted for a different
	// attempt does not satisfy this one.
	verdict, err := s.integrity.Verify(ctx, submission.IntegrityToken, challenge.Nonce)
	if err != nil {
		slog.Error("play integrity verification failed",
			"session_id", submission.SessionID, "error", err)
		// An unreachable Play Integrity service is not proof of a bad device, but
		// it is not proof of a good one either. In production the attempt refuses.
		return integrityOutcome{Verified: false, Allowed: s.cfg.IntegrityLogOnly}, nil
	}

	if verdict.Acceptable() {
		return integrityOutcome{Verified: true, Allowed: true}, nil
	}

	slog.Warn("play integrity verdict rejected",
		"session_id", submission.SessionID,
		"verdicts", verdict.Verdicts,
		"log_only", s.cfg.IntegrityLogOnly,
		"emulator_likely", submission.RiskSignals.EmulatorLikely,
		"root_artifacts", submission.RiskSignals.RootArtifacts,
	)
	return integrityOutcome{Verified: false, Allowed: s.cfg.IntegrityLogOnly}, nil
}

// loadReference fetches the enrolled KTP photo for face match.
//
// A missing reference is not an error here: the provider is told there is none and
// refuses on that basis. Turning it into a 500 would hide the real situation,
// which is that face match cannot be performed.
func (s *BiometricService) loadReference(ctx context.Context, sessionID string) []byte {
	if s.storage == nil || s.ocrResults == nil {
		return nil
	}
	ocrResult, err := s.ocrResults.FindBySessionID(ctx, sessionID)
	if err != nil || ocrResult == nil || ocrResult.PhotoPath == "" {
		return nil
	}

	// PhotoPath is a full object path ("local://bucket/key"), not a key. Passing
	// it straight to Download as the key is what it used to do, which only went
	// unnoticed because the mock storage ignored both arguments and returned nil.
	// With real storage that lookup misses every time, and the symptom is a face
	// match that silently never finds its reference.
	bucket, key, ok := SplitObjectPath(ocrResult.PhotoPath)
	if !ok {
		slog.Warn("face match reference has an unreadable object path",
			"session_id", sessionID, "path", ocrResult.PhotoPath)
		return nil
	}

	data, err := s.storage.Download(ctx, bucket, key)
	if err != nil || len(data) == 0 {
		slog.Warn("face match reference unavailable", "session_id", sessionID, "error", err)
		return nil
	}
	if s.aes != nil {
		decrypted, err := s.aes.Decrypt(data)
		if err != nil {
			slog.Error("decrypt face match reference failed", "session_id", sessionID)
			return nil
		}
		return decrypted
	}
	return data
}

// --- Outcomes ---

// refuse records a failed attempt that never reached the provider.
func (s *BiometricService) refuse(
	ctx context.Context,
	submission *LivenessSubmission,
	challenge *LivenessChallenge,
	reason string,
	appErr apperr.Error,
	ipAddress, userAgent string,
) error {
	return s.refuseVerdict(ctx, submission, challenge,
		&LivenessVerdict{Reason: reason}, appErr, ipAddress, userAgent)
}

// refuseVerdict records the failure, advances the cooldown, and escalates when the
// customer has run out of self-service attempts.
func (s *BiometricService) refuseVerdict(
	ctx context.Context,
	submission *LivenessSubmission,
	challenge *LivenessChallenge,
	verdict *LivenessVerdict,
	appErr apperr.Error,
	ipAddress, userAgent string,
) error {
	challengeID := submission.ChallengeID
	if challenge != nil {
		challengeID = challenge.ChallengeID
	}

	outcome := LivenessOutcomeFailed
	status := s.recordFailure(ctx, submission)

	if status != nil && status.Blocked {
		// Self-service liveness is over for this onboarding session (decision Q6
		// step 2). The applicant is routed to the video-call queue that already
		// exists, with a risk flag in the audit trail — no new feature is built
		// for this, which the decisions explicitly rule out.
		outcome = LivenessOutcomeEscalated
		appErr = apperr.LivenessEscalated
		s.escalateToVideoCall(ctx, submission.SessionID, verdict, ipAddress, userAgent)
	} else if status != nil && status.RetryAfter > 0 {
		// The customer is now in a cooldown, and the remaining time is more useful
		// to them than the name of the check that refused.
		s.writeAttempt(ctx, submission, challengeID, LivenessOutcomeFailed, verdict,
			ipAddress, userAgent)
		return livenessCooldownError(status.RetryAfter)
	}

	s.writeAttempt(ctx, submission, challengeID, outcome, verdict, ipAddress, userAgent)

	// The reason is audited but never returned: naming the check that refused
	// tells whoever is probing which one to work around next.
	s.writeAudit(ctx, submission.SessionID, AuditBiometricFailed, "system", map[string]any{
		"event":            "liveness_refused",
		"challenge_id":     challengeID,
		"reason":           verdict.Reason,
		"provider":         s.providerName(),
		"liveness_score":   verdict.LivenessScore,
		"face_match_score": verdict.FaceMatchScore,
		"unavailable":      verdict.Unavailable,
	}, ipAddress, userAgent)

	return appErr
}

// escalateToVideoCall marks the session for agent verification with a risk flag.
func (s *BiometricService) escalateToVideoCall(
	ctx context.Context,
	sessionID string,
	verdict *LivenessVerdict,
	ipAddress, userAgent string,
) {
	s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
		"event":        "liveness_escalated_to_video_call",
		"risk_flag":    "liveness_failed",
		"reason":       verdict.Reason,
		"provider":     s.providerName(),
		"escalated_at": time.Now().UTC(),
	}, ipAddress, userAgent)

	// The step moves to VIDEO_CALL so the existing queue picks the applicant up.
	// The biometric step is NOT marked complete: an agent still has to verify the
	// identity, and recording it as verified would be a lie in the audit trail.
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		slog.Error("escalate liveness: session unavailable", "session_id", sessionID, "error", err)
		return
	}
	if err := s.sessions.UpdateStep(ctx, sessionID, StepVideoCall, session.StepsCompleted); err != nil {
		slog.Error("escalate liveness: update step failed", "session_id", sessionID, "error", err)
		return
	}
	if s.cache != nil {
		session.CurrentStep = StepVideoCall
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("escalate liveness: cache update failed", "error", cacheErr)
		}
	}
}

// recordPass stores the result and advances BIOMETRIC -> VIDEO_CALL.
func (s *BiometricService) recordPass(
	ctx context.Context,
	session *Session,
	submission *LivenessSubmission,
	challenge *LivenessChallenge,
	verdict *LivenessVerdict,
	ipAddress, userAgent string,
) (*BiometricResponse, error) {
	shortID, err := generateShortID()
	if err != nil {
		return nil, fmt.Errorf("generate biometric id: %w", err)
	}
	bioID := "bio_" + shortID
	now := time.Now().UTC()

	// The neutral frame is the only frame kept, and only because the account file
	// needs a face on record. The step frames are never persisted: there is no
	// retention policy for them, and §"no face images on disk" of the Phase 2
	// decisions says not to invent one.
	faceKey := fmt.Sprintf("%s/face_%s.jpg", submission.SessionID, uuid.New().String())
	uploaded := false
	if s.storage != nil {
		payload := submission.NeutralFrame
		if s.aes != nil {
			payload, err = s.aes.Encrypt(submission.NeutralFrame)
			if err != nil {
				return nil, fmt.Errorf("encrypt face photo: %w", err)
			}
		}
		if _, err := s.storage.Upload(
			ctx, biometricPhotoBucket, faceKey, payload, "application/octet-stream",
		); err != nil {
			return nil, fmt.Errorf("upload face photo: %w", err)
		}
		uploaded = true
	}

	result := &BiometricResult{
		ID:                uuid.New(),
		BiometricID:       bioID,
		SessionID:         submission.SessionID,
		FacePhotoPath:     faceKey,
		LivenessVerified:  true,
		LivenessScore:     verdict.LivenessScore,
		FaceMatchVerified: verdict.FaceMatchPassed,
		FaceMatchScore:    verdict.FaceMatchScore,
		FrameCount:        len(submission.StepFrames),
		CreatedAt:         now,
		AutoDeleteAt:      now.Add(biometricAutoDeleteTTL),
	}

	if err := s.biometrics.Create(ctx, result); err != nil {
		if uploaded {
			if delErr := s.storage.Delete(ctx, biometricPhotoBucket, faceKey); delErr != nil {
				slog.Error("discard face photo failed", "key", faceKey, "error", delErr)
			}
		}
		return nil, fmt.Errorf("store biometric result: %w", err)
	}

	completed := session.StepsCompleted
	completed.BiometricVerified = true
	if err := s.sessions.UpdateStep(ctx, submission.SessionID, StepVideoCall, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}
	if s.cache != nil {
		session.CurrentStep = StepVideoCall
		session.StepsCompleted = completed
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "error", cacheErr)
		}
	}

	if s.attempts != nil {
		if err := s.attempts.Reset(ctx, submission.SessionID, submission.DeviceID); err != nil {
			slog.Error("reset liveness attempts failed", "session_id", submission.SessionID, "error", err)
		}
	}

	s.writeAttempt(ctx, submission, challenge.ChallengeID, LivenessOutcomePassed, verdict,
		ipAddress, userAgent)
	s.writeAudit(ctx, submission.SessionID, AuditBiometricVerified, "system", map[string]any{
		"event":            "liveness_passed",
		"biometric_id":     bioID,
		"challenge_id":     challenge.ChallengeID,
		"actions":          actionNames(challenge.Actions),
		"provider":         s.providerName(),
		"liveness_score":   verdict.LivenessScore,
		"face_match_score": verdict.FaceMatchScore,
	}, ipAddress, userAgent)

	return &BiometricResponse{
		BiometricID:      bioID,
		LivenessVerified: true,
		LivenessScore:    verdict.LivenessScore,
		FaceMatchWithKTP: verdict.FaceMatchPassed,
		FaceMatchScore:   verdict.FaceMatchScore,
		CurrentStep:      StepVideoCall,
	}, nil
}

// --- Helpers ---

func (s *BiometricService) assertAttemptAllowed(ctx context.Context, sessionID, deviceID string) error {
	if s.attempts == nil {
		return nil
	}
	status, err := s.attempts.Status(ctx, sessionID, deviceID)
	if err != nil {
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

func (s *BiometricService) recordFailure(
	ctx context.Context,
	submission *LivenessSubmission,
) *LivenessAttemptStatus {
	if s.attempts == nil {
		return nil
	}
	status, err := s.attempts.RecordFailure(ctx, submission.SessionID, submission.DeviceID)
	if err != nil {
		slog.Error("record liveness failure failed",
			"session_id", submission.SessionID, "error", err)
		return nil
	}
	return status
}

func (s *BiometricService) providerName() string {
	if s.provider == nil {
		return "none"
	}
	return s.provider.Name()
}

// writeAttempt appends the audit row. It carries scores and reasons, never frames.
func (s *BiometricService) writeAttempt(
	ctx context.Context,
	submission *LivenessSubmission,
	challengeID string,
	outcome LivenessAttemptOutcome,
	verdict *LivenessVerdict,
	ipAddress, userAgent string,
) {
	if s.attemptLog == nil {
		return
	}
	attempt := &LivenessAttempt{
		ID:             uuid.New(),
		SessionID:      submission.SessionID,
		ChallengeID:    challengeID,
		DeviceID:       submission.DeviceID,
		Outcome:        outcome,
		Reason:         verdict.Reason,
		LivenessScore:  verdict.LivenessScore,
		FaceMatchScore: verdict.FaceMatchScore,
		FrameCount:     len(submission.StepFrames),
		IntegrityOK:    verdict.IntegrityOK,
		RiskSignals:    submission.RiskSignals,
		IPAddress:      ipAddress,
		UserAgent:      userAgent,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.attemptLog.Insert(ctx, attempt); err != nil {
		slog.Error("insert liveness attempt failed",
			"session_id", submission.SessionID, "error", err)
	}
}

func (s *BiometricService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
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
	if s.cache != nil {
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("backfill cache failed", "error", cacheErr)
		}
	}
	return session, nil
}

func (s *BiometricService) writeAudit(
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
		slog.Error("biometric audit insert failed",
			"session_id", sessionID, "event_type", eventType, "error", err)
	}
}
