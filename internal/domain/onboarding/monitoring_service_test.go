package onboarding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/metrics"
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

// --- Alert rasio kartu tidak tersedia (§Prompt 8) ---
//
// Diuji dengan data sintetis: counter dinaikkan langsung, tanpa menjalankan
// seluruh flow. Yang diuji di sini adalah aturan alert-nya, bukan jalan yang
// menaikkan angkanya — itu sudah punya testnya sendiri.

func monitoringWithMetrics() (*MonitoringService, *metrics.Registry) {
	reg := metrics.NewRegistry()
	svc := NewMonitoringService(MonitoringServiceConfig{
		Sessions: newMockSessionRepo(),
		Audit:    &mockAuditRepo{},
		Metrics:  reg,
	})
	return svc, reg
}

func findAlert(status *MonitoringStatus, rule string) *MonitoringAlert {
	for i := range status.Alerts {
		if status.Alerts[i].Rule == rule {
			return &status.Alerts[i]
		}
	}
	return nil
}

func TestEvaluateAlerts_CardUnavailableRatioAboveThreshold(t *testing.T) {
	svc, reg := monitoringWithMetrics()

	// 30 pemilihan berhasil, 6 ditolak → 6/36 = 16,7%, di atas ambang 5%.
	for i := 0; i < 30; i++ {
		reg.Inc(metrics.CardSelected, metrics.Labels{"card_type": "PASPOR_BLUE"})
	}
	for i := 0; i < 6; i++ {
		reg.Inc(metrics.CardUnavailable, metrics.Labels{
			"card_type": "PASPOR_PLATINUM", "reason_key": "STOCK_EMPTY_IN_REGION",
		})
	}

	alert := findAlert(svc.EvaluateAlerts(context.Background()), "CARD_UNAVAILABLE_RATIO_HIGH")
	if alert == nil {
		t.Fatal("alert tidak berbunyi padahal rasio 16,7%")
	}
	if !strings.Contains(alert.Message, "6 dari 36") {
		t.Errorf("pesan alert harus menyebut angkanya: %q", alert.Message)
	}
}

func TestEvaluateAlerts_CardUnavailableRatioBelowThreshold(t *testing.T) {
	svc, reg := monitoringWithMetrics()

	// 60 berhasil, 1 ditolak → 1,6%, di bawah ambang.
	for i := 0; i < 60; i++ {
		reg.Inc(metrics.CardSelected, metrics.Labels{"card_type": "PASPOR_BLUE"})
	}
	reg.Inc(metrics.CardUnavailable, metrics.Labels{"card_type": "PASPOR_GOLD"})

	if alert := findAlert(svc.EvaluateAlerts(context.Background()), "CARD_UNAVAILABLE_RATIO_HIGH"); alert != nil {
		t.Errorf("alert berbunyi untuk rasio normal: %q", alert.Message)
	}
}

// Sampel kecil tidak boleh membunyikan alert: satu penolakan di antara tiga
// pemilihan adalah 33% dan sepenuhnya normal di jam sepi.
func TestEvaluateAlerts_CardUnavailableIgnoresSmallSample(t *testing.T) {
	svc, reg := monitoringWithMetrics()

	reg.Inc(metrics.CardSelected, metrics.Labels{"card_type": "PASPOR_BLUE"})
	reg.Inc(metrics.CardSelected, metrics.Labels{"card_type": "PASPOR_BLUE"})
	reg.Inc(metrics.CardUnavailable, metrics.Labels{"card_type": "PASPOR_GOLD"})

	if alert := findAlert(svc.EvaluateAlerts(context.Background()), "CARD_UNAVAILABLE_RATIO_HIGH"); alert != nil {
		t.Errorf("alert berbunyi untuk sampel kecil: %q", alert.Message)
	}
}

// Penggantian kartu ikut menjadi penyebut: tanpa itu, rasio akan terlihat lebih
// buruk daripada keadaan sebenarnya pada sesi yang bolak-balik mengganti kartu.
func TestEvaluateAlerts_CardChangedCountsInDenominator(t *testing.T) {
	svc, reg := monitoringWithMetrics()

	for i := 0; i < 5; i++ {
		reg.Inc(metrics.CardSelected, metrics.Labels{"card_type": "PASPOR_BLUE"})
	}
	for i := 0; i < 25; i++ {
		reg.Inc(metrics.CardChanged, metrics.Labels{"from": "PASPOR_BLUE", "to": "PASPOR_GOLD"})
	}
	// 1 dari 31 = 3,2% — di bawah ambang HANYA kalau penggantian ikut dihitung.
	reg.Inc(metrics.CardUnavailable, metrics.Labels{"card_type": "PASPOR_PLATINUM"})

	if alert := findAlert(svc.EvaluateAlerts(context.Background()), "CARD_UNAVAILABLE_RATIO_HIGH"); alert != nil {
		t.Errorf("penggantian kartu tidak masuk penyebut: %q", alert.Message)
	}
}

// Tanpa registry, aturan ini dilewati — bukan panic, dan bukan membuat seluruh
// endpoint monitoring gagal.
func TestEvaluateAlerts_NoMetricsRegistryIsSafe(t *testing.T) {
	svc := NewMonitoringService(MonitoringServiceConfig{
		Sessions: newMockSessionRepo(),
		Audit:    &mockAuditRepo{},
	})

	status := svc.EvaluateAlerts(context.Background())
	if findAlert(status, "CARD_UNAVAILABLE_RATIO_HIGH") != nil {
		t.Error("alert berbunyi tanpa registry")
	}
}
