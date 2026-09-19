package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

const (
	sessionTTL           = 24 * time.Hour
	maxSessionsPerDevice = 3
	sessionRateWindow    = 1 * time.Hour
)

type SessionService struct {
	sessions SessionRepository
	cache    SessionCache
	audit    AuditRepository
}

type SessionServiceConfig struct {
	Sessions SessionRepository
	Cache    SessionCache
	Audit    AuditRepository
}

func NewSessionService(cfg SessionServiceConfig) *SessionService {
	return &SessionService{
		sessions: cfg.Sessions,
		cache:    cfg.Cache,
		audit:    cfg.Audit,
	}
}

// CreateSession creates a new onboarding session after validating product_type
// and enforcing the per-device rate limit.
func (s *SessionService) CreateSession(ctx context.Context, req CreateSessionRequest, ipAddress, userAgent string) (*CreateSessionResponse, error) {
	// Validate product type
	pt := ProductType(req.ProductType)
	if !pt.Valid() {
		return nil, apperr.OnboardingProductUnavailable
	}

	if req.DeviceID == "" {
		return nil, apperr.ValidationError
	}

	if req.AcceptedTNCVersion == "" {
		return nil, apperr.ValidationError
	}

	// Rate limit: max 3 sessions per device per hour
	count, err := s.sessions.CountActiveByDevice(ctx, req.DeviceID, time.Now().Add(-sessionRateWindow))
	if err != nil {
		return nil, fmt.Errorf("count active sessions: %w", err)
	}
	if count >= maxSessionsPerDevice {
		return nil, apperr.OnboardingSessionLimit
	}

	// Generate session_id with prefix "onb_"
	sessionID := "onb_" + generateShortID()

	now := time.Now().UTC()
	session := &Session{
		ID:          uuid.New(),
		SessionID:   sessionID,
		DeviceID:    req.DeviceID,
		ProductType: pt,
		CurrentStep: StepOCR, // TNC accepted at creation time
		TNCVersion:  req.AcceptedTNCVersion,
		StepsCompleted: StepsCompleted{
			TNCAccepted: true,
		},
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(sessionTTL),
	}

	// Store in PostgreSQL
	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Cache in Redis
	if s.cache != nil {
		if err := s.cache.Store(ctx, session); err != nil {
			slog.Error("cache onboarding session failed", "error", err)
		}
	}

	// Audit
	s.writeAudit(ctx, sessionID, AuditSessionCreated, "nasabah:"+req.DeviceID, map[string]any{
		"product_type": req.ProductType,
		"tnc_version":  req.AcceptedTNCVersion,
	}, ipAddress, userAgent)

	product := ProductCatalog[pt]
	return &CreateSessionResponse{
		SessionID:   sessionID,
		Product:     product,
		CurrentStep: StepOCR,
		ExpiresAt:   session.ExpiresAt,
	}, nil
}

// GetSession retrieves a session by ID, checking cache first then DB.
// Returns an expired-session error if the session has passed its TTL.
func (s *SessionService) GetSession(ctx context.Context, sessionID string) (*GetSessionResponse, error) {
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	return &GetSessionResponse{
		SessionID:      session.SessionID,
		Product:        ProductCatalog[session.ProductType],
		CurrentStep:    session.CurrentStep,
		StepsCompleted: session.StepsCompleted,
		CreatedAt:      session.CreatedAt,
		ExpiresAt:      session.ExpiresAt,
	}, nil
}

// CancelSession soft-deletes a session and writes an audit log.
func (s *SessionService) CancelSession(ctx context.Context, sessionID, ipAddress, userAgent string) error {
	// Verify session exists
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return err
	}

	// Soft-delete in DB
	if err := s.sessions.SoftDelete(ctx, sessionID); err != nil {
		return fmt.Errorf("soft delete session: %w", err)
	}

	// Remove from cache
	if s.cache != nil {
		if err := s.cache.Delete(ctx, sessionID); err != nil {
			slog.Error("delete onboarding cache failed", "error", err)
		}
	}

	// Audit
	s.writeAudit(ctx, sessionID, AuditSessionCancelled, "nasabah:"+session.DeviceID, map[string]any{
		"last_step": string(session.CurrentStep),
	}, ipAddress, userAgent)

	return nil
}

// TransitionStep advances the session to the next step. Used by other services.
func (s *SessionService) TransitionStep(ctx context.Context, sessionID string, to Step, ipAddress, userAgent string) error {
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return err
	}

	if !CanTransition(session.CurrentStep, to) {
		return apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Tidak dapat pindah dari %s ke %s.", session.CurrentStep, to),
		}
	}

	completed := session.StepsCompleted
	switch to {
	case StepOCR:
		completed.TNCAccepted = true
	case StepPersonalData:
		completed.OCRVerified = true
	case StepOTPVerify:
		completed.PersonalDataSaved = true
	case StepBiometric:
		completed.OTPVerified = true
	case StepVideoCall:
		completed.BiometricVerified = true
	case StepCredentials:
		completed.VideoCallVerified = true
	case StepReview:
		completed.CredentialsSet = true
	case StepCompleted:
		completed.Submitted = true
	}

	if err := s.sessions.UpdateStep(ctx, sessionID, to, completed); err != nil {
		return fmt.Errorf("update step: %w", err)
	}

	// Update cache
	if s.cache != nil {
		session.CurrentStep = to
		session.StepsCompleted = completed
		session.ExpiresAt = time.Now().Add(sessionTTL)
		if err := s.cache.Store(ctx, session); err != nil {
			slog.Error("cache step update failed", "error", err)
		}
	}

	// Audit
	s.writeAudit(ctx, sessionID, AuditStepTransition, "system", map[string]any{
		"from": string(session.CurrentStep),
		"to":   string(to),
	}, ipAddress, userAgent)

	return nil
}

// resolveSession loads a session from cache or DB.
func (s *SessionService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
	// Try cache first
	if s.cache != nil {
		session, err := s.cache.Get(ctx, sessionID)
		if err != nil {
			slog.Error("cache get onboarding session failed", "error", err)
		}
		if session != nil {
			if session.IsExpired() {
				return nil, apperr.OnboardingSessionExpired
			}
			return session, nil
		}
	}

	// Fallback to DB
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

	// Backfill cache
	if s.cache != nil {
		if err := s.cache.Store(ctx, session); err != nil {
			slog.Error("backfill cache failed", "error", err)
		}
	}

	return session, nil
}

func (s *SessionService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("onboarding audit insert failed",
			"session_id", sessionID,
			"event_type", eventType,
			"error", err,
		)
	}
}

// generateShortID returns 12 hex chars (6 random bytes).
func generateShortID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
