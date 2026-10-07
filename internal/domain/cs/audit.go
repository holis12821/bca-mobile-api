package cs

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// Jenis peristiwa audit petugas/terminal. Cocok dengan CHECK di migrasi 000037.
//
// Enam dari dua belas tindakan Rule 7 TIDAK ada di sini, dan itu disengaja: reservasi
// antrean, panggilan dimulai, panggilan berakhir, dan keputusan verifikasi sudah tercatat
// di `onboarding_audit_logs` (ber-kunci session_id nasabah), sementara pembukaan PII
// tercatat di `cs_access_logs`. Menuliskannya dua kali akan membuat setiap pemeriksaan
// harus memutuskan sumber mana yang benar.
const (
	EventAgentRegistered  = "AGENT_REGISTERED"
	EventAgentLogin       = "AGENT_LOGIN"
	EventAgentLoginFailed = "AGENT_LOGIN_FAILED"
	EventAgentLogout      = "AGENT_LOGOUT"
	EventAgentPasswordSet = "AGENT_PASSWORD_SET"

	EventSupervisorAuthorized = "SUPERVISOR_AUTHORIZED"
	EventSupervisorAuthFailed = "SUPERVISOR_AUTH_FAILED"

	EventDeviceHealthcheck = "DEVICE_HEALTHCHECK"
	EventPIIAcknowledged   = "PII_ACKNOWLEDGED"

	EventTerminalRegistered  = "TERMINAL_REGISTERED"
	EventTerminalActivated   = "TERMINAL_ACTIVATED"
	EventTerminalDeactivated = "TERMINAL_DEACTIVATED"
)

// AuditEvent adalah satu baris cs_audit_events.
type AuditEvent struct {
	ID        uuid.UUID `json:"-"`
	EventType string    `json:"event_type"`
	Actor     string    `json:"actor"`

	TerminalID string     `json:"terminal_id,omitempty"`
	SessionID  *uuid.UUID `json:"-"`

	Details map[string]any `json:"details,omitempty"`

	IPAddress string    `json:"ip_address,omitempty"`
	UserAgent string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditFilter menyaring pencarian jejak untuk pengawas.
type AuditFilter struct {
	Actor      string
	EventType  string
	TerminalID string

	From *time.Time
	To   *time.Time

	Limit  int
	Cursor *AuditCursor
}

// AuditCursor memegang nilai keyset untuk ORDER BY created_at DESC, id DESC.
type AuditCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// writeCSAudit menulis satu peristiwa audit petugas/terminal.
//
// Kegagalannya TIDAK menggagalkan tindakan yang memicunya: login yang berhasil lalu
// gagal dicatat tetap login yang berhasil, dan menolaknya setelah sesi terbuka hanya
// menambah keadaan yang tidak konsisten. Yang penting kegagalannya berbunyi di log.
//
// Repo nil melewatkannya dengan diam — proses yang berjalan tanpa penulis audit sudah
// punya peringatan sendiri di perakitan rute.
func writeCSAudit(
	ctx context.Context,
	repo AuditEventRepository,
	now time.Time,
	eventType, actor, terminalID string,
	sessionID *uuid.UUID,
	details map[string]any,
	ip, userAgent string,
) {
	if repo == nil {
		return
	}
	err := repo.Insert(ctx, &AuditEvent{
		ID:         uuid.New(),
		EventType:  eventType,
		Actor:      actor,
		TerminalID: terminalID,
		SessionID:  sessionID,
		Details:    details,
		IPAddress:  ip,
		UserAgent:  userAgent,
		CreatedAt:  now,
	})
	if err != nil {
		slog.Error("write cs audit event failed",
			"event_type", eventType, "actor", actor, "error", err)
	}
}

// --- Pencarian jejak untuk pengawas ---

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

// ValidAuditEventType melaporkan apakah sebuah jenis peristiwa dikenal.
//
// Dipakai menolak filter yang salah ketik. Nol baris karena `AGENT_LOGOUT_` tidak bisa
// dibedakan dari nol baris karena memang belum ada yang logout, dan pengawas yang
// menyimpulkan yang kedua dari yang pertama akan salah mengambil kesimpulan.
func ValidAuditEventType(t string) bool {
	switch t {
	case EventAgentRegistered, EventAgentLogin, EventAgentLoginFailed,
		EventAgentLogout, EventAgentPasswordSet,
		EventSupervisorAuthorized, EventSupervisorAuthFailed,
		EventDeviceHealthcheck, EventPIIAcknowledged,
		EventTerminalRegistered, EventTerminalActivated, EventTerminalDeactivated:
		return true
	}
	return false
}

// AuditQueryService membaca jejak petugas/terminal.
//
// Terpisah dari penulisnya: penulisan terjadi di dalam tindakan yang memicunya (lihat
// writeCSAudit), sementara pembacaan adalah pekerjaan pengawas yang tidak boleh ikut
// memegang jalur tulis.
type AuditQueryService struct {
	events AuditEventRepository
}

func NewAuditQueryService(events AuditEventRepository) *AuditQueryService {
	return &AuditQueryService{events: events}
}

// List mengembalikan peristiwa, penanda masih-ada-lagi, dan kursor berikutnya.
//
// Bentuk kembaliannya mengikuti MonitoringService.ListSessions supaya handler-nya
// mengemas pagination dengan cara yang sama — dua endpoint daftar yang berbeda bentuk
// kursornya akan membuat klien menulis dua pembaca.
func (s *AuditQueryService) List(ctx context.Context, filter AuditFilter) ([]AuditEvent, bool, *AuditCursor, error) {
	if s.events == nil {
		// Proses tanpa penulis audit juga tanpa pembacanya. Menolak, bukan menjawab
		// daftar kosong: daftar kosong terbaca sebagai "tidak ada yang terjadi".
		return nil, false, nil, apperr.ProviderNotConfigured
	}

	if filter.Limit <= 0 {
		filter.Limit = auditDefaultLimit
	}
	if filter.Limit > auditMaxLimit {
		filter.Limit = auditMaxLimit
	}

	rows, err := s.events.List(ctx, filter)
	if err != nil {
		return nil, false, nil, err
	}

	// Baris ke-(limit+1) hanya penanda bahwa masih ada lagi; ia tidak ikut dikirim.
	hasMore := len(rows) > filter.Limit
	if hasMore {
		rows = rows[:filter.Limit]
	}

	var next *AuditCursor
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return rows, hasMore, next, nil
}
