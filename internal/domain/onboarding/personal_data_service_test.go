package onboarding

import (
	"context"
	"testing"
	"time"
)

// --- in-memory mocks for personal data service ---

type mockPersonalDataRepo struct {
	data map[string]*PersonalData
}

func newMockPersonalDataRepo() *mockPersonalDataRepo {
	return &mockPersonalDataRepo{data: make(map[string]*PersonalData)}
}

func (m *mockPersonalDataRepo) Create(_ context.Context, pd *PersonalData) error {
	m.data[pd.SessionID] = pd
	return nil
}

func (m *mockPersonalDataRepo) Update(_ context.Context, pd *PersonalData) error {
	m.data[pd.SessionID] = pd
	return nil
}

func (m *mockPersonalDataRepo) FindBySessionID(_ context.Context, sessionID string) (*PersonalData, error) {
	return m.data[sessionID], nil
}

type mockOCRResultRepo struct {
	data map[string]*OCRResult
}

func newMockOCRResultRepo() *mockOCRResultRepo {
	return &mockOCRResultRepo{data: make(map[string]*OCRResult)}
}

func (m *mockOCRResultRepo) Create(_ context.Context, r *OCRResult) error {
	m.data[r.SessionID] = r
	return nil
}

func (m *mockOCRResultRepo) FindBySessionID(_ context.Context, sessionID string) (*OCRResult, error) {
	return m.data[sessionID], nil
}

type mockOTPCache struct {
	otps        map[string]string
	attempts    map[string]int64
	blocked     map[string]bool
	blockExpiry map[string]time.Time
}

func newMockOTPCache() *mockOTPCache {
	return &mockOTPCache{
		otps:        make(map[string]string),
		attempts:    make(map[string]int64),
		blocked:     make(map[string]bool),
		blockExpiry: make(map[string]time.Time),
	}
}

func (m *mockOTPCache) StoreOTP(_ context.Context, sessionID, otpHash string, ttl time.Duration) (time.Time, error) {
	m.otps[sessionID] = otpHash
	return time.Now().Add(ttl), nil
}

func (m *mockOTPCache) GetOTP(_ context.Context, sessionID string) (string, error) {
	return m.otps[sessionID], nil
}

func (m *mockOTPCache) DeleteOTP(_ context.Context, sessionID string) error {
	delete(m.otps, sessionID)
	return nil
}

func (m *mockOTPCache) IncrAttempt(_ context.Context, sessionID string) (int64, error) {
	m.attempts[sessionID]++
	return m.attempts[sessionID], nil
}

func (m *mockOTPCache) IsBlocked(_ context.Context, sessionID string) (bool, error) {
	return m.blocked[sessionID], nil
}

func (m *mockOTPCache) Block(_ context.Context, sessionID string, d time.Duration) error {
	m.blocked[sessionID] = true
	m.blockExpiry[sessionID] = time.Now().Add(d)
	return nil
}

func (m *mockOTPCache) BlockRemaining(_ context.Context, sessionID string) (time.Duration, error) {
	exp, ok := m.blockExpiry[sessionID]
	if !ok {
		return 0, nil
	}
	rem := time.Until(exp)
	if rem < 0 {
		return 0, nil
	}
	return rem, nil
}

type mockSMSGateway struct {
	sent []struct{ phone, otp string }
}

func (m *mockSMSGateway) SendOTP(_ context.Context, phone, otp string) error {
	m.sent = append(m.sent, struct{ phone, otp string }{phone, otp})
	return nil
}

func setupPDService() (*PersonalDataService, *mockSessionRepo, *mockSessionCache, *mockOCRResultRepo, *mockOTPCache, *mockSMSGateway, *mockAuditRepo) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	ocrRepo := newMockOCRResultRepo()
	pdRepo := newMockPersonalDataRepo()
	otpCache := newMockOTPCache()
	sms := &mockSMSGateway{}
	audit := &mockAuditRepo{}

	svc := NewPersonalDataService(PersonalDataServiceConfig{
		Sessions:     sessionRepo,
		Cache:        cache,
		OCRResults:   ocrRepo,
		PersonalData: pdRepo,
		OTPCache:     otpCache,
		SMS:          sms,
		AES:          nil, // no encryption in tests
		Audit:        audit,
	})

	return svc, sessionRepo, cache, ocrRepo, otpCache, sms, audit
}

