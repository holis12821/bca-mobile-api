package cs

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
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
