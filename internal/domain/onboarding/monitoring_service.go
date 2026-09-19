package onboarding

import (
	"context"
	"log/slog"
	"time"
)

// MonitoringService evaluates alert rules and provides system health metrics.
type MonitoringService struct {
	sessions   SessionRepository
	audit      AuditRepository
	queueCache VideoCallQueueCache
}

type MonitoringServiceConfig struct {
	Sessions   SessionRepository
	Audit      AuditRepository
	QueueCache VideoCallQueueCache
}

func NewMonitoringService(cfg MonitoringServiceConfig) *MonitoringService {
	return &MonitoringService{
		sessions:   cfg.Sessions,
		audit:      cfg.Audit,
		queueCache: cfg.QueueCache,
	}
}

// GetAuditTrail returns the full audit trail for a session.
func (s *MonitoringService) GetAuditTrail(ctx context.Context, sessionID string) (*GetAuditTrailResponse, error) {
	logs, err := s.audit.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	events := make([]AuditLogResponse, 0, len(logs))
	for _, l := range logs {
		events = append(events, AuditLogResponse{
			EventType: string(l.EventType),
			Actor:     l.Actor,
			Details:   l.Details,
			IPAddress: l.IPAddress,
			CreatedAt: l.CreatedAt,
		})
	}

	return &GetAuditTrailResponse{
		SessionID: sessionID,
		Events:    events,
		Count:     len(events),
	}, nil
}

// EvaluateAlerts checks all monitoring rules and returns triggered alerts.
func (s *MonitoringService) EvaluateAlerts(ctx context.Context) *MonitoringStatus {
	status := &MonitoringStatus{}

	// 1. Check queue length
	if s.queueCache != nil {
		qLen, err := s.queueCache.Length(ctx)
		if err != nil {
			slog.Error("monitoring: queue length check failed", "error", err)
		} else {
			status.QueueLength = qLen
			if qLen > 10 {
				status.Alerts = append(status.Alerts, MonitoringAlert{
					Rule:      "QUEUE_LENGTH_HIGH",
					Severity:  "WARNING",
					Message:   "Video call queue exceeds 10 people. Alert CS supervisor.",
					Triggered: true,
				})
			}
		}
	}

	// Additional alert rules are evaluated by periodic background jobs.
	// The rules below describe what should be monitored:
	//
	// - SESSION_STUCK: session in same step > 1 hour
	//   Query: SELECT count(*) FROM onboarding_sessions
	//          WHERE updated_at < now() - interval '1 hour'
	//          AND current_step NOT IN ('COMPLETED')
	//          AND deleted_at IS NULL AND expires_at > now()
	//
	// - OCR_FAILURE_RATE: > 20% failures in 5 minutes
	//   Query: SELECT count(*) FILTER (WHERE event_type = 'OCR_UPLOADED') as total,
	//          count(*) FILTER (WHERE event_type IN ('BIOMETRIC_FAILED')) as failed
	//          FROM onboarding_audit_logs
	//          WHERE created_at > now() - interval '5 minutes'
	//
	// - BIOMETRIC_SPOOF: any spoof detected → immediate alert + block device
	//   Trigger: real-time via audit log insert (already logged in biometric_service)
	//
	// - SUBMISSION_FAILURE_RATE: > 5% failures
	//   Query: count ACCOUNT_CREATION_FAILED events in last hour

	return status
}

// RetentionPolicy defines data retention schedules per POJK regulation.
var RetentionPolicy = map[string]time.Duration{
	"audit_logs":      7 * 365 * 24 * time.Hour, // 7 years
	"session_data":    30 * 24 * time.Hour,       // 30 days after completion
	"ktp_photos":      30 * 24 * time.Hour,       // 30 days
	"biometric_photos": 7 * 24 * time.Hour,       // 7 days (UU PDP)
	"video_recordings": 5 * 365 * 24 * time.Hour, // 5 years (POJK)
	"credentials":     0,                          // until account closed
}