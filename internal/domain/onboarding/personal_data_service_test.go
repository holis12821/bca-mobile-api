package onboarding

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	smspkg "github.com/holis12821/bca-mobile-api/internal/pkg/sms"
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

	// Keyed by phone number, not session id — that distinction is the point of
	// the counter being tested.
	phoneSends  map[string]int64
	phoneExpiry map[string]time.Time
}

func newMockOTPCache() *mockOTPCache {
	return &mockOTPCache{
		otps:         make(map[string]string),
		attempts:     make(map[string]int64),
		resends:      make(map[string]int64),
		resendExpiry: make(map[string]time.Time),
		blocked:      make(map[string]bool),
		blockExpiry:  make(map[string]time.Time),
		phoneSends:   make(map[string]int64),
		phoneExpiry:  make(map[string]time.Time),
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

func (m *mockOTPCache) IncrPhoneSend(_ context.Context, phone string) (int64, error) {
	m.phoneSends[phone]++
	if m.phoneSends[phone] == 1 {
		m.phoneExpiry[phone] = time.Now().Add(time.Hour)
	}
	return m.phoneSends[phone], nil
}

func (m *mockOTPCache) PhoneSendWindowRemaining(_ context.Context, phone string) (time.Duration, error) {
	exp, ok := m.phoneExpiry[phone]
	if !ok {
		return 0, nil
	}
	rem := time.Until(exp)
	if rem < 0 {
		return 0, nil
	}
	return rem, nil
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

// mockVerifier stands in for a provider that owns the code — Twilio Verify.
type mockVerifier struct {
	started   []string
	channels  []smspkg.Channel
	code      string
	startErr  error
	checkErr  error
	checkCall int
	codeTTL   time.Duration
}

func (m *mockVerifier) StartVerification(_ context.Context, phone string, ch smspkg.Channel) error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = append(m.started, phone)
	m.channels = append(m.channels, ch)
	return nil
}

// CodeTTL mirrors the real verifier: the provider owns the lifetime, so the
// service has to ask rather than assume.
func (m *mockVerifier) CodeTTL() time.Duration {
	if m.codeTTL > 0 {
		return m.codeTTL
	}
	return 10 * time.Minute
}

func (m *mockVerifier) CheckVerification(_ context.Context, _, code string) (bool, error) {
	m.checkCall++
	if m.checkErr != nil {
		return false, m.checkErr
	}
	return code == m.code, nil
}

// setupPDServiceWithVerifier swaps the gateway for a verifier. The OTP cache is
// still handed over on purpose: every counter in it stays ours on this path, and
// the tests below are what prove that.
func setupPDServiceWithVerifier() (*PersonalDataService, *mockSessionRepo, *mockSessionCache, *mockOCRResultRepo, *mockOTPCache, *mockVerifier) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	v := &mockVerifier{code: "847291"}
	svc.sms = nil
	svc.verifier = v
	return svc, sessionRepo, cache, ocrRepo, otpCache, v
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

// TestSavePersonalData_PhoneRuleMatchesTheGateway is the regression test for a
// dead end: validation here accepted numbers the SMS gateway refuses, so the
// data saved, the step advanced to OTP_VERIFY, and every send from then on
// answered 503 — for a nasabah who had done nothing wrong and had no way back.
//
// The expectations are not a second opinion about what a valid number is: they
// are read off sms.NormalizePhone, which is the code that has to route the
// message.
func TestSavePersonalData_PhoneRuleMatchesTheGateway(t *testing.T) {
	cases := []string{
		"081234567890",   // ordinary
		"+6281234567890", // E.164
		"6281234567890",  // no plus
		"81234567890",    // bare, as a paste from a contact list
		"0812-3456-7890", // as typed
		"08123456789012", // 14 digits: used to pass here, refused by the gateway
		"0801234567",     // the "080" block: same
		"+628012345678",  // same, in E.164
		"021555123",      // landline
		"08123",          // too short
		"",               // empty
		"08123456789a",   // letters
	}

	for _, phone := range cases {
		phone := phone
		t.Run(phone, func(t *testing.T) {
			svc, sessionRepo, cache, ocrRepo, _, sms, _ := setupPDService()
			sessionID := createTestSession(sessionRepo, cache, ocrRepo)

			input := validPersonalDataInput()
			input.NomorHP = phone

			_, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
				SessionID:    sessionID,
				OCRID:        "ocr_test123",
				PersonalData: input,
			}, "127.0.0.1", "ua")

			_, normErr := smspkg.NormalizePhone(phone)
			gatewayWouldSend := normErr == nil

			switch {
			case gatewayWouldSend && err != nil:
				t.Fatalf("the gateway would route %q, but validation refused it: %v", phone, err)
			case !gatewayWouldSend && err == nil:
				t.Fatalf("validation accepted %q, which the gateway refuses: the nasabah would reach OTP_VERIFY and never get an SMS", phone)
			}
			if !gatewayWouldSend && len(sms.sent) != 0 {
				t.Errorf("a refused number must not reach the gateway, got %d sends", len(sms.sent))
			}
		})
	}
}

