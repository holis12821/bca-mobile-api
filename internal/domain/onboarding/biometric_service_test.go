package onboarding

import (
	"context"
	"testing"
	"time"
)

// --- in-memory mocks for biometric service ---

type mockBiometricRepo struct {
	data map[string]*BiometricResult
}

func newMockBiometricRepo() *mockBiometricRepo {
	return &mockBiometricRepo{data: make(map[string]*BiometricResult)}
}

func (m *mockBiometricRepo) Create(_ context.Context, r *BiometricResult) error {
	m.data[r.SessionID] = r
	return nil
}

func (m *mockBiometricRepo) FindBySessionID(_ context.Context, sessionID string) (*BiometricResult, error) {
	return m.data[sessionID], nil
}

type mockBiometricEngine struct {
	result *FaceAnalysisResult
}

func (m *mockBiometricEngine) Analyze(_ context.Context, _ []byte, _ [][]byte, _ []byte) (*FaceAnalysisResult, error) {
	return m.result, nil
}

type mockBioRateLimiter struct {
	allowed bool
}

func (m *mockBioRateLimiter) CheckBiometricAttempt(_ context.Context, _ string) (bool, error) {
	return m.allowed, nil
}

func setupBioService(engine *mockBiometricEngine) (*BiometricService, *mockSessionRepo, *mockSessionCache) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	ocrRepo := newMockOCRResultRepo()
	bioRepo := newMockBiometricRepo()
	audit := &mockAuditRepo{}

	// Set up OCR result for face matching reference
	ocrRepo.data["onb_bio_test"] = &OCRResult{
		OCRID:     "ocr_test",
		SessionID: "onb_bio_test",
		Extracted: KTPData{NIK: "3174082104950001", NamaLengkap: "TEST USER"},
	}

	svc := NewBiometricService(BiometricServiceConfig{
		Sessions:    sessionRepo,
		Cache:       cache,
		OCRResults:  ocrRepo,
		Biometrics:  bioRepo,
		Engine:      engine,
		Storage:     NewMockObjectStorage(),
		RateLimiter: &mockBioRateLimiter{allowed: true},
		AES:         nil,
		Audit:       audit,
	})

	return svc, sessionRepo, cache
}

func createBioTestSession(sessionRepo *mockSessionRepo, cache *mockSessionCache) string {
	sessionID := "onb_bio_test"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_bio",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepBiometric,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session
	return sessionID
}

func dummyFrames(count int) [][]byte {
	frames := make([][]byte, count)
	for i := range frames {
		frames[i] = []byte("frame-data")
	}
	return frames
}

func TestProcessBiometric_Success(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{
			FaceCount:      1,
			LivenessScore:  98.2,
			FaceMatchScore: 96.7,
			ISOCompliant:   true,
			SpoofDetected:  false,
			Quality:        "HIGH",
		},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	resp, err := svc.ProcessBiometric(ctx, sessionID, []byte("face-photo"), dummyFrames(3), LivenessMeta{
		ChallengeType:    "BLINK",
		CompletedActions: 3,
		PrecisionScore:   98.2,
	}, "127.0.0.1", "test-agent")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.BiometricID == "" || resp.BiometricID[:4] != "bio_" {
		t.Errorf("biometric_id should start with bio_, got %q", resp.BiometricID)
	}
	if !resp.LivenessVerified {
		t.Error("expected liveness_verified = true")
	}
	if !resp.FaceMatchWithKTP {
		t.Error("expected face_match_with_ktp = true")
	}
	if resp.CurrentStep != StepVideoCall {
		t.Errorf("expected VIDEO_CALL step, got %s", resp.CurrentStep)
	}

	// Session should be at VIDEO_CALL
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepVideoCall {
		t.Errorf("session should be at VIDEO_CALL, got %s", s.CurrentStep)
	}
	if !s.StepsCompleted.BiometricVerified {
		t.Error("biometric_verified should be true")
	}
}

func TestProcessBiometric_MultipleFaces(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{
			FaceCount: 2,
			Quality:   "HIGH",
		},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	_, err := svc.ProcessBiometric(ctx, sessionID, []byte("face"), dummyFrames(3), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for multiple faces")
	}
}

func TestProcessBiometric_LivenessFailed(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{
			FaceCount:      1,
			LivenessScore:  60.0, // below 90 threshold
			FaceMatchScore: 96.7,
			Quality:        "HIGH",
		},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	_, err := svc.ProcessBiometric(ctx, sessionID, []byte("face"), dummyFrames(3), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for liveness failure")
	}
}

func TestProcessBiometric_FaceNotMatch(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{
			FaceCount:      1,
			LivenessScore:  95.0,
			FaceMatchScore: 50.0, // below 85 threshold
			Quality:        "HIGH",
		},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	_, err := svc.ProcessBiometric(ctx, sessionID, []byte("face"), dummyFrames(3), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for face mismatch")
	}
}

func TestProcessBiometric_SpoofDetected(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{
			FaceCount:     1,
			SpoofDetected: true,
			Quality:       "HIGH",
		},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	_, err := svc.ProcessBiometric(ctx, sessionID, []byte("face"), dummyFrames(3), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for spoof detection")
	}
}

func TestProcessBiometric_TooFewFrames(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{FaceCount: 1, Quality: "HIGH"},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	_, err := svc.ProcessBiometric(ctx, sessionID, []byte("face"), dummyFrames(1), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for too few liveness frames")
	}
}

func TestProcessBiometric_WrongStep(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{FaceCount: 1, Quality: "HIGH"},
	}

	svc, sessionRepo, cache := setupBioService(engine)
	ctx := context.Background()
	sessionID := createBioTestSession(sessionRepo, cache)

	// Set session to wrong step
	sessionRepo.sessions[sessionID].CurrentStep = StepOCR
	cache.data[sessionID].CurrentStep = StepOCR

	_, err := svc.ProcessBiometric(ctx, sessionID, []byte("face"), dummyFrames(3), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for wrong step")
	}
}

func TestProcessBiometric_RateLimited(t *testing.T) {
	engine := &mockBiometricEngine{
		result: &FaceAnalysisResult{FaceCount: 1, Quality: "HIGH"},
	}

	sessionRepo := newMockSessionRepo()
	cacheStore := newMockSessionCache()
	ocrRepo := newMockOCRResultRepo()

	svc := NewBiometricService(BiometricServiceConfig{
		Sessions:    sessionRepo,
		Cache:       cacheStore,
		OCRResults:  ocrRepo,
		Biometrics:  newMockBiometricRepo(),
		Engine:      engine,
		Storage:     NewMockObjectStorage(),
		RateLimiter: &mockBioRateLimiter{allowed: false}, // rate limited
		Audit:       &mockAuditRepo{},
	})

	sessionID := "onb_bio_test"
	session := &Session{
		SessionID: sessionID, DeviceID: "dev", CurrentStep: StepBiometric,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cacheStore.data[sessionID] = session

	_, err := svc.ProcessBiometric(context.Background(), sessionID, []byte("face"), dummyFrames(3), LivenessMeta{}, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected rate limit error")
	}
}
