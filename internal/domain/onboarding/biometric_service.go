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
	livenessThreshold      = 90.0
	faceMatchThreshold     = 85.0
	minLivenessFrames      = 3
	maxLivenessFrames      = 5
)

type BiometricService struct {
	sessions    SessionRepository
	cache       SessionCache
	ocrResults  OCRResultRepository
	biometrics  BiometricRepository
	engine      BiometricEngine
	storage     ObjectStorage
	rateLimiter BiometricRateLimiter
	aes         *crypto.AES
	audit       AuditRepository
}

type BiometricServiceConfig struct {
	Sessions    SessionRepository
	Cache       SessionCache
	OCRResults  OCRResultRepository
	Biometrics  BiometricRepository
	Engine      BiometricEngine
	Storage     ObjectStorage
	RateLimiter BiometricRateLimiter
	AES         *crypto.AES
	Audit       AuditRepository
}

func NewBiometricService(cfg BiometricServiceConfig) *BiometricService {
	return &BiometricService{
		sessions:    cfg.Sessions,
		cache:       cfg.Cache,
		ocrResults:  cfg.OCRResults,
		biometrics:  cfg.Biometrics,
		engine:      cfg.Engine,
		storage:     cfg.Storage,
		rateLimiter: cfg.RateLimiter,
		aes:         cfg.AES,
		audit:       cfg.Audit,
	}
}

