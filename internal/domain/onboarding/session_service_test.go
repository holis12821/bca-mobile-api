package onboarding

import (
	"context"
	"testing"
	"time"
)

// --- in-memory mocks ---

type mockSessionRepo struct {
	sessions map[string]*Session
	counts   map[string]int // deviceID -> count
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		sessions: make(map[string]*Session),
		counts:   make(map[string]int),
	}
}

func (m *mockSessionRepo) Create(_ context.Context, s *Session) error {
	m.sessions[s.SessionID] = s
	m.counts[s.DeviceID]++
	return nil
}

func (m *mockSessionRepo) FindBySessionID(_ context.Context, sessionID string) (*Session, error) {
	s, ok := m.sessions[sessionID]
	if !ok || s.DeletedAt != nil {
		return nil, nil
	}
	return s, nil
}

func (m *mockSessionRepo) SoftDelete(_ context.Context, sessionID string) error {
	s := m.sessions[sessionID]
	if s == nil {
		return nil
	}
	now := time.Now()
	s.DeletedAt = &now
	return nil
}

func (m *mockSessionRepo) UpdateStep(_ context.Context, sessionID string, step Step, completed StepsCompleted) error {
	s := m.sessions[sessionID]
	if s != nil {
		s.CurrentStep = step
		s.StepsCompleted = completed
		s.ExpiresAt = time.Now().Add(24 * time.Hour) // extend TTL on step transition
	}
	return nil
}

func (m *mockSessionRepo) CountActiveByDevice(_ context.Context, deviceID string, _ time.Time) (int, error) {
	return m.counts[deviceID], nil
}

type mockSessionCache struct {
	data map[string]*Session
}

func newMockSessionCache() *mockSessionCache {
	return &mockSessionCache{data: make(map[string]*Session)}
}

func (m *mockSessionCache) Store(_ context.Context, s *Session) error {
	m.data[s.SessionID] = s
	return nil
}

func (m *mockSessionCache) Get(_ context.Context, sessionID string) (*Session, error) {
	return m.data[sessionID], nil
}

func (m *mockSessionCache) Delete(_ context.Context, sessionID string) error {
	delete(m.data, sessionID)
	return nil
}

type mockAuditRepo struct {
	logs []*AuditLog
}

func (m *mockAuditRepo) Insert(_ context.Context, log *AuditLog) error {
	m.logs = append(m.logs, log)
	return nil
}

func (m *mockAuditRepo) FindBySessionID(_ context.Context, sessionID string) ([]*AuditLog, error) {
	var result []*AuditLog
	for _, l := range m.logs {
		if l.SessionID == sessionID {
			result = append(result, l)
		}
	}
	return result, nil
}

func newService() (*SessionService, *mockSessionRepo, *mockSessionCache, *mockAuditRepo) {
	repo := newMockSessionRepo()
	cache := newMockSessionCache()
	audit := &mockAuditRepo{}
	svc := NewSessionService(SessionServiceConfig{
		Sessions: repo,
		Cache:    cache,
		Audit:    audit,
	})
	return svc, repo, cache, audit
}

func TestCreateSession_Success(t *testing.T) {
	svc, repo, cache, audit := newService()
	ctx := context.Background()

	resp, err := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_123",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.SessionID == "" || resp.SessionID[:4] != "onb_" {
		t.Errorf("session_id should start with onb_, got %q", resp.SessionID)
	}
	if resp.Product.Type != ProductTahapanBCA {
		t.Errorf("expected TAHAPAN_BCA, got %s", resp.Product.Type)
	}
	if resp.CurrentStep != StepOCR {
		t.Errorf("expected OCR step, got %s", resp.CurrentStep)
	}

	// Verify stored in repo and cache
	if len(repo.sessions) != 1 {
		t.Errorf("expected 1 session in repo, got %d", len(repo.sessions))
	}
	if len(cache.data) != 1 {
		t.Errorf("expected 1 session in cache, got %d", len(cache.data))
	}

	// Verify audit log
	if len(audit.logs) != 1 {
		t.Fatalf("expected 1 audit log, got %d", len(audit.logs))
	}
	if audit.logs[0].EventType != AuditSessionCreated {
		t.Errorf("expected SESSION_CREATED audit, got %s", audit.logs[0].EventType)
	}
}

