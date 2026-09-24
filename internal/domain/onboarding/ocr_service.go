package onboarding

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

const (
	ocrPhotoBucket   = "onboarding-ktp"
	ocrAutoDeleteTTL = 30 * 24 * time.Hour // 30 days
)

type OCRService struct {
	sessions    SessionRepository
	cache       SessionCache
	ocrResults  OCRResultRepository
	ocrEngine   OCREngine
	dukcapil    DukcapilClient
	storage     ObjectStorage
	rateLimiter OCRRateLimiter
	aes         *crypto.AES
	audit       AuditRepository
}

type OCRServiceConfig struct {
	Sessions    SessionRepository
	Cache       SessionCache
	OCRResults  OCRResultRepository
	OCREngine   OCREngine
	Dukcapil    DukcapilClient
	Storage     ObjectStorage
	RateLimiter OCRRateLimiter
	AES         *crypto.AES
	Audit       AuditRepository
}

func NewOCRService(cfg OCRServiceConfig) *OCRService {
	return &OCRService{
		sessions:    cfg.Sessions,
		cache:       cfg.Cache,
		ocrResults:  cfg.OCRResults,
		ocrEngine:   cfg.OCREngine,
		dukcapil:    cfg.Dukcapil,
		storage:     cfg.Storage,
		rateLimiter: cfg.RateLimiter,
		aes:         cfg.AES,
		audit:       cfg.Audit,
	}
}

