package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

const (
	avgCallDurationSeconds = 180 // 3 minutes estimated per call
	signalingTokenTTL      = 1 * time.Hour
)

type VideoCallService struct {
	sessions   SessionRepository
	cache      SessionCache
	videoCalls VideoCallRepository
	queueCache VideoCallQueueCache
	jwt        *crypto.JWTManager
	audit      AuditRepository
	sigBaseURL string // e.g. "wss://signal.bcamobile.id"
	clock      func() time.Time
}

type VideoCallServiceConfig struct {
	Sessions        SessionRepository
	Cache           SessionCache
	VideoCalls      VideoCallRepository
	QueueCache      VideoCallQueueCache
	JWTManager      *crypto.JWTManager
	Audit           AuditRepository
	SignalingBaseURL string
	Clock           func() time.Time // optional; defaults to time.Now
}

func NewVideoCallService(cfg VideoCallServiceConfig) *VideoCallService {
	baseURL := cfg.SignalingBaseURL
	if baseURL == "" {
		baseURL = "ws://localhost:8080"
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &VideoCallService{
		sessions:   cfg.Sessions,
		cache:      cfg.Cache,
		videoCalls: cfg.VideoCalls,
		queueCache: cfg.QueueCache,
		jwt:        cfg.JWTManager,
		audit:      cfg.Audit,
		sigBaseURL: baseURL,
		clock:      clock,
	}
}

// JoinQueue adds the session to the video call queue.
func (s *VideoCallService) JoinQueue(ctx context.Context, req JoinQueueRequest, ipAddress, userAgent string) (*JoinQueueResponse, error) {
	// 1. Validate session step
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepVideoCall {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan VIDEO_CALL.", session.CurrentStep),
		}
	}

	// 2. Check operating hours (06:00-22:00 WIB)
	if !s.isWithinOperatingHours() {
		return nil, apperr.Error{
			Status:  422,
			Code:    "VIDEO_CALL_OUTSIDE_HOURS",
			Message: "Video call hanya tersedia pukul 06:00-22:00 WIB.",
		}
	}

	// 3. Check if already queued
	existing, err := s.videoCalls.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("check existing queue: %w", err)
	}
	if existing != nil && (existing.Status == VCStatusQueued || existing.Status == VCStatusActive) {
		// Already in queue — return current position
		pos, _ := s.queueCache.Position(ctx, existing.QueueID)
		qLen, _ := s.queueCache.Length(ctx)
		return &JoinQueueResponse{
			QueueID:              existing.QueueID,
			QueueNumber:          existing.QueueNumber,
			Position:             pos,
			EstimatedWaitSeconds: pos * avgCallDurationSeconds,
			OperatingHours:       defaultOperatingHours(),
			SignalingURL:         s.buildSignalingURL(req.SessionID, existing.QueueID, qLen),
		}, nil
	}

	// 4. Generate queue ID and number
	queueID := "q_" + generateShortID()
	seqNum, err := s.queueCache.IncrDailyCounter(ctx)
	if err != nil {
		return nil, fmt.Errorf("incr queue counter: %w", err)
	}
	queueNumber := fmt.Sprintf("A-%03d", seqNum)

	now := time.Now().UTC()
	vc := &VideoCall{
		ID:          uuid.New(),
		QueueID:     queueID,
		SessionID:   req.SessionID,
		QueueNumber: queueNumber,
		Status:      VCStatusQueued,
		JoinedAt:    now,
	}

	if err := s.videoCalls.Create(ctx, vc); err != nil {
		return nil, fmt.Errorf("create video call: %w", err)
	}

	// 5. Add to Redis sorted set
	score := float64(now.UnixMilli())
	if err := s.queueCache.Add(ctx, queueID, score); err != nil {
		return nil, fmt.Errorf("add to queue: %w", err)
	}

	// 6. Get position
	pos, _ := s.queueCache.Position(ctx, queueID)

	// Audit
	s.writeAudit(ctx, req.SessionID, AuditVideoCallQueued, "nasabah:"+session.DeviceID, map[string]any{
		"queue_id":     queueID,
		"queue_number": queueNumber,
		"position":     pos,
	}, ipAddress, userAgent)

	return &JoinQueueResponse{
		QueueID:              queueID,
		QueueNumber:          queueNumber,
		Position:             pos,
		EstimatedWaitSeconds: pos * avgCallDurationSeconds,
		OperatingHours:       defaultOperatingHours(),
		SignalingURL:         s.buildSignalingURL(req.SessionID, queueID, pos),
	}, nil
}