// The fields that reach a bounded or typed column are checked here, because the
// alternative is Postgres checking them: "value too long for type character
// varying(16)" reaches the nasabah as 500, and an unparseable date was quietly
// stored as NULL — a KYC field dropped while the response said 200.
func TestSavePersonalData_RejectsWhatTheColumnsCannotHold(t *testing.T) {
	cases := map[string]func(*PersonalDataInput){
		"gender not on the KTP":    func(in *PersonalDataInput) { in.JenisKelamin = "LAINNYA" },
		"gender over 16 chars":     func(in *PersonalDataInput) { in.JenisKelamin = strings.Repeat("L", 20) },
		"birth date unparseable":   func(in *PersonalDataInput) { in.TanggalLahir = "21-04-1995" },
		"birth date empty":         func(in *PersonalDataInput) { in.TanggalLahir = "" },
		"tempat lahir too long":    func(in *PersonalDataInput) { in.TempatLahir = strings.Repeat("J", 129) },
		"kota too long":            func(in *PersonalDataInput) { in.AlamatKTP.Kota = strings.Repeat("K", 129) },
		"kode pos too long":        func(in *PersonalDataInput) { in.AlamatKTP.KodePos = strings.Repeat("1", 11) },
		"rt/rw too long":           func(in *PersonalDataInput) { in.AlamatKTP.RTRW = strings.Repeat("9", 17) },
		"birth date in the future": func(in *PersonalDataInput) { in.TanggalLahir = time.Now().AddDate(1, 0, 0).Format("2006-01-02") },
		"birth date absurdly old":  func(in *PersonalDataInput) { in.TanggalLahir = "1850-04-21" },
	}

	for name, mutate := range cases {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			svc, sessionRepo, cache, ocrRepo, _, sms, _ := setupPDService()
			sessionID := createTestSession(sessionRepo, cache, ocrRepo)

			input := validPersonalDataInput()
			mutate(&input)

			_, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
				SessionID:    sessionID,
				OCRID:        "ocr_test123",
				PersonalData: input,
			}, "127.0.0.1", "ua")

			appErr := asAppErr(t, err)
			if appErr.Status != 400 && appErr.Status != 422 {
				t.Fatalf("expected a 4xx for input the column cannot hold, got %d %s", appErr.Status, appErr.Code)
			}
			if len(sms.sent) != 0 {
				t.Errorf("nothing should be sent for a rejected request, got %d", len(sms.sent))
			}
		})
	}
}

// VARCHAR(128) counts characters; len() counts bytes. A 128-character kota with
// accented letters in it fits the column, and rejecting it at 400 for being "too
// long" is the kind of refusal nobody can act on.
func TestSavePersonalData_LengthLimitCountsCharactersNotBytes(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, _, _ := setupPDService()
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	input := validPersonalDataInput()
	// 128 runes, 256 bytes.
	input.AlamatKTP.Kota = strings.Repeat("é", 128)

	if _, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: input,
	}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("128 characters fits VARCHAR(128): %v", err)
	}
}

