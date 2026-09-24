package onboarding

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- in-memory mocks for submit ---

type mockCoreBanking struct {
	fail bool

	// Bagian penerbitan kartu (§10).
	issueFail  bool
	issueCalls int
	lastIssue  CardIssuanceRequest
}

func (m *mockCoreBanking) CreateAccount(_ context.Context, _ string, _ ProductType, _, _ string) (*CoreBankingResult, error) {
	if m.fail {
		return nil, fmt.Errorf("core banking unavailable")
	}
	return &CoreBankingResult{
		AccountNumber: "5420001234",
		Branch:        "KCU Jakarta Thamrin",
		BranchCode:    "0539",
	}, nil
}

func (m *mockCoreBanking) IssueCard(_ context.Context, req CardIssuanceRequest) (*CardIssuanceResult, error) {
	m.issueCalls++
	m.lastIssue = req
	if m.issueFail {
		return nil, fmt.Errorf("card printer offline")
	}
	return &CardIssuanceResult{
		MaskedNumber: "•••• 5678",
		Status:       CardIssuanceRequested,
	}, nil
}

// mockCardIssuanceRepo menirukan UNIQUE(session_id) di tabel antrean: klaim
// kedua untuk sesi yang sama mengembalikan false, sama seperti
// ON CONFLICT DO NOTHING di Postgres.
type mockCardIssuanceRepo struct {
	rows map[string]*CardIssuance
}

func newMockCardIssuanceRepo() *mockCardIssuanceRepo {
	return &mockCardIssuanceRepo{rows: map[string]*CardIssuance{}}
}

func (m *mockCardIssuanceRepo) Claim(_ context.Context, iss CardIssuance) (bool, error) {
	if _, exists := m.rows[iss.SessionID]; exists {
		return false, nil
	}
	row := iss
	m.rows[iss.SessionID] = &row
	return true, nil
}

func (m *mockCardIssuanceRepo) MarkResult(_ context.Context, sessionID string, result CardIssuanceResult) error {
	row := m.rows[sessionID]
	if row == nil {
		return nil
	}
	masked := result.MaskedNumber
	row.MaskedNumber = &masked
	row.Status = result.Status
	row.TrackingNumber = result.TrackingNumber
	row.NextRetryAt = nil
	return nil
}

func (m *mockCardIssuanceRepo) MarkFailed(_ context.Context, sessionID, reason string, nextRetryAt time.Time) error {
	row := m.rows[sessionID]
	if row == nil {
		return nil
	}
	row.Status = CardIssuanceFailed
	row.Attempts++
	row.LastError = &reason
	retry := nextRetryAt
	row.NextRetryAt = &retry
	return nil
}

func (m *mockCardIssuanceRepo) FindBySessionID(_ context.Context, sessionID string) (*CardIssuance, error) {
	return m.rows[sessionID], nil
}

func (m *mockCardIssuanceRepo) DueForRetry(_ context.Context, now time.Time, _ int) ([]CardIssuance, error) {
	var out []CardIssuance
	for _, row := range m.rows {
		if row.NextRetryAt != nil && !row.NextRetryAt.After(now) {
			out = append(out, *row)
		}
	}
	return out, nil
}

// mockIdempotencyCache mirrors the Redis implementation: a claim is a slot
// that exists but holds no response yet.
type mockIdempotencyCache struct {
	data map[string]string
}

func newMockIdempotencyCache() *mockIdempotencyCache {
	return &mockIdempotencyCache{data: make(map[string]string)}
}

const mockIdemProcessing = "PROCESSING"

func (m *mockIdempotencyCache) key(sessionID, key string) string {
	return sessionID + ":" + key
}

func (m *mockIdempotencyCache) Claim(_ context.Context, sessionID, key string) (IdempotencyClaim, error) {
	k := m.key(sessionID, key)
	stored, exists := m.data[k]
	if !exists {
		m.data[k] = mockIdemProcessing
		return IdempotencyClaim{}, nil
	}
	if stored == mockIdemProcessing {
		return IdempotencyClaim{AlreadyClaimed: true, StillProcessing: true}, nil
	}
	return IdempotencyClaim{AlreadyClaimed: true, StoredResponse: stored}, nil
}

