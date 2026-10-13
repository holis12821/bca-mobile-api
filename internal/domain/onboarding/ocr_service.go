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
//  3. Obtain the KTP text (server-side engine, else the client's on-device OCR)
//  4. Confirm the text actually comes from an e-KTP
//  5. Parse KTP fields
//  6. Validate NIK structure and its internal consistency with the parsed fields
//  7. Verify via Dukcapil, when a registry is configured at all
//  8. Assess photo quality
//  9. Upload photo to object storage (encrypted at rest)
//  10. Store results (encrypted PII)
//  11. Transition session to PERSONAL_DATA
//
// The upload deliberately comes after every rejection path: a photo that fails
// NIK parsing, Dukcapil, or the quality gate must never be left sitting in the
// bucket, and anything written after it is cleaned up on failure.
//
// Step 3 and 4 are what make this read a card rather than accept a file. Before
// them the engine ignored the image bytes and returned a hardcoded identity, so
// a plain grey rectangle was accepted with accuracy_percent 99.4 and advanced
// the session to PERSONAL_DATA with somebody else's name in it.
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

	// 3. Obtain the KTP text.
	//
	// A server-side engine wins when one is configured. Otherwise the text comes
	// from the client's on-device recognizer (ML Kit), which already ran on this
	// exact photo before the upload. There is no third branch: without text
	// nothing is extracted, and the request is refused rather than answered with
	// an invented identity.
	rawText, textSource, err := s.extractText(ctx, imageData, captureMeta)
	if err != nil {
		return nil, err
	}

	// 4. Confirm the text belongs to an e-KTP.
	//
	// This is the check that rejects a photo of something else. It looks for the
	// card's own LABELS, not its values: labels are identical on every card,
	// values differ per person. A receipt, a driving licence or a blank wall does
	// not carry five e-KTP labels at once.
	assessment := AssessKTPText(rawText)
	if !assessment.IsKTP {
		slog.Info("ocr rejected: text is not an e-KTP",
			"session_id", sessionID,
			"markers_found", assessment.MarkersFound,
			"markers_total", assessment.MarkersTotal,
			"text_source", textSource,
		)
		return nil, apperr.OCRNotKTP
	}

	// Confidence is the share of e-KTP markers that were legible — how sure we
	// are this is a readable card. It is NOT an OCR engine's own self-report,
	// which is what used to be stored here: a constant 99.4 that said nothing
	// about the photo.
	confidence := assessment.Confidence()

	// 5. Parse KTP fields from raw text
	ktpData := ParseKTPFromText(rawText)

	// 6. Validate NIK structure, and cross-check it against the parsed fields.
	//
	// The NIK carries the birth date and sex inside itself, so those two can be
	// checked without any registry. A single misread digit almost always breaks
	// one of them, which is how an OCR error is caught here instead of becoming
	// a wrong identity downstream.
	if ktpData.NIK == "" {
		slog.Info("ocr rejected: no NIK in text", "session_id", sessionID,
			"text_source", textSource)
		return nil, apperr.OCRNotKTP
	}
	if err := ValidateKTPConsistency(ktpData); err != nil {
		slog.Info("ocr rejected: KTP data inconsistent",
			"session_id", sessionID, "reason", err.Error(), "text_source", textSource)
		return nil, apperr.OCRNotKTP
	}

	// Province is filled in from the NIK when OCR could not read the header. The
	// NIK is the more reliable source of the two, and ValidateKTPConsistency has
	// already confirmed they agree whenever OCR did read it.
	if ktpData.Provinsi == "" {
		ktpData.Provinsi = ProvinceNameFromNIK(ktpData.NIK)
	}
	// Same for sex: the NIK encodes it, so a line OCR missed is recoverable.
	if ktpData.JenisKelamin == "" {
		ktpData.JenisKelamin = GenderFromNIK(ktpData.NIK)
	}

	// 7. Verify via Dukcapil, if a registry is configured at all.
	dukcapilMatch, dukcapilChecked, err := s.verifyAgainstRegistry(ctx, sessionID, ktpData)
	if err != nil {
		return nil, err
	}

	// 8. Assess photo quality from what the capture SDK measured plus how much
	// of the card was legible.
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

	// 9. Upload photo to object storage (encrypted at rest)
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
		// Keep the same bucket/key shape even with no storage configured, so
		// SplitObjectPath can read it back. Without the bucket segment the split
		// returns the session id as the bucket and the lookup goes nowhere.
		photoPath = "local://" + ocrPhotoBucket + "/" + photoObjectKey
	}

	// 10. Encrypt PII and store results
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

	// 11. Transition session step: OCR → PERSONAL_DATA
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
		OCRID:           ocrID,
		AccuracyPct:     confidence,
		Extracted:       ktpData, // Note: nik/nama/alamat are encrypted in DB but we return original values
		DukcapilMatch:   dukcapilMatch,
		DukcapilChecked: dukcapilChecked,
		PhotoQuality:    quality,
		CurrentStep:     StepPersonalData,
	}, nil
}

// textSource names where the KTP text came from, for the audit trail and logs.
type textSource string

const (
	// textSourceServer means a server-side OCR engine read the image.
	textSourceServer textSource = "server_engine"

	// textSourceClient means the text came from the client's on-device
	// recognizer. The server still validates every field it produces; what it
	// cannot do is prove the text was read from the photo that was uploaded.
	// That limitation is recorded rather than hidden.
	textSourceClient textSource = "client_ondevice"
)

