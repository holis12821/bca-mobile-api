package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

var (
	testJWTOnce sync.Once
	testJWTMgr  *crypto.JWTManager
)

// testJWTManager builds one RSA-backed manager for the whole package: signing
// tokens is the only way to get a signaling URL now, and generating a 2048-bit
// key per test would dominate the runtime.
func testJWTManager() *crypto.JWTManager {
	testJWTOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic("generate test rsa key: " + err.Error())
		}
		testJWTMgr = crypto.NewJWTManager(
			&crypto.RSAKeyPair{PrivateKey: key, PublicKey: &key.PublicKey},
			15*time.Minute, time.Hour,
		)
	})
	return testJWTMgr
}

// --- in-memory mocks for video call ---

type mockVideoCallRepo struct {
	byQueue   map[string]*VideoCall
	bySession map[string]*VideoCall

	// markActiveErr dan cancelErr memaksa jalur kegagalan yang perilakunya penting:
	// MarkActive yang gagal harus MENOLAK penerbitan token, dan Cancel yang gagal tidak
	// boleh menggagalkan pembatalan sesi.
	markActiveErr error
	cancelErr     error
}

func newMockVideoCallRepo() *mockVideoCallRepo {
	return &mockVideoCallRepo{
		byQueue:   make(map[string]*VideoCall),
		bySession: make(map[string]*VideoCall),
	}
}

func (m *mockVideoCallRepo) Create(_ context.Context, vc *VideoCall) error {
	m.byQueue[vc.QueueID] = vc
	m.bySession[vc.SessionID] = vc
	return nil
}

func (m *mockVideoCallRepo) FindByQueueID(_ context.Context, queueID string) (*VideoCall, error) {
	return m.byQueue[queueID], nil
}

func (m *mockVideoCallRepo) FindBySessionID(_ context.Context, sessionID string) (*VideoCall, error) {
	return m.bySession[sessionID], nil
}

func (m *mockVideoCallRepo) MarkActive(_ context.Context, queueID, agentEmployeeID, agentName string) error {
	if m.markActiveErr != nil {
		return m.markActiveErr
	}
	vc := m.byQueue[queueID]
	if vc == nil {
		return nil
	}
	vc.Status = VCStatusActive
	if agentEmployeeID != "" {
		vc.AgentEmployeeID = agentEmployeeID
	}
	if agentName != "" {
		vc.AgentName = agentName
	}
	if vc.StartedAt == nil {
		now := time.Now()
		vc.StartedAt = &now
	}
	return nil
}

// Cancel meniru `WHERE status IN ('QUEUED','ACTIVE')`: COMPLETED tidak pernah tersentuh,
// dan panggilan yang sudah dibatalkan melaporkan false tanpa error.
func (m *mockVideoCallRepo) Cancel(_ context.Context, queueID string) (bool, error) {
	if m.cancelErr != nil {
		return false, m.cancelErr
	}
	vc := m.byQueue[queueID]
	if vc == nil {
		return false, nil
	}
	if vc.Status != VCStatusQueued && vc.Status != VCStatusActive {
		return false, nil
	}
	vc.Status = VCStatusCancelled
	if vc.EndedAt == nil {
		now := time.Now()
		vc.EndedAt = &now
	}
	return true, nil
}

func (m *mockVideoCallRepo) UpdateResult(_ context.Context, queueID string, result VideoCallResult, agentEmployeeID, agentName, notes, recordingID string, ktpShown, identityConfirmed bool, durationSeconds int) error {
	vc := m.byQueue[queueID]
	if vc == nil {
		return nil
	}
	vc.Status = VCStatusCompleted
	vc.Result = result
	// Meniru COALESCE(agent_employee_id, NULLIF($3,'')) di SQL: yang sudah tercatat
	// menang, pelapor tidak bisa menulis ulang atribusi panggilan.
	if vc.AgentEmployeeID == "" && agentEmployeeID != "" {
		vc.AgentEmployeeID = agentEmployeeID
	}
	// agent_name masih COALESCE(NULLIF($4,''), agent_name): nilai kosong tidak menghapus
	// nama yang sudah ada.
	if agentName != "" {
		vc.AgentName = agentName
	}
	vc.Notes = notes
	vc.RecordingID = recordingID
	vc.KTPShownLive = ktpShown
	vc.IdentityConfirmed = identityConfirmed
	vc.CallDurationSeconds = durationSeconds
	now := time.Now()
	vc.EndedAt = &now
	return nil
}

type mockQueueCache struct {
	members map[string]float64
	counter int64
}

func newMockQueueCache() *mockQueueCache {
	return &mockQueueCache{members: make(map[string]float64)}
}

func (m *mockQueueCache) Add(_ context.Context, queueID string, score float64) error {
	m.members[queueID] = score
	return nil
}

func (m *mockQueueCache) Remove(_ context.Context, queueID string) error {
	delete(m.members, queueID)
	return nil
}

func (m *mockQueueCache) Position(_ context.Context, queueID string) (int64, error) {
	if _, ok := m.members[queueID]; !ok {
		return 0, nil
	}
	// Simple: count members with lower score
	target := m.members[queueID]
	pos := int64(1)
	for _, s := range m.members {
		if s < target {
			pos++
		}
	}
	return pos, nil
}

func (m *mockQueueCache) Length(_ context.Context) (int64, error) {
	return int64(len(m.members)), nil
}

// List mengurutkan berdasarkan score, sama seperti ZRANGE pada sorted set Redis.
//
// Score yang sama dipecah secara leksikografis menurut nama anggota, persis aturan Redis.
// Sebelumnya hanya score yang dibandingkan lewat sort.Slice — yang tidak stabil — di atas
// iterasi map yang urutannya acak. Jadi dua anggota berskor sama keluar dalam urutan yang
// berubah tiap kali dijalankan, dan test yang memeriksa urutan antrean lulus atau gagal
// secara acak. Mock antrean yang tidak deterministik lebih buruk daripada tidak ada.
func (m *mockQueueCache) List(_ context.Context) ([]string, error) {
	ids := make([]string, 0, len(m.members))
	for id := range m.members {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if m.members[ids[i]] != m.members[ids[j]] {
			return m.members[ids[i]] < m.members[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids, nil
}

func (m *mockQueueCache) IncrDailyCounter(_ context.Context) (int64, error) {
	m.counter++
	return m.counter, nil
}

// testAgent adalah petugas CS baku untuk test. Identitasnya sekarang datang dari
// kredensial yang diautentikasi middleware, jadi service menerimanya sebagai satu
// AgentInfo — bukan dua string dari body yang bisa dikarang pemanggil.
var testAgent = AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"}

// pickUpCall menjalankan pengambilan panggilan oleh petugas.
//
// Wajib mendahului setiap SubmitResult: hasil hanya sah untuk panggilan yang berstatus
// ACTIVE, dan satu-satunya yang memindahkannya ke sana adalah pengambilan ini. Dulu status
// apa pun diterima, jadi test bisa menyelesaikan panggilan yang belum pernah terjadi.
func pickUpCall(t *testing.T, svc *VideoCallService, queueID string, agent AgentInfo) {
	t.Helper()
	if _, err := svc.AgentSignalingURL(context.Background(), queueID, agent, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent pickup: %v", err)
	}
}

// testClockWIB mengembalikan clock yang mulai pukul 10:00 WIB — di dalam jam operasional —
// dan MAJU satu milidetik setiap kali dibaca.
//
// Maju, bukan beku, karena score sorted set antrean adalah `joined_at` dalam milidetik.
// Clock yang beku memberi setiap panggilan score yang sama persis, sesuatu yang tidak
// pernah terjadi di produksi tempat time.Now() selalu bergerak — dan dengan score yang
// sama urutan antrean ditentukan pemecah seri, bukan urutan kedatangan. Itu yang membuat
// test urutan antrean lulus-gagal secara acak.
//
// Satu milidetik per pembacaan cukup kecil untuk tidak pernah keluar dari jam operasional
// dalam satu test, dan cukup untuk membuat urutan kedatangan benar-benar terurut.
func testClockWIB() func() time.Time {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	start := time.Date(2026, 9, 19, 10, 0, 0, 0, loc)
	var reads int64
	return func() time.Time {
		reads++
		return start.Add(time.Duration(reads) * time.Millisecond)
	}
}

// mockNotifier mencatat pesan server→client per sesi.
//
// Keberadaan test ini yang penting: sebelumnya tidak ada satu pun pemanggil
// SendToNasabah di luar relai `instruction`, dan tidak ada test yang menangkapnya.
type mockNotifier struct {
	sent map[string][]SignalMessage

	// toAgent dipisah dari sent, bukan digabung dengan penanda sisi: `call_ended` yang
	// seharusnya sampai ke petugas tapi hanya sampai ke nasabah adalah persis bug yang
	// test ini ada untuk menangkapnya, dan satu slice gabungan menyembunyikannya.
	toAgent map[string][]SignalMessage
}

func newMockNotifier() *mockNotifier {
	return &mockNotifier{
		sent:    make(map[string][]SignalMessage),
		toAgent: make(map[string][]SignalMessage),
	}
}

func (m *mockNotifier) SendToNasabah(sessionID string, msg SignalMessage) {
	m.sent[sessionID] = append(m.sent[sessionID], msg)
}

func (m *mockNotifier) SendToAgent(sessionID string, msg SignalMessage) {
	m.toAgent[sessionID] = append(m.toAgent[sessionID], msg)
}

// first mengembalikan pesan pertama bertipe [msgType] untuk sesi itu, atau nil.
func (m *mockNotifier) first(sessionID, msgType string) *SignalMessage {
	return firstOfType(m.sent[sessionID], msgType)
}

// firstToAgent adalah pasangan [mockNotifier.first] untuk sisi petugas.
func (m *mockNotifier) firstToAgent(sessionID, msgType string) *SignalMessage {
	return firstOfType(m.toAgent[sessionID], msgType)
}

func firstOfType(msgs []SignalMessage, msgType string) *SignalMessage {
	for i := range msgs {
		if msgs[i].Type == msgType {
			return &msgs[i]
		}
	}
	return nil
}

func setupVCServiceWithNotifier() (*VideoCallService, *mockSessionRepo, *mockSessionCache, *mockVideoCallRepo, *mockQueueCache, *mockNotifier) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	vcRepo := newMockVideoCallRepo()
	queueCache := newMockQueueCache()
	notifier := newMockNotifier()

	svc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         sessionRepo,
		Cache:            cache,
		VideoCalls:       vcRepo,
		QueueCache:       queueCache,
		JWTManager:       testJWTManager(),
		Audit:            &mockAuditRepo{},
		SignalingBaseURL: "ws://test:8080",
		Clock:            testClockWIB(),
		Notifier:         notifier,
	})

	return svc, sessionRepo, cache, vcRepo, queueCache, notifier
}