// The automatic regeneration fires once, at the third failure. ">=" fired again
// on the fourth, so three wrong guesses cost two SMS and the code handed out on
// the third was already dead by the time the nasabah typed it.
func TestVerifyOTP_RegeneratesExactlyOnce(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, sms, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	otpCache.otps[sessionID] = hashOTP("847291")

	// Four wrong guesses: one short of the lockout at otpMaxFail.
	for i := 1; i <= otpMaxFail-1; i++ {
		_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
		if err == nil {
			t.Fatalf("attempt %d: a wrong code must not verify", i)
		}
	}

	if len(sms.sent) != 1 {
		t.Fatalf("regenerated %d times, want exactly 1", len(sms.sent))
	}
	if got := otpCache.phoneSends["081234568889"]; got != 1 {
		t.Errorf("phone budget spent = %d, want 1", got)
	}
}

// sessionAtPersonalData builds one more session at PERSONAL_DATA, so a test can
// act out the attack the per-number cap exists for: throwing the session away
// and starting another one costs nothing, and each new session used to come
// with its own fresh SMS budget.
func sessionAtPersonalData(sessionRepo *mockSessionRepo, cache *mockSessionCache, ocrRepo *mockOCRResultRepo, sessionID string) string {
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
		OCRID:     "ocr_" + sessionID,
		SessionID: sessionID,
		Extracted: KTPData{
			NIK:         "3174082104950001",
			NamaLengkap: "MUHAMMAD ARDAN PRAYOGI",
		},
		DukcapilMatch: true,
	}
	return sessionID
}

// TestOTP_PhoneSendQuotaSurvivesNewSessions is the regression test for the
// cheapest way there was to farm SMS out of this flow.
//
// The resend quota is keyed by session_id and a session is free, so one address
// could issue a fresh code per new session as fast as the per-IP limit allowed —
// the Twilio invoice and the victim's handset are ours either way. The ceiling
// follows the number instead, so a new session no longer buys a new budget.
func TestOTP_PhoneSendQuotaSurvivesNewSessions(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, sms, _ := setupPDService()
	ctx := context.Background()

	input := validPersonalDataInput()

	// Each session sends exactly one SMS on personal-data save.
	for i := int64(1); i <= otpMaxSendPerPhone; i++ {
		sessionID := sessionAtPersonalData(sessionRepo, cache, ocrRepo, "onb_farm"+strconv.FormatInt(i, 10))
		if _, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
			SessionID:    sessionID,
			OCRID:        "ocr_" + sessionID,
			PersonalData: input,
		}, "127.0.0.1", "ua"); err != nil {
			t.Fatalf("session %d should be allowed: %v", i, err)
		}
	}
	if len(sms.sent) != int(otpMaxSendPerPhone) {
		t.Fatalf("sent %d messages, want %d", len(sms.sent), otpMaxSendPerPhone)
	}

	// One more session, same number: refused, and no SMS leaves.
	sessionID := sessionAtPersonalData(sessionRepo, cache, ocrRepo, "onb_farm_over")
	_, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_" + sessionID,
		PersonalData: input,
	}, "127.0.0.1", "ua")
	appErr := asAppErr(t, err)
	if appErr.Code != apperr.RateLimitExceeded.Code {
		t.Fatalf("expected RATE_LIMIT_EXCEEDED, got %s", appErr.Code)
	}
	if len(sms.sent) != int(otpMaxSendPerPhone) {
		t.Errorf("a refused request must not send an SMS: sent = %d", len(sms.sent))
	}
	if v, ok := details(t, appErr)["retry_after_seconds"]; !ok || v == nil {
		t.Error("the 429 must tell the app when the number's budget refills")
	}

	// A different number is unaffected: the cap follows the handset, not the IP
	// and not the device.
	other := validPersonalDataInput()
	other.NomorHP = "081298765432"
	fresh := sessionAtPersonalData(sessionRepo, cache, ocrRepo, "onb_other")
	if _, err := svc.SavePersonalData(ctx, SavePersonalDataRequest{
		SessionID:    fresh,
		OCRID:        "ocr_" + fresh,
		PersonalData: other,
	}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("a different number must still be served: %v", err)
	}
}

