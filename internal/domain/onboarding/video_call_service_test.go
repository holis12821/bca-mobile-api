package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/url"
	"sync"
	"testing"
	"time"

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

func (m *mockVideoCallRepo) UpdateResult(_ context.Context, queueID string, result VideoCallResult, agentEmployeeID, agentName, notes, recordingID string, ktpShown, identityConfirmed bool, durationSeconds int) error {
	vc := m.byQueue[queueID]
	if vc == nil {
		return nil
	}
	vc.Status = VCStatusCompleted
	vc.Result = result
	vc.AgentEmployeeID = agentEmployeeID
	vc.AgentName = agentName
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

func (m *mockQueueCache) IncrDailyCounter(_ context.Context) (int64, error) {
	m.counter++
	return m.counter, nil
}

// testClockWIB returns a clock function that always returns 10:00 WIB (within operating hours).
func testClockWIB() func() time.Time {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	fixed := time.Date(2026, 9, 19, 10, 0, 0, 0, loc)
	return func() time.Time { return fixed }
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
	sessionID := "onb_vc_test"
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

	// Submit approved result
	resp, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:           sessionID,
		QueueID:             joinResp.QueueID,
		AgentEmployeeID:     "CS-1042",
		Result:              "APPROVED",
		KTPShownLive:        true,
		IdentityConfirmed:   true,
		Notes:               "Verified OK",
		CallDurationSeconds: 195,
		RecordingID:         "rec_xyz",
	}, "127.0.0.1", "test")

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

	resp, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:       sessionID,
		QueueID:         joinResp.QueueID,
		AgentEmployeeID: "CS-1042",
		Result:          "REJECTED",
	}, "127.0.0.1", "test")

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

	result := SubmitVideoCallResultRequest{
		SessionID:       sessionID,
		QueueID:         joinResp.QueueID,
		AgentEmployeeID: "CS-1042",
		Result:          "APPROVED",
	}
	if _, err := svc.SubmitResult(ctx, result, "127.0.0.1", "test"); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// The nasabah moves on: credentials set, session now at REVIEW.
	session := sessionRepo.sessions[sessionID]
	session.CurrentStep = StepReview
	session.StepsCompleted.CredentialsSet = true
	cache.data[sessionID] = session

	resp, err := svc.SubmitResult(ctx, result, "127.0.0.1", "test")
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

	session := sessionRepo.sessions[sessionID]
	session.CurrentStep = StepCompleted
	cache.data[sessionID] = session

	resp, err := svc.SubmitResult(ctx, SubmitVideoCallResultRequest{
		SessionID:       sessionID,
		QueueID:         joinResp.QueueID,
		AgentEmployeeID: "CS-1042",
		Result:          "APPROVED",
	}, "127.0.0.1", "test")
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

	agentResp, err := svc.AgentSignalingURL(ctx, joinResp.QueueID)
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
		SessionID: sessionID, QueueID: joinResp.QueueID,
		AgentEmployeeID: "CS-1", Result: "APPROVED",
	}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("submit result: %v", err)
	}
	if _, err := svc.AgentSignalingURL(ctx, joinResp.QueueID); err == nil {
		t.Error("expected an error for a completed call")
	}
}
