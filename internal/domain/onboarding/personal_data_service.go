package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	otpLength  = 6
	otpTTL     = 5 * time.Minute
	otpMaxFail = 5
	otpRegenAt = 3 // regenerate OTP after this many failures
	// otpBlockTime is how long a session is locked out after otpMaxFail
	// failures, and also how long the failure counter itself lives: a shorter
	// counter window would let an attacker reset their budget simply by
	// pausing, which defeats the lockout.
	otpBlockTime = 30 * time.Minute
	// otpMaxResend is the nasabah's resend quota per hour per session.
	//
	// Three policy decisions sit behind this number, none of them derivable
	// from the counters alone (spec §10a records them too):
	//
	//  1. The failure lockout is the only limiter on verify-otp. There is no
	//     separate 5-per-5-minutes window: the fifth wrong code blocks the
	//     session for otpBlockTime, which is strictly tighter, and a second
	//     limiter with the same numbers would only make the reason for a 429
	//     ambiguous.
	//  2. Automatic regeneration after otpRegenAt failures does NOT spend
	//     this quota. The nasabah did not ask for that SMS, and charging them
	//     for it would mean three wrong guesses silently cost a resend.
	//  3. A resend does NOT reset the failure counter. If it did, three
	//     resends would buy twelve guesses without ever reaching the lockout.
	otpMaxResend int64 = 3
)

// What put an OTP_SENT row in the audit trail. Never the code itself.
const (
	otpTriggerPersonalData = "personal_data"
	otpTriggerResend       = "resend"
	otpTriggerRegenerated  = "regenerated_after_failures"
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
	// devMode mirrors APP_ENV=development and is the only thing that lets an
	// OTP leave the process in a response body.
	devMode bool
	sms     SMSGateway
	aes     *crypto.AES
	audit   AuditRepository
}

type PersonalDataServiceConfig struct {
	Sessions     SessionRepository
	Cache        SessionCache
	OCRResults   OCRResultRepository
	PersonalData PersonalDataRepository
	OTPCache     OTPCache
	DevMode      bool
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
		devMode:      cfg.DevMode,
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
		shortID, idErr := generateShortID()
		if idErr != nil {
			return nil, fmt.Errorf("generate personal data id: %w", idErr)
		}
		pdID = "pd_" + shortID
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

	// A fresh code deserves a fresh budget on both counters: the failures
	// belonged to whatever OTP came before, and the hourly resend quota is
	// measured from the first resend of this step, not from an earlier one.
	if resetErr := s.otpCache.ResetAttempts(ctx, req.SessionID); resetErr != nil {
		slog.Error("reset otp attempts failed", "session_id", req.SessionID, "error", resetErr)
	}
	if resetErr := s.otpCache.ResetResend(ctx, req.SessionID); resetErr != nil {
		slog.Error("reset otp resend quota failed", "session_id", req.SessionID, "error", resetErr)
	}

	// SMS goes out only after the hash is stored, so a storage failure never
	// leaves a code in the nasabah's inbox that the server cannot verify.
	sendErr := s.sms.SendOTP(ctx, pd.NomorHP, otp)

	// 8. Transition step: PERSONAL_DATA → OTP_VERIFY
	completed := session.StepsCompleted
	completed.PersonalDataSaved = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepOTPVerify, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepOTPVerify
		session.StepsCompleted = completed
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
		"reason":       otpTriggerPersonalData,
		"delivered":    sendErr == nil,
	}, ipAddress, userAgent)

	// The step has already advanced and the code is stored and valid, so the
	// nasabah can recover with resend-otp. What they must not get is a 200
	// with an otp_expires_at for an SMS that was never handed over.
	if sendErr != nil {
		slog.Error("send otp sms failed", "session_id", req.SessionID, "error", sendErr)
		return nil, apperr.OTPDeliveryFailed
	}

	resp := &SavePersonalDataResponse{
		PersonalDataID: pdID,
		OTPSentTo:      maskPhone(pd.NomorHP),
		OTPExpiresAt:   expiresAt,
		CurrentStep:    StepOTPVerify,
	}
	if s.devMode {
		resp.OTPDebug = otp
	}
	return resp, nil
}

