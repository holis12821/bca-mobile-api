package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

const (
	otpLength    = 6
	otpTTL       = 5 * time.Minute
	otpMaxFail   = 5
	otpRegenAt   = 3  // regenerate OTP after this many failures
	otpBlockTime = 30 * time.Minute
)

var (
	phoneRegex = regexp.MustCompile(`^(\+62|62|0)8[0-9]{8,12}$`)
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

type PersonalDataService struct {
	sessions     SessionRepository
	cache        SessionCache
	ocrResults   OCRResultRepository
	personalData PersonalDataRepository
	otpCache     OTPCache
	sms          SMSGateway
	aes          *crypto.AES
	audit        AuditRepository
}

type PersonalDataServiceConfig struct {
	Sessions     SessionRepository
	Cache        SessionCache
	OCRResults   OCRResultRepository
	PersonalData PersonalDataRepository
	OTPCache     OTPCache
	SMS          SMSGateway
	AES          *crypto.AES
	Audit        AuditRepository
}

func NewPersonalDataService(cfg PersonalDataServiceConfig) *PersonalDataService {
	return &PersonalDataService{
		sessions:     cfg.Sessions,
		cache:        cfg.Cache,
		ocrResults:   cfg.OCRResults,
		personalData: cfg.PersonalData,
		otpCache:     cfg.OTPCache,
		sms:          cfg.SMS,
		aes:          cfg.AES,
		audit:        cfg.Audit,
	}
}

// SavePersonalData validates, encrypts, and stores personal data, then sends OTP.
func (s *PersonalDataService) SavePersonalData(ctx context.Context, req SavePersonalDataRequest, ipAddress, userAgent string) (*SavePersonalDataResponse, error) {
	// 1. Resolve session and validate step
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepPersonalData {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan PERSONAL_DATA.", session.CurrentStep),
		}
	}

	pd := req.PersonalData

	// 2. Validate enums
	if !Pekerjaan(pd.Pekerjaan).Valid() {
		return nil, apperr.ValidationError
	}
	if !Penghasilan(pd.PenghasilanPerBulan).Valid() {
		return nil, apperr.ValidationError
	}
	if !SumberDana(pd.SumberDanaUtama).Valid() {
		return nil, apperr.ValidationError
	}

	// 3. Validate phone format
	if !phoneRegex.MatchString(pd.NomorHP) {
		return nil, apperr.PersonalDataInvalidPhone
	}

	// 4. Validate email format
	if !emailRegex.MatchString(pd.Email) {
		return nil, apperr.PersonalDataInvalidEmail
	}

	// 5. Cross-validate with OCR results
	ocrResult, err := s.ocrResults.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("find ocr result: %w", err)
	}
	if ocrResult == nil {
		return nil, apperr.OnboardingNotFound
	}

	// Decrypt OCR PII for comparison
	ocrNIK := ocrResult.Extracted.NIK
	ocrNama := ocrResult.Extracted.NamaLengkap
	if s.aes != nil {
		if decrypted, err := s.decryptField(ocrNIK); err == nil {
			ocrNIK = decrypted
		}
		if decrypted, err := s.decryptField(ocrNama); err == nil {
			ocrNama = decrypted
		}
	}

	if pd.NIK != ocrNIK {
		return nil, apperr.PersonalDataNIKMismatch
	}
	if !strings.EqualFold(pd.NamaLengkap, ocrNama) {
		return nil, apperr.PersonalDataNamaMismatch
	}

	// 6. Encrypt PII fields
	nikEnc, err := s.encryptField(pd.NIK)
	if err != nil {
		return nil, fmt.Errorf("encrypt nik: %w", err)
	}
	namaEnc, err := s.encryptField(pd.NamaLengkap)
	if err != nil {
		return nil, fmt.Errorf("encrypt nama: %w", err)
	}
	phoneEnc, err := s.encryptField(pd.NomorHP)
	if err != nil {
		return nil, fmt.Errorf("encrypt phone: %w", err)
	}
	emailEnc, err := s.encryptField(pd.Email)
	if err != nil {
		return nil, fmt.Errorf("encrypt email: %w", err)
	}

	now := time.Now().UTC()

	// Check if personal data already exists (re-submission)
	existing, err := s.personalData.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("find existing personal data: %w", err)
	}

	var pdID string
	if existing != nil {
		// Update existing record
		pdID = existing.PersonalDataID
		existing.NIK = nikEnc
		existing.NamaLengkap = namaEnc
		existing.TempatLahir = pd.TempatLahir
		existing.TanggalLahir = pd.TanggalLahir
		existing.JenisKelamin = pd.JenisKelamin
		existing.AlamatKTP = pd.AlamatKTP
		existing.AlamatDomisiliSama = pd.AlamatDomisiliSama
		existing.Pekerjaan = Pekerjaan(pd.Pekerjaan)
		existing.PenghasilanPerBulan = Penghasilan(pd.PenghasilanPerBulan)
		existing.SumberDanaUtama = SumberDana(pd.SumberDanaUtama)
		existing.NomorHP = phoneEnc
		existing.Email = emailEnc
		existing.UpdatedAt = now
		if err := s.personalData.Update(ctx, existing); err != nil {
			return nil, fmt.Errorf("update personal data: %w", err)
		}
	} else {
		// Create new record
		pdID = "pd_" + generateShortID()
		personalData := &PersonalData{
			ID:                  uuid.New(),
			PersonalDataID:      pdID,
			SessionID:           req.SessionID,
			NIK:                 nikEnc,
			NamaLengkap:         namaEnc,
			TempatLahir:         pd.TempatLahir,
			TanggalLahir:        pd.TanggalLahir,
			JenisKelamin:        pd.JenisKelamin,
			AlamatKTP:           pd.AlamatKTP,
			AlamatDomisiliSama:  pd.AlamatDomisiliSama,
			Pekerjaan:           Pekerjaan(pd.Pekerjaan),
			PenghasilanPerBulan: Penghasilan(pd.PenghasilanPerBulan),
			SumberDanaUtama:     SumberDana(pd.SumberDanaUtama),
			NomorHP:             phoneEnc,
			Email:               emailEnc,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		if err := s.personalData.Create(ctx, personalData); err != nil {
			return nil, fmt.Errorf("store personal data: %w", err)
		}
	}

	// 7. Generate and send OTP
	otp, err := generateOTP(otpLength)
	if err != nil {
		return nil, fmt.Errorf("generate otp: %w", err)
	}

	otpHash := hashOTP(otp)
	expiresAt, err := s.otpCache.StoreOTP(ctx, req.SessionID, otpHash, otpTTL)
	if err != nil {
		return nil, fmt.Errorf("store otp: %w", err)
	}

	if err := s.sms.SendOTP(ctx, pd.NomorHP, otp); err != nil {
		slog.Error("send otp sms failed", "error", err)
		// Don't fail the request — OTP is stored, client can request resend
	}

	// 8. Transition step: PERSONAL_DATA → OTP_VERIFY
	completed := session.StepsCompleted
	completed.PersonalDataSaved = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepOTPVerify, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepOTPVerify
		session.StepsCompleted = completed
		session.ExpiresAt = time.Now().Add(sessionTTL)
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "error", cacheErr)
		}
	}

	// Audit
	s.writeAudit(ctx, req.SessionID, AuditPersonalDataSaved, "nasabah:"+session.DeviceID, map[string]any{
		"personal_data_id": pdID,
	}, ipAddress, userAgent)
	s.writeAudit(ctx, req.SessionID, AuditOTPSent, "system", map[string]any{
		"phone_masked": maskPhone(pd.NomorHP),
	}, ipAddress, userAgent)

	return &SavePersonalDataResponse{
		PersonalDataID: pdID,
		OTPSentTo:      maskPhone(pd.NomorHP),
		OTPExpiresAt:   expiresAt,
		CurrentStep:    StepOTPVerify,
	}, nil
}