// createTestSession creates a session at PERSONAL_DATA step with OCR result.
func createTestSession(sessionRepo *mockSessionRepo, cache *mockSessionCache, ocrRepo *mockOCRResultRepo) string {
	sessionID := "onb_test123"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_test",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepPersonalData,
		StepsCompleted: StepsCompleted{
			TNCAccepted: true,
			OCRVerified: true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session

	ocrRepo.data[sessionID] = &OCRResult{
		OCRID:     "ocr_test123",
		SessionID: sessionID,
		Extracted: KTPData{
			NIK:         "3174082104950001",
			NamaLengkap: "MUHAMMAD ARDAN PRAYOGI",
		},
		DukcapilMatch: true,
	}

	return sessionID
}

func validPersonalDataInput() PersonalDataInput {
	return PersonalDataInput{
		NIK:          "3174082104950001",
		NamaLengkap:  "MUHAMMAD ARDAN PRAYOGI",
		TempatLahir:  "Jakarta",
		TanggalLahir: "1995-04-21",
		JenisKelamin: "LAKI_LAKI",
		AlamatKTP: AlamatKTP{
			AlamatLengkap: "Jl. Sudirman Kav. 45",
			RTRW:          "003/005",
			KodePos:       "12190",
			Kelurahan:     "Senayan",
			Kecamatan:     "Kebayoran Baru",
			Kota:          "Jakarta Selatan",
			Provinsi:      "DKI Jakarta",
		},
		AlamatDomisiliSama:  true,
		Pekerjaan:           "KARYAWAN_SWASTA",
		PenghasilanPerBulan: "10_20_JUTA",
		SumberDanaUtama:     "GAJI",
		NomorHP:             "081234568889",
		Email:               "m.ardan@example.com",
	}
}

func TestSavePersonalData_Success(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, sms, audit := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	resp, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
	}, "127.0.0.1", "test-agent")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.PersonalDataID == "" || resp.PersonalDataID[:3] != "pd_" {
		t.Errorf("personal_data_id should start with pd_, got %q", resp.PersonalDataID)
	}
	if resp.CurrentStep != StepOTPVerify {
		t.Errorf("expected OTP_VERIFY step, got %s", resp.CurrentStep)
	}
	if resp.OTPSentTo != "0812****8889" {
		t.Errorf("expected masked phone, got %q", resp.OTPSentTo)
	}

	// Verify SMS was sent
	if len(sms.sent) != 1 {
		t.Errorf("expected 1 SMS sent, got %d", len(sms.sent))
	}

	// Verify audit logs (personal_data_saved + otp_sent)
	if len(audit.logs) < 2 {
		t.Errorf("expected at least 2 audit logs, got %d", len(audit.logs))
	}

	// Verify session stepped to OTP_VERIFY
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepOTPVerify {
		t.Errorf("session should be at OTP_VERIFY, got %s", s.CurrentStep)
	}
}

func TestSavePersonalData_InvalidPhone(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	pd := validPersonalDataInput()
	pd.NomorHP = "12345" // invalid

	_, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: pd,
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected error for invalid phone")
	}
}

func TestSavePersonalData_NIKMismatch(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	pd := validPersonalDataInput()
	pd.NIK = "9999999999999999" // doesn't match OCR

	_, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: pd,
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected error for NIK mismatch")
	}
}

func TestSavePersonalData_InvalidEnum(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	pd := validPersonalDataInput()
	pd.Pekerjaan = "INVALID_JOB"

	_, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: pd,
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected error for invalid pekerjaan enum")
	}
}