func setupVCService() (*VideoCallService, *mockSessionRepo, *mockSessionCache, *mockVideoCallRepo, *mockQueueCache) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	vcRepo := newMockVideoCallRepo()
	queueCache := newMockQueueCache()
	audit := &mockAuditRepo{}

	svc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         sessionRepo,
		Cache:            cache,
		VideoCalls:       vcRepo,
		QueueCache:       queueCache,
		JWTManager:       testJWTManager(),
		Audit:            audit,
		SignalingBaseURL: "ws://test:8080",
		Clock:            testClockWIB(),
	})

	return svc, sessionRepo, cache, vcRepo, queueCache
}

func createVCTestSession(sessionRepo *mockSessionRepo, cache *mockSessionCache) string {
	return createVCTestSessionWithID(sessionRepo, cache, "onb_vc_test")
}

// createVCTestSessionWithID dibutuhkan test antrean: dua nasabah harus punya session_id
// berbeda, dan helper ber-ID tetap membuat JoinQueue kedua mengembalikan tiket yang sama.
func createVCTestSessionWithID(sessionRepo *mockSessionRepo, cache *mockSessionCache, id string) string {
	sessionID := id
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_vc",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepVideoCall,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
			BiometricVerified: true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session
	return sessionID
}

func TestJoinQueue_Success(t *testing.T) {
	svc, sessionRepo, cache, _, queueCache := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	resp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.QueueID == "" || resp.QueueID[:2] != "q_" {
		t.Errorf("queue_id should start with q_, got %q", resp.QueueID)
	}
	if resp.QueueNumber != "A-001" {
		t.Errorf("expected A-001, got %q", resp.QueueNumber)
	}
	if resp.Position != 1 {
		t.Errorf("expected position 1, got %d", resp.Position)
	}
	if resp.OperatingHours.Timezone != "Asia/Jakarta" {
		t.Errorf("expected Asia/Jakarta timezone, got %q", resp.OperatingHours.Timezone)
	}
	if resp.SignalingURL == "" {
		t.Error("expected signaling URL")
	}

	// Queue should have one member
	if len(queueCache.members) != 1 {
		t.Errorf("expected 1 queue member, got %d", len(queueCache.members))
	}
}

func TestJoinQueue_AlreadyQueued(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	// First join
	resp1, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("first join error: %v", err)
	}

	// Second join — should return same queue
	resp2, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("second join error: %v", err)
	}

	if resp2.QueueID != resp1.QueueID {
		t.Errorf("expected same queue_id on re-join, got %q vs %q", resp1.QueueID, resp2.QueueID)
	}
}

func TestJoinQueue_WrongStep(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	sessionRepo.sessions[sessionID].CurrentStep = StepOCR
	cache.data[sessionID].CurrentStep = StepOCR

	_, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for wrong step")
	}
}

func TestSubmitResult_Approved(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, queueCache := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	// Join queue first
	joinResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	pickUpCall(t, svc, joinResp.QueueID, testAgent)

	// Submit approved result
	resp, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:           sessionID,
		QueueID:             joinResp.QueueID,
		Result:              "APPROVED",
		KTPShownLive:        true,
		IdentityConfirmed:   true,
		Notes:               "Verified OK",
		CallDurationSeconds: 195,
		RecordingID:         "rec_xyz",
	}, testAgent, "127.0.0.1", "test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result != "APPROVED" {
		t.Errorf("expected APPROVED, got %s", resp.Result)
	}
	if resp.CurrentStep != StepCredentials {
		t.Errorf("expected CREDENTIALS step, got %s", resp.CurrentStep)
	}

	// Session should be at CREDENTIALS
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepCredentials {
		t.Errorf("session should be at CREDENTIALS, got %s", s.CurrentStep)
	}
	if !s.StepsCompleted.VideoCallVerified {
		t.Error("video_call_verified should be true")
	}

	// Queue should be empty
	if len(queueCache.members) != 0 {
		t.Errorf("expected empty queue, got %d", len(queueCache.members))
	}

	// Video call should be completed
	vc := vcRepo.byQueue[joinResp.QueueID]
	if vc.Status != VCStatusCompleted {
		t.Errorf("expected COMPLETED status, got %s", vc.Status)
	}
}

func TestSubmitResult_Rejected(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	pickUpCall(t, svc, joinResp.QueueID, testAgent)

	// RejectionReason wajib dan ber-enum sejak §38: penolakan tanpa alasan
	// terstruktur tidak bisa dilaporkan maupun dihitung.
	resp, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:       sessionID,
		QueueID:         joinResp.QueueID,
		Result:          "REJECTED",
		RejectionReason: string(RejectIdentityMismatch),
	}, testAgent, "127.0.0.1", "test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepVideoCall {
		t.Errorf("rejected should stay at VIDEO_CALL, got %s", resp.CurrentStep)
	}
}

func TestQueueNumberSequencing(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()

	// Create 3 sessions and join queue
	for i := 0; i < 3; i++ {
		sid := "onb_seq_" + string(rune('a'+i))
		s := &Session{
			SessionID: sid, DeviceID: "dev", CurrentStep: StepVideoCall,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
		sessionRepo.sessions[sid] = s
		cache.data[sid] = s

		resp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sid}, "127.0.0.1", "test")
		if err != nil {
			t.Fatalf("join %d error: %v", i, err)
		}
		expected := "A-" + []string{"001", "002", "003"}[i]
		if resp.QueueNumber != expected {
			t.Errorf("session %d: expected %s, got %s", i, expected, resp.QueueNumber)
		}
	}
}

// A replayed result must not re-run the step transition. Before the replay
// guard, a second APPROVED for the same queue dragged a session that had
// already reached REVIEW back to CREDENTIALS.
func TestSubmitResult_ReplayDoesNotRewindSession(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	pickUpCall(t, svc, joinResp.QueueID, testAgent)

	result := SubmitVideoCallResultRequest{
		SessionID: sessionID,
		QueueID:   joinResp.QueueID,
		Result:    "APPROVED",
	}
	if _, err := svc.SubmitResult(ctx, result, testAgent, "127.0.0.1", "test"); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// The nasabah moves on: credentials set, session now at REVIEW.
	session := sessionRepo.sessions[sessionID]
	session.CurrentStep = StepReview
	session.StepsCompleted.CredentialsSet = true
	cache.data[sessionID] = session

	resp, err := svc.SubmitResult(ctx, result, testAgent, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("replayed submit should be accepted as a no-op: %v", err)
	}
	if resp.CurrentStep != StepReview {
		t.Errorf("replay should report the current step REVIEW, got %s", resp.CurrentStep)
	}
	if sessionRepo.sessions[sessionID].CurrentStep != StepReview {
		t.Errorf("replay must not rewind the session, got %s", sessionRepo.sessions[sessionID].CurrentStep)
	}
}