// VerifyOTP verifies the OTP code for a session.
func (s *PersonalDataService) VerifyOTP(ctx context.Context, req VerifyOTPRequest, ipAddress, userAgent string) (*VerifyOTPResponse, error) {
	// 1. Resolve session and validate step
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepOTPVerify {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan OTP_VERIFY.", session.CurrentStep),
		}
	}

	// 2. Check if blocked
	blocked, err := s.otpCache.IsBlocked(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("check otp block: %w", err)
	}
	if blocked {
		return nil, s.otpBlockedError(ctx, req.SessionID)
	}

	// 3. Get stored OTP hash
	storedHash, err := s.otpCache.GetOTP(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("get otp: %w", err)
	}
	if storedHash == "" {
		return nil, apperr.OTPExpired
	}

	// 4. Compare
	inputHash := hashOTP(req.OTPCode)
	if inputHash != storedHash {
		// Increment attempts
		attempts, err := s.otpCache.IncrAttempt(ctx, req.SessionID)
		if err != nil {
			slog.Error("incr otp attempt failed", "error", err)
		}

		s.writeAudit(ctx, req.SessionID, AuditOTPFailed, "nasabah:"+session.DeviceID, map[string]any{
			"attempt": attempts,
		}, ipAddress, userAgent)

		// After 5 failed attempts: block for 30 minutes
		if attempts >= otpMaxFail {
			if blockErr := s.otpCache.Block(ctx, req.SessionID, otpBlockTime); blockErr != nil {
				slog.Error("block otp failed", "error", blockErr)
			}
			_ = s.otpCache.DeleteOTP(ctx, req.SessionID)
			return nil, s.otpBlockedError(ctx, req.SessionID)
		}

		// After 3 failed attempts: regenerate new OTP
		if attempts >= otpRegenAt {
			if regenErr := s.regenerateOTP(ctx, req.SessionID, session.DeviceID, ipAddress, userAgent); regenErr != nil {
				slog.Error("regen otp failed", "error", regenErr)
			}
			return nil, apperr.OTPExpired
		}

		return nil, apperr.OTPInvalid
	}

	// 5. OTP valid — transition to BIOMETRIC
	_ = s.otpCache.DeleteOTP(ctx, req.SessionID)

	completed := session.StepsCompleted
	completed.OTPVerified = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepBiometric, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepBiometric
		session.StepsCompleted = completed
		session.ExpiresAt = time.Now().Add(sessionTTL)
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "error", cacheErr)
		}
	}

	s.writeAudit(ctx, req.SessionID, AuditOTPVerified, "nasabah:"+session.DeviceID, nil, ipAddress, userAgent)

	return &VerifyOTPResponse{
		Verified:    true,
		CurrentStep: StepBiometric,
	}, nil
}

