package onboarding

import (
	"context"
	"testing"
	"time"
)

// --- in-memory mock for credentials ---

type mockCredentialRepo struct {
	bySession map[string]*Credential
}

func newMockCredentialRepo() *mockCredentialRepo {
	return &mockCredentialRepo{bySession: make(map[string]*Credential)}
}

func (m *mockCredentialRepo) Create(_ context.Context, cred *Credential) error {
	m.bySession[cred.SessionID] = cred
	return nil
}

func (m *mockCredentialRepo) FindBySessionID(_ context.Context, sessionID string) (*Credential, error) {
	return m.bySession[sessionID], nil
}

func setupCredentialService() (*CredentialService, *mockSessionRepo, *mockSessionCache, *mockCredentialRepo) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	credRepo := newMockCredentialRepo()
	audit := &mockAuditRepo{}

	svc := NewCredentialService(CredentialServiceConfig{
		Sessions:    sessionRepo,
		Cache:       cache,
		Credentials: credRepo,
		PINKeys:     nil, // dev mode — plaintext
		Audit:       audit,
	})

	return svc, sessionRepo, cache, credRepo
}

func createCredTestSession(sessionRepo *mockSessionRepo, cache *mockSessionCache) string {
	sessionID := "onb_cred_test"
	session := &Session{
		SessionID:   sessionID,
		DeviceID:    "dev_cred",
		ProductType: ProductTahapanBCA,
		CurrentStep: StepCredentials,
		StepsCompleted: StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
			BiometricVerified: true,
			VideoCallVerified: true,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	sessionRepo.sessions[sessionID] = session
	cache.data[sessionID] = session
	return sessionID
}

func TestSetCredentials_Success(t *testing.T) {
	svc, sessionRepo, cache, credRepo := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	resp, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Kd7m2q",
		PINEncrypted:        "284917",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CredentialID == "" || resp.CredentialID[:5] != "cred_" {
		t.Errorf("credential_id should start with cred_, got %q", resp.CredentialID)
	}
	if resp.CurrentStep != StepReview {
		t.Errorf("expected REVIEW step, got %s", resp.CurrentStep)
	}
	if !resp.BiometricLoginAvailable {
		t.Error("biometric_login_available should be true")
	}

	// Credential stored
	cred := credRepo.bySession[sessionID]
	if cred == nil {
		t.Fatal("credential not stored")
	}
	if cred.AccessCodeHash == "" || cred.PINHash == "" {
		t.Error("hashes should not be empty")
	}

	// Session should advance to REVIEW
	s := sessionRepo.sessions[sessionID]
	if s.CurrentStep != StepReview {
		t.Errorf("session should be at REVIEW, got %s", s.CurrentStep)
	}
	if !s.StepsCompleted.CredentialsSet {
		t.Error("credentials_set should be true")
	}
}

func TestSetCredentials_WrongStep(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	sessionRepo.sessions[sessionID].CurrentStep = StepOCR
	cache.data[sessionID].CurrentStep = StepOCR

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Kd7m2q",
		PINEncrypted:        "284917",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for wrong step")
	}
}

func TestSetCredentials_WeakAccessCode_AllSame(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "aaaaaa",
		PINEncrypted:        "284917",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for weak access code (all same)")
	}
}

func TestSetCredentials_WeakAccessCode_Sequential(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "abcdef",
		PINEncrypted:        "284917",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for sequential access code")
	}
}

func TestSetCredentials_WeakPIN_AllSame(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Kd7m2q",
		PINEncrypted:        "111111",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for weak PIN (all same)")
	}
}

func TestSetCredentials_WeakPIN_Sequential(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Kd7m2q",
		PINEncrypted:        "123456",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for sequential PIN")
	}
}

func TestSetCredentials_PINSameAsAccessCode(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Abc789",
		PINEncrypted:        "abc789",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected error for PIN same as access code")
	}
}

func TestSetCredentials_EmptyFields(t *testing.T) {
	svc, sessionRepo, cache, _ := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	_, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "",
		PINEncrypted:        "284917",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")

	if err == nil {
		t.Fatal("expected validation error for empty access code")
	}
}

func TestSetCredentials_Idempotent(t *testing.T) {
	svc, sessionRepo, cache, credRepo := setupCredentialService()
	ctx := context.Background()
	sessionID := createCredTestSession(sessionRepo, cache)

	// First call — creates credential and transitions step
	resp1, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Kd7m2q",
		PINEncrypted:        "284917",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	// Second call — should return existing credential without creating duplicate
	resp2, err := svc.SetCredentials(ctx, SetCredentialsRequest{
		SessionID:           sessionID,
		AccessCodeEncrypted: "Qm4v8t",
		PINEncrypted:        "654321",
		EncryptionKeyID:     "key_001",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}

	// Same credential_id should be returned
	if resp1.CredentialID != resp2.CredentialID {
		t.Errorf("expected same credential_id on retry, got %q vs %q", resp1.CredentialID, resp2.CredentialID)
	}

	// Only one credential should exist
	cred := credRepo.bySession[sessionID]
	if cred == nil {
		t.Fatal("credential should exist")
	}
}

func TestIsSequential(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"123456", true},
		{"654321", true},
		{"abcdef", true},
		{"fedcba", true},
		{"135791", false},
		// Three consecutive characters are enough, wherever they sit: "Abc123"
		// carries two such runs. The old implementation needed four and let
		// this through, contradicting its own documentation.
		{"Abc123", true},
		{"ab12cd", false}, // runs of two only
		{"k3lmn9", true},  // "lmn" in the middle
		{"ab", false},
		{"111111", false},
		{"192837", false},
	}
	for _, tc := range cases {
		got := isSequential(tc.input)
		if got != tc.want {
			t.Errorf("isSequential(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestIsAllSameChar(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"aaaaaa", true},
		{"111111", true},
		{"", false},
		{"aaaaab", false},
		{"abcabc", false},
	}
	for _, tc := range cases {
		got := isAllSameChar(tc.input)
		if got != tc.want {
			t.Errorf("isAllSameChar(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
