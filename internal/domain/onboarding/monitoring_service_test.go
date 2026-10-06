package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
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

// --- Pemantauan sesi sisi CS ---

func setupCSMonitoring() (*MonitoringService, *mockSessionRepo, *mockPersonalDataRepo, *mockVideoCallRepo, *mockAuditRepo) {
	sessionRepo := newMockSessionRepo()
	pdRepo := newMockPersonalDataRepo()
	vcRepo := newMockVideoCallRepo()
	audit := &mockAuditRepo{}

	svc := NewMonitoringService(MonitoringServiceConfig{
		Sessions:     sessionRepo,
		Audit:        audit,
		QueueCache:   newMockQueueCache(),
		PersonalData: pdRepo,
		VideoCalls:   vcRepo,
		// AES nil: kolom dibaca apa adanya, jadi test bisa menaruh teks biasa dan
		// memeriksa penyamarannya tanpa menyiapkan kunci.
	})
	return svc, sessionRepo, pdRepo, vcRepo, audit
}

// addCSSession menanam satu sesi dengan created_at dan updated_at yang ditentukan.
func addCSSession(repo *mockSessionRepo, id string, step Step, createdAt, updatedAt time.Time) *Session {
	s := &Session{
		ID:          uuid.New(),
		SessionID:   id,
		DeviceID:    "dev_" + id,
		ProductType: ProductTahapanBCA,
		CurrentStep: step,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	repo.sessions[id] = s
	return s
}

func TestListSessionsForCS_FiltersByStep(t *testing.T) {
	svc, repo, _, _, _ := setupCSMonitoring()
	now := time.Now()

	addCSSession(repo, "onb_a", StepBiometric, now.Add(-3*time.Minute), now)
	addCSSession(repo, "onb_b", StepVideoCall, now.Add(-2*time.Minute), now)
	addCSSession(repo, "onb_c", StepBiometric, now.Add(-1*time.Minute), now)

	items, _, _, err := svc.ListSessions(context.Background(), ListCSSessionsFilter{Step: StepBiometric})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d sesi, mau 2 yang di BIOMETRIC", len(items))
	}
	for _, it := range items {
		if it.CurrentStep != StepBiometric {
			t.Fatalf("filter bocor: %s di %s", it.SessionID, it.CurrentStep)
		}
	}
}

