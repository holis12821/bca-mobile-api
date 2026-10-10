package onboarding

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
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

func (m *mockSessionRepo) UpdateCard(_ context.Context, sessionID string, upd SessionCardUpdate) error {
	s := m.sessions[sessionID]
	if s == nil {
		return nil
	}
	s.CardType = upd.CardType
	s.CardCatalogVersion = upd.CatalogVersion
	selectedAt := upd.SelectedAt
	s.CardSelectedAt = &selectedAt
	s.CurrentStep = upd.CurrentStep
	s.StepsCompleted = upd.StepsCompleted
	return nil
}

func (m *mockSessionRepo) CountActiveByDevice(_ context.Context, deviceID string, _ time.Time) (int, error) {
	return m.counts[deviceID], nil
}

// ListForCS meniru keyset Postgres, termasuk mengembalikan limit+1 baris.
//
// Diurut dan disaring sungguhan, bukan mengembalikan seluruh map: paginasi yang
// mock-nya tidak pernah memotong apa pun adalah paginasi yang tidak pernah diuji.
func (m *mockSessionRepo) ListForCS(_ context.Context, f ListCSSessionsFilter) ([]*Session, error) {
	now := time.Now()

	var out []*Session
	for _, s := range m.sessions {
		if s.DeletedAt != nil {
			continue
		}
		if f.Step != "" && s.CurrentStep != f.Step {
			continue
		}
		if !f.IncludeExpired && !s.ExpiresAt.After(now) {
			continue
		}
		if f.StalledFor > 0 && s.UpdatedAt.After(now.Add(-f.StalledFor)) {
			continue
		}
		if f.Cursor != nil {
			// (created_at, id) < (cursor.created_at, cursor.id)
			if s.CreatedAt.After(f.Cursor.CreatedAt) {
				continue
			}
			if s.CreatedAt.Equal(f.Cursor.CreatedAt) && s.ID.String() >= f.Cursor.ID.String() {
				continue
			}
		}
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID.String() > out[j].ID.String()
	})

	if f.Limit > 0 && len(out) > f.Limit+1 {
		out = out[:f.Limit+1]
	}
	return out, nil
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