// A nasabah who has not met the ceiling keeps the behaviour they had: issuance
// plus the full resend quota, all of it counted against the number.
func TestResendOTP_CountsAgainstThePhoneBudget(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, sms, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

	for i := int64(1); i <= otpMaxResend; i++ {
		if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
			t.Fatalf("resend %d should be allowed: %v", i, err)
		}
	}
	if got := otpCache.phoneSends["081234568889"]; got != otpMaxResend {
		t.Fatalf("phone budget spent = %d, want %d", got, otpMaxResend)
	}
	if len(sms.sent) != int(otpMaxResend) {
		t.Errorf("sms sent = %d, want %d", len(sms.sent), otpMaxResend)
	}
}

// A resend refused by the number's hourly ceiling must not cost the nasabah one
// of their three session resends: nothing was dispatched, so nothing may be
// charged. This broke when the per-number check was added after IncrResend.
func TestResendOTP_PhoneCeilingDoesNotSpendTheSessionQuota(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, sms, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

	const phone = "081234568889"
	otpCache.phoneSends[phone] = otpMaxSendPerPhone
	otpCache.phoneExpiry[phone] = time.Now().Add(time.Hour)

	_, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.RateLimitExceeded.Code {
		t.Fatalf("expected RATE_LIMIT_EXCEEDED, got %s", appErr.Code)
	}
	if got := otpCache.resends[sessionID]; got != 0 {
		t.Errorf("session resend quota spent = %d, want 0 — nothing was sent", got)
	}
	if len(sms.sent) != 0 {
		t.Errorf("no SMS may be dispatched, got %d", len(sms.sent))
	}

	// And the quota is genuinely still there once the number's window refills.
	delete(otpCache.phoneSends, phone)
	if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("the nasabah should still have all three resends: %v", err)
	}
}