func (m *mockIdempotencyCache) Persist(_ context.Context, sessionID, key, responseJSON string) error {
	m.data[m.key(sessionID, key)] = responseJSON
	return nil
}

func (m *mockIdempotencyCache) Release(_ context.Context, sessionID, key string) error {
	delete(m.data, m.key(sessionID, key))
	return nil
}

// mockProvisioner stands in for the transaction that creates the m-BCA user.
// Submit refuses outright without one — a session that ends with an account
// number and no user row is worse than an error.
type mockProvisioner struct {
	called bool
	params ProvisionParams
	err    error
}

func (m *mockProvisioner) ProvisionAccount(_ context.Context, params ProvisionParams) (*ProvisionResult, error) {
	m.called = true
	m.params = params
	if m.err != nil {
		return nil, m.err
	}
	return &ProvisionResult{
		UserID:        "11111111-1111-1111-1111-111111111111",
		AccountID:     "22222222-2222-2222-2222-222222222222",
		AccountNumber: params.AccountNumber,
	}, nil
}

func setupSubmitService() (*SubmitService, *mockSessionRepo, *mockSessionCache, *mockPersonalDataRepo, *mockCredentialRepo, *mockCoreBanking, *mockIdempotencyCache) {
	svc, sessionRepo, cache, pdRepo, credRepo, cb, idem, _ := setupSubmitServiceWithProvisioner(&mockProvisioner{})
	return svc, sessionRepo, cache, pdRepo, credRepo, cb, idem
}

func setupSubmitServiceWithProvisioner(prov AccountProvisioner) (*SubmitService, *mockSessionRepo, *mockSessionCache, *mockPersonalDataRepo, *mockCredentialRepo, *mockCoreBanking, *mockIdempotencyCache, AccountProvisioner) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	pdRepo := newMockPersonalDataRepo()
	credRepo := newMockCredentialRepo()
	cb := &mockCoreBanking{}
	idem := newMockIdempotencyCache()
	audit := &mockAuditRepo{}

	svc := NewSubmitService(SubmitServiceConfig{
		Sessions:     sessionRepo,
		Cache:        cache,
		PersonalData: pdRepo,
		Credentials:  credRepo,
		CoreBanking:  cb,
		Provisioner:  prov,
		Idempotency:  idem,
		AES:          nil,
		Audit:        audit,
	})

	return svc, sessionRepo, cache, pdRepo, credRepo, cb, idem, prov
}

func createSubmitTestSession(sessionRepo *mockSessionRepo, cache *mockSessionCache, pdRepo *mockPersonalDataRepo, credRepo *mockCredentialRepo) string {
	sessionID := "onb_submit_test"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_submit",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepReview,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
			BiometricVerified: true,
			VideoCallVerified: true,
			CredentialsSet:    true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session

	pdRepo.data[sessionID] = &PersonalData{
		SessionID:   sessionID,
		NamaLengkap: "MUHAMMAD ARDAN PRAYOGI",
		NIK:         "3174082104950001",
		NomorHP:     "081234567890",
		Email:       "ardan@example.com",
	}

	credRepo.bySession[sessionID] = &Credential{
		SessionID:      sessionID,
		CredentialID:   "cred_test123",
		AccessCodeHash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$YWNjZXNzY29kZQ",
		PINHash:        "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$cGluaGFzaA",
	}

	return sessionID
}

