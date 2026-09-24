package auth

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Audit action constants.
const (
	AuditLoginSuccess    = "AUTH_LOGIN_SUCCESS"
	AuditLoginFailed     = "AUTH_LOGIN_FAILED"
	AuditAccountLocked   = "AUTH_ACCOUNT_LOCKED"
	AuditTokenRefresh    = "AUTH_TOKEN_REFRESH"
	AuditSuspiciousLogin = "SECURITY_SUSPICIOUS_LOGIN"
	AuditLogout          = "AUTH_LOGOUT"
	AuditLogoutAll       = "AUTH_LOGOUT_ALL"
)

const (
	auditBufferSize = 256
	auditWorkers    = 4

	// auditInsertTimeout bounds one insert. Without it a stalled database parks
	// all four workers forever: the buffer then fills, and every audit entry
	// silently degrades to an slog line with nobody told why.
	auditInsertTimeout = 5 * time.Second
)

// AuditService writes audit entries via a buffered channel + fixed worker pool.
// Audit failure never fails the business transaction.
// If the buffer is full or DB insert fails, the entry is logged via slog as fallback.
type AuditService struct {
	repo AuditRepository
	ch   chan *AuditEntry
	wg   sync.WaitGroup
}

func NewAuditService(repo AuditRepository) *AuditService {
	s := &AuditService{
		repo: repo,
		ch:   make(chan *AuditEntry, auditBufferSize),
	}
	s.wg.Add(auditWorkers)
	for i := 0; i < auditWorkers; i++ {
		go s.worker()
	}
	return s
}

// Log enqueues an audit entry. Non-blocking: if buffer is full, falls back to slog.
func (s *AuditService) Log(entry *AuditEntry) {
	if entry == nil {
		return
	}
	select {
	case s.ch <- entry:
	default:
		slog.Warn("audit buffer full, falling back to slog",
			"action", entry.Action,
			"user_id", entry.UserID,
			"resource_type", entry.ResourceType,
			"metadata", entry.Metadata,
		)
	}
}

// Close drains the buffer and waits for workers to finish.
func (s *AuditService) Close() {
	close(s.ch)
	s.wg.Wait()
}

func (s *AuditService) worker() {
	defer s.wg.Done()
	for entry := range s.ch {
		s.insert(entry)
	}
}

// insert writes one entry and survives both a hung datastore and a panic.
//
// A panic here has no Recovery middleware above it — the HTTP one only covers
// the request goroutine — so one malformed entry used to take the whole server
// down with it. An audit line is never worth a process.
func (s *AuditService) insert(entry *AuditEntry) {
	if entry == nil {
		return
	}

	defer func() {
		if rv := recover(); rv != nil {
			slog.Error("audit worker panic recovered",
				"panic", rv,
				"stack", string(debug.Stack()),
				"action", entry.Action,
			)
		}
	}()

	// Background, not a request context: the request that triggered this has
	// long returned. The deadline is this function's own.
	ctx, cancel := context.WithTimeout(context.Background(), auditInsertTimeout)
	defer cancel()

	if err := s.repo.Insert(ctx, entry); err != nil {
		slog.Error("audit insert failed, falling back to slog",
			"error", err,
			"action", entry.Action,
			"user_id", entry.UserID,
			"resource_type", entry.ResourceType,
			"metadata", entry.Metadata,
		)
	}
}