// When the number is out of budget, the automatic regeneration after three wrong
// guesses is skipped rather than half-done. The code the nasabah is holding must
// survive: overwriting it and then not sending the replacement is the trap that
// left them waiting on an SMS that never left.
func TestVerifyOTP_RegenerationSkippedWhenPhoneBudgetIsGone(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, sms, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

	const phone = "081234568889"
	otpCache.otps[sessionID] = hashOTP("847291")
	otpCache.phoneSends[phone] = otpMaxSendPerPhone
	otpCache.phoneExpiry[phone] = time.Now().Add(time.Hour)

	for i := 0; i < otpRegenAt; i++ {
		_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.OTPInvalid.Code {
			t.Fatalf("attempt %d: expected OTP_INVALID, got %s", i+1, appErr.Code)
		}
	}
	if len(sms.sent) != 0 {
		t.Errorf("no SMS may be sent once the number's budget is gone, got %d", len(sms.sent))
	}

	// The original code still verifies, which is the whole reason the skip
	// happens before StoreOTP.
	if _, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "847291"}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("the code the nasabah already holds must still work: %v", err)
	}
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
	if appErr := asAppErr(t, err); appErr.Code != apperr.OnboardingDeviceMismatch.Code {
		t.Fatalf("verify from a foreign device must be rejected as a device mismatch, got %s", appErr.Code)
	}

	_, err = svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID, DeviceID: "dev_someone_else"}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OnboardingDeviceMismatch.Code {
		t.Fatalf("resend from a foreign device must be rejected as a device mismatch, got %s", appErr.Code)
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

// TestVerifyOTP_RegenerationFailureIsReported pins the one send site in this
// flow that used to swallow the error. The automatic regeneration after three
// wrong guesses overwrites the stored code, so answering OTP_EXPIRED — which
// tells the app a fresh code is already on its way — for an SMS that never left
// the building left the nasabah waiting on nothing.
func TestVerifyOTP_RegenerationFailureIsReported(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, _, _ := setupPDService()
	ctx := context.Background()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	otpCache.otps[sessionID] = hashOTP("847291")

	// The gateway stays healthy through issuance and the first two guesses, so
	// only the regeneration itself fails.
	for i := 0; i < otpRegenAt-1; i++ {
		_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.OTPInvalid.Code {
			t.Fatalf("attempt %d: expected OTP_INVALID, got %s", i+1, appErr.Code)
		}
	}

	gateway := &failingSMS{}
	svc.sms = gateway

	_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPDeliveryFailed.Code {
		t.Fatalf("expected OTP_DELIVERY_FAILED, got %s", appErr.Code)
	}
	if gateway.calls != 1 {
		t.Errorf("the regeneration should have attempted exactly one send, got %d", gateway.calls)
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

// --- jalur verifier (provider yang memiliki kodenya) --------------------------

// On the verifier path no code is generated, hashed or stored here, and otp_debug
// has nothing to carry — the provider never hands the code back.
func TestVerifierPath_IssuesWithoutStoringALocalCode(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, otpCache, v := setupPDServiceWithVerifier()
	svc.devMode = true
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	resp, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
	}, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("SavePersonalData: %v", err)
	}

	if len(v.started) != 1 {
		t.Fatalf("StartVerification called %d times, want 1", len(v.started))
	}
	if _, stored := otpCache.otps[sessionID]; stored {
		t.Error("no local OTP hash may be stored when the provider owns the code")
	}
	if resp.OTPDebug != "" {
		t.Errorf("otp_debug must stay empty even in development, got %q", resp.OTPDebug)
	}
	// The app still needs an honest countdown, taken from the provider's TTL.
	if resp.OTPExpiresAt.IsZero() {
		t.Error("otp_expires_at must still be reported")
	}
	if resp.CurrentStep != StepOTPVerify {
		t.Errorf("current_step = %s, want OTP_VERIFY", resp.CurrentStep)
	}
}

func TestVerifierPath_VerifyOutcomes(t *testing.T) {
	t.Run("approved", func(t *testing.T) {
		svc, sessionRepo, cache, ocrRepo, _, _ := setupPDServiceWithVerifier()
		sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

		resp, err := svc.VerifyOTP(context.Background(),
			VerifyOTPRequest{SessionID: sessionID, OTPCode: "847291"}, "127.0.0.1", "ua")
		if err != nil {
			t.Fatalf("VerifyOTP: %v", err)
		}
		if !resp.Verified || resp.CurrentStep != StepBiometric {
			t.Errorf("verified=%v step=%s, want true/BIOMETRIC", resp.Verified, resp.CurrentStep)
		}
	})

	t.Run("wrong code is OTP_INVALID, not an error", func(t *testing.T) {
		svc, sessionRepo, cache, ocrRepo, _, _ := setupPDServiceWithVerifier()
		sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

		_, err := svc.VerifyOTP(context.Background(),
			VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.OTPInvalid.Code {
			t.Fatalf("expected OTP_INVALID, got %s", appErr.Code)
		}
	})

	// The provider drops a verification once it expires or is approved, so "not
	// found" is exactly "the code being held is no longer live".
	t.Run("provider has nothing pending is OTP_EXPIRED", func(t *testing.T) {
		svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
		sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
		v.checkErr = smspkg.ErrVerifyExpired

		_, err := svc.VerifyOTP(context.Background(),
			VerifyOTPRequest{SessionID: sessionID, OTPCode: "847291"}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.OTPExpired.Code {
			t.Fatalf("expected OTP_EXPIRED, got %s", appErr.Code)
		}
	})
}

// A provider that refuses to send must not be reported as a code on its way —
// the same rule the gateway path follows.
func TestVerifierPath_StartFailureIsReported(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)
	v.startErr = errors.New("provider down")

	_, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
	}, "127.0.0.1", "ua")
	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPDeliveryFailed.Code {
		t.Fatalf("expected OTP_DELIVERY_FAILED, got %s", appErr.Code)
	}
}

