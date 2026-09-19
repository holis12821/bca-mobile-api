package onboarding

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

type SubmitService struct {
	sessions     SessionRepository
	cache        SessionCache
	personalData PersonalDataRepository
	credentials  CredentialRepository
	coreBanking  CoreBankingClient
	idempotency  IdempotencyCache
	aes          *crypto.AES
	audit        AuditRepository
}

type SubmitServiceConfig struct {
	Sessions     SessionRepository
	Cache        SessionCache
	PersonalData PersonalDataRepository
	Credentials  CredentialRepository
	CoreBanking  CoreBankingClient
	Idempotency  IdempotencyCache
	AES          *crypto.AES
	Audit        AuditRepository
}

func NewSubmitService(cfg SubmitServiceConfig) *SubmitService {
	return &SubmitService{
		sessions:     cfg.Sessions,
		cache:        cfg.Cache,
		personalData: cfg.PersonalData,
		credentials:  cfg.Credentials,
		coreBanking:  cfg.CoreBanking,
		idempotency:  cfg.Idempotency,
		aes:          cfg.AES,
		audit:        cfg.Audit,
	}
}

// Submit validates all steps, creates the account via core banking, and finalizes the session.
func (s *SubmitService) Submit(ctx context.Context, req SubmitRequest, idempotencyKey, ipAddress, userAgent string) (*SubmitResponse, error) {
	// 1. Idempotency check
	if idempotencyKey != "" && s.idempotency != nil {
		cached, err := s.idempotency.Check(ctx, idempotencyKey)
		if err != nil {
			slog.Error("idempotency check failed", "error", err)
		}
		if cached != "" {
			var resp SubmitResponse
			if err := json.Unmarshal([]byte(cached), &resp); err == nil {
				return &resp, nil
			}
		}
	}

	// 2. Validate session
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepReview {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan REVIEW.", session.CurrentStep),
		}
	}

	// 3. Validate agreement
	if !req.AgreementAccepted || req.AgreementVersion == "" {
		return nil, apperr.ValidationError
	}

	// 4. Validate all steps completed
	if err := validateAllStepsCompleted(session.StepsCompleted); err != nil {
		return nil, err
	}

	// 5. Fetch personal data for account holder name
	pd, err := s.personalData.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("find personal data: %w", err)
	}
	if pd == nil {
		return nil, apperr.OnboardingIncomplete
	}

	holderName := pd.NamaLengkap
	nik := pd.NIK
	// Decrypt PII if AES is configured
	if s.aes != nil {
		if dec, err := s.decryptField(pd.NamaLengkap); err == nil {
			holderName = dec
		}
		if dec, err := s.decryptField(pd.NIK); err == nil {
			nik = dec
		}
	}

	// 6. Verify credentials exist
	cred, err := s.credentials.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("find credentials: %w", err)
	}
	if cred == nil {
		return nil, apperr.OnboardingIncomplete
	}

	// 7. Call core banking to create account
	cbResult, err := s.coreBanking.CreateAccount(ctx, req.SessionID, session.ProductType, holderName, nik)
	if err != nil {
		slog.Error("core banking create account failed", "session_id", req.SessionID, "error", err)
		return nil, apperr.AccountCreationFailed
	}

	// 8. Transition to COMPLETED
	completed := session.StepsCompleted
	completed.Submitted = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepCompleted, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepCompleted
		session.StepsCompleted = completed
		session.ExpiresAt = time.Now().Add(24 * time.Hour)
		_ = s.cache.Store(ctx, session)
	}

	// 9. Build response
	product := ProductCatalog[session.ProductType]
	mbcaUserID := "mbca_" + generateShortID()

	resp := &SubmitResponse{
		Account: AccountInfo{
			AccountNumber:          cbResult.AccountNumber,
			AccountType:            string(session.ProductType),
			AccountHolder:          holderName,
			Branch:                 cbResult.Branch,
			BranchCode:             cbResult.BranchCode,
			Currency:               product.Currency,
			Status:                 "ACTIVE",
			MinInitialDeposit:      product.MinInitialDeposit,
			InitialDepositDeadline: time.Now().UTC().Add(30 * 24 * time.Hour),
		},
		MBCA: MBCAInfo{
			UserID:        mbcaUserID,
			AccessCodeSet: true,
			PINSet:        true,
		},
		CreatedAt: time.Now().UTC(),
	}

	// 10. Cache idempotency response
	if idempotencyKey != "" && s.idempotency != nil {
		if respJSON, err := json.Marshal(resp); err == nil {
			_ = s.idempotency.Store(ctx, idempotencyKey, string(respJSON))
		}
	}

	// 11. Audit
	s.writeAudit(ctx, req.SessionID, AuditAccountCreated, "system", map[string]any{
		"account_number": cbResult.AccountNumber,
		"product_type":   string(session.ProductType),
		"branch_code":    cbResult.BranchCode,
		"mbca_user_id":   mbcaUserID,
	}, ipAddress, userAgent)

	return resp, nil
}

// validateAllStepsCompleted checks that every required step is done.
func validateAllStepsCompleted(sc StepsCompleted) error {
	if !sc.TNCAccepted || !sc.OCRVerified || !sc.PersonalDataSaved ||
		!sc.OTPVerified || !sc.BiometricVerified || !sc.VideoCallVerified ||
		!sc.CredentialsSet {
		return apperr.OnboardingIncomplete
	}
	return nil
}

func (s *SubmitService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
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
		_ = s.cache.Store(ctx, session)
	}
	return session, nil
}

func (s *SubmitService) decryptField(ciphertextHex string) (string, error) {
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

func (s *SubmitService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("submit audit failed", "session_id", sessionID, "error", err)
	}
}