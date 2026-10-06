package onboarding

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/metrics"
)

// MonitoringService evaluates alert rules and provides system health metrics.
type MonitoringService struct {
	sessions   SessionRepository
	audit      AuditRepository
	queueCache VideoCallQueueCache

	// personalData dan videoCalls hanya dipakai detail sesi sisi CS. Keduanya opsional:
	// tanpa mereka endpoint detail menjawab tanpa bagian itu, bukan gagal — alert dan
	// daftar sesi tidak boleh ikut mati karena satu dependensi yang tidak terpasang.
	personalData PersonalDataRepository
	videoCalls   VideoCallRepository

	// aes membuka PII yang tersimpan terenkripsi. Nil berarti kolomnya dibaca apa adanya,
	// mengikuti perilaku PersonalDataService supaya development tanpa kunci tetap jalan.
	aes *crypto.AES

	// metrics opsional: tanpa registry, aturan alert yang bersandar pada counter
	// dilewati, bukan membuat seluruh endpoint monitoring gagal.
	metrics *metrics.Registry

	clock func() time.Time
}

type MonitoringServiceConfig struct {
	Sessions     SessionRepository
	Audit        AuditRepository
	QueueCache   VideoCallQueueCache
	PersonalData PersonalDataRepository
	VideoCalls   VideoCallRepository
	AES          *crypto.AES
	Metrics      *metrics.Registry

	// Clock disuntik test. Nil memakai time.Now.
	Clock func() time.Time
}

func NewMonitoringService(cfg MonitoringServiceConfig) *MonitoringService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &MonitoringService{
		sessions:     cfg.Sessions,
		audit:        cfg.Audit,
		queueCache:   cfg.QueueCache,
		personalData: cfg.PersonalData,
		videoCalls:   cfg.VideoCalls,
		aes:          cfg.AES,
		metrics:      cfg.Metrics,
		clock:        clock,
	}
}

// Batas paginasi daftar sesi CS.
//
// Maksimum 100, bukan tak terbatas: daftar ini di belakang kredensial petugas, dan satu
// permintaan yang menarik seluruh tabel adalah cara termurah mengeluarkan isinya.
const (
	csSessionsDefaultLimit = 20
	csSessionsMaxLimit     = 100
)

// ListSessions mengembalikan sesi onboarding untuk layar pemantauan petugas.
//
// Mengembalikan (daftar, hasMore, cursorBerikutnya). Tidak memuat PII — lihat
// [CSSessionSummary].
func (s *MonitoringService) ListSessions(ctx context.Context, filter ListCSSessionsFilter) ([]CSSessionSummary, bool, *CSSessionCursor, error) {
	if filter.Limit <= 0 {
		filter.Limit = csSessionsDefaultLimit
	}
	if filter.Limit > csSessionsMaxLimit {
		filter.Limit = csSessionsMaxLimit
	}

	rows, err := s.sessions.ListForCS(ctx, filter)
	if err != nil {
		return nil, false, nil, err
	}

	// Baris ke-(limit+1) hanya penanda bahwa masih ada lagi; ia tidak ikut dikirim.
	hasMore := len(rows) > filter.Limit
	if hasMore {
		rows = rows[:filter.Limit]
	}

	now := s.clock()
	out := make([]CSSessionSummary, 0, len(rows))
	for _, sess := range rows {
		out = append(out, summarizeSession(sess, now))
	}

	var next *CSSessionCursor
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &CSSessionCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, hasMore, next, nil
}

