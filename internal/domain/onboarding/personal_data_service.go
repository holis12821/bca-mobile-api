package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/sms"
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

	// otpMaxSendPerPhone is the hourly SMS ceiling for one destination number,
	// counted across every session that number appears in.
	//
	// otpMaxResend above cannot do this job: it is keyed by session_id, and a
	// session is free to create. One address was able to farm roughly 1 issuance
	// + 3 resends per session, as fast as the per-IP limit allowed — hundreds of
	// messages an hour to a number of the attacker's choosing, billed to this
	// Twilio account and landing on someone else's handset.
	//
	// Ten leaves a real nasabah room to be clumsy: a full session is 1 + 3 = 4
	// messages, so they can restart the flow twice inside the same hour and
	// still not meet this. Anything past that is not somebody opening one
	// account.
	otpMaxSendPerPhone int64 = 10
)

// errPhoneSendQuota means the destination number has used up otpMaxSendPerPhone
// for this hour. It is not a delivery failure: nothing was sent, and nothing was
// overwritten, so each call site decides what the nasabah should see.
var errPhoneSendQuota = errors.New("otp: hourly send quota for this phone number is used up")

// errOTPDelivery means the code exists but the provider would not take it.
//
// Separate from any other issueCode failure because the caller owes the app a
// different answer: a storage failure is ours to fix (500), while a refused
// delivery is recoverable by the nasabah through resend-otp (503
// OTP_DELIVERY_FAILED). Collapsing the two is how "OTP gagal dikirim" came to be
// reported for problems no resend could ever fix.
var errOTPDelivery = errors.New("otp: provider refused delivery")

