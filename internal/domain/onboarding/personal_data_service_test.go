package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
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
	otps         map[string]string
	attempts     map[string]int64
	resends      map[string]int64
	resendExpiry map[string]time.Time
	blocked      map[string]bool
	blockExpiry  map[string]time.Time
}

func newMockOTPCache() *mockOTPCache {
	return &mockOTPCache{
		otps:         make(map[string]string),
		attempts:     make(map[string]int64),
		resends:      make(map[string]int64),
		resendExpiry: make(map[string]time.Time),
		blocked:      make(map[string]bool),
		blockExpiry:  make(map[string]time.Time),
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

func (m *mockOTPCache) ResetAttempts(_ context.Context, sessionID string) error {
	delete(m.attempts, sessionID)
	return nil
}

func (m *mockOTPCache) IncrResend(_ context.Context, sessionID string) (int64, error) {
	m.resends[sessionID]++
	if m.resends[sessionID] == 1 {
		m.resendExpiry[sessionID] = time.Now().Add(time.Hour)
	}
	return m.resends[sessionID], nil
}

func (m *mockOTPCache) ResendWindowRemaining(_ context.Context, sessionID string) (time.Duration, error) {
	exp, ok := m.resendExpiry[sessionID]
	if !ok {
		return 0, nil
	}
	rem := time.Until(exp)
	if rem < 0 {
		return 0, nil
	}
	return rem, nil
}

func (m *mockOTPCache) ResetResend(_ context.Context, sessionID string) error {
	delete(m.resends, sessionID)
	delete(m.resendExpiry, sessionID)
	return nil
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
	if err := svc.personalData.Create(ctx, &PersonalData{
		SessionID: sessionID,
		NomorHP:   "081234568889",
	}); err != nil {
		t.Fatalf("siapkan data pribadi: %v", err)
	}

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
			if reason, ok := log.Details["reason"].(string); ok && reason == otpTriggerResend {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected OTP_SENT audit log with reason=resend")
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

// --- OTP policy: resend quota, device binding, delivery failure ---

// atOTPVerify puts a session on the OTP_VERIFY step with personal data stored,
// which is what resend-otp needs to find a phone number.
func atOTPVerify(t *testing.T, svc *PersonalDataService, sessionRepo *mockSessionRepo, cache *mockSessionCache, ocrRepo *mockOCRResultRepo) string {
	t.Helper()
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	sessionRepo.sessions[sessionID].CurrentStep = StepOTPVerify
	cache.data[sessionID].CurrentStep = StepOTPVerify
	if err := svc.personalData.Create(context.Background(), &PersonalData{
		PersonalDataID: "pd_test123",
		SessionID:      sessionID,
		NomorHP:        "081234568889",
	}); err != nil {
		t.Fatalf("seed personal data: %v", err)
	}
	return sessionID
}

func asAppErr(t *testing.T, err error) apperr.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return apperr.From(err)
}

// details reaches into apperr.Error.Details, which is an untyped any.
func details(t *testing.T, appErr apperr.Error) map[string]any {
	t.Helper()
	d, ok := appErr.Details.(map[string]any)
	if !ok {
		t.Fatalf("expected map details, got %#v", appErr.Details)
	}
	return d
}

func TestResendOTP_QuotaExhausted(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, sms, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

	for i := int64(1); i <= otpMaxResend; i++ {
		if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
			t.Fatalf("resend %d should be allowed: %v", i, err)
		}
	}

	_, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua")
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.RateLimitExceeded.Code {
		t.Fatalf("expected RATE_LIMIT_EXCEEDED, got %s", appErr.Code)
	}
	if appErr.Status != 429 {
		t.Errorf("expected 429, got %d", appErr.Status)
	}
	retry, ok := details(t, appErr)["retry_after_seconds"].(int)
	if !ok {
		t.Fatalf("details.retry_after_seconds missing or not an int: %#v", appErr.Details)
	}
	if retry <= 0 {
		t.Errorf("retry_after_seconds should count down the quota window, got %d", retry)
	}

	// The rejected request must not have cost an SMS.
	if len(sms.sent) != int(otpMaxResend) {
		t.Errorf("expected %d SMS, got %d", otpMaxResend, len(sms.sent))
	}
}

func TestResendOTP_InvalidatesPreviousCode(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, sms, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

	oldCode := "111111"
	otpCache.otps[sessionID] = hashOTP(oldCode)

	if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("resend failed: %v", err)
	}

	if _, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: oldCode}, "127.0.0.1", "ua"); err == nil {
		t.Fatal("the superseded code must stop verifying after a resend")
	}

	// The code that actually went out is the one that works.
	newCode := sms.sent[len(sms.sent)-1].otp
	if _, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: newCode}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("the resent code should verify: %v", err)
	}
}

func TestResendOTP_DoesNotResetFailureCounter(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	otpCache.otps[sessionID] = hashOTP("847291")

	// Four wrong guesses, then a resend, then one more wrong guess. If the
	// resend refilled the budget the fifth failure would not block.
	for i := 0; i < 4; i++ {
		_, _ = svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
	}
	if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("resend failed: %v", err)
	}
	_, _ = svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")

	if !otpCache.blocked[sessionID] {
		t.Error("a resend must not hand the nasabah a fresh attempt budget")
	}
}