// A session_id is a bearer secret with no token behind it, so a copy of one —
// from a screenshot, a log line, a shared clipboard — must not be usable from
// another phone. Every session-scoped handler calls this before touching the
// session (docs/10-HANDOVER-BLOCKER-BACKEND.md butir 3).
func TestAssertDeviceOwnsSession(t *testing.T) {
	svc, _, _, _ := newService()
	ctx := context.Background()

	resp, err := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_owner",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := svc.AssertDeviceOwnsSession(ctx, resp.SessionID, "dev_owner"); err != nil {
		t.Fatalf("the owning device must pass: %v", err)
	}

	// An absent header still passes: answering 403 to every build already in
	// testers' hands would turn a hardening step into an outage.
	if err := svc.AssertDeviceOwnsSession(ctx, resp.SessionID, ""); err != nil {
		t.Fatalf("a client that sends no header must not be locked out: %v", err)
	}

	err = svc.AssertDeviceOwnsSession(ctx, resp.SessionID, "dev_someone_else")
	if err == nil {
		t.Fatal("a foreign device must be rejected")
	}
	if code := asAppErr(t, err).Code; code != apperr.OnboardingDeviceMismatch.Code {
		t.Fatalf("expected ONBOARDING_DEVICE_MISMATCH, got %s", code)
	}

	// An unknown session is still reported as unknown — the device check must
	// not turn a 404 into a 403.
	err = svc.AssertDeviceOwnsSession(ctx, "onb_does_not_exist", "dev_owner")
	if code := asAppErr(t, err).Code; code != apperr.OnboardingNotFound.Code {
		t.Fatalf("expected ONBOARDING_NOT_FOUND, got %s", code)
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
		{StepOCR, StepBiometric, false},    // skipping steps
		{StepCompleted, StepReview, false}, // backwards
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

// Membatalkan sesi harus membatalkan panggilan video call yang masih hidup.
//
// Soft-delete sesi tidak menyentuh baris panggilan — foreign key-nya ON DELETE CASCADE,
// tapi tidak ada baris yang benar-benar dihapus. Jadi panggilannya dulu tetap QUEUED,
// tetap anggota sorted set, dan tetap muncul di daftar petugas sebagai panggilan yang bisa
// diambil, padahal sesinya sudah tidak ada. `CANCELLED` adalah status yang sebelumnya
// tidak pernah ditulis siapa pun.
func TestCancelSessionCancelsLiveVideoCall(t *testing.T) {
	repo := newMockSessionRepo()
	cache := newMockSessionCache()
	audit := &mockAuditRepo{}

	vcRepo := newMockVideoCallRepo()
	queueCache := newMockQueueCache()
	vcSvc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         repo,
		Cache:            cache,
		VideoCalls:       vcRepo,
		QueueCache:       queueCache,
		JWTManager:       testJWTManager(),
		Audit:            audit,
		SignalingBaseURL: "ws://test:8080",
		Clock:            testClockWIB(),
	})

	svc := NewSessionService(SessionServiceConfig{
		Sessions:   repo,
		Cache:      cache,
		Audit:      audit,
		VideoCalls: vcSvc,
	})

	ctx := context.Background()
	created, err := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_cancel_vc",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Sesi dibawa ke VIDEO_CALL lalu mengantre.
	session := repo.sessions[created.SessionID]
	session.CurrentStep = StepVideoCall
	cache.data[created.SessionID] = session

	joined, err := vcSvc.JoinQueue(ctx, JoinQueueRequest{SessionID: created.SessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	if err := svc.CancelSession(ctx, created.SessionID, "127.0.0.1", "test"); err != nil {
		t.Fatalf("cancel session: %v", err)
	}

	if got := vcRepo.byQueue[joined.QueueID].Status; got != VCStatusCancelled {
		t.Errorf("status panggilan = %s, mau CANCELLED", got)
	}
	if _, ok := queueCache.members[joined.QueueID]; ok {
		t.Error("panggilan sesi yang dibatalkan masih jadi anggota antrean")
	}
}

// Pembatalan panggilan yang gagal tidak boleh menggagalkan pembatalan sesi: nasabah sudah
// meminta sesinya dibatalkan dan itu sudah terjadi.
func TestCancelSessionSucceedsWhenVideoCallCancelFails(t *testing.T) {
	repo := newMockSessionRepo()
	cache := newMockSessionCache()
	audit := &mockAuditRepo{}

	vcRepo := newMockVideoCallRepo()
	vcRepo.cancelErr = errors.New("postgres sedang tersendat")
	vcSvc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         repo,
		Cache:            cache,
		VideoCalls:       vcRepo,
		QueueCache:       newMockQueueCache(),
		JWTManager:       testJWTManager(),
		Audit:            audit,
		SignalingBaseURL: "ws://test:8080",
		Clock:            testClockWIB(),
	})

	svc := NewSessionService(SessionServiceConfig{
		Sessions: repo, Cache: cache, Audit: audit, VideoCalls: vcSvc,
	})

	ctx := context.Background()
	created, err := svc.CreateSession(ctx, CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_cancel_err",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session := repo.sessions[created.SessionID]
	session.CurrentStep = StepVideoCall
	cache.data[created.SessionID] = session
	if _, err := vcSvc.JoinQueue(ctx, JoinQueueRequest{SessionID: created.SessionID}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("join queue: %v", err)
	}

	if err := svc.CancelSession(ctx, created.SessionID, "127.0.0.1", "test"); err != nil {
		t.Fatalf("pembatalan sesi harus tetap berhasil: %v", err)
	}
	if repo.sessions[created.SessionID].DeletedAt == nil {
		t.Error("sesinya tidak di-soft-delete")
	}
}

// --- Jejak katalog produk di baris sesi (fase 5) ---

// newServiceWithProducts merakit SessionService dengan katalog produk terpasang.
func newServiceWithProducts(catalog *SavingsProductCatalog, enabled bool) (*SessionService, *mockSessionRepo) {
	repo := newMockSessionRepo()
	products := NewProductService(ProductServiceConfig{
		Repo:    &mockProductRepo{catalog: catalog},
		Enabled: enabled,
	})
	svc := NewSessionService(SessionServiceConfig{
		Sessions: repo,
		Cache:    newMockSessionCache(),
		Audit:    &mockAuditRepo{},
		Products: products,
	})
	return svc, repo
}

// Sesi baru mencatat setoran awal yang DILIHAT nasabah beserta versi katalognya.
//
// Yang perlu dibuktikan saat sengketa adalah angka saat itu, bukan angka hari ini.
func TestCreateSession_RecordsProductCatalogTrail(t *testing.T) {
	svc, repo := newServiceWithProducts(sampleProductCatalog(), true)

	resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType: "TAHAPAN_XPRESI",
		DeviceID:    "dev-trail",
		// AcceptedTNCVersion wajib — tanpa TNCService terpasang ia hanya diperiksa
		// tidak kosong, lalu tersimpan apa adanya.
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stored := repo.sessions[resp.SessionID]
	if stored == nil {
		t.Fatal("sesi tidak tersimpan")
	}
	if stored.MinInitialDepositShown != 50000 {
		t.Errorf("min_initial_deposit_shown = %d, mau 50000 (angka TAHAPAN_XPRESI)",
			stored.MinInitialDepositShown)
	}
	if stored.ProductCatalogVersion != "2026-10-07.1" {
		t.Errorf("product_catalog_version = %q", stored.ProductCatalogVersion)
	}
}