// Even a first-time approval must respect the step machine: a session that has
// moved past VIDEO_CALL is recorded, not rewound.
func TestSubmitResult_ApprovalPastVideoCallDoesNotRewind(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	pickUpCall(t, svc, joinResp.QueueID, testAgent)

	session := sessionRepo.sessions[sessionID]
	session.CurrentStep = StepCompleted
	cache.data[sessionID] = session

	resp, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID: sessionID,
		QueueID:   joinResp.QueueID,
		Result:    "APPROVED",
	}, testAgent, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepCompleted {
		t.Errorf("expected COMPLETED to stand, got %s", resp.CurrentStep)
	}
	// The call record still captures the agent's verdict.
	if vcRepo.byQueue[joinResp.QueueID].Result != VCResultApproved {
		t.Error("the video call record should still store the approval")
	}
}

// The signaling URL must carry a signed nasabah role — the WebSocket endpoint
// no longer accepts a role from the query string.
func TestJoinQueue_SignalingTokenCarriesRole(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	resp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	parsed, err := url.Parse(resp.SignalingURL)
	if err != nil {
		t.Fatalf("signaling url is not a URL: %v", err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatal("signaling url carries no token")
	}

	claims, err := testJWTManager().VerifyToken(token, crypto.TokenTypeSignaling)
	if err != nil {
		t.Fatalf("signaling token does not verify: %v", err)
	}
	if claims.Role != RoleNasabah {
		t.Errorf("expected role %q, got %q", RoleNasabah, claims.Role)
	}
	if claims.Subject != sessionID {
		t.Errorf("expected subject %q, got %q", sessionID, claims.Subject)
	}
}

func TestAgentSignalingURL(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	agentResp, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}

	parsed, _ := url.Parse(agentResp.SignalingURL)
	claims, err := testJWTManager().VerifyToken(parsed.Query().Get("token"), crypto.TokenTypeSignaling)
	if err != nil {
		t.Fatalf("agent token does not verify: %v", err)
	}
	if claims.Role != RoleAgent {
		t.Errorf("expected role %q, got %q", RoleAgent, claims.Role)
	}

	// A finished call issues no more agent tokens.
	if _, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID: sessionID, QueueID: joinResp.QueueID, Result: "APPROVED",
	}, testAgent, "127.0.0.1", "test"); err != nil {
		t.Fatalf("submit result: %v", err)
	}
	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"}, "127.0.0.1", "test"); err == nil {
		t.Error("expected an error for a completed call")
	}
}

// --- Siklus panggilan: emisi server→client ---------------------------------
//
// Seluruh blok ini menutup kebuntuan protokol yang sebelumnya ada: nasabah menunggu
// `agent_assigned` untuk membuat SDP offer, dan tidak ada apa pun di backend yang
// mengirimnya. Tanpa test ini, lubang yang sama bisa kembali tanpa terlihat.

func TestAgentPickupNotifiesNasabah(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, queueCache, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}

	msg := notifier.first(sessionID, SignalAgentAssigned)
	if msg == nil {
		t.Fatal("agent_assigned tidak terkirim; tanpa itu nasabah tidak pernah membuat offer")
	}
	if msg.Agent == nil {
		t.Fatal("agent_assigned tanpa objek agent")
	}
	if msg.Agent.Name != "Sarah Adisti" || msg.Agent.EmployeeID != "CS-1042" {
		t.Fatalf("identitas agent salah: %+v", msg.Agent)
	}

	// Status dan nama tercatat; keduanya dulu tidak pernah terisi.
	vc := vcRepo.byQueue[joinResp.QueueID]
	if vc.Status != VCStatusActive {
		t.Fatalf("status = %s, mau ACTIVE", vc.Status)
	}
	if vc.AgentName != "Sarah Adisti" {
		t.Fatalf("agent_name = %q, mau tercatat", vc.AgentName)
	}
	if vc.StartedAt == nil {
		t.Fatal("started_at tidak diisi saat agent mengambil panggilan")
	}

	// Panggilan yang sudah dilayani harus keluar dari antrean, kalau tidak seluruh
	// antrean membeku sampai hasilnya disubmit.
	if _, ok := queueCache.members[joinResp.QueueID]; ok {
		t.Fatal("panggilan yang diambil agent masih di antrean")
	}
}

func TestSubmitResultNotifiesCallEnded(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}

	if _, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:           sessionID,
		QueueID:             joinResp.QueueID,
		Result:              string(VCResultApproved),
		KTPShownLive:        true,
		IdentityConfirmed:   true,
		CallDurationSeconds: 195,
	}, testAgent, "127.0.0.1", "test"); err != nil {
		t.Fatalf("submit result: %v", err)
	}

	msg := notifier.first(sessionID, SignalCallEnded)
	if msg == nil {
		t.Fatal("call_ended tidak terkirim; nasabah hanya melihat socket yang mendadak sunyi")
	}
	if msg.Result != string(VCResultApproved) {
		t.Fatalf("result = %q", msg.Result)
	}
	// Nama diambil dari rekaman, karena request §5c hanya membawa agent_employee_id.
	if msg.AgentName != "Sarah Adisti" {
		t.Fatalf("agent_name = %q, mau diambil dari rekaman panggilan", msg.AgentName)
	}
	if msg.DurationSeconds != 195 {
		t.Fatalf("duration_seconds = %d", msg.DurationSeconds)
	}
}

func TestCallEndedFallsBackToEmployeeID(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	// CS mengambil panggilan tanpa mengirim nama.
	agentNoName := AgentInfo{EmployeeID: "CS-9001"}
	pickUpCall(t, svc, joinResp.QueueID, agentNoName)

	if _, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID: sessionID,
		QueueID:   joinResp.QueueID,
		Result:    string(VCResultApproved),
	}, agentNoName, "127.0.0.1", "test"); err != nil {
		t.Fatalf("submit result: %v", err)
	}

	// ID pegawai lebih berguna daripada string hampa di tag petugas.
	if msg := notifier.first(sessionID, SignalCallEnded); msg == nil || msg.AgentName != "CS-9001" {
		t.Fatalf("agent_name tidak jatuh ke employee id: %+v", msg)
	}
}

func TestNasabahConnectedReceivesQueuePosition(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	svc.OnNasabahConnected(ctx, sessionID, joinResp.QueueID)

	msg := notifier.first(sessionID, SignalQueueUpdate)
	if msg == nil {
		t.Fatal("queue_update tidak terkirim saat socket nasabah tersambung")
	}
	if msg.Position != 1 {
		t.Fatalf("position = %d, mau 1", msg.Position)
	}
	if msg.EstimatedWaitSeconds != avgCallDurationSeconds {
		t.Fatalf("estimated_wait_seconds = %d", msg.EstimatedWaitSeconds)
	}
}

func TestQueuePositionsShiftWhenCallLeaves(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()

	first := createVCTestSessionWithID(sessionRepo, cache, "onb_vc_first")
	firstResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: first}, "127.0.0.1", "test")
	second := createVCTestSessionWithID(sessionRepo, cache, "onb_vc_second")
	secondResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: second}, "127.0.0.1", "test")
	_ = secondResp

	// Yang terdepan diambil agent; yang di belakangnya harus naik ke posisi 1.
	if _, err := svc.AgentSignalingURL(ctx, firstResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}

	msg := notifier.first(second, SignalQueueUpdate)
	if msg == nil {
		t.Fatal("yang mengantre di belakang tidak diberi tahu posisinya bergeser")
	}
	if msg.Position != 1 {
		t.Fatalf("position = %d, mau 1 setelah yang depan dilayani", msg.Position)
	}
}

func TestNotifierOptional(t *testing.T) {
	// Tanpa notifier, antrean tetap bekerja — service tidak boleh panik karena nil.
	svc, sessionRepo, cache, _, _ := setupVCService()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}
	svc.OnNasabahConnected(ctx, sessionID, joinResp.QueueID)
}