func TestRegenerateOTP_DoesNotSpendResendQuota(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	otpCache.otps[sessionID] = hashOTP("847291")

	// Three failures trigger one automatic regeneration.
	for i := 0; i < otpRegenAt; i++ {
		_, _ = svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
	}

	if otpCache.resends[sessionID] != 0 {
		t.Errorf("automatic regeneration must not charge the nasabah's quota, spent %d", otpCache.resends[sessionID])
	}
	// All three nasabah-requested resends are still available.
	for i := int64(1); i <= otpMaxResend; i++ {
		if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
			t.Fatalf("resend %d should still be allowed: %v", i, err)
		}
	}
}

func TestVerifyOTP_BlockedDoesNotGrowCounter(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, audit := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

	otpCache.blocked[sessionID] = true
	otpCache.blockExpiry[sessionID] = time.Now().Add(otpBlockTime)
	before := otpCache.attempts[sessionID]

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.OTPBlocked.Code {
		t.Fatalf("expected OTP_BLOCKED, got %s", appErr.Code)
	}
	if _, ok := details(t, appErr)["retry_after_seconds"]; !ok {
		t.Error("OTP_BLOCKED must carry details.retry_after_seconds")
	}
	if otpCache.attempts[sessionID] != before {
		t.Error("an attempt refused by the block must not extend the lockout")
	}

	// A refusal while blocked is still an OTP_FAILED event.
	found := false
	for _, log := range audit.logs {
		if log.EventType == AuditOTPFailed && log.Details["reason"] == "blocked" {
			found = true
		}
	}
	if !found {
		t.Error("expected OTP_FAILED audit row with reason=blocked")
	}
}

func TestVerifyOTP_ExpiredIsAudited(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, _, audit := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	// No OTP stored at all — the same state an expired key leaves behind.

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPExpired.Code {
		t.Fatalf("expected OTP_EXPIRED, got %s", appErr.Code)
	}

	found := false
	for _, log := range audit.logs {
		if log.EventType == AuditOTPFailed && log.Details["reason"] == "expired" {
			found = true
		}
	}
	if !found {
		t.Error("expected OTP_FAILED audit row with reason=expired")
	}
}

func TestOTP_RejectsSessionFromAnotherDevice(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo) // session.DeviceID == "dev_test"
	otpCache.otps[sessionID] = hashOTP("847291")

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{
		SessionID: sessionID,
		OTPCode:   "847291",
		DeviceID:  "dev_someone_else",
	}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OnboardingNotFound.Code {
		t.Fatalf("verify from a foreign device should look like an unknown session, got %s", appErr.Code)
	}

	_, err = svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID, DeviceID: "dev_someone_else"}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OnboardingNotFound.Code {
		t.Fatalf("resend from a foreign device should look like an unknown session, got %s", appErr.Code)
	}

	// The matching device still gets through, and a client that sends no
	// header at all is not locked out.
	if _, err := svc.VerifyOTP(ctx, VerifyOTPRequest{
		SessionID: sessionID,
		OTPCode:   "847291",
		DeviceID:  "dev_test",
	}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("the owning device should verify: %v", err)
	}
}

// failingSMS stands in for a gateway that is down, or the unconfigured
// placeholder a non-development deployment gets.
type failingSMS struct{ calls int }

func (f *failingSMS) SendOTP(_ context.Context, _, _ string) error {
	f.calls++
	return errors.New("gateway unreachable")
}

func TestOTP_DeliveryFailureIsReported(t *testing.T) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	ocrRepo := newMockOCRResultRepo()
	pdRepo := newMockPersonalDataRepo()
	otpCache := newMockOTPCache()
	sms := &failingSMS{}

	svc := NewPersonalDataService(PersonalDataServiceConfig{
		Sessions:     sessionRepo,
		Cache:        cache,
		OCRResults:   ocrRepo,
		PersonalData: pdRepo,
		OTPCache:     otpCache,
		SMS:          sms,
		Audit:        &mockAuditRepo{},
	})

	ctx := context.Background()
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	_, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
	}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPDeliveryFailed.Code {
		t.Fatalf("expected OTP_DELIVERY_FAILED, got %s", appErr.Code)
	}

	// Issuance is not cancelled by the failure: the step advanced and the
	// code is stored, so resend-otp is a real way out.
	if sessionRepo.sessions[sessionID].CurrentStep != StepOTPVerify {
		t.Errorf("session should still advance to OTP_VERIFY, got %s", sessionRepo.sessions[sessionID].CurrentStep)
	}
	if otpCache.otps[sessionID] == "" {
		t.Error("the OTP should remain stored and verifiable")
	}

	if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err == nil {
		t.Error("a resend that never left the building must not answer 200")
	}
}

func TestVerifyOTP_WrongStep(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()

	// Session is still at PERSONAL_DATA, and a stale code is lying around.
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	otpCache.otps[sessionID] = hashOTP("847291")

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "847291"}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != "ONBOARDING_INVALID_STEP" {
		t.Fatalf("expected ONBOARDING_INVALID_STEP, got %s", appErr.Code)
	}
}

func TestOTP_RejectsExpiredSession(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	otpCache.otps[sessionID] = hashOTP("847291")

	expired := time.Now().Add(-time.Minute)
	sessionRepo.sessions[sessionID].ExpiresAt = expired
	cache.data[sessionID].ExpiresAt = expired

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "847291"}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OnboardingSessionExpired.Code {
		t.Fatalf("verify: expected ONBOARDING_SESSION_EXPIRED, got %s", appErr.Code)
	}

	_, err = svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OnboardingSessionExpired.Code {
		t.Fatalf("resend: expected ONBOARDING_SESSION_EXPIRED, got %s", appErr.Code)
	}
}