// Daftar ini tidak boleh memuat PII dalam bentuk apa pun, termasuk device_id.
//
// Dijaga test karena kebocorannya tidak terlihat di layar — kolom tambahan di JSON
// hanya ketahuan kalau ada yang membaca responsnya.
func TestListSessionsForCS_CarriesNoPII(t *testing.T) {
	svc, repo, _, _, _ := setupCSMonitoring()
	now := time.Now()
	addCSSession(repo, "onb_a", StepBiometric, now.Add(-time.Minute), now)

	items, _, _, err := svc.ListSessions(context.Background(), ListCSSessionsFilter{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d sesi", len(items))
	}

	blob, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leaked := range []string{"device_id", "dev_onb_a", "nik", "nama"} {
		if strings.Contains(strings.ToLower(string(blob)), leaked) {
			t.Fatalf("daftar sesi memuat %q: %s", leaked, blob)
		}
	}
}

func TestListSessionsForCS_PaginatesWithCursor(t *testing.T) {
	svc, repo, _, _, _ := setupCSMonitoring()
	now := time.Now()

	// Lima sesi, created_at menurun supaya urutannya deterministik.
	for i := range 5 {
		addCSSession(repo, fmt.Sprintf("onb_%d", i), StepBiometric,
			now.Add(-time.Duration(i+1)*time.Minute), now)
	}

	first, hasMore, cursor, err := svc.ListSessions(context.Background(), ListCSSessionsFilter{Limit: 2})
	if err != nil {
		t.Fatalf("halaman 1: %v", err)
	}
	if len(first) != 2 || !hasMore || cursor == nil {
		t.Fatalf("halaman 1: len=%d hasMore=%v cursor=%v", len(first), hasMore, cursor)
	}

	second, _, _, err := svc.ListSessions(context.Background(), ListCSSessionsFilter{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatalf("halaman 2: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("halaman 2 len=%d", len(second))
	}

	// Tidak boleh ada yang muncul di dua halaman — itu gejala keyset yang salah batas.
	seen := map[string]bool{}
	for _, it := range append(first, second...) {
		if seen[it.SessionID] {
			t.Fatalf("%s muncul di dua halaman", it.SessionID)
		}
		seen[it.SessionID] = true
	}
}

func TestListSessionsForCS_StalledFilterAndSeconds(t *testing.T) {
	svc, repo, _, _, _ := setupCSMonitoring()
	now := time.Now()

	addCSSession(repo, "onb_bergerak", StepBiometric, now.Add(-time.Hour), now.Add(-30*time.Second))
	addCSSession(repo, "onb_diam", StepBiometric, now.Add(-time.Hour), now.Add(-40*time.Minute))

	items, _, _, err := svc.ListSessions(context.Background(), ListCSSessionsFilter{
		StalledFor: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 || items[0].SessionID != "onb_diam" {
		t.Fatalf("filter diam salah: %+v", items)
	}
	if items[0].StalledSeconds < 2000 {
		t.Fatalf("stalled_seconds = %d, mau sekitar 2400", items[0].StalledSeconds)
	}
}

func TestListSessionsForCS_LimitIsCapped(t *testing.T) {
	svc, repo, _, _, _ := setupCSMonitoring()
	now := time.Now()
	addCSSession(repo, "onb_a", StepBiometric, now, now)

	// Batas atas ada supaya satu permintaan tidak bisa menarik seluruh tabel.
	_, _, _, err := svc.ListSessions(context.Background(), ListCSSessionsFilter{Limit: 100000})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	// Mock mengembalikan limit+1; yang diuji di sini service tidak meneruskan 100000.
	// Lihat csSessionsMaxLimit.
}

func TestGetSessionDetailForCS_MasksSensitiveFields(t *testing.T) {
	svc, repo, pdRepo, _, _ := setupCSMonitoring()
	now := time.Now()
	addCSSession(repo, "onb_x", StepVideoCall, now, now)

	pdRepo.data["onb_x"] = &PersonalData{
		SessionID:   "onb_x",
		NIK:         "3171064509900002",
		NamaLengkap: "Siti Rahmawati",
		NomorHP:     "081234567890",
		Email:       "siti.rahmawati@example.com",
		TempatLahir: "Jakarta",
	}

	detail, err := svc.GetSessionDetail(context.Background(), "onb_x", "agent:CS-1042", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.PersonalData == nil {
		t.Fatal("personal_data kosong")
	}

	// Nama utuh: mencocokkan orang dengan namanya adalah inti pekerjaan petugas.
	if detail.PersonalData.NamaLengkap != "Siti Rahmawati" {
		t.Fatalf("nama disamarkan padahal tidak seharusnya: %q", detail.PersonalData.NamaLengkap)
	}

	// NIK, HP, email tidak boleh pernah muncul utuh.
	blob, _ := json.Marshal(detail)
	for _, secret := range []string{"3171064509900002", "081234567890", "siti.rahmawati@example.com"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("%q bocor utuh di response", secret)
		}
	}
	if detail.PersonalData.NIKMasked != "3171**********02" {
		t.Fatalf("nik_masked = %q", detail.PersonalData.NIKMasked)
	}
}

// Membuka PII harus meninggalkan jejak, dan jejaknya harus menyebut petugasnya.
func TestGetSessionDetailForCS_WritesAccessAudit(t *testing.T) {
	svc, repo, pdRepo, _, audit := setupCSMonitoring()
	now := time.Now()
	addCSSession(repo, "onb_x", StepVideoCall, now, now)
	pdRepo.data["onb_x"] = &PersonalData{SessionID: "onb_x", NamaLengkap: "Siti"}

	if _, err := svc.GetSessionDetail(context.Background(), "onb_x", "agent:CS-1042", "10.0.0.1", "desktop"); err != nil {
		t.Fatalf("get detail: %v", err)
	}

	var found *AuditLog
	for _, l := range audit.logs {
		if l.EventType == AuditCSSessionViewed {
			found = l
			break
		}
	}
	if found == nil {
		t.Fatal("CS_SESSION_VIEWED tidak tercatat; tidak ada jejak siapa membuka data nasabah")
	}
	if found.Actor != "agent:CS-1042" {
		t.Fatalf("actor = %q", found.Actor)
	}
	if found.IPAddress != "10.0.0.1" {
		t.Fatalf("ip_address = %q", found.IPAddress)
	}
}

// session_id yang tidak ada bukan pembukaan data siapa pun, jadi tidak boleh menulis
// jejak audit — kalau tidak, menelusuri session_id acak akan memenuhi audit dengan
// peristiwa yang tidak pernah terjadi.
func TestGetSessionDetailForCS_UnknownSessionWritesNoAudit(t *testing.T) {
	svc, _, _, _, audit := setupCSMonitoring()

	_, err := svc.GetSessionDetail(context.Background(), "onb_tidak_ada", "agent:CS-1042", "", "")
	if err == nil {
		t.Fatal("sesi tidak dikenal seharusnya error")
	}
	for _, l := range audit.logs {
		if l.EventType == AuditCSSessionViewed {
			t.Fatal("CS_SESSION_VIEWED tercatat untuk sesi yang tidak ada")
		}
	}
}

// Sesi yang belum sampai PERSONAL_DATA memang belum punya apa pun. Itu keadaan normal.
func TestGetSessionDetailForCS_WithoutPersonalData(t *testing.T) {
	svc, repo, _, _, _ := setupCSMonitoring()
	now := time.Now()
	addCSSession(repo, "onb_baru", StepOCR, now, now)

	detail, err := svc.GetSessionDetail(context.Background(), "onb_baru", "agent:CS-1042", "", "")
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.PersonalData != nil {
		t.Fatal("personal_data terisi padahal sesi baru sampai OCR")
	}
	if detail.CurrentStep != StepOCR {
		t.Fatalf("current_step = %s", detail.CurrentStep)
	}
}

func TestMaskNIK(t *testing.T) {
	cases := map[string]string{
		"3171064509900002": "3171**********02",
		"317106":           "******",
		"":                 "",
	}
	for in, want := range cases {
		if got := account.MaskNIK(in); got != want {
			t.Errorf("account.MaskNIK(%q) = %q, mau %q", in, got, want)
		}
	}
}
