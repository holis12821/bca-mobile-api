package onboarding

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
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
	sessions   SessionRepository
	cache      SessionCache
	ocrResults OCRResultRepository
	ocrEngine  OCREngine
	dukcapil   DukcapilClient
	storage    ObjectStorage
	rateLimiter OCRRateLimiter
	aes        *crypto.AES
	audit      AuditRepository
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
// 1. Validate session step == OCR
// 2. Rate limit check
// 3. Upload photo to object storage
// 4. Run OCR engine
// 5. Parse KTP fields
// 6. Validate NIK
// 7. Verify via Dukcapil
// 8. Assess photo quality
// 9. Store results (encrypted PII)
// 10. Transition session to PERSONAL_DATA
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
		"image_size":   len(imageData),
		"flash_used":   captureMeta.FlashUsed,
		"auto_captured": captureMeta.AutoCaptured,
	}, ipAddress, userAgent)

	// 3. Upload photo to object storage (encrypted at rest)
	photoKey := fmt.Sprintf("%s/%s.jpg", sessionID, uuid.New().String())
	var photoPath string
	if s.storage != nil {
		// Encrypt image before uploading
		encrypted, encErr := s.aes.Encrypt(imageData)
		if encErr != nil {
			return nil, fmt.Errorf("encrypt ktp photo: %w", encErr)
		}
		path, uploadErr := s.storage.Upload(ctx, ocrPhotoBucket, photoKey, encrypted, "application/octet-stream")
		if uploadErr != nil {
			return nil, fmt.Errorf("upload ktp photo: %w", uploadErr)
		}
		photoPath = path
	} else {
		photoPath = "local://" + photoKey
	}

	// 4. Run OCR engine
	var rawText string
	var confidence float64
	if s.ocrEngine != nil {
		rawText, confidence, err = s.ocrEngine.ExtractText(ctx, imageData)
		if err != nil {
			return nil, fmt.Errorf("ocr extract: %w", err)
		}
	}

	// 5. Parse KTP fields from raw text
	ktpData := ParseKTPFromText(rawText)

	// 6. Validate NIK format
	if ktpData.NIK == "" {
		return nil, apperr.OCRNotKTP
	}
	if err := ValidateNIK(ktpData.NIK); err != nil {
		return nil, apperr.OCRNotKTP
	}

	// 7. Verify via Dukcapil
	dukcapilMatch := false
	if s.dukcapil != nil {
		match, dErr := s.dukcapil.VerifyNIK(ctx, ktpData.NIK, ktpData.NamaLengkap)
		if dErr != nil {
			// Timeout is retryable
			return nil, apperr.OCRDukcapilTimeout
		}
		dukcapilMatch = match
		if !match {
			return nil, apperr.OCRDukcapilMismatch
		}
	} else {
		// Mock mode: assume match
		dukcapilMatch = true
	}

	// 8. Assess photo quality (simplified; production uses CV model)
	quality := assessPhotoQuality(captureMeta)
	if quality.Sharpness == "LOW" {
		return nil, apperr.OCRPhotoBlurry
	}
	if quality.GlareDetected {
		return nil, apperr.OCRGlareDetected
	}
	if !quality.AllCornersVisible {
		return nil, apperr.OCRCornersMissing
	}

	// 9. Encrypt PII and store results
	ocrID := "ocr_" + generateShortID()

	nikEnc, err := s.encryptField(ktpData.NIK)
	if err != nil {
		return nil, fmt.Errorf("encrypt nik: %w", err)
	}
	namaEnc, err := s.encryptField(ktpData.NamaLengkap)
	if err != nil {
		return nil, fmt.Errorf("encrypt nama: %w", err)
	}
	alamatEnc, err := s.encryptField(ktpData.Alamat)
	if err != nil {
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
		return nil, fmt.Errorf("store ocr result: %w", err)
	}

	// 10. Transition session step: OCR → PERSONAL_DATA
	completed := session.StepsCompleted
	completed.OCRVerified = true
	if err := s.sessions.UpdateStep(ctx, sessionID, StepPersonalData, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	// Update cache
	if s.cache != nil {
		session.CurrentStep = StepPersonalData
		session.StepsCompleted = completed
		session.ExpiresAt = time.Now().Add(sessionTTL)
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

// assessPhotoQuality performs a simplified quality check.
// In production, this would use a computer vision model.
func assessPhotoQuality(meta DeviceCaptureMeta) PhotoQuality {
	q := PhotoQuality{
		Sharpness:         "HIGH",
		GlareDetected:     false,
		AllCornersVisible: true,
	}

	// If flash was used, there's a higher chance of glare
	if meta.FlashUsed {
		q.GlareDetected = false // still allow, but flag is available
	}

	return q
}