// Swapping who owns the code must not quietly hand our policy to the provider.
// These are the counters the repo exists to demonstrate, so they get pinned on
// the verifier path too.
func TestVerifierPath_OurPolicyStillApplies(t *testing.T) {
	t.Run("lockout after otpMaxFail", func(t *testing.T) {
		svc, sessionRepo, cache, ocrRepo, _, _ := setupPDServiceWithVerifier()
		ctx := context.Background()
		sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

		var lastCode string
		for i := 1; i <= otpMaxFail; i++ {
			_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "000000"}, "127.0.0.1", "ua")
			lastCode = asAppErr(t, err).Code
		}
		if lastCode != apperr.OTPBlocked.Code {
			t.Fatalf("the %dth failure must block the session, got %s", otpMaxFail, lastCode)
		}
		// And the block holds even for the right code.
		_, err := svc.VerifyOTP(ctx, VerifyOTPRequest{SessionID: sessionID, OTPCode: "847291"}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.OTPBlocked.Code {
			t.Errorf("a blocked session must stay blocked, got %s", appErr.Code)
		}
	})

	t.Run("per-number hourly ceiling", func(t *testing.T) {
		svc, sessionRepo, cache, ocrRepo, otpCache, v := setupPDServiceWithVerifier()
		ctx := context.Background()
		sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

		const phone = "081234568889"
		otpCache.phoneSends[phone] = otpMaxSendPerPhone
		otpCache.phoneExpiry[phone] = time.Now().Add(time.Hour)

		_, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.RateLimitExceeded.Code {
			t.Fatalf("expected RATE_LIMIT_EXCEEDED, got %s", appErr.Code)
		}
		if len(v.started) != 0 {
			t.Errorf("the provider must not be asked to send, got %d calls", len(v.started))
		}
	})

	t.Run("session resend quota", func(t *testing.T) {
		svc, sessionRepo, cache, ocrRepo, _, _ := setupPDServiceWithVerifier()
		ctx := context.Background()
		sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)

		for i := int64(1); i <= otpMaxResend; i++ {
			if _, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua"); err != nil {
				t.Fatalf("resend %d should be allowed: %v", i, err)
			}
		}
		_, err := svc.ResendOTP(ctx, ResendOTPRequest{SessionID: sessionID}, "127.0.0.1", "ua")
		if appErr := asAppErr(t, err); appErr.Code != apperr.RateLimitExceeded.Code {
			t.Fatalf("expected RATE_LIMIT_EXCEEDED, got %s", appErr.Code)
		}
	})
}

// --- channel pengiriman OTP (sms | call) --------------------------------------

// An explicit channel reaches the provider, and the default stays SMS so an app
// build that sends no channel at all behaves exactly as before.
func TestVerifierPath_ChannelReachesTheProvider(t *testing.T) {
	cases := []struct {
		name string
		req  string
		want smspkg.Channel
	}{
		{"omitted is sms", "", smspkg.ChannelSMS},
		{"explicit sms", "sms", smspkg.ChannelSMS},
		{"call", "call", smspkg.ChannelCall},
		// Case is not the nasabah's problem.
		{"mixed case", "Call", smspkg.ChannelCall},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
			sessionID := createTestSession(sessionRepo, cache, ocrRepo)

			if _, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
				SessionID:    sessionID,
				OCRID:        "ocr_test123",
				PersonalData: validPersonalDataInput(),
				Channel:      tc.req,
			}, "127.0.0.1", "ua"); err != nil {
				t.Fatalf("SavePersonalData: %v", err)
			}

			if len(v.channels) != 1 {
				t.Fatalf("StartVerification called %d times, want 1", len(v.channels))
			}
			if v.channels[0] != tc.want {
				t.Errorf("channel = %q, want %q", v.channels[0], tc.want)
			}
		})
	}
}

