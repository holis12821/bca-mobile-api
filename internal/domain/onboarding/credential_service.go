package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

var (
	accessCodeRegex = regexp.MustCompile(`^[a-zA-Z0-9]{6}$`)
	pinRegex        = regexp.MustCompile(`^[0-9]{6}$`)
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

	if req.AccessCodeEncrypted == "" || req.PINEncrypted == "" {
		return nil, apperr.ValidationError
	}

	// 2. Decrypt access code and PIN
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

	// 3. Validate access code rules
	if err := validateAccessCode(accessCode); err != nil {
		return nil, err
	}

	// 4. Validate PIN rules
	if err := validatePIN(pin); err != nil {
		return nil, err
	}

	// 5. PIN must differ from access code
	if strings.EqualFold(pin, accessCode) {
		return nil, apperr.CredSameAsAccessCode
	}

	// 6. Hash with Argon2id
	accessCodeHash, err := crypto.HashPassword(ctx, accessCode, crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("hash access code: %w", err)
	}
	pinHash, err := crypto.HashPassword(ctx, pin, crypto.DefaultArgon2Params)
	if err != nil {
		return nil, fmt.Errorf("hash pin: %w", err)
	}

	// 7. Store
	credID := "cred_" + generateShortID()
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

	// 8. Transition step: CREDENTIALS → REVIEW
	completed := session.StepsCompleted
	completed.CredentialsSet = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepReview, completed); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepReview
		session.StepsCompleted = completed
		session.ExpiresAt = time.Now().Add(24 * time.Hour)
		_ = s.cache.Store(ctx, session)
	}

	// Audit — NEVER include plaintext credentials
	s.writeAudit(ctx, req.SessionID, AuditCredentialsSet, "nasabah:"+session.DeviceID, map[string]any{
		"credential_id":    credID,
		"encryption_key_id": req.EncryptionKeyID,
	}, ipAddress, userAgent)

	return &SetCredentialsResponse{
		CredentialID:           credID,
		BiometricLoginAvailable: true,
		CurrentStep:            StepReview,
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

// validateAccessCode checks access code rules:
// - Exactly 6 alphanumeric
// - Not all same character
// - Not sequential
func validateAccessCode(code string) error {
	if !accessCodeRegex.MatchString(code) {
		return apperr.CredWeakAccessCode
	}
	lower := strings.ToLower(code)
	if isAllSameChar(lower) {
		return apperr.CredWeakAccessCode
	}
	if isSequential(lower) {
		return apperr.CredWeakAccessCode
	}
	return nil
}

// validatePIN checks PIN rules:
// - Exactly 6 digits
// - Not all same digit
// - Not sequential
func validatePIN(pin string) error {
	if !pinRegex.MatchString(pin) {
		return apperr.CredWeakPIN
	}
	if isAllSameChar(pin) {
		return apperr.CredWeakPIN
	}
	if isSequential(pin) {
		return apperr.CredWeakPIN
	}
	return nil
}

// isAllSameChar checks if all characters are the same (e.g. "aaaaaa", "111111").
func isAllSameChar(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

// isSequential checks if the string forms an arithmetic sequence.
// Detects both ascending and descending sequences like "123456", "abcdef", "654321".
// Uses sliding window: 3+ consecutive characters with same diff counts as sequential.
func isSequential(s string) bool {
	if len(s) < 3 {
		return false
	}

	consecutiveCount := 1
	prevDiff := int(s[1]) - int(s[0])

	for i := 2; i < len(s); i++ {
		diff := int(s[i]) - int(s[i-1])
		if diff == prevDiff && (diff == 1 || diff == -1) {
			consecutiveCount++
			if consecutiveCount >= 3 {
				return true
			}
		} else {
			consecutiveCount = 1
			prevDiff = diff
		}
	}

	return false
}

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