// GetSessionDetail mengembalikan satu sesi berikut data pribadinya yang sudah disamarkan.
//
// actor WAJIB terisi: setiap pembukaan PII dicatat sebagai CS_SESSION_VIEWED, dan baris
// audit tanpa pelaku tidak menjawab pertanyaan yang membuatnya ditulis. Pemanggil yang
// tidak punya identitas petugas tidak boleh sampai ke sini.
func (s *MonitoringService) GetSessionDetail(ctx context.Context, sessionID, actor, ip, userAgent string) (*CSSessionDetail, error) {
	sess, err := s.sessions.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, apperr.OnboardingNotFound
	}

	detail := &CSSessionDetail{CSSessionSummary: summarizeSession(sess, s.clock())}

	if s.personalData != nil {
		pd, err := s.personalData.FindBySessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if pd != nil {
			masked, err := s.maskPersonalData(pd)
			if err != nil {
				return nil, err
			}
			detail.PersonalData = masked
		}
	}

	if s.videoCalls != nil {
		vc, err := s.videoCalls.FindBySessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if vc != nil {
			detail.VideoCall = &CSSessionVideoCall{
				QueueID:     vc.QueueID,
				QueueNumber: vc.QueueNumber,
				Status:      vc.Status,
				Result:      vc.Result,
				AgentName:   vc.AgentName,
				JoinedAt:    vc.JoinedAt,
				EndedAt:     vc.EndedAt,
			}
		}
	}

	// Dicatat SETELAH berhasil dirakit: pencarian session_id yang tidak ada bukan
	// pembukaan data siapa pun, dan mencatatnya akan memenuhi jejak audit dengan
	// peristiwa yang tidak pernah terjadi.
	//
	// Kegagalan menulis audit TIDAK menggagalkan response: data sudah dibaca petugas
	// saat itu juga, dan menyembunyikannya setelah terbaca tidak memperbaiki apa pun.
	// Yang penting kegagalannya terlihat di log.
	s.writeAudit(ctx, sessionID, AuditCSSessionViewed, actor, map[string]any{
		"current_step":      string(sess.CurrentStep),
		"personal_data":     detail.PersonalData != nil,
		"video_call_viewed": detail.VideoCall != nil,
	}, ip, userAgent)

	return detail, nil
}

func (s *MonitoringService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
	if s.audit == nil {
		return
	}
	err := s.audit.Insert(ctx, &AuditLog{
		ID:        uuid.New(),
		SessionID: sessionID,
		EventType: eventType,
		Actor:     actor,
		Details:   details,
		IPAddress: ip,
		UserAgent: ua,
		CreatedAt: s.clock(),
	})
	if err != nil {
		slog.Error("write cs audit failed",
			"session_id", sessionID, "event_type", string(eventType),
			"actor", actor, "error", err)
	}
}

// maskPersonalData mendekripsi lalu menyamarkan. Kebijakan penyamarannya di
// [MaskedPersonalData].
func (s *MonitoringService) maskPersonalData(pd *PersonalData) (*MaskedPersonalData, error) {
	nama, err := s.decryptField(pd.NamaLengkap)
	if err != nil {
		return nil, fmt.Errorf("decrypt nama: %w", err)
	}
	nik, err := s.decryptField(pd.NIK)
	if err != nil {
		return nil, fmt.Errorf("decrypt nik: %w", err)
	}
	phone, err := s.decryptField(pd.NomorHP)
	if err != nil {
		return nil, fmt.Errorf("decrypt phone: %w", err)
	}
	email, err := s.decryptField(pd.Email)
	if err != nil {
		return nil, fmt.Errorf("decrypt email: %w", err)
	}

	return &MaskedPersonalData{
		NamaLengkap:         nama,
		NIKMasked:           account.MaskNIK(nik),
		TempatLahir:         pd.TempatLahir,
		TanggalLahir:        pd.TanggalLahir,
		JenisKelamin:        pd.JenisKelamin,
		AlamatKTP:           pd.AlamatKTP,
		AlamatDomisiliSama:  pd.AlamatDomisiliSama,
		Pekerjaan:           pd.Pekerjaan,
		PenghasilanPerBulan: pd.PenghasilanPerBulan,
		SumberDanaUtama:     pd.SumberDanaUtama,
		NomorHPMasked:       account.MaskPhone(phone),
		EmailMasked:         account.MaskEmail(email),
	}, nil
}

func (s *MonitoringService) decryptField(ciphertextHex string) (string, error) {
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

// summarizeSession memetakan sesi ke baris daftar, menghitung turunan yang tidak ada
// di tabel.
func summarizeSession(sess *Session, now time.Time) CSSessionSummary {
	stalled := int(now.Sub(sess.UpdatedAt).Seconds())
	if stalled < 0 {
		// Jam yang mundur, atau baris yang baru ditulis di transaksi lain. Nol lebih
		// jujur daripada angka negatif yang tampil sebagai "diam -3 detik".
		stalled = 0
	}
	return CSSessionSummary{
		SessionID:      sess.SessionID,
		ProductType:    sess.ProductType,
		CurrentStep:    sess.CurrentStep,
		CardType:       sess.CardType,
		StepsCompleted: sess.StepsCompleted,
		CreatedAt:      sess.CreatedAt,
		UpdatedAt:      sess.UpdatedAt,
		ExpiresAt:      sess.ExpiresAt,
		Expired:        now.After(sess.ExpiresAt),
		StalledSeconds: stalled,
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