func TestVerifyOTP_Success(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, audit := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	// Set session to OTP_VERIFY step
	sessionRepo.sessions[sessionID].CurrentStep = StepOTPVerify
	cache.data[sessionID].CurrentStep = StepOTPVerify

	// Store OTP
	otp := "847291"
	otpCache.otps[sessionID] = hashOTP(otp)

	resp, err := svc.VerifyOTP(ctx, VerifyOTPRequest{
		SessionID: sessionID,
		OTPCode:   otp,
	}, "127.0.0.1", "test-agent")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Verified {
		t.Error("expected verified to be true")
	}
	if resp.CurrentStep != StepBiometric {
		t.Errorf("expected BIOMETRIC step, got %s", resp.CurrentStep)
	}

	// OTP should be deleted
	if otpCache.otps[sessionID] != "" {
		t.Error("OTP should be deleted after verification")
	}

	// Session should be at BIOMETRIC
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepBiometric {
		t.Errorf("session should be at BIOMETRIC, got %s", s.CurrentStep)
	}

	// Audit log for OTP_VERIFIED
	found := false
	for _, log := range audit.logs {
		if log.EventType == AuditOTPVerified {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected OTP_VERIFIED audit log")
	}
}

func TestVerifyOTP_InvalidCode(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	sessionRepo.sessions[sessionID].CurrentStep = StepOTPVerify
	cache.data[sessionID].CurrentStep = StepOTPVerify

	otpCache.otps[sessionID] = hashOTP("847291")

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{
		SessionID: sessionID,
		OTPCode:   "000000", // wrong
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected error for wrong OTP")
	}
}

func TestVerifyOTP_BlockedAfter5Attempts(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	sessionRepo.sessions[sessionID].CurrentStep = StepOTPVerify
	cache.data[sessionID].CurrentStep = StepOTPVerify

	otpCache.otps[sessionID] = hashOTP("847291")

	// Fail 5 times
	for i := 0; i < 5; i++ {
		_, _ = svc.VerifyOTP(ctx, VerifyOTPRequest{
			SessionID: sessionID,
			OTPCode:   "000000",
		}, "127.0.0.1", "test-agent")
	}

	// Should now be blocked
	if !otpCache.blocked[sessionID] {
		t.Error("expected session to be blocked after 5 failed OTP attempts")
	}
}

func TestResendOTP_Success(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, sms, audit := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	// Move session to OTP_VERIFY and store personal data
	sessionRepo.sessions[sessionID].CurrentStep = StepOTPVerify
	cache.data[sessionID].CurrentStep = StepOTPVerify

	// Store personal data so resend can find the phone number
	svc.personalData.Create(ctx, &PersonalData{
		SessionID: sessionID,
		NomorHP:   "081234568889",
	})

	// Store initial OTP
	otpCache.otps[sessionID] = hashOTP("111111")

	resp, err := svc.ResendOTP(ctx, ResendOTPRequest{
		SessionID: sessionID,
	}, "127.0.0.1", "test-agent")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.OTPSentTo != "0812****8889" {
		t.Errorf("expected masked phone, got %q", resp.OTPSentTo)
	}
	if resp.OTPExpiresAt.IsZero() {
		t.Error("expected non-zero OTP expiry")
	}

	// New OTP should be stored (different from old)
	newHash := otpCache.otps[sessionID]
	if newHash == "" {
		t.Error("expected new OTP to be stored")
	}
	if newHash == hashOTP("111111") {
		// Extremely unlikely but possible — new OTP is random, should differ
		t.Log("warning: new OTP hash matches old (statistically very unlikely)")
	}

	// SMS should be sent
	if len(sms.sent) != 1 {
		t.Errorf("expected 1 SMS sent, got %d", len(sms.sent))
	}

	// Audit log for OTP_SENT with reason=user_resend
	found := false
	for _, log := range audit.logs {
		if log.EventType == AuditOTPSent {
			if reason, ok := log.Details["reason"].(string); ok && reason == "user_resend" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected OTP_SENT audit log with reason=user_resend")
	}
}

func TestResendOTP_WrongStep(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	// Session is at PERSONAL_DATA, not OTP_VERIFY

	_, err := svc.ResendOTP(ctx, ResendOTPRequest{
		SessionID: sessionID,
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected error when not at OTP_VERIFY step")
	}
}

func TestResendOTP_Blocked(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()

	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	sessionRepo.sessions[sessionID].CurrentStep = StepOTPVerify
	cache.data[sessionID].CurrentStep = StepOTPVerify

	// Block the session
	otpCache.blocked[sessionID] = true

	_, err := svc.ResendOTP(ctx, ResendOTPRequest{
		SessionID: sessionID,
	}, "127.0.0.1", "test-agent")

	if err == nil {
		t.Fatal("expected OTP_BLOCKED error")
	}
}

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"081234568889", "0812****8889"},
		{"+6281234568889", "+628****8889"},
		{"0812", "0812"},
	}
	for _, tt := range tests {
		got := maskPhone(tt.input)
		if got != tt.want {
			t.Errorf("maskPhone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateOTP(t *testing.T) {
	otp, err := generateOTP(6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(otp) != 6 {
		t.Errorf("expected 6-digit OTP, got %q (len %d)", otp, len(otp))
	}
	// Check all digits
	for _, c := range otp {
		if c < '0' || c > '9' {
			t.Errorf("OTP contains non-digit: %q", otp)
			break
		}
	}
}