// ResendOTP allows the frontend to explicitly request a new OTP.
func (s *PersonalDataService) ResendOTP(ctx context.Context, req ResendOTPRequest, ipAddress, userAgent string) (*ResendOTPResponse, error) {
	// 1. Validate session step
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepOTPVerify {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan OTP_VERIFY.", session.CurrentStep),
		}
	}

	// 2. Check if blocked
	blocked, err := s.otpCache.IsBlocked(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("check otp block: %w", err)
	}
	if blocked {
		return nil, s.otpBlockedError(ctx, req.SessionID)
	}

	// 3. Get phone from personal data
	pd, err := s.personalData.FindBySessionID(ctx, req.SessionID)
	if err != nil || pd == nil {
		return nil, apperr.OnboardingNotFound
	}

	phone := pd.NomorHP
	if s.aes != nil {
		if dec, err := s.decryptField(phone); err == nil {
			phone = dec
		}
	}

	// 4. Generate and send new OTP
	otp, err := generateOTP(otpLength)
	if err != nil {
		return nil, fmt.Errorf("generate otp: %w", err)
	}

	otpHash := hashOTP(otp)
	expiresAt, err := s.otpCache.StoreOTP(ctx, req.SessionID, otpHash, otpTTL)
	if err != nil {
		return nil, fmt.Errorf("store otp: %w", err)
	}

	if err := s.sms.SendOTP(ctx, phone, otp); err != nil {
		slog.Error("send resend otp sms failed", "error", err)
	}

	s.writeAudit(ctx, req.SessionID, AuditOTPSent, "nasabah:"+session.DeviceID, map[string]any{
		"phone_masked": maskPhone(phone),
		"reason":       "user_resend",
	}, ipAddress, userAgent)

	return &ResendOTPResponse{
		OTPSentTo:    maskPhone(phone),
		OTPExpiresAt: expiresAt,
	}, nil
}