// ProcessKTP handles the full OCR pipeline:
//  1. Validate session step == OCR
//  2. Rate limit check
//  3. Run OCR engine
//  4. Parse KTP fields
//  5. Validate NIK
//  6. Verify via Dukcapil
//  7. Assess photo quality
//  8. Upload photo to object storage (encrypted at rest)
//  9. Store results (encrypted PII)
//  10. Transition session to PERSONAL_DATA
//
// The upload deliberately comes after every rejection path: a photo that fails
// NIK parsing, Dukcapil, or the quality gate must never be left sitting in the
// bucket, and anything written after it is cleaned up on failure.
func (s *OCRService) ProcessKTP(ctx context.Context, sessionID string, imageData []byte, captureMeta DeviceCaptureMeta, ipAddress, userAgent string) (*OCRResponse, error) {
	// 1. Resolve session and validate step
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepOCR {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan OCR.", session.CurrentStep),
		}
	}

	// 2. Rate limit: 10 attempts per hour per session
	if s.rateLimiter != nil {
		allowed, err := s.rateLimiter.CheckOCRAttempt(ctx, sessionID)
		if err != nil {
			slog.Error("ocr rate limiter error", "error", err)
			return nil, apperr.InternalError
		}
		if !allowed {
			return nil, apperr.RateLimitExceeded
		}
	}

	// Audit: upload event
	s.writeAudit(ctx, sessionID, AuditOCRUploaded, "nasabah:"+session.DeviceID, map[string]any{
		"image_size":    len(imageData),
		"flash_used":    captureMeta.FlashUsed,
		"auto_captured": captureMeta.AutoCaptured,
	}, ipAddress, userAgent)

	// 3. Run OCR engine
	var rawText string
	var confidence float64
	if s.ocrEngine != nil {
		rawText, confidence, err = s.ocrEngine.ExtractText(ctx, imageData)
		if err != nil {
			return nil, fmt.Errorf("ocr extract: %w", err)
		}
	}

	// 4. Parse KTP fields from raw text
	ktpData := ParseKTPFromText(rawText)

	// 5. Validate NIK format
	if ktpData.NIK == "" {
		return nil, apperr.OCRNotKTP
	}
	if err := ValidateNIK(ktpData.NIK); err != nil {
		return nil, apperr.OCRNotKTP
	}

	// 6. Verify via Dukcapil
	dukcapilMatch := false
	if s.dukcapil != nil {
		match, dErr := s.dukcapil.VerifyNIK(ctx, ktpData.NIK, ktpData.NamaLengkap)
		if dErr != nil {
			// Only a genuine timeout is retryable; anything else is an
			// upstream fault and must not masquerade as one.
			if isTimeout(dErr) {
				slog.Warn("dukcapil verify timed out", "session_id", sessionID, "error", dErr)
				return nil, apperr.OCRDukcapilTimeout
			}
			slog.Error("dukcapil verify failed", "session_id", sessionID, "error", dErr)
			return nil, apperr.OCRDukcapilUnavailable
		}
		dukcapilMatch = match
		if !match {
			return nil, apperr.OCRDukcapilMismatch
		}
	} else {
		// Mock mode: assume match
		dukcapilMatch = true
	}

	// 7. Assess photo quality from what the capture SDK measured plus the
	// engine's own confidence.
	quality := assessPhotoQuality(captureMeta, confidence)
	if quality.Sharpness == "LOW" {
		return nil, apperr.OCRPhotoBlurry
	}
	if quality.GlareDetected {
		return nil, apperr.OCRGlareDetected
	}
	if !quality.AllCornersVisible {
		return nil, apperr.OCRCornersMissing
	}

	// 8. Upload photo to object storage (encrypted at rest)
	photoObjectKey := fmt.Sprintf("%s/%s.jpg", sessionID, uuid.New().String())
	var photoPath string
	if s.storage != nil {
		encrypted, encErr := s.encryptBytes(imageData)
		if encErr != nil {
			return nil, fmt.Errorf("encrypt ktp photo: %w", encErr)
		}
		path, uploadErr := s.storage.Upload(ctx, ocrPhotoBucket, photoObjectKey, encrypted, "application/octet-stream")
		if uploadErr != nil {
			return nil, fmt.Errorf("upload ktp photo: %w", uploadErr)
		}
		photoPath = path
	} else {
		photoPath = "local://" + photoObjectKey
	}

	// 9. Encrypt PII and store results
	shortID, err := generateShortID()
	if err != nil {
		s.discardPhoto(ctx, photoObjectKey)
		return nil, fmt.Errorf("generate ocr id: %w", err)
	}
	ocrID := "ocr_" + shortID

	nikEnc, err := s.encryptField(ktpData.NIK)
	if err != nil {
		s.discardPhoto(ctx, photoObjectKey)
		return nil, fmt.Errorf("encrypt nik: %w", err)
	}
	namaEnc, err := s.encryptField(ktpData.NamaLengkap)
	if err != nil {
		s.discardPhoto(ctx, photoObjectKey)
		return nil, fmt.Errorf("encrypt nama: %w", err)
	}
	alamatEnc, err := s.encryptField(ktpData.Alamat)
	if err != nil {
		s.discardPhoto(ctx, photoObjectKey)
		return nil, fmt.Errorf("encrypt alamat: %w", err)
	}

	result := &OCRResult{
		ID:            uuid.New(),
		OCRID:         ocrID,
		SessionID:     sessionID,
		PhotoPath:     photoPath,
		AccuracyPct:   confidence,
		Extracted:     ktpData,
		DukcapilMatch: dukcapilMatch,
		PhotoQuality:  quality,
		CreatedAt:     time.Now().UTC(),
		AutoDeleteAt:  time.Now().UTC().Add(ocrAutoDeleteTTL),
	}

	// Store with encrypted PII in internal fields for repo
	result.Extracted.NIK = nikEnc
	result.Extracted.NamaLengkap = namaEnc
	result.Extracted.Alamat = alamatEnc

	if err := s.ocrResults.Create(ctx, result); err != nil {
		s.discardPhoto(ctx, photoObjectKey)
		return nil, fmt.Errorf("store ocr result: %w", err)
	}

	// 10. Transition session step: OCR → PERSONAL_DATA
	completed := session.StepsCompleted
	completed.OCRVerified = true
	if err := s.sessions.UpdateStep(ctx, sessionID, StepPersonalData, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	// Update cache (expiry is fixed at creation — see SessionService.TransitionStep)
	if s.cache != nil {
		session.CurrentStep = StepPersonalData
		session.StepsCompleted = completed
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "error", cacheErr)
		}
	}

	// Audit: OCR verified
	s.writeAudit(ctx, sessionID, AuditOCRVerified, "system", map[string]any{
		"ocr_id":         ocrID,
		"accuracy_pct":   confidence,
		"dukcapil_match": dukcapilMatch,
	}, ipAddress, userAgent)

	// Return response with plaintext data (decrypted for client display)
	return &OCRResponse{
		OCRID:         ocrID,
		AccuracyPct:   confidence,
		Extracted:     ktpData, // Note: nik/nama/alamat are encrypted in DB but we return original values
		DukcapilMatch: dukcapilMatch,
		PhotoQuality:  quality,
		CurrentStep:   StepPersonalData,
	}, nil
}

// discardPhoto removes an already-uploaded KTP photo when a later step fails.
// Best effort: a failure here is logged, never returned — the caller is already
// on an error path and the object has an auto-delete lifecycle behind it.
func (s *OCRService) discardPhoto(ctx context.Context, key string) {
	if s.storage == nil || key == "" {
		return
	}
	if err := s.storage.Delete(ctx, ocrPhotoBucket, key); err != nil {
		slog.Error("discard ktp photo failed", "key", key, "error", err)
	}
}