func TestSubmit_Success(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	resp, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, "idem_001", "127.0.0.1", "test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Account.AccountNumber == "" {
		t.Error("account_number should not be empty")
	}
	if resp.Account.AccountType != "TAHAPAN_BCA" {
		t.Errorf("expected TAHAPAN_BCA, got %s", resp.Account.AccountType)
	}
	if resp.Account.AccountHolder != "MUHAMMAD ARDAN PRAYOGI" {
		t.Errorf("unexpected holder: %s", resp.Account.AccountHolder)
	}
	if resp.Account.Status != "ACTIVE" {
		t.Errorf("expected ACTIVE, got %s", resp.Account.Status)
	}
	// The user id is the real provisioned one. It used to be a fabricated
	// "mbca_xxxx" string that matched no row in the database.
	if resp.MBCA.UserID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("expected the provisioned user id, got %q", resp.MBCA.UserID)
	}
	if !resp.MBCA.AccessCodeSet || !resp.MBCA.PINSet {
		t.Error("access_code_set and pin_set should be true")
	}

	// Session should be COMPLETED
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepCompleted {
		t.Errorf("session should be COMPLETED, got %s", s.CurrentStep)
	}
	if !s.StepsCompleted.Submitted {
		t.Error("submitted should be true")
	}
}

func TestSubmit_WrongStep(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	sessionRepo.sessions[sessionID].CurrentStep = StepCredentials
	cache.data[sessionID].CurrentStep = StepCredentials

	_, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for wrong step")
	}
}

func TestSubmit_IncompleteSteps(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	// Mark biometric as NOT verified
	sessionRepo.sessions[sessionID].StepsCompleted.BiometricVerified = false
	cache.data[sessionID].StepsCompleted.BiometricVerified = false

	_, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for incomplete steps")
	}
}

func TestSubmit_AgreementNotAccepted(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	_, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: false,
		AgreementVersion:  "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for agreement not accepted")
	}
}

func TestSubmit_CoreBankingFailure(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, cb, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	cb.fail = true

	_, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for core banking failure")
	}

	// Session should NOT advance
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepReview {
		t.Errorf("session should stay at REVIEW on failure, got %s", s.CurrentStep)
	}
}

func TestSubmit_Idempotent(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, idem := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	idemKey := "idem_test_002"

	// First submit
	resp1, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, idemKey, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}

	// Idempotency cache should have the response, stored under a key scoped to
	// this session — two sessions may legitimately send the same client key.
	if _, ok := idem.data[sessionID+":"+idemKey]; !ok {
		t.Fatal("idempotency cache should have the response")
	}

	// Second submit with same key — should return cached response
	resp2, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, idemKey, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("second submit error: %v", err)
	}

	if resp2.Account.AccountNumber != resp1.Account.AccountNumber {
		t.Errorf("expected same account number on idempotent retry, got %s vs %s",
			resp1.Account.AccountNumber, resp2.Account.AccountNumber)
	}
}

func TestSubmit_MissingPersonalData(t *testing.T) {
	svc, sessionRepo, cache, _, credRepo, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := "onb_no_pd"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_nopd",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepReview,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
			BiometricVerified: true,
			VideoCallVerified: true,
			CredentialsSet:    true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session
	credRepo.bySession[sessionID] = &Credential{SessionID: sessionID}
	// NO personal data stored

	_, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for missing personal data")
	}
}

func TestSubmit_MissingCredentials(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, _, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := "onb_no_cred"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_nocred",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepReview,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
			BiometricVerified: true,
			VideoCallVerified: true,
			CredentialsSet:    true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session
	pdRepo.data[sessionID] = &PersonalData{SessionID: sessionID, NamaLengkap: "Test", NIK: "1234"}
	// NO credentials stored

	_, err := svc.Submit(ctx, SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

// Without a provisioner the flow used to invent an "mbca_xxxx" id and answer
// 200, leaving the nasabah with an account number and no way to log in.
func TestSubmit_RefusesWithoutProvisioner(t *testing.T) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	pdRepo := newMockPersonalDataRepo()
	credRepo := newMockCredentialRepo()

	svc := NewSubmitService(SubmitServiceConfig{
		Sessions:     sessionRepo,
		Cache:        cache,
		PersonalData: pdRepo,
		Credentials:  credRepo,
		CoreBanking:  &mockCoreBanking{},
		Idempotency:  newMockIdempotencyCache(),
		Audit:        &mockAuditRepo{},
	})

	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	_, err := svc.Submit(context.Background(), SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "v1.0",
	}, "idem-no-prov", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected submit to be refused when provisioning is unavailable")
	}
	if apperr.From(err).Code != apperr.ProviderNotConfigured.Code {
		t.Fatalf("expected PROVIDER_NOT_CONFIGURED, got %v", err)
	}
}

