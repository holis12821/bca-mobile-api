package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/metrics"
)

// MonitoringService evaluates alert rules and provides system health metrics.
type MonitoringService struct {
	sessions   SessionRepository
	audit      AuditRepository
	queueCache VideoCallQueueCache

	// metrics opsional: tanpa registry, aturan alert yang bersandar pada counter
	// dilewati, bukan membuat seluruh endpoint monitoring gagal.
	metrics *metrics.Registry
}

type MonitoringServiceConfig struct {
	Sessions   SessionRepository
	Audit      AuditRepository
	QueueCache VideoCallQueueCache
	Metrics    *metrics.Registry
}

func NewMonitoringService(cfg MonitoringServiceConfig) *MonitoringService {
	return &MonitoringService{
		sessions:   cfg.Sessions,
		audit:      cfg.Audit,
		queueCache: cfg.QueueCache,
		metrics:    cfg.Metrics,
	}
}

// Ambang alert rasio kartu tidak tersedia (§Prompt 8).
const (
	cardUnavailableWindow = 15 * time.Minute
	cardUnavailableRatio  = 0.05

	// minSample menahan alert dari beberapa kejadian pertama. Tanpa itu, satu
	// penolakan di tengah lima pemilihan sudah 20% dan alert berbunyi untuk
	// keadaan yang sepenuhnya normal di jam sepi.
	cardUnavailableMinSample = 20
)

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

	// 2. Rasio kartu tidak tersedia (§Prompt 8).
	//
	// Rasio, bukan jumlah mutlak: pemilihan kartu yang naik dua kali lipat juga
	// menaikkan jumlah penolakan tanpa ada yang salah. Yang menandakan
	// konfigurasi stok keliru adalah PORSI penolakan yang melonjak.
	if alert := s.evaluateCardUnavailableRatio(); alert != nil {
		status.Alerts = append(status.Alerts, *alert)
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
	"audit_logs":       7 * 365 * 24 * time.Hour, // 7 years
	"session_data":     30 * 24 * time.Hour,      // 30 days after completion
	"ktp_photos":       30 * 24 * time.Hour,      // 30 days
	"biometric_photos": 7 * 24 * time.Hour,       // 7 days (UU PDP)
	"video_recordings": 5 * 365 * 24 * time.Hour, // 5 years (POJK)
	"credentials":      0,                        // until account closed
}

// evaluateCardUnavailableRatio menilai porsi penolakan CARD_TYPE_UNAVAILABLE
// terhadap seluruh pemilihan kartu dalam 15 menit terakhir.
//
// Di atas 5% adalah pertanda konfigurasi stok salah, bukan perilaku nasabah:
// nasabah memilih kartu yang ditawarkan layar, dan layar hanya menawarkan kartu
// yang katalog sebut tersedia. Penolakan yang sering berarti katalog
// mengatakan dua hal yang berbeda.
func (s *MonitoringService) evaluateCardUnavailableRatio() *MonitoringAlert {
	if s.metrics == nil {
		return nil
	}

	unavailable := s.metrics.WindowSum(metrics.CardUnavailable, cardUnavailableWindow)
	selected := s.metrics.WindowSum(metrics.CardSelected, cardUnavailableWindow)
	changed := s.metrics.WindowSum(metrics.CardChanged, cardUnavailableWindow)

	// Penyebutnya adalah SELURUH upaya pemilihan: yang berhasil (pilih + ganti)
	// dan yang ditolak. Memakai hanya yang berhasil akan membuat rasio meledak
	// justru ketika hampir semuanya ditolak — saat penyebutnya mendekati nol.
	attempts := selected + changed + unavailable
	if attempts < cardUnavailableMinSample {
		return nil
	}

	ratio := float64(unavailable) / float64(attempts)
	if ratio <= cardUnavailableRatio {
		return nil
	}

	return &MonitoringAlert{
		Rule:     "CARD_UNAVAILABLE_RATIO_HIGH",
		Severity: "WARNING",
		Message: fmt.Sprintf(
			"%.1f%% pemilihan kartu ditolak CARD_TYPE_UNAVAILABLE dalam %s (%d dari %d). "+
				"Periksa availability_status dan stok per wilayah di katalog, bukan perilaku nasabah.",
			ratio*100, cardUnavailableWindow, unavailable, attempts),
		Triggered: true,
	}
}