func TestReconnectMidCallReannouncesAgent(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}

	// Nasabah terputus lalu menyambung ulang dengan tiket baru. Socket barunya tidak pernah
	// menerima agent_assigned yang dikirim saat agent mengambil panggilan, dan client
	// menggantungkan pembuatan SDP offer pada pesan itu.
	notifier.sent = make(map[string][]SignalMessage)
	svc.OnNasabahConnected(ctx, sessionID, joinResp.QueueID)

	msg := notifier.first(sessionID, SignalAgentAssigned)
	if msg == nil {
		t.Fatal("agent_assigned tidak diumumkan ulang; penyambungan ulang berakhir diam")
	}
	if msg.Agent == nil || msg.Agent.Name != "Sarah Adisti" {
		t.Fatalf("identitas agent hilang saat diumumkan ulang: %+v", msg.Agent)
	}
	// Panggilan aktif sudah keluar dari antrean, jadi posisi antrean tidak berarti lagi.
	if notifier.first(sessionID, SignalQueueUpdate) != nil {
		t.Fatal("queue_update dikirim untuk panggilan yang sudah dilayani")
	}
}

// --- Pintu masuk sisi CS ----------------------------------------------------

func TestListQueuedGivesCSAWayIn(t *testing.T) {
	svc, sessionRepo, cache, _, _, _ := setupVCServiceWithNotifier()
	ctx := context.Background()

	first := createVCTestSessionWithID(sessionRepo, cache, "onb_q_first")
	firstResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: first}, "127.0.0.1", "test")
	second := createVCTestSessionWithID(sessionRepo, cache, "onb_q_second")
	secondResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: second}, "127.0.0.1", "test")

	listed, err := svc.ListQueued(ctx)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}

	if len(listed.Calls) != 2 {
		t.Fatalf("calls = %d, mau 2", len(listed.Calls))
	}
	// Urutan dan posisi harus sama dengan yang dilihat nasabah di layarnya.
	if listed.Calls[0].QueueID != firstResp.QueueID || listed.Calls[0].Position != 1 {
		t.Fatalf("panggilan terdepan salah: %+v", listed.Calls[0])
	}
	if listed.Calls[1].QueueID != secondResp.QueueID || listed.Calls[1].Position != 2 {
		t.Fatalf("panggilan kedua salah: %+v", listed.Calls[1])
	}
	// queue_id inilah yang dibutuhkan /video-call/agent-token; tanpa daftar ini CS tidak
	// punya cara mendapatkannya.
	if _, err := svc.AgentSignalingURL(ctx, listed.Calls[0].QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("queue_id dari daftar tidak bisa dipakai mengambil panggilan: %v", err)
	}
	if !listed.WithinOperatingHours {
		t.Fatal("jam operasional seharusnya terbuka pada clock test")
	}
}

func TestListQueuedDropsCallsAlreadyPickedUp(t *testing.T) {
	svc, sessionRepo, cache, _, _, _ := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah"}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("agent signaling url: %v", err)
	}

	listed, err := svc.ListQueued(ctx)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	// Dua petugas tidak boleh mengambil panggilan yang sama.
	if len(listed.Calls) != 0 {
		t.Fatalf("panggilan yang sudah dilayani masih terdaftar: %+v", listed.Calls)
	}
}

func TestListQueuedSkipsOrphanedQueueMembers(t *testing.T) {
	svc, sessionRepo, cache, _, queueCache, _ := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	// Sesi yang dibatalkan bisa meninggalkan anggota yatim di sorted set.
	if err := queueCache.Add(ctx, "q_yatim", 1); err != nil {
		t.Fatalf("add orphan: %v", err)
	}

	listed, err := svc.ListQueued(ctx)
	if err != nil {
		t.Fatalf("satu anggota basi menutup antrean untuk semua petugas: %v", err)
	}
	if len(listed.Calls) != 1 || listed.Calls[0].QueueID != joinResp.QueueID {
		t.Fatalf("daftar salah: %+v", listed.Calls)
	}
}

// --- Pembebasan panggilan basi & pemulihan antrean --------------------------

// setupVCServiceAudited sama dengan setupVCService tapi memberi akses ke jejak auditnya.
func setupVCServiceAudited() (*VideoCallService, *mockSessionRepo, *mockSessionCache, *mockVideoCallRepo, *mockQueueCache, *mockAuditRepo, *mockNotifier) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	vcRepo := newMockVideoCallRepo()
	queueCache := newMockQueueCache()
	audit := &mockAuditRepo{}
	notifier := newMockNotifier()

	svc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         sessionRepo,
		Cache:            cache,
		VideoCalls:       vcRepo,
		QueueCache:       queueCache,
		JWTManager:       testJWTManager(),
		Audit:            audit,
		SignalingBaseURL: "ws://test:8080",
		Clock:            testClockWIB(),
		Notifier:         notifier,
	})
	return svc, sessionRepo, cache, vcRepo, queueCache, audit, notifier
}

// hasAudit melaporkan apakah ada jejak audit bertipe itu untuk sesi tersebut.
func hasAudit(audit *mockAuditRepo, sessionID string, eventType AuditEventType) *AuditLog {
	for _, l := range audit.logs {
		if l.SessionID == sessionID && l.EventType == eventType {
			return l
		}
	}
	return nil
}

// Panggilan ACTIVE yang agennya hilang harus bisa dibebaskan, kalau tidak sesinya
// terkurung permanen: `idx_vc_session_active_unique` menolak baris antrean baru selama
// baris lamanya masih QUEUED atau ACTIVE.
func TestStaleActiveCallIsReleasedOnRejoin(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, queueCache, audit, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	first, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	pickUpCall(t, svc, first.QueueID, testAgent)

	// Agennya hilang: panggilan tetap ACTIVE dan tidak ada hasil yang masuk.
	stale := svc.clock().UTC().Add(-activeCallMaxAge - time.Minute)
	vcRepo.byQueue[first.QueueID].StartedAt = &stale

	second, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("rejoin after a stale call must succeed: %v", err)
	}
	if second.QueueID == first.QueueID {
		t.Fatal("nasabah harus mendapat tiket baru, bukan tiket yang panggilannya sudah mati")
	}
	if got := vcRepo.byQueue[first.QueueID].Status; got != VCStatusCancelled {
		t.Errorf("panggilan basi harus CANCELLED, dapat %s", got)
	}
	if _, ok := queueCache.members[first.QueueID]; ok {
		t.Error("panggilan basi masih jadi anggota antrean")
	}
	if _, ok := queueCache.members[second.QueueID]; !ok {
		t.Error("tiket baru tidak masuk antrean")
	}
	if l := hasAudit(audit, sessionID, AuditVideoCallEnded); l == nil {
		t.Error("pembebasan panggilan basi tidak meninggalkan jejak audit")
	} else if l.Details["reason"] != cancelReasonStale {
		t.Errorf("alasan audit = %v, mau %s", l.Details["reason"], cancelReasonStale)
	}
}

// Panggilan ACTIVE yang MASIH berjalan tidak boleh dibebaskan — nasabah yang menyambung
// ulang di tengah panggilan justru harus menemukan panggilannya utuh.
func TestLiveActiveCallIsNotReleasedOnRejoin(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, _, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	first, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	pickUpCall(t, svc, first.QueueID, testAgent)

	// Baru satu menit berjalan.
	fresh := svc.clock().UTC().Add(-time.Minute)
	vcRepo.byQueue[first.QueueID].StartedAt = &fresh

	second, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if second.QueueID != first.QueueID {
		t.Fatal("panggilan yang masih hidup harus dikembalikan apa adanya, bukan diganti tiket baru")
	}
	if got := vcRepo.byQueue[first.QueueID].Status; got != VCStatusActive {
		t.Errorf("status = %s, mau tetap ACTIVE", got)
	}
}

// Redis yang hilang tidak boleh membuat nasabah tak terlihat oleh petugas: tiketnya tampak
// sah di layarnya sendiri, tapi `GET /video-call/queued` membaca sorted set.
func TestQueuedMemberMissingFromSortedSetIsRestored(t *testing.T) {
	svc, sessionRepo, cache, _, queueCache, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}

	// Redis-nya ter-flush.
	delete(queueCache.members, joined.QueueID)

	again, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if again.QueueID != joined.QueueID {
		t.Fatal("tiketnya harus tetap sama; yang hilang hanya anggota sorted set")
	}
	if _, ok := queueCache.members[joined.QueueID]; !ok {
		t.Fatal("anggota antrean yang hilang tidak dimasukkan kembali")
	}
	if again.Position != 1 {
		t.Errorf("position = %d, mau 1 setelah dimasukkan kembali", again.Position)
	}

	listed, err := svc.ListQueued(ctx)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(listed.Calls) != 1 {
		t.Fatalf("petugas harus melihat %d panggilan, dapat %d", 1, len(listed.Calls))
	}
}