// What put an OTP_SENT row in the audit trail. Never the code itself.
const (
	otpTriggerPersonalData = "personal_data"
	otpTriggerResend       = "resend"
	otpTriggerRegenerated  = "regenerated_after_failures"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// jenisKelaminValid is the pair the KTP carries. Unvalidated, this field went
// straight into a VARCHAR(16): anything longer failed the INSERT and surfaced as
// 500, and anything shorter was stored as-is and shipped to core banking.
var jenisKelaminValid = map[string]bool{"LAKI_LAKI": true, "PEREMPUAN": true}

// Column widths from migration 000012. These are checked here because the
// alternative is Postgres checking them: "value too long for type character
// varying(16)" reaches the nasabah as 500 INTERNAL_ERROR, for input that is
// simply too long.
const (
	maxTempatLahir = 128
	maxRTRW        = 16
	maxKodePos     = 10
	maxWilayah     = 128
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
	// verifier is set instead of sms when the provider owns the code. Exactly one
	// of the two is non-nil; issueCode and checkCode are the only places that
	// care which.
	verifier OTPVerifier
	aes      *crypto.AES
	audit    AuditRepository
}

type PersonalDataServiceConfig struct {
	Sessions     SessionRepository
	Cache        SessionCache
	OCRResults   OCRResultRepository
	PersonalData PersonalDataRepository
	OTPCache     OTPCache
	DevMode      bool
	SMS          SMSGateway
	Verifier     OTPVerifier
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
		verifier:     cfg.Verifier,
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

	// 3. Validate phone format.
	//
	// Asking the gateway's own normaliser instead of a second regex. The regex
	// that used to live here disagreed with it in five ways — it accepted a
	// 14-digit national number, accepted the "080" block, and rejected both a
	// bare "8123…" and anything with the dashes a nasabah actually types. The
	// first three were the expensive ones: the data saved, the step advanced to
	// OTP_VERIFY, and then every send answered 503 OTP_DELIVERY_FAILED forever,
	// because the gateway refuses what this check had waved through. One rule,
	// owned by the code that has to route the message.
	if _, err := sms.NormalizePhone(pd.NomorHP); err != nil {
		return nil, apperr.PersonalDataInvalidPhone
	}

	// 4. Validate email format
	if !emailRegex.MatchString(pd.Email) {
		return nil, apperr.PersonalDataInvalidEmail
	}

	// 5. Resolve the delivery channel. Still in the shape-checking block on
	//    purpose: nothing has been written and no send budget charged yet, so an
	//    unavailable channel leaves the session exactly where it was.
	channel, err := s.resolveChannel(req.Channel)
	if err != nil {
		return nil, err
	}

	// 5. Validate the fields that reach bounded or typed columns.
	//
	// Every one of these used to be taken on trust. tanggal_lahir was the worst:
	// the repository parsed it and, on failure, stored NULL and reported success,
	// so a typo silently dropped a KYC field and the account went to core banking
	// without a date of birth. The rest are VARCHAR widths from migration 000012,
	// where "too long" came back as 500.
	if !jenisKelaminValid[pd.JenisKelamin] {
		return nil, apperr.ValidationError
	}
	birthDate, err := time.Parse("2006-01-02", pd.TanggalLahir)
	if err != nil {
		return nil, apperr.ValidationError
	}
	// A parseable date is not yet a plausible one. "2099-01-01" parses fine and
	// used to travel all the way to core banking as a customer's date of birth.
	// This is a sanity range, not an age policy: the minimum age for a KTP is a
	// product rule and is deliberately not decided here.
	if birthDate.After(time.Now()) || birthDate.Before(time.Now().AddDate(-120, 0, 0)) {
		return nil, apperr.ValidationError
	}
	// Counted in characters, which is what VARCHAR(n) counts. len() counts bytes,
	// so a 128-character value with one multibyte rune in it was rejected at 400
	// for being too long when the column would have taken it.
	if tooLong(pd.TempatLahir, maxTempatLahir) ||
		tooLong(pd.AlamatKTP.RTRW, maxRTRW) ||
		tooLong(pd.AlamatKTP.KodePos, maxKodePos) ||
		tooLong(pd.AlamatKTP.Kelurahan, maxWilayah) ||
		tooLong(pd.AlamatKTP.Kecamatan, maxWilayah) ||
		tooLong(pd.AlamatKTP.Kota, maxWilayah) ||
		tooLong(pd.AlamatKTP.Provinsi, maxWilayah) {
		return nil, apperr.ValidationError
	}

	// 6. Cross-validate with OCR results
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
		// A swallowed decrypt error left these holding ciphertext, so the
		// comparison below always failed and the nasabah was told their NIK did
		// not match their own KTP — a dead end they could do nothing about, from
		// a key problem on our side. Same rule the resend path already follows:
		// there is no safe way to continue without the plaintext.
		dec, decErr := s.decryptField(ocrNIK)
		if decErr != nil {
			return nil, fmt.Errorf("decrypt ocr nik: %w", decErr)
		}
		ocrNIK = dec

		dec, decErr = s.decryptField(ocrNama)
		if decErr != nil {
			return nil, fmt.Errorf("decrypt ocr nama: %w", decErr)
		}
		ocrNama = dec
	}

	if pd.NIK != ocrNIK {
		return nil, apperr.PersonalDataNIKMismatch
	}
	if !strings.EqualFold(pd.NamaLengkap, ocrNama) {
		return nil, apperr.PersonalDataNamaMismatch
	}

	// 7. Encrypt PII fields
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

	// 7. Generate and send OTP.
	//
	// The number's hourly ceiling is charged first. This is the site the cap
	// exists for: a fresh session is the cheapest way to buy another SMS, so
	// checking only the per-session resend quota here left the budget unbounded
	// in practice. Nothing has been generated or stored yet, so refusing now
	// leaves the step at PERSONAL_DATA and the flow resumable.
	if err := s.spendPhoneSendBudget(ctx, pd.NomorHP); err != nil {
		if errors.Is(err, errPhoneSendQuota) {
			return nil, s.phoneSendQuotaError(ctx, pd.NomorHP)
		}
		return nil, err
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

	expiresAt, debugCode, sendErr := s.issueCode(ctx, req.SessionID, pd.NomorHP, channel)
	// A storage failure is ours, and there is nothing for the nasabah to retry
	// into: stop before the step advances. A refused delivery is different — it is
	// handled after the transition, because resend-otp is the way out of it.
	if sendErr != nil && !errors.Is(sendErr, errOTPDelivery) {
		return nil, sendErr
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
	// Empty on the verifier path whatever APP_ENV says: the provider owns the
	// code and never hands it back, so there is nothing to put here.
	if s.devMode {
		resp.OTPDebug = debugCode
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

	// 4. Ask whoever owns the code.
	//
	// The number is only needed on the verifier path, where the provider keys the
	// verification by destination rather than by our session id. Resolved before
	// the check so a decrypt problem cannot be mistaken for a wrong code.
	var phone string
	if s.verifier != nil {
		var phoneErr error
		phone, phoneErr = s.phoneForSession(ctx, req.SessionID)
		if phoneErr != nil {
			return nil, phoneErr
		}
	}

	ok, expired, err := s.checkCode(ctx, req.SessionID, phone, req.OTPCode)
	if err != nil {
		return nil, fmt.Errorf("check otp: %w", err)
	}
	if expired {
		s.writeAudit(ctx, req.SessionID, AuditOTPFailed, "nasabah:"+session.DeviceID, map[string]any{
			"reason": "expired",
		}, ipAddress, userAgent)
		return nil, apperr.OTPExpired
	}

	// 5. Wrong code: count it, and let the counters decide what happens next.
	if !ok {
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

		// After 3 failed attempts: regenerate new OTP.
		//
		// OTP_EXPIRED tells the app a fresh code is already on its way, so it
		// must not be the answer when the regen never reached the gateway: the
		// code the nasabah was holding is already overwritten, and they would
		// sit waiting for an SMS that never left. OTP_DELIVERY_FAILED is what
		// makes the app offer "kirim ulang", which is the actual way out.
		// Exactly at otpRegenAt, not from there on. ">=" fired again on the
		// fourth wrong guess, so three failures cost two SMS and the code from
		// the third was already dead when the nasabah typed it. The docs promised
		// one regeneration, and one is also what the phone budget is sized for.
		if attempts == otpRegenAt {
			if regenErr := s.regenerateOTP(ctx, req.SessionID, ipAddress, userAgent); regenErr != nil {
				// The number is out of hourly budget, so no replacement was
				// generated and the code the nasabah already holds is still
				// valid. OTP_INVALID is then the honest answer: this guess was
				// wrong, attempts remain, and nothing new is on its way.
				if errors.Is(regenErr, errPhoneSendQuota) {
					slog.Warn("skipped otp regeneration: phone send quota used up",
						"session_id", req.SessionID)
					return nil, apperr.OTPInvalid
				}
				slog.Error("regen otp failed", "session_id", req.SessionID, "error", regenErr)
				return nil, apperr.OTPDeliveryFailed
			}
			return nil, apperr.OTPExpired
		}

		return nil, apperr.OTPInvalid
	}

	// 6. OTP valid — clear the code and the failure budget, then move on.
	//
	// Nothing to delete on the verifier path: the provider consumed the
	// verification when it approved it, and there was never a local copy.
	if s.verifier == nil {
		_ = s.otpCache.DeleteOTP(ctx, req.SessionID)
	}
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

	channel, err := s.resolveChannel(req.Channel)
	if err != nil {
		return nil, err
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

	phone, err := s.phoneForSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	// 4. Charge the number's hourly ceiling FIRST.
	//
	// Order matters, and getting it wrong cost the nasabah something real: with
	// IncrResend first, a resend refused by this ceiling had already spent one of
	// their three session resends on a request that sent nothing. Nothing is
	// dispatched on this path, so nothing may be charged. The number's ceiling is
	// also the broader limit, so failing it first is the right precedence.
	if err := s.spendPhoneSendBudget(ctx, phone); err != nil {
		if errors.Is(err, errPhoneSendQuota) {
			return nil, s.phoneSendQuotaError(ctx, phone)
		}
		return nil, err
	}

	// 5. Spend one of the hourly session quota. Counted before the send, so a
	//    gateway outage cannot be turned into unlimited SMS attempts.
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

	// 6. Issue a new code. On the local path StoreOTP overwrites the key, so the
	//    previous code stops verifying the moment this one lands.
	//
	//    Deliberately NOT resetting the failure counter here: a resend that handed
	//    back a fresh budget would turn three resends into twelve guesses without
	//    ever reaching the lockout.
	expiresAt, debugCode, sendErr := s.issueCode(ctx, req.SessionID, phone, channel)
	if sendErr != nil && !errors.Is(sendErr, errOTPDelivery) {
		return nil, sendErr
	}

	s.writeAudit(ctx, req.SessionID, AuditOTPSent, "nasabah:"+session.DeviceID, map[string]any{
		"phone_masked": maskPhone(phone),
		"reason":       otpTriggerResend,
		"resend_count": count,
		"channel":      string(channel),
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
		resp.OTPDebug = debugCode
	}
	return resp, nil
}

// tooLong reports whether s exceeds a VARCHAR(max) column, counting runes the
// way Postgres does rather than bytes the way len() does.
func tooLong(s string, max int) bool {
	return utf8.RuneCountInString(s) > max
}

// resolveChannel turns the optional request field into a channel to deliver on.
//
// Checked before anything is spent — no code generated, no hourly send budget
// charged — because a channel this deployment cannot serve has to cost the
// nasabah nothing.
//
// The one thing it cannot see is the provider's own allowlist
// (SMS_VERIFY_CHANNELS), which lives in the transport. A channel that passes here
// and is refused there still costs one unit of the per-number hourly budget.
// That is accepted: it means a client asking for a channel this deployment never
// enabled, which is a build or config mismatch answered with an immediate 400,
// not something a nasabah can walk into.
func (s *PersonalDataService) resolveChannel(raw string) (sms.Channel, error) {
	switch sms.Channel(strings.ToLower(strings.TrimSpace(raw))) {
	case "", sms.ChannelSMS:
		return sms.ChannelSMS, nil
	case sms.ChannelCall:
		// Only a Verifier can read a code out loud. The Gateway path hands a code
		// we generated to an SMS API and has no voice transport at all, so the
		// honest answer is a refusal: sending an SMS instead would leave the
		// nasabah waiting for a call that is never placed.
		if s.verifier == nil {
			return "", apperr.OTPChannelNotAllowed
		}
		return sms.ChannelCall, nil
	default:
		return "", apperr.OTPChannelNotAllowed
	}
}

// issueCode puts a fresh code in the nasabah's hands and reports when it dies.
//
// The two channels differ only in who owns the code, so that difference is
// confined to this function and checkCode. Everything the caller does around them
// — the per-number ceiling, the resend quota, the counters, the audit row — is
// policy that belongs to us and reads the same on both paths.
//
// debugCode is non-empty only on the local path in development: a verifier never
// hands the code back, so otp_debug simply stops existing there.
func (s *PersonalDataService) issueCode(ctx context.Context, sessionID, phone string, ch sms.Channel) (expiresAt time.Time, debugCode string, err error) {
	if s.verifier != nil {
		if err := s.verifier.StartVerification(ctx, phone, ch); err != nil {
			// A refused channel is not a delivery failure: the provider was never
			// called, so there is no code in flight and nothing to retry into.
			// Returning it as errOTPDelivery would answer 503 and advance the step
			// to OTP_VERIFY for a code that does not exist.
			if errors.Is(err, sms.ErrChannelNotAllowed) {
				return time.Time{}, "", apperr.OTPChannelNotAllowed
			}
			return time.Time{}, "", fmt.Errorf("%w: %v", errOTPDelivery, err)
		}
		// The provider owns the lifetime too, so it is asked rather than assumed:
		// the expiry is set on the Verify service in the console, and a constant
		// here would be a second copy that drifts the first time it changes there.
		return time.Now().UTC().Add(s.verifier.CodeTTL()), "", nil
	}

	otp, err := generateOTP(otpLength)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("generate otp: %w", err)
	}
	// Stored before sending, so a storage failure never leaves a code in the
	// nasabah's inbox that this server cannot verify.
	expiresAt, err = s.otpCache.StoreOTP(ctx, sessionID, hashOTP(otp), otpTTL)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("store otp: %w", err)
	}
	if err := s.sms.SendOTP(ctx, phone, otp); err != nil {
		return expiresAt, otp, fmt.Errorf("%w: %v", errOTPDelivery, err)
	}
	return expiresAt, otp, nil
}

// checkCode answers whether the submitted code is the live one.
//
// expired separates "wrong code, attempts remain" from "there is no live code any
// more", because the caller owes the app different answers for the two.
func (s *PersonalDataService) checkCode(ctx context.Context, sessionID, phone, code string) (ok bool, expired bool, err error) {
	if s.verifier != nil {
		approved, err := s.verifier.CheckVerification(ctx, phone, code)
		switch {
		case errors.Is(err, sms.ErrVerifyExpired):
			// Verify drops a verification once it expires or is approved, so
			// "not found" is exactly "the code being held is no longer live".
			return false, true, nil
		case err != nil:
			return false, false, err
		}
		return approved, false, nil
	}

	storedHash, err := s.otpCache.GetOTP(ctx, sessionID)
	if err != nil {
		return false, false, fmt.Errorf("get otp: %w", err)
	}
	if storedHash == "" {
		return false, true, nil
	}
	// Constant time. Both sides are SHA-256 hex, so a plain != leaks only which
	// prefix byte differed — but leaking nothing costs one function call.
	return subtle.ConstantTimeCompare([]byte(hashOTP(code)), []byte(storedHash)) == 1, false, nil
}

// phoneForSession returns the plaintext number a session's OTP goes to.
func (s *PersonalDataService) phoneForSession(ctx context.Context, sessionID string) (string, error) {
	pd, err := s.personalData.FindBySessionID(ctx, sessionID)
	if err != nil || pd == nil {
		return "", apperr.OnboardingNotFound
	}
	phone := pd.NomorHP
	if s.aes != nil {
		// Ciphertext is not a phone number: a failed decrypt used to hand hex to
		// the gateway and mask that hex back to the nasabah as their own number.
		dec, decErr := s.decryptField(phone)
		if decErr != nil {
			slog.Error("decrypt phone failed", "session_id", sessionID, "error", decErr)
			return "", apperr.OTPDeliveryFailed
		}
		phone = dec
	}
	return phone, nil
}

// spendPhoneSendBudget counts one SMS against the destination number's hourly
// ceiling. It returns errPhoneSendQuota when the ceiling is already met.
//
// Always called BEFORE the code is generated and stored. Counting after the
// store would leave the nasabah's working code overwritten by one that was
// never sent — the same trap the delivery-failure path had.
func (s *PersonalDataService) spendPhoneSendBudget(ctx context.Context, phone string) error {
	count, err := s.otpCache.IncrPhoneSend(ctx, phone)
	if err != nil {
		return fmt.Errorf("incr otp phone send: %w", err)
	}
	if count > otpMaxSendPerPhone {
		return errPhoneSendQuota
	}
	return nil
}

// phoneSendQuotaError is the 429 the nasabah sees, carrying how long until the
// number's budget refills. Shape matches the resend-quota 429 the app already
// handles, so no new client branch is needed.
func (s *PersonalDataService) phoneSendQuotaError(ctx context.Context, phone string) error {
	remaining, err := s.otpCache.PhoneSendWindowRemaining(ctx, phone)
	if err != nil {
		slog.Error("get otp phone send window remaining failed", "error", err)
	}
	remainSec := int(remaining.Seconds())
	if remainSec < 0 {
		remainSec = 0
	}
	return apperr.Error{
		Status:  apperr.RateLimitExceeded.Status,
		Code:    apperr.RateLimitExceeded.Code,
		Message: "Nomor ini sudah menerima terlalu banyak kode OTP. Silakan coba lagi nanti.",
		Details: map[string]any{
			"retry_after_seconds": remainSec,
		},
	}
}

// regenerateOTP generates a new OTP and sends it via SMS after repeated
// failures. It bypasses the resend quota on purpose: the nasabah did not ask
// for this SMS, so charging them for it would mean three wrong guesses
// silently cost one of their three resends.
func (s *PersonalDataService) regenerateOTP(ctx context.Context, sessionID, ipAddress, userAgent string) error {
	phone, err := s.phoneForSession(ctx, sessionID)
	if err != nil {
		return err
	}

	// The courtesy SMS is still an SMS, so it is charged to the number as well.
	// Checked before anything is generated: when the budget is gone the right
	// outcome is to leave the code the nasabah already has alone, which is why
	// the caller turns this into a plain OTP_INVALID rather than OTP_EXPIRED.
	if err := s.spendPhoneSendBudget(ctx, phone); err != nil {
		return err
	}

	// Always SMS: this send is triggered by the server noticing an expired code,
	// not by the nasabah asking for anything. An unrequested phone call is a
	// worse surprise than an unrequested SMS.
	_, _, sendErr := s.issueCode(ctx, sessionID, phone, sms.ChannelSMS)

	s.writeAudit(ctx, sessionID, AuditOTPSent, "system", map[string]any{
		"phone_masked": maskPhone(phone),
		"reason":       otpTriggerRegenerated,
		"delivered":    sendErr == nil,
	}, ipAddress, userAgent)

	// Same rule as the other issue sites: a code that was never handed over must
	// not be reported as a code on its way. Swallowing this was the one place in
	// this flow that still did.
	if sendErr != nil {
		slog.Error("regen otp failed", "session_id", sessionID, "error", sendErr)
		return sendErr
	}

	return nil
}

// assertDeviceOwnsSession rejects a session_id replayed from another device.
//
// The header stays optional for now — see assertSessionDevice, which this
// delegates to so both paths answer the same way. It used to answer
// ONBOARDING_NOT_FOUND to avoid confirming the session exists; it now answers
// 403 ONBOARDING_DEVICE_MISMATCH, which the Android client asked for and which
// costs nothing: a session_id is a v4 UUID, so there is no id space to walk,
// and "not found" sent a client hunting for a session it was holding correctly.
func assertDeviceOwnsSession(session *Session, deviceID string) error {
	return assertSessionDevice(session, deviceID)
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