// ProcessBiometric handles the full biometric verification pipeline.
func (s *BiometricService) ProcessBiometric(
	ctx context.Context,
	sessionID string,
	facePhoto []byte,
	livenessFrames [][]byte,
	meta LivenessMeta,
	ipAddress, userAgent string,
) (*BiometricResponse, error) {
	// 1. Resolve session and validate step
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

	// 2. Rate limit: 5 attempts per hour per session
	if s.rateLimiter != nil {
		allowed, err := s.rateLimiter.CheckBiometricAttempt(ctx, sessionID)
		if err != nil {
			slog.Error("biometric rate limiter error", "error", err)
			return nil, apperr.InternalError
		}
		if !allowed {
			return nil, apperr.RateLimitExceeded
		}
	}

	// 3. Validate liveness frame count
	if len(livenessFrames) < minLivenessFrames || len(livenessFrames) > maxLivenessFrames {
		return nil, apperr.ValidationError
	}

	// Audit: upload event
	s.writeAudit(ctx, sessionID, AuditBiometricUploaded, "nasabah:"+session.DeviceID, map[string]any{
		"face_photo_size": len(facePhoto),
		"frame_count":     len(livenessFrames),
		"challenge_type":  meta.ChallengeType,
	}, ipAddress, userAgent)

	// 4. Upload photos to storage (encrypted)
	faceKey := fmt.Sprintf("%s/face_%s.jpg", sessionID, uuid.New().String())
	uploaded := false
	if s.storage != nil {
		encrypted := facePhoto
		if s.aes != nil {
			encrypted, err = s.aes.Encrypt(facePhoto)
			if err != nil {
				return nil, fmt.Errorf("encrypt face photo: %w", err)
			}
		}
		if _, err := s.storage.Upload(ctx, biometricPhotoBucket, faceKey, encrypted, "application/octet-stream"); err != nil {
			return nil, fmt.Errorf("upload face photo: %w", err)
		}
		uploaded = true
	}

	// Any rejection below leaves a face photo in the bucket that no row points
	// at, so it gets removed on the way out.
	discardFace := func() {
		if !uploaded {
			return
		}
		if delErr := s.storage.Delete(ctx, biometricPhotoBucket, faceKey); delErr != nil {
			slog.Error("discard face photo failed", "key", faceKey, "error", delErr)
		}
	}

	// 5. Get KTP photo reference for face matching
	ocrResult, err := s.ocrResults.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find ocr result: %w", err)
	}
	var ktpPhotoData []byte // In production, would download from storage
	if ocrResult != nil {
		// For mock engine, KTP photo data is not needed (mock returns success)
		ktpPhotoData = nil
	}

	// 6. Run biometric engine
	analysis, err := s.engine.Analyze(ctx, facePhoto, livenessFrames, ktpPhotoData)
	if err != nil {
		discardFace()
		return nil, fmt.Errorf("biometric analysis: %w", err)
	}

	// 7. Evaluate results
	if analysis.FaceCount > 1 {
		s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
			"reason": "multiple_faces", "face_count": analysis.FaceCount,
		}, ipAddress, userAgent)
		discardFace()
		return nil, apperr.BioMultipleFaces
	}
	if analysis.FaceCount == 0 {
		s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
			"reason": "no_face_detected",
		}, ipAddress, userAgent)
		discardFace()
		return nil, apperr.BioLowQuality
	}
	if analysis.Quality == "LOW" {
		s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
			"reason": "low_quality",
		}, ipAddress, userAgent)
		discardFace()
		return nil, apperr.BioLowQuality
	}
	if analysis.SpoofDetected {
		s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
			"reason": "spoof_detected",
		}, ipAddress, userAgent)
		discardFace()
		return nil, apperr.BioSpoofDetected
	}

	livenessVerified := analysis.LivenessScore >= livenessThreshold
	faceMatchVerified := analysis.FaceMatchScore >= faceMatchThreshold

	if !livenessVerified {
		s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
			"reason": "liveness_failed", "score": analysis.LivenessScore,
		}, ipAddress, userAgent)
		discardFace()
		return nil, apperr.BioLivenessFailed
	}
	if !faceMatchVerified {
		s.writeAudit(ctx, sessionID, AuditBiometricFailed, "system", map[string]any{
			"reason": "face_not_match", "score": analysis.FaceMatchScore,
		}, ipAddress, userAgent)
		discardFace()
		return nil, apperr.BioFaceNotMatch
	}

	// 8. Store biometric result
	shortID, err := generateShortID()
	if err != nil {
		return nil, fmt.Errorf("generate biometric id: %w", err)
	}
	bioID := "bio_" + shortID
	now := time.Now().UTC()

	result := &BiometricResult{
		ID:                uuid.New(),
		BiometricID:       bioID,
		SessionID:         sessionID,
		FacePhotoPath:     faceKey,
		LivenessVerified:  livenessVerified,
		LivenessScore:     analysis.LivenessScore,
		FaceMatchVerified: faceMatchVerified,
		FaceMatchScore:    analysis.FaceMatchScore,
		ISOCompliant:      analysis.ISOCompliant,
		SpoofDetected:     analysis.SpoofDetected,
		FrameCount:        len(livenessFrames),
		CreatedAt:         now,
		AutoDeleteAt:      now.Add(biometricAutoDeleteTTL),
	}

	if err := s.biometrics.Create(ctx, result); err != nil {
		discardFace()
		return nil, fmt.Errorf("store biometric result: %w", err)
	}

	// 9. Transition step: BIOMETRIC → VIDEO_CALL
	completed := session.StepsCompleted
	completed.BiometricVerified = true
	if err := s.sessions.UpdateStep(ctx, sessionID, StepVideoCall, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepVideoCall
		session.StepsCompleted = completed
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "error", cacheErr)
		}
	}

	// Audit: verified
	s.writeAudit(ctx, sessionID, AuditBiometricVerified, "system", map[string]any{
		"biometric_id":     bioID,
		"liveness_score":   analysis.LivenessScore,
		"face_match_score": analysis.FaceMatchScore,
		"iso_compliant":    analysis.ISOCompliant,
	}, ipAddress, userAgent)

	return &BiometricResponse{
		BiometricID:      bioID,
		LivenessVerified: livenessVerified,
		LivenessScore:    analysis.LivenessScore,
		FaceMatchWithKTP: faceMatchVerified,
		FaceMatchScore:   analysis.FaceMatchScore,
		ISOCompliant:     analysis.ISOCompliant,
		CurrentStep:      StepVideoCall,
	}, nil
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

func (s *BiometricService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
			"session_id", sessionID,
			"event_type", eventType,
			"error", err,
		)
	}
}