// --- Satu panggilan, satu petugas ------------------------------------------

// Dua petugas yang menekan tombol hampir bersamaan: yang kedua ditolak. Tanpa ini
// keduanya mendapat token, socket yang pertama ditutup Hub.Register, tapi identitas yang
// tercatat tetap milik yang pertama — jadi yang berbicara dengan nasabah dan yang tercatat
// di audit adalah dua orang berbeda.
func TestPickupRejectedWhenCallAlreadyTaken(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, _, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	pickUpCall(t, svc, joined.QueueID, testAgent)

	other := AgentInfo{EmployeeID: "CS-2099", Name: "Budi"}
	_, err := svc.AgentSignalingURL(ctx, joined.QueueID, other, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("petugas kedua harus ditolak")
	}
	if appErr := asAppErr(t, err); appErr.Code != "VIDEO_CALL_ALREADY_TAKEN" {
		t.Errorf("code = %q, mau VIDEO_CALL_ALREADY_TAKEN", appErr.Code)
	}
	if got := vcRepo.byQueue[joined.QueueID].AgentEmployeeID; got != testAgent.EmployeeID {
		t.Errorf("petugas tercatat = %q, mau tetap %q", got, testAgent.EmployeeID)
	}
}

// Petugas yang SAMA boleh mengambil ulang: itu jalur menyambung ulang yang sah, karena
// token signaling sekali pakai.
func TestSameAgentMayPickUpAgainToReconnect(t *testing.T) {
	svc, sessionRepo, cache, _, _, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	pickUpCall(t, svc, joined.QueueID, testAgent)

	if _, err := svc.AgentSignalingURL(ctx, joined.QueueID, testAgent, "127.0.0.1", "test"); err != nil {
		t.Fatalf("pengambilan ulang oleh petugas yang sama harus boleh: %v", err)
	}
}

// MarkActive yang gagal harus MENOLAK token. Dulu kegagalannya hanya dicatat dan token
// tetap keluar, jadi panggilan berjalan tanpa catatan siapa yang menanganinya.
func TestPickupRefusedWhenMarkActiveFails(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, _, _, notifier := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	vcRepo.markActiveErr = errors.New("postgres sedang tersendat")

	if _, err := svc.AgentSignalingURL(ctx, joined.QueueID, testAgent, "127.0.0.1", "test"); err == nil {
		t.Fatal("token tidak boleh diterbitkan kalau pengambilannya gagal dicatat")
	}
	// Nasabah tidak boleh diberi tahu panggilan dimulai padahal tidak.
	if msg := notifier.first(sessionID, SignalAgentAssigned); msg != nil {
		t.Error("agent_assigned terkirim untuk pengambilan yang gagal")
	}
}

// Pengambilan panggilan harus meninggalkan jejak VIDEO_CALL_STARTED. Konstantanya sudah
// ada sejak migrasi 000010 dan tidak pernah ditulis siapa pun, jadi jejak audit melompat
// dari VIDEO_CALL_QUEUED langsung ke VIDEO_CALL_ENDED — dan panggilan yang berakhir tanpa
// hasil tidak meninggalkan baris itu sama sekali.
func TestAgentPickupWritesStartedAudit(t *testing.T) {
	svc, sessionRepo, cache, _, _, audit, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	pickUpCall(t, svc, joined.QueueID, testAgent)

	l := hasAudit(audit, sessionID, AuditVideoCallStarted)
	if l == nil {
		t.Fatal("VIDEO_CALL_STARTED tidak pernah tercatat")
	}
	if l.Actor != "agent:"+testAgent.EmployeeID {
		t.Errorf("actor = %q, mau agent:%s", l.Actor, testAgent.EmployeeID)
	}
}

// --- Hasil terikat ke petugas yang menangani -------------------------------

// Hasil dari petugas lain ditolak, dan atribusi panggilan tidak berubah.
func TestResultFromDifferentAgentRejected(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, _, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	pickUpCall(t, svc, joined.QueueID, testAgent)

	impostor := AgentInfo{EmployeeID: "CS-6666", Name: "Bukan Petugasnya"}
	_, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID: sessionID, QueueID: joined.QueueID, Result: "APPROVED",
	}, impostor, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("hasil dari petugas lain harus ditolak")
	}
	if appErr := asAppErr(t, err); appErr.Code != "VIDEO_CALL_AGENT_MISMATCH" {
		t.Errorf("code = %q, mau VIDEO_CALL_AGENT_MISMATCH", appErr.Code)
	}

	vc := vcRepo.byQueue[joined.QueueID]
	if vc.Status == VCStatusCompleted {
		t.Error("panggilan tidak boleh selesai karena hasil yang ditolak")
	}
	if vc.AgentEmployeeID != testAgent.EmployeeID {
		t.Errorf("atribusi panggilan berubah menjadi %q", vc.AgentEmployeeID)
	}
	if sessionRepo.sessions[sessionID].CurrentStep != StepVideoCall {
		t.Error("sesi tidak boleh maju karena hasil yang ditolak")
	}
}

// Hasil untuk panggilan yang belum pernah diambil petugas ditolak: tanpa penjaga ini satu
// permintaan bisa memindahkan sesi ke CREDENTIALS tanpa verifikasi tatap muka sama sekali.
func TestResultBeforePickupRejected(t *testing.T) {
	svc, sessionRepo, cache, _, _, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")

	_, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID: sessionID, QueueID: joined.QueueID, Result: "APPROVED",
	}, testAgent, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("hasil untuk panggilan yang masih QUEUED harus ditolak")
	}
	if appErr := asAppErr(t, err); appErr.Code != "VIDEO_CALL_NOT_ACTIVE" {
		t.Errorf("code = %q, mau VIDEO_CALL_NOT_ACTIVE", appErr.Code)
	}
	if sessionRepo.sessions[sessionID].CurrentStep != StepVideoCall {
		t.Error("sesi maju tanpa verifikasi yang pernah terjadi")
	}
}

// --- Pembersihan antrean ---------------------------------------------------

// Anggota antrean yang sesinya sudah tidak ada dibatalkan, bukan hanya dilewati: baris
// QUEUED-nya menggantung selamanya dan `ZRANK` nasabah lain tetap menghitungnya.
func TestListQueuedCancelsMembersWhoseSessionIsGone(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, queueCache, _, _ := setupVCServiceAudited()
	ctx := context.Background()

	goneID := createVCTestSessionWithID(sessionRepo, cache, "onb_gone")
	liveID := createVCTestSessionWithID(sessionRepo, cache, "onb_live")

	goneJoin, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: goneID}, "127.0.0.1", "test")
	liveJoin, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: liveID}, "127.0.0.1", "test")

	// Sesi pertama dibatalkan di luar jalur video call — persis keadaan yang ditinggalkan
	// soft-delete sesi sebelum CancelForSession ada.
	if err := sessionRepo.SoftDelete(ctx, goneID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	delete(cache.data, goneID)

	listed, err := svc.ListQueued(ctx)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(listed.Calls) != 1 {
		t.Fatalf("mau 1 panggilan tersisa, dapat %d", len(listed.Calls))
	}
	if listed.Calls[0].QueueID != liveJoin.QueueID {
		t.Errorf("panggilan tersisa = %q, mau %q", listed.Calls[0].QueueID, liveJoin.QueueID)
	}
	// Posisi dihitung dari anggota yang dipertahankan: yang di belakang harus maju ke 1,
	// bukan tetap 2 karena anggota yang baru saja dibuang.
	if listed.Calls[0].Position != 1 {
		t.Errorf("position = %d, mau 1 setelah anggota di depannya dibuang", listed.Calls[0].Position)
	}
	if got := vcRepo.byQueue[goneJoin.QueueID].Status; got != VCStatusCancelled {
		t.Errorf("panggilan sesi yang hilang harus CANCELLED, dapat %s", got)
	}
	if _, ok := queueCache.members[goneJoin.QueueID]; ok {
		t.Error("panggilan sesi yang hilang masih jadi anggota antrean")
	}
}