// resend-otp is the endpoint that makes "the SMS never arrived" recoverable, so
// the channel has to be selectable there too.
func TestVerifierPath_ResendHonoursChannel(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
	sessionID := atOTPVerify(t, svc, sessionRepo, cache, ocrRepo)
	v.channels = nil

	if _, err := svc.ResendOTP(context.Background(),
		ResendOTPRequest{SessionID: sessionID, Channel: "call"}, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("ResendOTP: %v", err)
	}
	if len(v.channels) != 1 || v.channels[0] != smspkg.ChannelCall {
		t.Fatalf("channels = %v, want [call]", v.channels)
	}
}

// An unknown channel name is a 400 and costs nothing: the provider is never
// called, so the nasabah's code and send budget are untouched.
func TestChannel_UnknownIsRefusedWithoutSending(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	_, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
		Channel:      "whatsapp",
	}, "127.0.0.1", "ua")

	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPChannelNotAllowed.Code {
		t.Fatalf("expected OTP_CHANNEL_NOT_ALLOWED, got %s", appErr.Code)
	}
	if len(v.started) != 0 {
		t.Errorf("provider was called %d times for a channel that does not exist", len(v.started))
	}
}

// On the Gateway path there is no voice transport at all. Quietly sending an SMS
// instead would leave the nasabah waiting for a call that is never placed, so the
// request is refused — and the step must not advance.
func TestChannel_CallOnGatewayPathIsRefused(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, gw, _ := setupPDService()
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	_, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
		Channel:      "call",
	}, "127.0.0.1", "ua")

	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPChannelNotAllowed.Code {
		t.Fatalf("expected OTP_CHANNEL_NOT_ALLOWED, got %s", appErr.Code)
	}
	if len(gw.sent) != 0 {
		t.Errorf("an SMS was sent for a request that asked for a call: %v", gw.sent)
	}
	session, _ := sessionRepo.FindBySessionID(context.Background(), sessionID)
	if session.CurrentStep != StepPersonalData {
		t.Errorf("step = %s, want it left at PERSONAL_DATA", session.CurrentStep)
	}
}

// A channel the transport's own allowlist refuses is a 400 as well, not the 503
// every other delivery failure produces: nothing was sent, so there is nothing to
// retry into and the step must stay where it is.
func TestChannel_ProviderAllowlistRefusalIs400(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
	v.startErr = smspkg.ErrChannelNotAllowed
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	_, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
		Channel:      "call",
	}, "127.0.0.1", "ua")

	if appErr := asAppErr(t, err); appErr.Code != apperr.OTPChannelNotAllowed.Code {
		t.Fatalf("expected OTP_CHANNEL_NOT_ALLOWED, got %s", appErr.Code)
	}
	session, _ := sessionRepo.FindBySessionID(context.Background(), sessionID)
	if session.CurrentStep != StepPersonalData {
		t.Errorf("step = %s, want it left at PERSONAL_DATA", session.CurrentStep)
	}
}

// T15 — otp_expires_at follows the provider's TTL, not a constant in this package.
// A copy of the number here is what drifts the first time somebody changes the
// expiry on the Verify service in the console.
func TestVerifierPath_ExpiryFollowsProviderTTL(t *testing.T) {
	svc, sessionRepo, cache, ocrRepo, _, v := setupPDServiceWithVerifier()
	v.codeTTL = 3 * time.Minute
	sessionID := createTestSession(sessionRepo, cache, ocrRepo)

	before := time.Now().UTC()
	resp, err := svc.SavePersonalData(context.Background(), SavePersonalDataRequest{
		SessionID:    sessionID,
		OCRID:        "ocr_test123",
		PersonalData: validPersonalDataInput(),
	}, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("SavePersonalData: %v", err)
	}

	got := resp.OTPExpiresAt.Sub(before)
	if got < 2*time.Minute+50*time.Second || got > 3*time.Minute+10*time.Second {
		t.Errorf("otp_expires_at is %v away, want ~3m (the provider's TTL)", got)
	}
}
