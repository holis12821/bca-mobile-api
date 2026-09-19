package onboarding

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// --- in-memory mocks for submit ---

type mockCoreBanking struct {
	fail bool
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

type mockIdempotencyCache struct {
	data map[string]string
}

func newMockIdempotencyCache() *mockIdempotencyCache {
	return &mockIdempotencyCache{data: make(map[string]string)}
}

func (m *mockIdempotencyCache) Check(_ context.Context, key string) (string, error) {
	return m.data[key], nil
}

func (m *mockIdempotencyCache) Store(_ context.Context, key, responseJSON string) error {
	m.data[key] = responseJSON
	return nil
}

func setupSubmitService() (*SubmitService, *mockSessionRepo, *mockSessionCache, *mockPersonalDataRepo, *mockCredentialRepo, *mockCoreBanking, *mockIdempotencyCache) {
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
		Idempotency:  idem,
		AES:          nil,
		Audit:        audit,
	})

	return svc, sessionRepo, cache, pdRepo, credRepo, cb, idem
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
	}

	credRepo.bySession[sessionID] = &Credential{
		SessionID:    sessionID,
		CredentialID: "cred_test123",
	}

	return sessionID
}

func TestSubmit_Success(t *testing.T) {
	svc, sessionRepo, cache, pdRepo, credRepo, _, _ := setupSubmitService()
	ctx := context.Background()
	sessionID := createSubmitTestSession(sessionRepo, cache, pdRepo, credRepo)

	resp, err := svc.Submit(ctx, SubmitRequest{
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
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
	if resp.MBCA.UserID == "" || resp.MBCA.UserID[:5] != "mbca_" {
		t.Errorf("user_id should start with mbca_, got %q", resp.MBCA.UserID)
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
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
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
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
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
		SessionID:        sessionID,
		AgreementAccepted: false,
		AgreementVersion: "2026-09-01",
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
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
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
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
	}, idemKey, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}

	// Idempotency cache should have the response
	if _, ok := idem.data[idemKey]; !ok {
		t.Fatal("idempotency cache should have the response")
	}

	// Second submit with same key — should return cached response
	resp2, err := svc.Submit(ctx, SubmitRequest{
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
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
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
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
		SessionID:        sessionID,
		AgreementAccepted: true,
		AgreementVersion: "2026-09-01",
	}, "", "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}