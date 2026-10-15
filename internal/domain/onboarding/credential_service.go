package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pinrule"
)

type CredentialService struct {
	sessions    SessionRepository
	cache       SessionCache
	credentials CredentialRepository
	pinKeys     *crypto.RSAKeyPair
	audit       AuditRepository
}

type CredentialServiceConfig struct {
	Sessions    SessionRepository
	Cache       SessionCache
	Credentials CredentialRepository
	PINKeys     *crypto.RSAKeyPair
	Audit       AuditRepository
}

func NewCredentialService(cfg CredentialServiceConfig) *CredentialService {
	return &CredentialService{
		sessions:    cfg.Sessions,
		cache:       cfg.Cache,
		credentials: cfg.Credentials,
		pinKeys:     cfg.PINKeys,
		audit:       cfg.Audit,
	}
}

// SetCredentials decrypts, validates, hashes, and stores credentials.
func (s *CredentialService) SetCredentials(ctx context.Context, req SetCredentialsRequest, ipAddress, userAgent string) (*SetCredentialsResponse, error) {
	// 1. Resolve session
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	// 2. Idempotency: if credentials already set, return existing
	existing, err := s.credentials.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("find existing credentials: %w", err)
	}
	if existing != nil {
		return &SetCredentialsResponse{
			CredentialID:            existing.CredentialID,
			BiometricLoginAvailable: true,
			CurrentStep:             session.CurrentStep,
		}, nil
	}

	// 3. Validate session step
	if session.CurrentStep != StepCredentials {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan CREDENTIALS.", session.CurrentStep),
		}
	}

	// 4. Required fields. encryption_key_id is NOT NULL in the credentials
	// table, so leaving it unchecked turned a client mistake into a 500.
	if req.AccessCodeEncrypted == "" || req.PINEncrypted == "" || req.EncryptionKeyID == "" {
		return nil, apperr.ValidationError
	}

	// The key id was stored but never compared, so a client encrypting under a
	// rotated-out key reached the decrypt step and got CRED_DECRYPTION_FAILED
	// with nothing naming the cause. Saying which key is expected turns that
	// into a one-line fix on the client.
	if s.pinKeys != nil && !s.pinKeys.AcceptsKeyID(req.EncryptionKeyID) {
		return nil, apperr.Error{
			Status:  apperr.CredDecryptionFailed.Status,
			Code:    apperr.CredDecryptionFailed.Code,
			Message: apperr.CredDecryptionFailed.Message,
			Details: map[string]any{
				"expected_key_id": s.pinKeys.ActiveKeyID(),
				"received_key_id": req.EncryptionKeyID,
			},
		}
	}

	// 5. Decrypt access code and PIN
	accessCode, err := s.decryptCredential(req.AccessCodeEncrypted)
	if err != nil {
		slog.Error("decrypt access code failed", "error", err)
		return nil, apperr.CredDecryptionFailed
	}
	pin, err := s.decryptCredential(req.PINEncrypted)
	if err != nil {
		slog.Error("decrypt pin failed", "error", err)
		return nil, apperr.CredDecryptionFailed
	}

	// 6. Validate access code rules
	if err := validateAccessCode(accessCode); err != nil {
		return nil, err
	}

	// 7. Validate PIN rules
	if err := validatePIN(pin); err != nil {
		return nil, err
	}

	// 8. PIN must differ from access code
	if strings.EqualFold(pin, accessCode) {
		return nil, apperr.CredSameAsAccessCode
	}

	// 9. Hash with Argon2id
	accessCodeHash, err := crypto.HashPassword(ctx, accessCode, crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("hash access code: %w", err)
	}
	pinHash, err := crypto.HashPassword(ctx, pin, crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("hash pin: %w", err)
	}

	// 10. Store
	shortID, err := generateShortID()
	if err != nil {
		return nil, fmt.Errorf("generate credential id: %w", err)
	}
	credID := "cred_" + shortID
	cred := &Credential{
		ID:              uuid.New(),
		CredentialID:    credID,
		SessionID:       req.SessionID,
		AccessCodeHash:  accessCodeHash,
		PINHash:         pinHash,
		EncryptionKeyID: req.EncryptionKeyID,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.credentials.Create(ctx, cred); err != nil {
		return nil, fmt.Errorf("store credentials: %w", err)
	}

	// 11. Transition step: CREDENTIALS → REVIEW
	completed := session.StepsCompleted
	completed.CredentialsSet = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepReview, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepReview
		session.StepsCompleted = completed
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "session_id", req.SessionID, "error", cacheErr)
		}
	}

	// Audit — NEVER include plaintext credentials
	s.writeAudit(ctx, req.SessionID, AuditCredentialsSet, "nasabah:"+session.DeviceID, map[string]any{
		"credential_id":     credID,
		"encryption_key_id": req.EncryptionKeyID,
	}, ipAddress, userAgent)

	return &SetCredentialsResponse{
		CredentialID:            credID,
		BiometricLoginAvailable: true,
		CurrentStep:             StepReview,
	}, nil
}

// decryptCredential decrypts an RSA-OAEP encrypted credential.
func (s *CredentialService) decryptCredential(encrypted string) (string, error) {
	if s.pinKeys == nil {
		// Dev mode: treat encrypted value as plaintext
		return encrypted, nil
	}
	payload, err := s.pinKeys.DecryptPIN(encrypted)
	if err != nil {
		return "", err
	}
	return payload.PIN, nil
}

// The six-character credential rules now live in internal/pkg/pinrule, because
// the CS desk PIN enforces exactly the same ones and internal/domain/cs cannot
// call an unexported helper in this package. The four wrappers below keep this
// package's own vocabulary at its call sites — and keep the single copy of the
// rules in one place, where it cannot drift.

func validateAccessCode(code string) error { return pinrule.ValidateAccessCode(code) }

func validatePIN(pin string) error { return pinrule.ValidatePIN(pin) }

func isAllSameChar(s string) bool { return pinrule.AllSameChar(s) }

func isSequential(s string) bool { return pinrule.Sequential(s) }

func (s *CredentialService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
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

func (s *CredentialService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("credential audit failed", "session_id", sessionID, "error", err)
	}
}