// Anggota yatim — ada di sorted set tapi rekamannya tidak ada — dibuang, bukan dilewati:
// selama di sana ia menggeser posisi semua yang di belakangnya tanpa pernah bisa diambil.
func TestListQueuedDropsOrphanMembers(t *testing.T) {
	svc, sessionRepo, cache, _, queueCache, _, _ := setupVCServiceAudited()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	// Anggota yatim masuk lebih dulu supaya ia berada di depan.
	if err := queueCache.Add(ctx, "q_orphan", 1); err != nil {
		t.Fatalf("add orphan: %v", err)
	}
	joined, _ := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")

	listed, err := svc.ListQueued(ctx)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(listed.Calls) != 1 || listed.Calls[0].QueueID != joined.QueueID {
		t.Fatalf("daftar tidak sesuai: %+v", listed.Calls)
	}
	if listed.Calls[0].Position != 1 {
		t.Errorf("position = %d, mau 1 setelah anggota yatim dibuang", listed.Calls[0].Position)
	}
	if _, ok := queueCache.members["q_orphan"]; ok {
		t.Error("anggota yatim masih di sorted set")
	}
}

// `call_ended` harus sampai ke KEDUA sisi.
//
// Dulu hanya nasabah yang menerimanya, dan akibatnya baru terasa di aplikasi desktop:
// satu-satunya tanda panggilan berakhir adalah socket yang berhenti bicara — tidak
// terbedakan dari jaringan petugas sendiri yang putus.
func TestSubmitResultNotifiesBothSides(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	pickUpCall(t, svc, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"})

	if _, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:           sessionID,
		QueueID:             joinResp.QueueID,
		Result:              string(VCResultApproved),
		CallDurationSeconds: 195,
	}, testAgent, "127.0.0.1", "test"); err != nil {
		t.Fatalf("submit result: %v", err)
	}

	toAgent := notifier.firstToAgent(sessionID, SignalCallEnded)
	if toAgent == nil {
		t.Fatal("call_ended tidak sampai ke petugas; aplikasi desktop tidak punya penanda sah untuk membongkar PeerConnection")
	}
	if toAgent.Result != string(VCResultApproved) {
		t.Fatalf("result ke petugas = %q", toAgent.Result)
	}
	if toAgent.DurationSeconds != 195 {
		t.Fatalf("duration_seconds ke petugas = %d", toAgent.DurationSeconds)
	}

	// Sisi nasabah tidak boleh ikut hilang saat sisi petugas ditambahkan.
	if notifier.first(sessionID, SignalCallEnded) == nil {
		t.Fatal("call_ended ke nasabah hilang")
	}
}

// Nasabah yang membatalkan sesinya meninggalkan petugas di dalam panggilan yang sudah
// tidak punya lawan bicara. Hanya signaling yang bisa memberitahunya — tidak ada response
// HTTP yang sedang ditunggu aplikasi desktop di saat itu.
func TestCancelForSessionNotifiesAgent(t *testing.T) {
	svc, sessionRepo, cache, _, _, notifier := setupVCServiceWithNotifier()
	ctx := context.Background()
	sessionID := createVCTestSession(sessionRepo, cache)

	joinResp, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	pickUpCall(t, svc, joinResp.QueueID, AgentInfo{EmployeeID: "CS-1042", Name: "Sarah Adisti"})

	if err := svc.CancelForSession(ctx, sessionID); err != nil {
		t.Fatalf("cancel for session: %v", err)
	}

	msg := notifier.firstToAgent(sessionID, SignalCallEnded)
	if msg == nil {
		t.Fatal("petugas tidak diberi tahu panggilannya dibatalkan nasabah")
	}
	// `reason` membedakannya dari panggilan yang petugas selesaikan sendiri: yang satu
	// menuntut kembali ke antrean, yang satu lagi menuntut mengisi hasil verifikasi.
	if msg.Reason == "" {
		t.Fatal("call_ended ke petugas tanpa reason; tidak terbedakan dari hasil submit")
	}
	if msg.Result != "" {
		t.Fatalf("pembatalan tidak punya result, tapi terisi %q", msg.Result)
	}
}

// --- Penjadwalan ulang video call ---

// mockScheduleRepo meniru onboarding_video_call_schedules, TERMASUK unique index yang
// hanya mengizinkan satu jadwal aktif per sesi.
//
// Index-nya ditegakkan di sini juga: tanpa itu, service yang lupa mengandalkan database
// untuk menahan jadwal ganda akan lulus test dan baru gagal di produksi.
type mockScheduleRepo struct {
	byID  map[string]*VideoCallSchedule
	err   error
	calls int
}

func newMockScheduleRepo() *mockScheduleRepo {
	return &mockScheduleRepo{byID: make(map[string]*VideoCallSchedule)}
}

func (m *mockScheduleRepo) Create(_ context.Context, sch *VideoCallSchedule) error {
	m.calls++
	if m.err != nil {
		return m.err
	}
	for _, existing := range m.byID {
		if existing.SessionID == sch.SessionID && existing.Status == VCScheduleScheduled {
			return apperr.VideoCallAlreadyScheduled
		}
	}
	copied := *sch
	m.byID[sch.ScheduleID] = &copied
	return nil
}

func (m *mockScheduleRepo) FindActiveBySessionID(_ context.Context, sessionID string) (*VideoCallSchedule, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, s := range m.byID {
		if s.SessionID == sessionID && s.Status == VCScheduleScheduled {
			copied := *s
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *mockScheduleRepo) CancelBySessionID(_ context.Context, sessionID string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	for _, s := range m.byID {
		if s.SessionID == sessionID && s.Status == VCScheduleScheduled {
			s.Status = VCScheduleCancelled
			return true, nil
		}
	}
	return false, nil
}

// setupScheduleService memaku jam ke Senin 2026-10-05 10:00 WIB — di dalam jam
// operasional, supaya test tidak lulus atau gagal tergantung kapan ia dijalankan.
func setupScheduleService() (*VideoCallService, *mockSessionRepo, *mockSessionCache, *mockScheduleRepo, time.Time) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, loc)

	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	schedules := newMockScheduleRepo()

	svc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         sessionRepo,
		Cache:            cache,
		VideoCalls:       newMockVideoCallRepo(),
		QueueCache:       newMockQueueCache(),
		JWTManager:       testJWTManager(),
		Audit:            &mockAuditRepo{},
		Schedules:        schedules,
		SignalingBaseURL: "wss://test",
		Clock:            func() time.Time { return now },
	})
	return svc, sessionRepo, cache, schedules, now
}

// scheduleAt membentuk waktu WIB pada hari yang sama dengan jam yang dipaku.
func scheduleAt(now time.Time, hour int) string {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	wib := now.In(loc)
	return time.Date(wib.Year(), wib.Month(), wib.Day(), hour, 0, 0, 0, loc).Format(time.RFC3339)
}