// regenerateOTP generates a new OTP and sends it via SMS.
func (s *PersonalDataService) regenerateOTP(ctx context.Context, sessionID, deviceID, ipAddress, userAgent string) error {
	// Get phone from personal data
	pd, err := s.personalData.FindBySessionID(ctx, sessionID)
	if err != nil || pd == nil {
		return fmt.Errorf("find personal data for regen: %w", err)
	}

	phone := pd.NomorHP
	if s.aes != nil {
		if dec, err := s.decryptField(phone); err == nil {
			phone = dec
		}
	}

	otp, err := generateOTP(otpLength)
	if err != nil {
		return fmt.Errorf("generate otp: %w", err)
	}

	otpHash := hashOTP(otp)
	if _, err := s.otpCache.StoreOTP(ctx, sessionID, otpHash, otpTTL); err != nil {
		return fmt.Errorf("store regen otp: %w", err)
	}

	if err := s.sms.SendOTP(ctx, phone, otp); err != nil {
		slog.Error("send regen otp sms failed", "error", err)
	}

	s.writeAudit(ctx, sessionID, AuditOTPSent, "system", map[string]any{
		"phone_masked": maskPhone(phone),
		"reason":       "regenerated_after_failures",
	}, ipAddress, userAgent)

	return nil
}

func (s *PersonalDataService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
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

// otpBlockedError returns an OTP_BLOCKED error with remaining wait time in Details.
func (s *PersonalDataService) otpBlockedError(ctx context.Context, sessionID string) error {
	remaining, err := s.otpCache.BlockRemaining(ctx, sessionID)
	if err != nil {
		slog.Error("get otp block remaining failed", "error", err)
	}
	remainSec := int(remaining.Seconds())
	if remainSec < 0 {
		remainSec = 0
	}
	return apperr.Error{
		Status:  apperr.OTPBlocked.Status,
		Code:    apperr.OTPBlocked.Code,
		Message: apperr.OTPBlocked.Message,
		Details: map[string]any{
			"retry_after_seconds": remainSec,
		},
	}
}

func (s *PersonalDataService) encryptField(plaintext string) (string, error) {
	if s.aes == nil || plaintext == "" {
		return plaintext, nil
	}
	enc, err := s.aes.Encrypt([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(enc), nil
}

func (s *PersonalDataService) decryptField(ciphertextHex string) (string, error) {
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

func (s *PersonalDataService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("personal data audit insert failed",
			"session_id", sessionID,
			"event_type", eventType,
			"error", err,
		)
	}
}

// generateOTP returns a cryptographically random numeric OTP of the given length.
func generateOTP(length int) (string, error) {
	max := new(big.Int)
	max.SetInt64(1)
	for i := 0; i < length; i++ {
		max.Mul(max, big.NewInt(10))
	}

	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%0*d", length, n), nil
}

// hashOTP returns a SHA-256 hex digest of the OTP.
func hashOTP(otp string) string {
	h := sha256.Sum256([]byte(otp))
	return hex.EncodeToString(h[:])
}

// maskPhone masks a phone number: "081234568889" → "0812****8889"
func maskPhone(phone string) string {
	if len(phone) < 8 {
		return phone
	}
	return phone[:4] + "****" + phone[len(phone)-4:]
}