// The kode akses chosen in the credentials step has to reach the provisioner —
// it used to be passed in and dropped, so login could only check the PIN.
func TestSubmit_PassesAccessCodeToProvisioner(t *testing.T) {
	prov := &mockProvisioner{}
	svc, sessionRepo, cache, pdRepo, credRepo, _, _, _ := setupSubmitServiceWithProvisioner(prov)

	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	if _, err := svc.Submit(context.Background(), SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "v1.0",
	}, "idem-access-code", "127.0.0.1", "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !prov.called {
		t.Fatal("provisioner was never called")
	}
	if prov.params.AccessCodeHash == "" {
		t.Fatal("access code hash did not reach the provisioner")
	}
	if prov.params.PINHash == "" {
		t.Fatal("pin hash did not reach the provisioner")
	}
}

// --- §10: kartu wajib saat sisipan pilih kartu menyala ---

// submitServiceWithCardFlag merakit SubmitService dengan sakelar kartu pada
// posisi yang diminta test.
func submitServiceWithCardFlag(enabled bool) (*SubmitService, *mockSessionRepo, *mockSessionCache, *mockPersonalDataRepo, *mockCredentialRepo) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	pdRepo := newMockPersonalDataRepo()
	credRepo := newMockCredentialRepo()

	svc := NewSubmitService(SubmitServiceConfig{
		Sessions:     sessionRepo,
		Cache:        cache,
		PersonalData: pdRepo,
		Credentials:  credRepo,
		CoreBanking:  &mockCoreBanking{},
		Provisioner:  &mockProvisioner{},
		Idempotency:  newMockIdempotencyCache(),
		Audit:        &mockAuditRepo{},
		CardFlag:     staticFlag(enabled),
	})
	return svc, sessionRepo, cache, pdRepo, credRepo
}

func submitRequestFor(sessionID string) SubmitRequest {
	return SubmitRequest{
		SessionID:         sessionID,
		AgreementAccepted: true,
		AgreementVersion:  "2026-09-01",
	}
}

func TestSubmit_RejectsSessionWithoutCardWhenFlagOn(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo := submitServiceWithCardFlag(true)
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	_, err := svc.Submit(context.Background(), submitRequestFor(sessionID), "", "127.0.0.1", "ua")

	appErr := apperr.From(err)
	if appErr.Code != apperr.OnboardingIncomplete.Code {
		t.Fatalf("got %v, want ONBOARDING_INCOMPLETE", err)
	}
	details, ok := appErr.Details.(map[string]any)
	if !ok || details["missing_step"] != string(StepCardSelection) {
		t.Fatalf("details.missing_step: %+v", appErr.Details)
	}
}