func TestScheduleVideoCall_Success(t *testing.T) {
	svc, sessionRepo, cache, _, now := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	resp, err := svc.ScheduleVideoCall(context.Background(), ScheduleVideoCallRequest{
		SessionID:   sessionID,
		ScheduledAt: scheduleAt(now, 14),
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if resp.ScheduleID == "" {
		t.Fatal("schedule_id kosong")
	}
	if resp.Status != string(VCScheduleScheduled) {
		t.Fatalf("status = %q", resp.Status)
	}
	// Jam operasional disertakan supaya client tidak menanamkan jamnya sendiri.
	if resp.OperatingHours.Start != "06:00" || resp.OperatingHours.End != "22:00" {
		t.Fatalf("operating_hours = %+v", resp.OperatingHours)
	}
}

// Jam divalidasi dalam zona Asia/Jakarta, bukan UTC. 03:00 WIB adalah di luar jam
// layanan meski 20:00 UTC hari sebelumnya terdengar wajar.
func TestScheduleVideoCall_RejectsOutsideOperatingHoursWIB(t *testing.T) {
	svc, sessionRepo, cache, _, now := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	for _, hour := range []int{3, 5, 22, 23} {
		_, err := svc.ScheduleVideoCall(context.Background(), ScheduleVideoCallRequest{
			SessionID:   sessionID,
			ScheduledAt: scheduleAt(now, hour),
		}, "", "")
		if err == nil {
			t.Errorf("pukul %02d:00 WIB diterima; di luar 06:00-22:00", hour)
			continue
		}
		if apperr.From(err).Code != "VIDEO_CALL_SCHEDULE_INVALID" {
			t.Errorf("pukul %02d:00 → code %q", hour, apperr.From(err).Code)
		}
	}
}

func TestScheduleVideoCall_RejectsTooSoonAndTooFar(t *testing.T) {
	svc, sessionRepo, cache, _, now := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	cases := map[string]time.Time{
		"di masa lalu":      now.Add(-time.Hour),
		"lima menit lagi":   now.Add(5 * time.Minute),
		"delapan hari lagi": now.Add(8 * 24 * time.Hour),
	}
	for name, at := range cases {
		_, err := svc.ScheduleVideoCall(context.Background(), ScheduleVideoCallRequest{
			SessionID:   sessionID,
			ScheduledAt: at.Format(time.RFC3339),
		}, "", "")
		if err == nil {
			t.Errorf("%s diterima", name)
		}
	}
}

// Waktu tanpa offset zona bisa berarti dua jam berbeda, dan yang salah tafsir adalah
// janji dengan nasabah.
func TestScheduleVideoCall_RejectsTimeWithoutOffset(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	for _, raw := range []string{"2026-10-06 14:00:00", "2026-10-06T14:00:00", "besok sore"} {
		_, err := svc.ScheduleVideoCall(context.Background(), ScheduleVideoCallRequest{
			SessionID:   sessionID,
			ScheduledAt: raw,
		}, "", "")
		if err == nil {
			t.Errorf("%q diterima tanpa offset zona", raw)
		}
	}
}

// Satu jadwal aktif per sesi. Yang menahannya unique index, bukan pemeriksaan aplikasi.
func TestScheduleVideoCall_RejectsSecondActiveSchedule(t *testing.T) {
	svc, sessionRepo, cache, _, now := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	req := ScheduleVideoCallRequest{SessionID: sessionID, ScheduledAt: scheduleAt(now, 14)}
	if _, err := svc.ScheduleVideoCall(context.Background(), req, "", ""); err != nil {
		t.Fatalf("jadwal pertama: %v", err)
	}

	req.ScheduledAt = scheduleAt(now, 16)
	_, err := svc.ScheduleVideoCall(context.Background(), req, "", "")
	if err == nil {
		t.Fatal("jadwal kedua diterima; nasabah tidak akan tahu mana yang berlaku")
	}
	if apperr.From(err).Code != "VIDEO_CALL_ALREADY_SCHEDULED" {
		t.Fatalf("code = %q", apperr.From(err).Code)
	}
}

// Membatalkan lalu menjadwalkan ulang HARUS bisa — itu sebabnya index-nya partial.
func TestScheduleVideoCall_CanRescheduleAfterCancel(t *testing.T) {
	svc, sessionRepo, cache, _, now := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	req := ScheduleVideoCallRequest{SessionID: sessionID, ScheduledAt: scheduleAt(now, 14)}
	if _, err := svc.ScheduleVideoCall(context.Background(), req, "", ""); err != nil {
		t.Fatalf("jadwal pertama: %v", err)
	}
	if err := svc.CancelVideoCallSchedule(context.Background(), sessionID, "", ""); err != nil {
		t.Fatalf("batal: %v", err)
	}

	req.ScheduledAt = scheduleAt(now, 16)
	if _, err := svc.ScheduleVideoCall(context.Background(), req, "", ""); err != nil {
		t.Fatalf("jadwal ulang ditolak: %v", err)
	}
}

func TestCancelVideoCallSchedule_WithoutScheduleIs404(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	err := svc.CancelVideoCallSchedule(context.Background(), sessionID, "", "")
	if apperr.From(err).Code != "VIDEO_CALL_SCHEDULE_NOT_FOUND" {
		t.Fatalf("err = %v", err)
	}
}

// Jadwal hanya masuk akal untuk sesi yang memang sedang di langkah VIDEO_CALL.
func TestScheduleVideoCall_RejectsWrongStep(t *testing.T) {
	svc, sessionRepo, cache, _, now := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	sessionRepo.sessions[sessionID].CurrentStep = StepBiometric
	cache.data[sessionID].CurrentStep = StepBiometric

	_, err := svc.ScheduleVideoCall(context.Background(), ScheduleVideoCallRequest{
		SessionID:   sessionID,
		ScheduledAt: scheduleAt(now, 14),
	}, "", "")
	if err == nil {
		t.Fatal("sesi di BIOMETRIC bisa menjadwalkan video call")
	}
}

// Tanpa repo jadwal, endpoint ini 503 — dan SISA jalur video call tidak terpengaruh.
func TestScheduleVideoCall_WithoutRepoIsUnavailable(t *testing.T) {
	svc, sessionRepo, cache, _, _, _ := setupVCServiceWithNotifier()
	sessionID := createVCTestSession(sessionRepo, cache)

	_, err := svc.ScheduleVideoCall(context.Background(), ScheduleVideoCallRequest{
		SessionID:   sessionID,
		ScheduledAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	}, "", "")
	if apperr.From(err).Status != 503 {
		t.Fatalf("err = %v, mau 503", err)
	}

	// Mengantre tetap jalan.
	if _, err := svc.JoinQueue(context.Background(), JoinQueueRequest{SessionID: sessionID}, "", ""); err != nil {
		t.Fatalf("antrean ikut mati: %v", err)
	}
}

func TestGetVideoCallSchedule_ReturnsNilWhenNone(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupScheduleService()
	sessionID := createVCTestSession(sessionRepo, cache)

	got, err := svc.GetVideoCallSchedule(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, mau nil", got)
	}
}

// --- NEED_REVIEW, alasan penolakan, dan eskalasi (§38) ---

// mockEscalationRepo meniru `idx_vc_escalations_one_open` di migrasi 000038:
// satu sesi hanya boleh punya satu eskalasi terbuka sekaligus.
type mockEscalationRepo struct {
	byID      map[string]*VideoCallEscalation
	createErr error
	findErr   error
}

func newMockEscalationRepo() *mockEscalationRepo {
	return &mockEscalationRepo{byID: make(map[string]*VideoCallEscalation)}
}

func (m *mockEscalationRepo) Create(_ context.Context, esc *VideoCallEscalation) error {
	if m.createErr != nil {
		return m.createErr
	}
	for _, e := range m.byID {
		if e.SessionID == esc.SessionID && e.ResolvedAt == nil {
			return apperr.VideoCallEscalationExists
		}
	}
	copied := *esc
	m.byID[esc.EscalationID] = &copied
	return nil
}

func (m *mockEscalationRepo) FindOpenBySessionID(_ context.Context, sessionID string) (*VideoCallEscalation, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	for _, e := range m.byID {
		if e.SessionID == sessionID && e.ResolvedAt == nil {
			return e, nil
		}
	}
	return nil, nil
}

// setupVCServiceEscalated memasang repo eskalasi. setupVCService sengaja TIDAK
// memasangnya — jalur "NEED_REVIEW tanpa tempat menyimpan eskalasi" juga perlu diuji.
func setupVCServiceEscalated() (*VideoCallService, *mockSessionRepo, *mockSessionCache, *mockVideoCallRepo, *mockEscalationRepo, *mockAuditRepo) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	vcRepo := newMockVideoCallRepo()
	queueCache := newMockQueueCache()
	escRepo := newMockEscalationRepo()
	audit := &mockAuditRepo{}

	svc := NewVideoCallService(VideoCallServiceConfig{
		Sessions:         sessionRepo,
		Cache:            cache,
		VideoCalls:       vcRepo,
		QueueCache:       queueCache,
		Escalations:      escRepo,
		JWTManager:       testJWTManager(),
		Audit:            audit,
		SignalingBaseURL: "ws://test:8080",
		Clock:            testClockWIB(),
	})

	return svc, sessionRepo, cache, vcRepo, escRepo, audit
}

// submitForReview menjalankan satu panggilan penuh sampai SubmitResult.
func submitForReview(t *testing.T, svc *VideoCallService, sessionRepo *mockSessionRepo, cache *mockSessionCache, req SubmitVideoCallResultRequest) (*SubmitVideoCallResultResponse, error) {
	t.Helper()
	sessionID := createVCTestSession(sessionRepo, cache)
	joinResp, err := svc.JoinQueue(context.Background(), JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	pickUpCall(t, svc, joinResp.QueueID, testAgent)

	req.SessionID = sessionID
	req.QueueID = joinResp.QueueID
	return svc.SubmitResult(context.Background(), req, testAgent, "127.0.0.1", "test")
}

// Penolakan tanpa alasan ditolak: alasan penolakan dilaporkan dan dihitung, jadi
// REJECTED yang lolos tanpa alasan membuat barisnya tak terpakai untuk hal itu.
func TestSubmitResult_RejectedWithoutReasonRefused(t *testing.T) {
	svc, sessionRepo, cache, vcRepo, _, _ := setupVCServiceEscalated()

	_, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{Result: "REJECTED"})
	if !errors.Is(err, apperr.ValidationError) {
		t.Fatalf("expected VALIDATION_ERROR, got %v", err)
	}
	// Panggilannya tidak boleh ikut tercatat selesai.
	for _, vc := range vcRepo.byQueue {
		if vc.Status == VCStatusCompleted {
			t.Error("panggilan tidak boleh COMPLETED saat permintaannya ditolak")
		}
	}
}

// Alasan di luar enum ditolak: teks bebas membuat tiga ejaan dari satu hal menjadi
// tiga kategori, dan itu alasan enum-nya ada.
func TestSubmitResult_RejectedWithFreeTextReasonRefused(t *testing.T) {
	svc, sessionRepo, cache, _, _, _ := setupVCServiceEscalated()

	_, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result:          "REJECTED",
		RejectionReason: "ktp blur",
	})
	if !errors.Is(err, apperr.ValidationError) {
		t.Fatalf("expected VALIDATION_ERROR, got %v", err)
	}
}

