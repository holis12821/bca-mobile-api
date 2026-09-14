package registration

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

type Service struct {
	cache      RegistrationCache
	executor   RegistrationExecutor
	jwtManager *crypto.JWTManager
	pinKeys    *crypto.RSAKeyPair
}

type ServiceConfig struct {
	Cache      RegistrationCache
	Executor   RegistrationExecutor
	JWTManager *crypto.JWTManager
	PINKeys    *crypto.RSAKeyPair
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		cache:      cfg.Cache,
		executor:   cfg.Executor,
		jwtManager: cfg.JWTManager,
		pinKeys:    cfg.PINKeys,
	}
}

// Initiate starts the registration process.
func (s *Service) Initiate(ctx context.Context, req InitiateRequest) (*InitiateResponse, error) {
	if len(req.NIK) != 16 {
		return nil, apperr.ValidationError
	}
	if len(req.PhoneNumber) < 10 || len(req.PhoneNumber) > 15 {
		return nil, apperr.ValidationError
	}

	regID := uuid.New()
	otpCode := fmt.Sprintf("%06d", rand.IntN(1000000))

	reg := &Registration{
		ID:          regID,
		FullName:    strings.ToUpper(strings.TrimSpace(req.FullName)),
		NIK:         req.NIK,
		PhoneNumber: req.PhoneNumber,
		Email:       strings.ToLower(strings.TrimSpace(req.Email)),
		Status:      "OTP_PENDING",
		OTPCode:     otpCode,
		OTPExpiry:   time.Now().Add(OTPCacheTTL),
		Documents:   make(map[string]string),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.cache.Store(ctx, reg); err != nil {
		return nil, fmt.Errorf("store registration: %w", err)
	}

	// In dev: log OTP instead of sending SMS
	slog.Info("registration OTP generated",
		"registration_id", regID.String(),
		"phone", req.PhoneNumber,
		"otp", otpCode,
	)

	maskedPhone := maskPhone(req.PhoneNumber)

	return &InitiateResponse{
		RegistrationID: regID.String(),
		Status:         "OTP_PENDING",
		OTPDestination: maskedPhone,
	}, nil
}

// VerifyOTP verifies the OTP and returns a registration token.
func (s *Service) VerifyOTP(ctx context.Context, req VerifyOTPRequest) (*VerifyOTPResponse, error) {
	reg, err := s.cache.Get(ctx, req.RegistrationID)
	if err != nil {
		return nil, fmt.Errorf("get registration: %w", err)
	}
	if reg == nil {
		return nil, apperr.RegistrationNotFound
	}
	if reg.Status != "OTP_PENDING" {
		return nil, apperr.RegistrationOTPInvalid
	}
	if time.Now().After(reg.OTPExpiry) {
		return nil, apperr.RegistrationOTPExpired
	}
	if reg.OTPCode != req.OTPCode {
		return nil, apperr.RegistrationOTPInvalid
	}

	reg.Status = "OTP_VERIFIED"
	reg.UpdatedAt = time.Now()
	if err := s.cache.Update(ctx, reg); err != nil {
		return nil, fmt.Errorf("update registration: %w", err)
	}

	// Generate registration token (JWT typ:"registration", TTL 30m)
	token, err := s.jwtManager.GenerateRegistrationToken(reg.ID.String())
	if err != nil {
		return nil, fmt.Errorf("generate registration token: %w", err)
	}

	return &VerifyOTPResponse{
		RegistrationID:    reg.ID.String(),
		Status:            "OTP_VERIFIED",
		RegistrationToken: token,
	}, nil
}

// UploadDocument validates and stores a document reference.
func (s *Service) UploadDocument(ctx context.Context, regID, docType, filePath string) error {
	if !ValidDocumentTypes[docType] {
		return apperr.ValidationError
	}

	reg, err := s.cache.Get(ctx, regID)
	if err != nil {
		return fmt.Errorf("get registration: %w", err)
	}
	if reg == nil {
		return apperr.RegistrationNotFound
	}
	if reg.Status != "OTP_VERIFIED" && reg.Status != "DOCUMENTS_UPLOADED" {
		return apperr.ValidationError
	}

	reg.Documents[docType] = filePath
	reg.Status = "DOCUMENTS_UPLOADED"
	reg.UpdatedAt = time.Now()

	return s.cache.Update(ctx, reg)
}

// Complete finalizes registration: creates user + account.
func (s *Service) Complete(ctx context.Context, regID string, req CompleteRequest) (*CompleteResponse, error) {
	reg, err := s.cache.Get(ctx, regID)
	if err != nil {
		return nil, fmt.Errorf("get registration: %w", err)
	}
	if reg == nil {
		return nil, apperr.RegistrationNotFound
	}
	if reg.Status != "OTP_VERIFIED" && reg.Status != "DOCUMENTS_UPLOADED" {
		return nil, apperr.ValidationError
	}

	// Decrypt and hash PIN
	pinPayload, err := s.pinKeys.DecryptPIN(req.PINEncrypted)
	if err != nil {
		return nil, apperr.ValidationError
	}

	pinHash, err := crypto.HashPassword(ctx, pinPayload.PIN, crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("hash pin: %w", err)
	}

	result, err := s.executor.CreateUserAndAccount(ctx, CreateUserParams{
		FullName:    reg.FullName,
		NIK:         reg.NIK,
		PhoneNumber: reg.PhoneNumber,
		Email:       reg.Email,
		PINHash:     pinHash,
		DeviceID:    req.DeviceID,
		DeviceModel: req.DeviceModel,
	})
	if err != nil {
		return nil, err
	}

	// Clean up registration cache
	_ = s.cache.Delete(ctx, regID)

	return &CompleteResponse{
		UserID:        result.UserID,
		AccountNumber: result.AccountNumber,
		Status:        "COMPLETED",
		Message:       "Pendaftaran berhasil. Silakan login dengan kode akses Anda.",
	}, nil
}

func maskPhone(phone string) string {
	if len(phone) <= 8 {
		return phone
	}
	return phone[:4] + "****" + phone[len(phone)-4:]
}