func TestCreateSession_InvalidProduct(t *testing.T) {
	svc, _, _, _ := newService()
	ctx := context.Background()

	_, err := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "INVALID",
		DeviceID:           "dev_123",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected error for invalid product")
	}
}

func TestCreateSession_RateLimit(t *testing.T) {
	svc, _, _, _ := newService()
	ctx := context.Background()

	// Create 3 sessions
	for i := 0; i < 3; i++ {
		_, err := svc.CreateSession(ctx, CreateSessionRequest{
			ProductType:        "TAHAPAN_BCA",
			DeviceID:           "dev_rate",
			AcceptedTNCVersion: "2026-09-01",
		}, "127.0.0.1", "test-agent")
		if err != nil {
			t.Fatalf("session %d: unexpected error: %v", i, err)
		}
	}

	// 4th should be rate limited
	_, err := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_rate",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected rate limit error on 4th session")
	}
}

func TestGetSession_Success(t *testing.T) {
	svc, _, _, _ := newService()
	ctx := context.Background()

	created, _ := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TABUNGANKU",
		DeviceID:           "dev_get",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")

	resp, err := svc.GetSession(ctx, created.SessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.SessionID != created.SessionID {
		t.Errorf("expected %s, got %s", created.SessionID, resp.SessionID)
	}
	if !resp.StepsCompleted.TNCAccepted {
		t.Error("tnc_accepted should be true")
	}
}

func TestGetSession_NotFound(t *testing.T) {
	svc, _, _, _ := newService()
	ctx := context.Background()

	_, err := svc.GetSession(ctx, "onb_doesnotexist")
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestCancelSession(t *testing.T) {
	svc, _, cache, audit := newService()
	ctx := context.Background()

	created, _ := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_cancel",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")

	err := svc.CancelSession(ctx, created.SessionID, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be removed from cache
	if _, ok := cache.data[created.SessionID]; ok {
		t.Error("session should be removed from cache after cancel")
	}

	// Should have 2 audit logs: created + cancelled
	if len(audit.logs) != 2 {
		t.Errorf("expected 2 audit logs, got %d", len(audit.logs))
	}
	if audit.logs[1].EventType != AuditSessionCancelled {
		t.Errorf("expected SESSION_CANCELLED, got %s", audit.logs[1].EventType)
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to Step
		want     bool
	}{
		{StepTNC, StepOCR, true},
		{StepOCR, StepPersonalData, true},
		{StepOCR, StepBiometric, false},     // skipping steps
		{StepCompleted, StepReview, false},   // backwards
		{StepReview, StepCompleted, true},
	}

	for _, tt := range tests {
		got := CanTransition(tt.from, tt.to)
		if got != tt.want {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestTransitionStep(t *testing.T) {
	svc, _, _, _ := newService()
	ctx := context.Background()

	created, _ := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_trans",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")

	// Current step is OCR, transition to PERSONAL_DATA
	err := svc.TransitionStep(ctx, created.SessionID, StepPersonalData, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, _ := svc.GetSession(ctx, created.SessionID)
	if resp.CurrentStep != StepPersonalData {
		t.Errorf("expected PERSONAL_DATA, got %s", resp.CurrentStep)
	}
	if !resp.StepsCompleted.OCRVerified {
		t.Error("ocr_verified should be true after transition")
	}

	// Invalid transition: PERSONAL_DATA -> BIOMETRIC (skipping OTP_VERIFY)
	err = svc.TransitionStep(ctx, created.SessionID, StepBiometric, "127.0.0.1", "test-agent")
	if err == nil {
		t.Fatal("expected error for invalid step transition")
	}
}