// Alasan penolakan masuk jejak audit, bukan hanya kolom panggilan: pemeriksaan pola
// penolakan per petugas dibaca dari jejak.
func TestSubmitResult_RejectionReasonInAudit(t *testing.T) {
	svc, sessionRepo, cache, _, _, audit := setupVCServiceEscalated()

	resp, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result:          "REJECTED",
		RejectionReason: string(RejectFaceMismatch),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepVideoCall {
		t.Errorf("rejected harus tetap di VIDEO_CALL, got %s", resp.CurrentStep)
	}
	if resp.Escalation != nil {
		t.Error("REJECTED tidak boleh membuat eskalasi")
	}

	log := hasAudit(audit, resp.SessionID, AuditVideoCallEnded)
	if log == nil {
		t.Fatal("jejak audit VIDEO_CALL_ENDED tidak ada")
	}
	if got := log.Details["rejection_reason"]; got != string(RejectFaceMismatch) {
		t.Errorf("rejection_reason di audit = %v, mau %s", got, RejectFaceMismatch)
	}
}

// NEED_REVIEW membuat eskalasi, TIDAK memindahkan step, dan antrean kosong berarti
// Tier 2 — default yang disebut dokumen alur, bukan tebakan pemanggil.
func TestSubmitResult_NeedReviewCreatesEscalationWithTier2Default(t *testing.T) {
	svc, sessionRepo, cache, _, escRepo, audit := setupVCServiceEscalated()

	resp, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result: "NEED_REVIEW",
		Notes:  "Wajah mirip tapi tanda tangan berbeda, perlu Tier 2.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepVideoCall {
		t.Errorf("NEED_REVIEW harus tetap di VIDEO_CALL, got %s", resp.CurrentStep)
	}
	if resp.Escalation == nil {
		t.Fatal("response harus membawa eskalasinya: petugas perlu nomor perkaranya")
	}
	if resp.Escalation.EscalationQueue != EscalationTier2 {
		t.Errorf("antrean eskalasi = %s, mau %s", resp.Escalation.EscalationQueue, EscalationTier2)
	}
	if resp.Escalation.Status != "PENDING" {
		t.Errorf("status eskalasi = %s, mau PENDING", resp.Escalation.Status)
	}
	if resp.Escalation.RaisedByAgent != testAgent.EmployeeID {
		t.Errorf("raised_by_agent = %s, mau %s", resp.Escalation.RaisedByAgent, testAgent.EmployeeID)
	}
	if len(escRepo.byID) != 1 {
		t.Fatalf("mau 1 baris eskalasi, ada %d", len(escRepo.byID))
	}

	// Sesi tetap di VIDEO_CALL di penyimpanan, bukan hanya di response.
	if s := sessionRepo.sessions[resp.SessionID]; s.CurrentStep != StepVideoCall {
		t.Errorf("sesi tersimpan di %s, mau VIDEO_CALL", s.CurrentStep)
	}

	log := hasAudit(audit, resp.SessionID, AuditVideoCallEnded)
	if log == nil {
		t.Fatal("jejak audit VIDEO_CALL_ENDED tidak ada")
	}
	if log.Details["escalation_id"] != resp.Escalation.EscalationID {
		t.Errorf("escalation_id di audit = %v, mau %s", log.Details["escalation_id"], resp.Escalation.EscalationID)
	}
	if log.Details["escalation_queue"] != EscalationTier2 {
		t.Errorf("escalation_queue di audit = %v, mau %s", log.Details["escalation_queue"], EscalationTier2)
	}
}

// Antrean yang disebut pemanggil dipakai apa adanya kalau sah.
func TestSubmitResult_NeedReviewHonoursExplicitQueue(t *testing.T) {
	svc, sessionRepo, cache, _, _, _ := setupVCServiceEscalated()

	resp, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result:          "NEED_REVIEW",
		Notes:           "Pola pendaftaran mencurigakan.",
		EscalationQueue: EscalationFraud,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Escalation.EscalationQueue != EscalationFraud {
		t.Errorf("antrean eskalasi = %s, mau %s", resp.Escalation.EscalationQueue, EscalationFraud)
	}
}

// Eskalasi WAJIB beralasan: perkara tanpa alasan tidak bisa ditangani Tier 2, dan
// spasi saja bukan alasan.
func TestSubmitResult_NeedReviewRequiresNotes(t *testing.T) {
	svc, sessionRepo, cache, _, escRepo, _ := setupVCServiceEscalated()

	_, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result: "NEED_REVIEW",
		Notes:  "   ",
	})
	if !errors.Is(err, apperr.ValidationError) {
		t.Fatalf("expected VALIDATION_ERROR, got %v", err)
	}
	if len(escRepo.byID) != 0 {
		t.Error("tidak boleh ada eskalasi tercatat saat permintaannya ditolak")
	}
}

func TestSubmitResult_NeedReviewRejectsUnknownQueue(t *testing.T) {
	svc, sessionRepo, cache, _, _, _ := setupVCServiceEscalated()

	_, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result:          "NEED_REVIEW",
		Notes:           "Perlu ditinjau.",
		EscalationQueue: "TIER_99",
	})
	if !errors.Is(err, apperr.ValidationError) {
		t.Fatalf("expected VALIDATION_ERROR, got %v", err)
	}
}

// Tanpa repo eskalasi, NEED_REVIEW DITOLAK — bukan disimpan hasilnya tanpa eskalasi.
// Sesi NEED_REVIEW tanpa baris eskalasi akan menggantung tanpa jalan keluar: penjaga
// di JoinQueue menahannya dari antrean justru berdasarkan baris itu.
func TestSubmitResult_NeedReviewRefusedWithoutEscalationRepo(t *testing.T) {
	svc, sessionRepo, cache, _, _ := setupVCService()

	_, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result: "NEED_REVIEW",
		Notes:  "Perlu ditinjau Tier 2.",
	})
	if !errors.Is(err, apperr.ProviderNotConfigured) {
		t.Fatalf("expected PROVIDER_NOT_CONFIGURED, got %v", err)
	}
}

// Eskalasi terbuka menahan nasabah dari antrean. Tanpa ini ia akan dilayani Tier 1
// lagi dan mendapat keputusan yang sama — eskalasinya jadi hiasan.
func TestJoinQueue_BlockedByOpenEscalation(t *testing.T) {
	svc, sessionRepo, cache, _, escRepo, _ := setupVCServiceEscalated()
	ctx := context.Background()

	resp, err := submitForReview(t, svc, sessionRepo, cache, SubmitVideoCallResultRequest{
		Result: "NEED_REVIEW",
		Notes:  "Perlu ditinjau Tier 2.",
	})
	if err != nil {
		t.Fatalf("submit need review: %v", err)
	}

	_, err = svc.JoinQueue(ctx, JoinQueueRequest{SessionID: resp.SessionID}, "127.0.0.1", "test")
	if !errors.Is(err, apperr.VideoCallUnderReview) {
		t.Fatalf("expected VIDEO_CALL_UNDER_REVIEW, got %v", err)
	}

	// Setelah eskalasinya selesai, nasabah boleh mengantre lagi.
	esc := escRepo.byID[resp.Escalation.EscalationID]
	resolved := time.Now()
	esc.ResolvedAt = &resolved
	esc.Status = "RESOLVED"

	if _, err := svc.JoinQueue(ctx, JoinQueueRequest{SessionID: resp.SessionID}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("antrean setelah eskalasi selesai: %v", err)
	}
}