// Angkanya diambil dari katalog SERVER, bukan dari body: berbeda dari
// accepted_tnc_version yang memang harus datang dari client karena ia bukti
// persetujuan, angka ini adalah apa yang server tampilkan. Menerimanya dari client
// berarti membiarkan yang disengketakan menentukan bukti sengketanya.
func TestCreateSession_DepositShownIgnoresClientClaims(t *testing.T) {
	svc, repo := newServiceWithProducts(sampleProductCatalog(), true)

	// CreateSessionRequest tidak punya field untuk angka setoran — dan itu memang
	// bentuk yang dijaga test ini. Yang tersimpan harus angka katalog TAHAPAN_BCA.
	resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType: "TAHAPAN_BCA",
		DeviceID:    "dev-claim",
		// AcceptedTNCVersion wajib — tanpa TNCService terpasang ia hanya diperiksa
		// tidak kosong, lalu tersimpan apa adanya.
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := repo.sessions[resp.SessionID].MinInitialDepositShown; got != 500000 {
		t.Errorf("min_initial_deposit_shown = %d, mau 500000 dari katalog server", got)
	}
}

// ATURAN WAJIB #9: matinya katalog TIDAK boleh mematikan POST /sessions.
//
// Sesi tetap lahir, kedua kolom jejaknya kosong, dan tidak ada error.
func TestCreateSession_SurvivesDeadCatalog(t *testing.T) {
	cases := map[string]*SessionService{}

	svcFlagOff, repoFlagOff := newServiceWithProducts(sampleProductCatalog(), false)
	cases["flag katalog mati"] = svcFlagOff

	svcEmpty, repoEmpty := newServiceWithProducts(nil, true)
	cases["katalog kosong"] = svcEmpty

	repos := map[string]*mockSessionRepo{
		"flag katalog mati": repoFlagOff,
		"katalog kosong":    repoEmpty,
	}

	for name, svc := range cases {
		resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
			ProductType:        "TAHAPAN_BCA",
			DeviceID:           "dev-" + name,
			AcceptedTNCVersion: "2026-09-01",
		}, "127.0.0.1", "test")
		if err != nil {
			t.Fatalf("%s: sesi harus tetap lahir, dapat error: %v", name, err)
		}

		stored := repos[name].sessions[resp.SessionID]
		if stored == nil {
			t.Fatalf("%s: sesi tidak tersimpan", name)
		}
		if stored.MinInitialDepositShown != 0 {
			t.Errorf("%s: min_initial_deposit_shown = %d, mau 0 (NULL di database)",
				name, stored.MinInitialDepositShown)
		}
		if stored.ProductCatalogVersion != "" {
			t.Errorf("%s: product_catalog_version = %q, mau kosong", name, stored.ProductCatalogVersion)
		}
	}
}

// Validasi product_type TIDAK berubah: tetap pt.Valid(), dan tidak pernah menuntut
// baris onboarding_products ada. Produk yang sah tapi belum ada di katalog tetap bisa
// membuat sesi — kolom jejaknya saja yang kosong.
func TestCreateSession_ProductValidationUnchangedByCatalog(t *testing.T) {
	// Katalog hanya memuat TAHAPAN_BCA; TABUNGANKU sah menurut enum tapi tidak ada di
	// katalog.
	partial := sampleProductCatalog()
	partial.Products = partial.Products[:1]
	svc, repo := newServiceWithProducts(partial, true)

	resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType: "TABUNGANKU",
		DeviceID:    "dev-partial",
		// AcceptedTNCVersion wajib — tanpa TNCService terpasang ia hanya diperiksa
		// tidak kosong, lalu tersimpan apa adanya.
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("produk sah di luar katalog harus tetap bisa membuat sesi: %v", err)
	}
	if got := repo.sessions[resp.SessionID].ProductCatalogVersion; got != "" {
		t.Errorf("product_catalog_version = %q, mau kosong", got)
	}

	// Produk yang TIDAK sah tetap ditolak seperti sebelumnya.
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType: "DEPOSITO_BERJANGKA",
		DeviceID:    "dev-invalid",
		// AcceptedTNCVersion wajib — tanpa TNCService terpasang ia hanya diperiksa
		// tidak kosong, lalu tersimpan apa adanya.
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test"); err == nil {
		t.Error("product_type di luar enum harus tetap ditolak")
	}
}

// SessionService tanpa katalog sama sekali (test lama, dan deployment yang belum
// merakitnya) tetap bekerja.
func TestCreateSession_WorksWithoutProductService(t *testing.T) {
	svc, repo, _, _ := newService()

	resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType: "TAHAPAN_BCA",
		DeviceID:    "dev-no-catalog",
		// AcceptedTNCVersion wajib — tanpa TNCService terpasang ia hanya diperiksa
		// tidak kosong, lalu tersimpan apa adanya.
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.sessions[resp.SessionID].ProductCatalogVersion != "" {
		t.Error("tanpa katalog, kolom jejak harus kosong")
	}
}