// Sesi yang dibuat sebelum sisipan ada tidak punya card_selected. Dengan
// sakelar mati, submit-nya harus tetap lewat — kalau tidak, menyalakan lalu
// mematikan sisipan akan mengunci nasabah yang sudah sampai REVIEW.
func TestSubmit_AllowsSessionWithoutCardWhenFlagOff(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo := submitServiceWithCardFlag(false)
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	resp, err := svc.Submit(context.Background(), submitRequestFor(sessionID), "", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Account.AccountNumber == "" {
		t.Error("submit berhasil tapi tidak mengembalikan nomor rekening")
	}
}

func TestSubmit_AcceptsSessionWithCardWhenFlagOn(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo := submitServiceWithCardFlag(true)
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	session := sessionRepo.sessions[sessionID]
	session.CardType = "PASPOR_GOLD"
	session.StepsCompleted.CardSelected = true

	if _, err := svc.Submit(context.Background(), submitRequestFor(sessionID), "", "127.0.0.1", "ua"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- §10: penerbitan kartu ke core banking ---

// submitServiceWithCard merakit SubmitService lengkap dengan katalog kartu dan
// antrean penerbitan — rakitan untuk keempat test §10.
func submitServiceWithCard(issueFail bool) (*SubmitService, *mockSessionRepo, *mockSessionCache, *mockPersonalDataRepo, *mockCredentialRepo, *mockCoreBanking, *mockCardIssuanceRepo) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	pdRepo := newMockPersonalDataRepo()
	credRepo := newMockCredentialRepo()
	cb := &mockCoreBanking{issueFail: issueFail}
	issuance := newMockCardIssuanceRepo()

	minDays, maxDays := 3, 7
	gold := card("PASPOR_GOLD", CardAvailable, true, 1)
	gold.Name = "Gold Mastercard"
	gold.Delivery = CardDelivery{
		PhysicalCardAvailable: true,
		EstimatedDaysMin:      &minDays,
		EstimatedDaysMax:      &maxDays,
		BranchPickupAvailable: true,
	}

	cardSvc := NewCardService(CardServiceConfig{
		Cards: &mockCardRepo{version: "2026-09-23.1", cards: []regionalCard{gold}},
		Cache: newMockCardCache(),
		Flag:  staticFlag(true),
	})

	svc := NewSubmitService(SubmitServiceConfig{
		Sessions:     sessionRepo,
		Cache:        cache,
		PersonalData: pdRepo,
		Credentials:  credRepo,
		CoreBanking:  cb,
		Provisioner:  &mockProvisioner{},
		Idempotency:  newMockIdempotencyCache(),
		Audit:        &mockAuditRepo{},
		CardFlag:     staticFlag(true),
		Cards:        cardSvc,
		CardIssuance: issuance,
		CardCodes:    func(string) string { return "CB-GOLD" },
	})
	return svc, sessionRepo, cache, pdRepo, credRepo, cb, issuance
}

// withSelectedCard menyiapkan sesi yang sudah memilih PASPOR_GOLD.
func withSelectedCard(sessionRepo *mockSessionRepo, cache *mockSessionCache, pdRepo *mockPersonalDataRepo, credRepo *mockCredentialRepo) string {
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)
	session := sessionRepo.sessions[sessionID]
	session.CardType = "PASPOR_GOLD"
	session.StepsCompleted.CardSelected = true
	return sessionID
}

func TestSubmit_IssuesExactlyOneCardPrintRequest(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, cb, issuance := submitServiceWithCard(false)
	sessionID := withSelectedCard(sessionRepo, cache, pdRepo, credRepo)

	resp, err := svc.Submit(context.Background(), submitRequestFor(sessionID), "", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cb.issueCalls != 1 {
		t.Fatalf("permintaan cetak dikirim %d kali, harusnya 1", cb.issueCalls)
	}
	if cb.lastIssue.CoreBankingCode != "CB-GOLD" {
		t.Errorf("kode core banking: got %q, want CB-GOLD", cb.lastIssue.CoreBankingCode)
	}
	if resp.Card == nil {
		t.Fatal("respons submit tidak memuat objek card")
	}
	if resp.Card.Status != CardIssuanceRequested {
		t.Errorf("status kartu: got %s, want REQUESTED", resp.Card.Status)
	}
	if resp.Card.MaskedNumber == nil || *resp.Card.MaskedNumber != "•••• 5678" {
		t.Errorf("masked_number: %v", resp.Card.MaskedNumber)
	}

	// Estimasi tanggal dihitung dari delivery_days_min/max katalog (3–7 hari),
	// bukan ditebak.
	d := resp.Card.Delivery
	if d.EstimatedArrivalFrom == nil || d.EstimatedArrivalTo == nil {
		t.Fatalf("estimasi kirim kosong: %+v", d)
	}
	wantFrom := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	wantTo := time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02")
	if *d.EstimatedArrivalFrom != wantFrom || *d.EstimatedArrivalTo != wantTo {
		t.Errorf("jendela kirim: got %s..%s, want %s..%s",
			*d.EstimatedArrivalFrom, *d.EstimatedArrivalTo, wantFrom, wantTo)
	}
	if d.Method != CardDeliveryCourier {
		t.Errorf("metode kirim: got %s", d.Method)
	}
	if len(issuance.rows) != 1 {
		t.Errorf("baris antrean: %d, harusnya 1", len(issuance.rows))
	}
}

// Submit ulang dengan Idempotency-Key yang sama tidak menambah permintaan cetak.
func TestSubmit_IdempotentKeyDoesNotReprintCard(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, cb, issuance := submitServiceWithCard(false)
	sessionID := withSelectedCard(sessionRepo, cache, pdRepo, credRepo)
	ctx := context.Background()

	if _, err := svc.Submit(ctx, submitRequestFor(sessionID), "idem-key-1", "127.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, submitRequestFor(sessionID), "idem-key-1", "127.0.0.1", "ua"); err != nil {
		t.Fatalf("submit ulang gagal: %v", err)
	}

	if cb.issueCalls != 1 {
		t.Fatalf("permintaan cetak dikirim %d kali, harusnya 1", cb.issueCalls)
	}
	if len(issuance.rows) != 1 {
		t.Errorf("baris antrean: %d, harusnya 1", len(issuance.rows))
	}
}

// Dan tanpa Idempotency-Key sekalipun — ketika slot Redis sudah kedaluwarsa —
// UNIQUE(session_id) di antrean yang menahan cetakan kedua.
func TestSubmit_ClaimGuardsSecondPrintWithoutIdempotencyKey(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, cb, issuance := submitServiceWithCard(false)
	sessionID := withSelectedCard(sessionRepo, cache, pdRepo, credRepo)
	ctx := context.Background()

	if _, err := svc.Submit(ctx, submitRequestFor(sessionID), "", "127.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}

	// Sesi dikembalikan ke REVIEW supaya submit kedua lolos pemeriksaan langkah,
	// menirukan permintaan ulang yang lolos semua penjaga di atasnya.
	session := sessionRepo.sessions[sessionID]
	session.CurrentStep = StepReview
	session.StepsCompleted.Submitted = false
	cache.data[sessionID] = session

	resp, err := svc.Submit(ctx, submitRequestFor(sessionID), "", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("submit kedua gagal: %v", err)
	}

	if cb.issueCalls != 1 {
		t.Fatalf("permintaan cetak dikirim %d kali, harusnya tetap 1", cb.issueCalls)
	}
	if len(issuance.rows) != 1 {
		t.Errorf("baris antrean: %d, harusnya 1", len(issuance.rows))
	}
	// Keadaan yang tersimpan tetap dilaporkan, bukan kosong.
	if resp.Card == nil || resp.Card.MaskedNumber == nil {
		t.Errorf("card pada submit kedua: %+v", resp.Card)
	}
}

// Kegagalan penerbitan kartu tidak membatalkan rekening.
func TestSubmit_CardIssuanceFailureKeepsAccountActive(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, issuance := submitServiceWithCard(true)
	sessionID := withSelectedCard(sessionRepo, cache, pdRepo, credRepo)

	resp, err := svc.Submit(context.Background(), submitRequestFor(sessionID), "", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("submit digagalkan oleh kegagalan cetak kartu: %v", err)
	}
	if resp.Account.Status != "ACTIVE" {
		t.Errorf("status rekening: got %s, want ACTIVE", resp.Account.Status)
	}
	if resp.Card == nil || resp.Card.Status != CardIssuanceRequested {
		t.Fatalf("kartu harus tetap dilaporkan REQUESTED: %+v", resp.Card)
	}
	if resp.Card.MaskedNumber != nil {
		t.Errorf("kartu gagal terbit tidak boleh punya nomor: %v", *resp.Card.MaskedNumber)
	}

	// Dan kegagalannya masuk antrean retry.
	row := issuance.rows[sessionID]
	if row == nil || row.Status != CardIssuanceFailed {
		t.Fatalf("baris antrean: %+v", row)
	}
	if row.NextRetryAt == nil || row.Attempts != 1 {
		t.Errorf("retry belum dijadwalkan: next=%v attempts=%d", row.NextRetryAt, row.Attempts)
	}
	due, _ := issuance.DueForRetry(context.Background(), row.NextRetryAt.Add(time.Minute), 10)
	if len(due) != 1 {
		t.Errorf("permintaan tidak terbaca pekerja retry: %d", len(due))
	}
}