// VerifyOTP verifies the OTP code for a session.
func (s *PersonalDataService) VerifyOTP(ctx context.Context, req VerifyOTPRequest, ipAddress, userAgent string) (*VerifyOTPResponse, error) {
	// 1. Resolve session (also rejects an expired one) and confirm the device
	//    asking is the device that started the flow.
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if err := assertDeviceOwnsSession(session, req.DeviceID); err != nil {
		return nil, err
	}

	// 2. Validate step
	if session.CurrentStep != StepOTPVerify {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan OTP_VERIFY.", session.CurrentStep),
		}
	}

	// 3. Check if blocked. A blocked session's attempt is rejected before it
	//    reaches the counter, so hammering the endpoint cannot extend the
	//    lockout past the 30 minutes it was set for.
	blocked, err := s.otpCache.IsBlocked(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("check otp block: %w", err)
	}
	if blocked {
		s.writeAudit(ctx, req.SessionID, AuditOTPFailed, "nasabah:"+session.DeviceID, map[string]any{
			"reason": "blocked",
		}, ipAddress, userAgent)
		return nil, s.otpBlockedError(ctx, req.SessionID)
	}

	// 4. Get stored OTP hash
	storedHash, err := s.otpCache.GetOTP(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("get otp: %w", err)
	}
	if storedHash == "" {
		s.writeAudit(ctx, req.SessionID, AuditOTPFailed, "nasabah:"+session.DeviceID, map[string]any{
			"reason": "expired",
		}, ipAddress, userAgent)
		return nil, apperr.OTPExpired
	}

	// 5. Compare in constant time. Both sides are SHA-256 hex, so a plain !=
	//    leaks only which prefix byte differed — but leaking nothing costs
	//    one function call.
	inputHash := hashOTP(req.OTPCode)
	if subtle.ConstantTimeCompare([]byte(inputHash), []byte(storedHash)) != 1 {
		// Increment attempts
		attempts, err := s.otpCache.IncrAttempt(ctx, req.SessionID)
		if err != nil {
			slog.Error("incr otp attempt failed", "error", err)
		}

		s.writeAudit(ctx, req.SessionID, AuditOTPFailed, "nasabah:"+session.DeviceID, map[string]any{
			"reason":  "wrong_code",
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
			if regenErr := s.regenerateOTP(ctx, req.SessionID, ipAddress, userAgent); regenErr != nil {
				slog.Error("regen otp failed", "error", regenErr)
			}
			return nil, apperr.OTPExpired
		}

		return nil, apperr.OTPInvalid
	}

	// 6. OTP valid — clear the code and the failure budget, then move on.
	_ = s.otpCache.DeleteOTP(ctx, req.SessionID)
	if resetErr := s.otpCache.ResetAttempts(ctx, req.SessionID); resetErr != nil {
		slog.Error("reset otp attempts failed", "session_id", req.SessionID, "error", resetErr)
	}

	completed := session.StepsCompleted
	completed.OTPVerified = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepBiometric, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepBiometric
		session.StepsCompleted = completed
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
	// 1. Resolve session, confirm the device, validate step
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if err := assertDeviceOwnsSession(session, req.DeviceID); err != nil {
		return nil, err
	}
	if session.CurrentStep != StepOTPVerify {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan OTP_VERIFY.", session.CurrentStep),
		}
	}

	// 2. Check if blocked. Resending must not become a way around the
	//    lockout: a new code would be useless anyway, since verify-otp
	//    refuses while the block stands.
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

	// 4. Spend one of the hourly quota. Counted before the send, so a gateway
	//    outage cannot be turned into unlimited SMS attempts.
	count, err := s.otpCache.IncrResend(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("incr otp resend: %w", err)
	}
	if count > otpMaxResend {
		remaining, remErr := s.otpCache.ResendWindowRemaining(ctx, req.SessionID)
		if remErr != nil {
			slog.Error("get otp resend window remaining failed", "session_id", req.SessionID, "error", remErr)
		}
		remainSec := int(remaining.Seconds())
		if remainSec < 0 {
			remainSec = 0
		}
		return nil, apperr.Error{
			Status:  apperr.RateLimitExceeded.Status,
			Code:    apperr.RateLimitExceeded.Code,
			Message: "Kirim ulang OTP sudah mencapai batas. Silakan coba lagi nanti.",
			Details: map[string]any{
				"retry_after_seconds": remainSec,
			},
		}
	}

	// 5. Generate and send new OTP. StoreOTP overwrites the key, so the
	//    previous code stops verifying the moment this one lands.
	otp, err := generateOTP(otpLength)
	if err != nil {
		return nil, fmt.Errorf("generate otp: %w", err)
	}

	otpHash := hashOTP(otp)
	expiresAt, err := s.otpCache.StoreOTP(ctx, req.SessionID, otpHash, otpTTL)
	if err != nil {
		return nil, fmt.Errorf("store otp: %w", err)
	}

	// Deliberately NOT resetting the failure counter here: a resend that
	// handed back a fresh budget would turn three resends into twelve guesses
	// without ever reaching the lockout.
	sendErr := s.sms.SendOTP(ctx, phone, otp)

	s.writeAudit(ctx, req.SessionID, AuditOTPSent, "nasabah:"+session.DeviceID, map[string]any{
		"phone_masked": maskPhone(phone),
		"reason":       otpTriggerResend,
		"resend_count": count,
		"delivered":    sendErr == nil,
	}, ipAddress, userAgent)

	if sendErr != nil {
		slog.Error("send resend otp sms failed", "session_id", req.SessionID, "error", sendErr)
		return nil, apperr.OTPDeliveryFailed
	}

	resp := &ResendOTPResponse{
		OTPSentTo:    maskPhone(phone),
		OTPExpiresAt: expiresAt,
	}
	if s.devMode {
		resp.OTPDebug = otp
	}
	return resp, nil
}

// regenerateOTP generates a new OTP and sends it via SMS after repeated
// failures. It bypasses the resend quota on purpose: the nasabah did not ask
// for this SMS, so charging them for it would mean three wrong guesses
// silently cost one of their three resends.
func (s *PersonalDataService) regenerateOTP(ctx context.Context, sessionID, ipAddress, userAgent string) error {
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
		"reason":       otpTriggerRegenerated,
	}, ipAddress, userAgent)

	return nil
}

// assertDeviceOwnsSession rejects a session_id replayed from another device.
//
// The header stays optional for now: nothing after POST /sessions carries
// X-Device-ID yet, and making it mandatory here would strand every Android
// build already in testers' hands mid-flow. Clients that do send it get the
// binding immediately. Answering ONBOARDING_NOT_FOUND rather than a
// forbidden keeps a wrong device from learning that the session is real.
func assertDeviceOwnsSession(session *Session, deviceID string) error {
	if deviceID == "" || session.DeviceID == "" {
		return nil
	}
	if deviceID != session.DeviceID {
		return apperr.OnboardingNotFound
	}
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
