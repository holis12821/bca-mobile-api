package onboarding

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func setupMonitoringService() (*MonitoringService, *mockSessionRepo, *mockAuditRepo, *mockQueueCache) {
	sessionRepo := newMockSessionRepo()
	audit := &mockAuditRepo{}
	queueCache := newMockQueueCache()

	svc := NewMonitoringService(MonitoringServiceConfig{
		Sessions:   sessionRepo,
		Audit:      audit,
		QueueCache: queueCache,
	})

	return svc, sessionRepo, audit, queueCache
}

func TestGetAuditTrail_Success(t *testing.T) {
	svc, _, audit, _ := setupMonitoringService()
	ctx := context.Background()

	sessionID := "onb_audit_test"

	// Insert some audit entries
	audit.logs = append(audit.logs,
		&AuditLog{
			ID:        uuid.New(),
			SessionID: sessionID,
			EventType: AuditSessionCreated,
			Actor:     "nasabah:dev_123",
			Details:   map[string]any{"product_type": "TAHAPAN_BCA"},
			IPAddress: "127.0.0.1",
			UserAgent: "test",
			CreatedAt: time.Now().Add(-10 * time.Minute),
		},
		&AuditLog{
			ID:        uuid.New(),
			SessionID: sessionID,
			EventType: AuditOCRVerified,
			Actor:     "system",
			Details:   map[string]any{"accuracy": 99.4},
			IPAddress: "127.0.0.1",
			UserAgent: "test",
			CreatedAt: time.Now().Add(-5 * time.Minute),
		},
		&AuditLog{
			ID:        uuid.New(),
			SessionID: "other_session",
			EventType: AuditSessionCreated,
			Actor:     "nasabah:dev_other",
			CreatedAt: time.Now(),
		},
	)

	resp, err := svc.GetAuditTrail(ctx, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.SessionID != sessionID {
		t.Errorf("expected session_id %s, got %s", sessionID, resp.SessionID)
	}
	if resp.Count != 2 {
		t.Errorf("expected 2 events, got %d", resp.Count)
	}
	if len(resp.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(resp.Events))
	}
	if resp.Events[0].EventType != "SESSION_CREATED" {
		t.Errorf("expected SESSION_CREATED, got %s", resp.Events[0].EventType)
	}
	if resp.Events[1].EventType != "OCR_VERIFIED" {
		t.Errorf("expected OCR_VERIFIED, got %s", resp.Events[1].EventType)
	}
}

func TestGetAuditTrail_Empty(t *testing.T) {
	svc, _, _, _ := setupMonitoringService()
	ctx := context.Background()

	resp, err := svc.GetAuditTrail(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("expected 0 events, got %d", resp.Count)
	}
}

func TestEvaluateAlerts_QueueHigh(t *testing.T) {
	svc, _, _, queueCache := setupMonitoringService()
	ctx := context.Background()

	// Add 11 members to queue
	for i := 0; i < 11; i++ {
		queueCache.members["q_"+string(rune('a'+i))] = float64(i)
	}

	status := svc.EvaluateAlerts(ctx)

	if status.QueueLength != 11 {
		t.Errorf("expected queue length 11, got %d", status.QueueLength)
	}
	if len(status.Alerts) == 0 {
		t.Fatal("expected at least one alert")
	}

	found := false
	for _, a := range status.Alerts {
		if a.Rule == "QUEUE_LENGTH_HIGH" && a.Triggered {
			found = true
		}
	}
	if !found {
		t.Error("expected QUEUE_LENGTH_HIGH alert to be triggered")
	}
}

func TestEvaluateAlerts_QueueNormal(t *testing.T) {
	svc, _, _, queueCache := setupMonitoringService()
	ctx := context.Background()

	// Add 3 members (under threshold)
	queueCache.members["q_a"] = 1
	queueCache.members["q_b"] = 2
	queueCache.members["q_c"] = 3

	status := svc.EvaluateAlerts(ctx)

	if status.QueueLength != 3 {
		t.Errorf("expected queue length 3, got %d", status.QueueLength)
	}
	for _, a := range status.Alerts {
		if a.Rule == "QUEUE_LENGTH_HIGH" {
			t.Error("QUEUE_LENGTH_HIGH should not trigger for 3 items")
		}
	}
}

func TestRetentionPolicy(t *testing.T) {
	if RetentionPolicy["audit_logs"] == 0 {
		t.Error("audit_logs retention should be non-zero (7 years)")
	}
	if RetentionPolicy["biometric_photos"] != 7*24*time.Hour {
		t.Errorf("biometric_photos should be 7 days, got %v", RetentionPolicy["biometric_photos"])
	}
	if RetentionPolicy["credentials"] != 0 {
		t.Error("credentials retention should be 0 (until account closed)")
	}
}