// isTimeout reports whether err is a deadline/timeout rather than a hard fault.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// GetOCRResult retrieves the OCR result for a session, decrypting PII fields.
func (s *OCRService) GetOCRResult(ctx context.Context, sessionID string) (*OCRResult, error) {
	result, err := s.ocrResults.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find ocr result: %w", err)
	}
	if result == nil {
		return nil, apperr.OnboardingNotFound
	}

	// Decrypt PII fields
	if s.aes != nil {
		if nik, err := s.decryptField(result.Extracted.NIK); err == nil {
			result.Extracted.NIK = nik
		}
		if nama, err := s.decryptField(result.Extracted.NamaLengkap); err == nil {
			result.Extracted.NamaLengkap = nama
		}
		if alamat, err := s.decryptField(result.Extracted.Alamat); err == nil {
			result.Extracted.Alamat = alamat
		}
	}

	return result, nil
}

// encryptBytes encrypts raw bytes when a PII key is configured. Without one
// (local dev, AES_KEY unset) the bytes pass through — the same nil-tolerance
// encryptField and BiometricService already have.
func (s *OCRService) encryptBytes(plaintext []byte) ([]byte, error) {
	if s.aes == nil {
		return plaintext, nil
	}
	return s.aes.Encrypt(plaintext)
}

func (s *OCRService) encryptField(plaintext string) (string, error) {
	if s.aes == nil || plaintext == "" {
		return plaintext, nil
	}
	enc, err := s.aes.Encrypt([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(enc), nil
}

func (s *OCRService) decryptField(ciphertextHex string) (string, error) {
	if s.aes == nil || ciphertextHex == "" {
		return ciphertextHex, nil
	}
	ct, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", err
	}
	pt, err := s.aes.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func (s *OCRService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
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

func (s *OCRService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("ocr audit insert failed",
			"session_id", sessionID,
			"event_type", eventType,
			"error", err,
		)
	}
}

// Quality thresholds. The capture SDK on the device reports scores on a 0-100
// scale; the OCR engine reports its own confidence the same way.
const (
	minSharpnessScore  = 60.0 // below this the KTP text is not reliably readable
	maxGlareScore      = 50.0 // above this a reflection covers part of the card
	minOCRConfidence   = 75.0 // engine confidence that low means a bad capture
	requiredKTPCorners = 4
	minCaptureWidth    = 640
	minCaptureHeight   = 480
)

// assessPhotoQuality judges the capture from the signals actually available:
// what the device's capture SDK measured, the frame resolution, and how
// confident the OCR engine was in its own reading.
//
// Every signal is optional — a client that reports nothing is not punished for
// it — but when a signal arrives and it is bad, the capture is rejected. That
// is the difference from the previous version, which always returned HIGH and
// so made OCR_PHOTO_BLURRY, OCR_GLARE_DETECTED and OCR_CORNERS_MISSING
// unreachable.
func assessPhotoQuality(meta DeviceCaptureMeta, ocrConfidence float64) PhotoQuality {
	q := PhotoQuality{
		Sharpness:         "HIGH",
		GlareDetected:     false,
		AllCornersVisible: true,
	}

	if meta.SharpnessScore != nil && *meta.SharpnessScore < minSharpnessScore {
		q.Sharpness = "LOW"
	}

	// A confident engine reading is itself evidence the frame was legible;
	// a weak one on a card we did manage to parse means a marginal capture.
	if ocrConfidence > 0 && ocrConfidence < minOCRConfidence {
		q.Sharpness = "LOW"
	}

	if w, h, ok := parseResolution(meta.Resolution); ok && (w < minCaptureWidth || h < minCaptureHeight) {
		q.Sharpness = "LOW"
	}

	// Flash on its own is not a rejection — it only raises the prior. The
	// measured glare score decides.
	if meta.GlareScore != nil && *meta.GlareScore >= maxGlareScore {
		q.GlareDetected = true
	}

	if meta.CornersDetected != nil && *meta.CornersDetected < requiredKTPCorners {
		q.AllCornersVisible = false
	}

	return q
}

// parseResolution reads a "1920x1080" capture resolution. Returns ok=false for
// anything it cannot make sense of, which the caller treats as "no signal".
func parseResolution(res string) (width, height int, ok bool) {
	res = strings.ToLower(strings.TrimSpace(res))
	wStr, hStr, found := strings.Cut(res, "x")
	if !found {
		return 0, 0, false
	}
	w, err := strconv.Atoi(strings.TrimSpace(wStr))
	if err != nil || w <= 0 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(strings.TrimSpace(hStr))
	if err != nil || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