// SubmitResult handles the CS backend submitting the result of a video call.
func (s *VideoCallService) SubmitResult(ctx context.Context, req SubmitVideoCallResultRequest, ipAddress, userAgent string) (*SubmitVideoCallResultResponse, error) {
	// 1. Find the video call
	vc, err := s.videoCalls.FindByQueueID(ctx, req.QueueID)
	if err != nil {
		return nil, fmt.Errorf("find video call: %w", err)
	}
	if vc == nil {
		return nil, apperr.OnboardingNotFound
	}
	if vc.SessionID != req.SessionID {
		return nil, apperr.ValidationError
	}

	// 2. Validate result
	result := VideoCallResult(req.Result)
	if result != VCResultApproved && result != VCResultRejected {
		return nil, apperr.ValidationError
	}

	// 3. Update video call record
	if err := s.videoCalls.UpdateResult(ctx, req.QueueID,
		result, req.AgentEmployeeID, "",
		req.Notes, req.RecordingID,
		req.KTPShownLive, req.IdentityConfirmed,
		req.CallDurationSeconds,
	); err != nil {
		return nil, fmt.Errorf("update video call result: %w", err)
	}

	// 4. Remove from queue
	_ = s.queueCache.Remove(ctx, req.QueueID)

	// 5. If approved, transition session step
	nextStep := StepVideoCall // stay if rejected
	if result == VCResultApproved {
		session, err := s.resolveSession(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		completed := session.StepsCompleted
		completed.VideoCallVerified = true
		if err := s.sessions.UpdateStep(ctx, req.SessionID, StepCredentials, completed); err != nil {
			return nil, fmt.Errorf("update step: %w", err)
		}
		if s.cache != nil {
			session.CurrentStep = StepCredentials
			session.StepsCompleted = completed
			session.ExpiresAt = time.Now().Add(sessionTTL)
			_ = s.cache.Store(ctx, session)
		}
		nextStep = StepCredentials
	}

	// Audit
	s.writeAudit(ctx, req.SessionID, AuditVideoCallEnded, "agent:"+req.AgentEmployeeID, map[string]any{
		"queue_id":           req.QueueID,
		"result":             req.Result,
		"ktp_shown_live":     req.KTPShownLive,
		"identity_confirmed": req.IdentityConfirmed,
		"duration_seconds":   req.CallDurationSeconds,
	}, ipAddress, userAgent)

	return &SubmitVideoCallResultResponse{
		SessionID:   req.SessionID,
		Result:      req.Result,
		CurrentStep: nextStep,
	}, nil
}

func (s *VideoCallService) buildSignalingURL(sessionID, queueID string, _ int64) string {
	// Generate short-lived JWT for signaling auth
	token := ""
	if s.jwt != nil {
		t, err := s.jwt.GenerateSignalingToken(sessionID, queueID, signalingTokenTTL)
		if err != nil {
			slog.Error("generate signaling token failed", "error", err)
		} else {
			token = t
		}
	}
	if token == "" {
		token = "dev-token-" + sessionID
	}
	return fmt.Sprintf("%s/v1/onboarding/video-call/signal?token=%s", s.sigBaseURL, token)
}

func (s *VideoCallService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
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

func (s *VideoCallService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("video call audit failed", "session_id", sessionID, "error", err)
	}
}

// isWithinOperatingHours checks if the current clock time is between 06:00-22:00 WIB.
func (s *VideoCallService) isWithinOperatingHours() bool {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return true // fallback: allow
	}
	now := s.clock().In(loc)
	hour := now.Hour()
	return hour >= 6 && hour < 22
}

func defaultOperatingHours() OperatingHours {
	return OperatingHours{
		Start:    "06:00",
		End:      "22:00",
		Timezone: "Asia/Jakarta",
	}
}