// extractText obtains the KTP text, preferring a server-side engine.
//
// Returning an error instead of empty text is the point. The previous behaviour
// was a nil engine leaving rawText empty, which flowed into a parser that
// produced an empty KTPData, which failed on an empty NIK — the right outcome by
// accident. With a mock engine present it instead produced a full identity from
// no input at all. Now the absence of text is handled explicitly and refused.
func (s *OCRService) extractText(
	ctx context.Context, imageData []byte, meta DeviceCaptureMeta,
) (string, textSource, error) {
	if s.ocrEngine != nil {
		raw, _, err := s.ocrEngine.ExtractText(ctx, imageData)
		if err != nil {
			return "", textSourceServer, fmt.Errorf("ocr extract: %w", err)
		}
		if strings.TrimSpace(raw) != "" {
			return raw, textSourceServer, nil
		}
		// An engine that read nothing is not a reason to fall back to a source
		// the engine was configured to replace: it means this photo is unreadable.
		return "", textSourceServer, apperr.OCRNotKTP
	}

	if strings.TrimSpace(meta.ClientOCRText) != "" {
		return meta.ClientOCRText, textSourceClient, nil
	}

	slog.Info("ocr refused: no server engine and no client text")
	return "", textSourceClient, apperr.OCRNotKTP
}

// verifyAgainstRegistry runs the Dukcapil check when one is configured.
//
// The second return value is what makes the answer honest. A nil client used to
// mean "assume match", and the response then carried dukcapil_match=true for an
// identity nothing had verified — the same class of untruth as an audit row
// claiming a device check passed when none ran. Now "not checked" is its own
// state, reported as dukcapil_checked=false, and the client shows no verified
// badge for it.
func (s *OCRService) verifyAgainstRegistry(
	ctx context.Context, sessionID string, data KTPData,
) (match, checked bool, err error) {
	if s.dukcapil == nil {
		return false, false, nil
	}

	ok, dErr := s.dukcapil.VerifyNIK(ctx, data.NIK, data.NamaLengkap)
	if dErr != nil {
		// Only a genuine timeout is retryable; anything else is an upstream
		// fault and must not masquerade as one.
		if isTimeout(dErr) {
			slog.Warn("dukcapil verify timed out", "session_id", sessionID, "error", dErr)
			return false, true, apperr.OCRDukcapilTimeout
		}
		slog.Error("dukcapil verify failed", "session_id", sessionID, "error", dErr)
		return false, true, apperr.OCRDukcapilUnavailable
	}
	if !ok {
		return false, true, apperr.OCRDukcapilMismatch
	}
	return true, true, nil
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

// SplitObjectPath splits a stored object path into bucket and key.
//
// Accepts the two schemes this codebase writes: "local://" from localfs.Storage
// and "s3://" from the mock. The scheme itself carries no meaning here — what
// matters is recovering the bucket and key, because the database column stores
// only the joined path while ObjectStorage.Download needs them apart.
func SplitObjectPath(path string) (bucket, key string, ok bool) {
	rest := path
	for _, scheme := range []string{"local://", "s3://"} {
		if trimmed, found := strings.CutPrefix(path, scheme); found {
			rest = trimmed
			break
		}
	}

	bucket, key, found := strings.Cut(rest, "/")
	if !found || bucket == "" || key == "" {
		return "", "", false
	}
	return bucket, key, true
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

	// Decrypt PII fields.
	//
	// A failed decrypt used to leave the field holding its ciphertext, and this
	// response is what prefills the data-pribadi form: the nasabah would be shown
	// a hex string as their own NIK and asked to confirm it. Worse, confirming it
	// cannot work — personal-data compares the submitted NIK against this same
	// decrypted value — so it is a dead end dressed up as data. The only honest
	// answer is that we cannot read what we stored.
	if s.aes != nil {
		nik, err := s.decryptField(result.Extracted.NIK)
		if err != nil {
			slog.Error("decrypt ocr nik failed", "session_id", sessionID, "error", err)
			return nil, apperr.InternalError
		}
		result.Extracted.NIK = nik

		nama, err := s.decryptField(result.Extracted.NamaLengkap)
		if err != nil {
			slog.Error("decrypt ocr nama failed", "session_id", sessionID, "error", err)
			return nil, apperr.InternalError
		}
		result.Extracted.NamaLengkap = nama

		alamat, err := s.decryptField(result.Extracted.Alamat)
		if err != nil {
			slog.Error("decrypt ocr alamat failed", "session_id", sessionID, "error", err)
			return nil, apperr.InternalError
		}
		result.Extracted.Alamat = alamat
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
	requiredKTPCorners = 4
	minCaptureWidth    = 640
	minCaptureHeight   = 480
)

// assessPhotoQuality judges the capture from what the device's capture SDK
// measured and the frame resolution.
//
// Every signal is optional — a client that reports nothing is not punished for
// it — but when a signal arrives and it is bad, the capture is rejected. That
// is the difference from the first version, which always returned HIGH and so
// made OCR_PHOTO_BLURRY, OCR_GLARE_DETECTED and OCR_CORNERS_MISSING unreachable.
//
// It deliberately no longer takes the OCR confidence. Those are two different
// questions — "was this frame legible" versus "how much of the card did we
// read" — and feeding the second into the first was a mistake made while
// changing what confidence means: it used to be an engine's self-report on a
// 0-100 scale, it is now the share of e-KTP labels found, and a perfectly good
// card reading 6 of 16 labels was rejected as blurry.
//
// Legibility is already enforced, and more strictly, elsewhere: a blurry photo
// cannot produce five e-KTP labels AND a NIK whose embedded birth date and sex
// agree with the parsed fields. That is a better blur test than a threshold on
// a self-reported number.
func assessPhotoQuality(meta DeviceCaptureMeta) PhotoQuality {
	q := PhotoQuality{
		Sharpness:         "HIGH",
		GlareDetected:     false,
		AllCornersVisible: true,
	}

	if meta.SharpnessScore != nil && *meta.SharpnessScore < minSharpnessScore {
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
