package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// --- Biometric mocks ---

type mockBiometricKeyRepo struct {
	keys map[string]*auth.BiometricKey // keyed by key_id
}

func newMockBiometricKeyRepo() *mockBiometricKeyRepo {
	return &mockBiometricKeyRepo{keys: make(map[string]*auth.BiometricKey)}
}

func (m *mockBiometricKeyRepo) FindActiveByKeyID(_ context.Context, keyID string) (*auth.BiometricKey, error) {
	k, ok := m.keys[keyID]
	if !ok {
		return nil, nil
	}
	return k, nil
}

func (m *mockBiometricKeyRepo) Create(_ context.Context, key *auth.BiometricKey) error {
	m.keys[key.KeyID] = key
	return nil
}

type mockBiometricChallengeCache struct {
	store map[string]*auth.ChallengeData
}

func newMockBiometricChallengeCache() *mockBiometricChallengeCache {
	return &mockBiometricChallengeCache{store: make(map[string]*auth.ChallengeData)}
}

func (m *mockBiometricChallengeCache) StoreChallenge(_ context.Context, id string, data *auth.ChallengeData) error {
	m.store[id] = data
	return nil
}

// ConsumeChallenge atomically retrieves and deletes — simulating GETDEL.
func (m *mockBiometricChallengeCache) ConsumeChallenge(_ context.Context, id string) (*auth.ChallengeData, error) {
	data, ok := m.store[id]
	if !ok {
		return nil, nil
	}
	delete(m.store, id) // atomically consumed
	return data, nil
}

// --- Test helpers ---

type biometricTestFixture struct {
	svc            *auth.Service
	bioKeyRepo     *mockBiometricKeyRepo
	challengeCache *mockBiometricChallengeCache
	auditRepo      *mockAuditRepo
	deviceID       uuid.UUID
	userID         uuid.UUID
	deviceIDStr    string
}

func setupBiometricService(t *testing.T) *biometricTestFixture {
	t.Helper()

	devicePK := uuid.New()
	userID := uuid.New()
	deviceIDStr := "bio-test-device-001"

	bioKeyRepo := newMockBiometricKeyRepo()
	challengeCache := newMockBiometricChallengeCache()
	auditRepo := &mockAuditRepo{}
	auditService := auth.NewAuditService(auditRepo)
	t.Cleanup(func() { auditService.Close() })

	mockDevices := &mockDeviceRepoWithID{
		devicePK:    devicePK,
		deviceIDStr: deviceIDStr,
		userID:      userID,
	}

	svc := auth.NewService(auth.ServiceConfig{
		Users: &mockUserRepo{user: &auth.User{
			ID:             userID,
			FullName:       "Bio User",
			DisplayName:    "Bio",
			PINHash:        "dummy",
			MaxPINAttempts: 5,
		}},
		Devices:            mockDevices,
		Sessions:           newMockSessionRepo(),
		SessionCache:       newMockSessionCache(),
		TokenRevocation:    newMockTokenRevocation(),
		BiometricKeys:      bioKeyRepo,
		BiometricChallenge: challengeCache,
		RateLimiter:        &mockRateLimiter{},
		Lockout:            &mockLockout{},
		PINKeys:            nil,
		JWTManager:         newTestJWTManager(t),
		Audit:              auditService,
		AccessTTL:          15 * time.Minute,
		RefreshTTL:         7 * 24 * time.Hour,
	})

	return &biometricTestFixture{
		svc:            svc,
		bioKeyRepo:     bioKeyRepo,
		challengeCache: challengeCache,
		auditRepo:      auditRepo,
		deviceID:       devicePK,
		userID:         userID,
		deviceIDStr:    deviceIDStr,
	}
}

// mockDeviceRepoWithID returns a specific device with known PK + deviceID string.
type mockDeviceRepoWithID struct {
	devicePK    uuid.UUID
	deviceIDStr string
	userID      uuid.UUID
}

func (m *mockDeviceRepoWithID) FindActiveByDeviceID(_ context.Context, deviceID string) (*auth.Device, error) {
	if deviceID == m.deviceIDStr {
		return &auth.Device{
			ID:       m.devicePK,
			UserID:   m.userID,
			DeviceID: m.deviceIDStr,
		}, nil
	}
	return nil, nil
}

func (m *mockDeviceRepoWithID) UpdateLastActive(_ context.Context, _ uuid.UUID) error { return nil }

func newTestJWTManager(t *testing.T) *crypto.JWTManager {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	kp := &crypto.RSAKeyPair{PrivateKey: privateKey, PublicKey: &privateKey.PublicKey}
	return crypto.NewJWTManager(kp, 15*time.Minute, 7*24*time.Hour)
}

// --- Tests ---

func TestCreateChallenge_HappyPath(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	resp, err := f.svc.CreateChallenge(ctx, auth.BiometricChallengeRequest{DeviceID: f.deviceIDStr})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ChallengeID == "" {
		t.Fatal("expected non-empty challenge_id")
	}
	if resp.Challenge == "" {
		t.Fatal("expected non-empty challenge")
	}
	if resp.ExpiresIn != 60 {
		t.Fatalf("expected 60s expiry, got %d", resp.ExpiresIn)
	}

	// Challenge should be base64 decodable
	decoded, err := base64.StdEncoding.DecodeString(resp.Challenge)
	if err != nil {
		t.Fatalf("challenge not valid base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(decoded))
	}
}

func TestCreateChallenge_UnknownDevice(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	_, err := f.svc.CreateChallenge(ctx, auth.BiometricChallengeRequest{DeviceID: "unknown-device"})
	if err == nil {
		t.Fatal("expected error for unknown device")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_DEVICE_NOT_RECOGNIZED" {
		t.Fatalf("expected AUTH_DEVICE_NOT_RECOGNIZED, got: %v", err)
	}
}

func TestChallenge_UsedTwice_SecondRejected(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	// Create challenge
	resp, err := f.svc.CreateChallenge(ctx, auth.BiometricChallengeRequest{DeviceID: f.deviceIDStr})
	if err != nil {
		t.Fatal(err)
	}

	// Generate key pair and register it
	privKey, pubPEM := generateTestKeyPair(t)
	keyID := "test-key-001"
	f.bioKeyRepo.keys[keyID] = &auth.BiometricKey{
		ID:            uuid.New(),
		UserID:        f.userID,
		DeviceID:      f.deviceID,
		KeyID:         keyID,
		PublicKey:     pubPEM,
		BiometricType: "FINGERPRINT",
		IsActive:      true,
	}

	// Sign the challenge
	challengeBytes, _ := base64.StdEncoding.DecodeString(resp.Challenge)
	sig := signChallenge(t, privKey, challengeBytes)

	// First use — should succeed
	_, err = f.svc.LoginByBiometric(ctx, auth.BiometricLoginRequest{
		DeviceID:    f.deviceIDStr,
		KeyID:       keyID,
		ChallengeID: resp.ChallengeID,
		Signature:   sig,
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("first login should succeed: %v", err)
	}

	// Second use with same challenge_id — must be rejected (GETDEL consumed it)
	_, err = f.svc.LoginByBiometric(ctx, auth.BiometricLoginRequest{
		DeviceID:    f.deviceIDStr,
		KeyID:       keyID,
		ChallengeID: resp.ChallengeID,
		Signature:   sig,
	}, "127.0.0.1")
	if err == nil {
		t.Fatal("second use of challenge should be rejected")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("expected AUTH_TOKEN_INVALID, got: %v", err)
	}
}

func TestBiometricLogin_HappyPath(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	// Create challenge
	resp, err := f.svc.CreateChallenge(ctx, auth.BiometricChallengeRequest{DeviceID: f.deviceIDStr})
	if err != nil {
		t.Fatal(err)
	}

	// Register key
	privKey, pubPEM := generateTestKeyPair(t)
	keyID := "login-key-001"
	f.bioKeyRepo.keys[keyID] = &auth.BiometricKey{
		ID:            uuid.New(),
		UserID:        f.userID,
		DeviceID:      f.deviceID,
		KeyID:         keyID,
		PublicKey:     pubPEM,
		BiometricType: "FACE_ID",
		IsActive:      true,
	}

	challengeBytes, _ := base64.StdEncoding.DecodeString(resp.Challenge)
	sig := signChallenge(t, privKey, challengeBytes)

	loginResp, err := f.svc.LoginByBiometric(ctx, auth.BiometricLoginRequest{
		DeviceID:    f.deviceIDStr,
		KeyID:       keyID,
		ChallengeID: resp.ChallengeID,
		Signature:   sig,
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if loginResp.AccessToken == "" || loginResp.RefreshToken == "" {
		t.Fatal("expected non-empty tokens")
	}
	if loginResp.TokenType != "Bearer" {
		t.Fatalf("expected Bearer, got %s", loginResp.TokenType)
	}
	if loginResp.User.ID != f.userID.String() {
		t.Fatalf("expected user_id %s, got %s", f.userID, loginResp.User.ID)
	}

	// Audit should have AUTH_BIOMETRIC_LOGIN
	time.Sleep(50 * time.Millisecond)
	entries := f.auditRepo.getEntries()
	found := false
	for _, e := range entries {
		if e.Action == auth.AuditBiometricLogin {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected AUTH_BIOMETRIC_LOGIN audit entry")
	}
}

func TestBiometricLogin_KeyDeviceMismatch(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	resp, err := f.svc.CreateChallenge(ctx, auth.BiometricChallengeRequest{DeviceID: f.deviceIDStr})
	if err != nil {
		t.Fatal(err)
	}

	// Register key with a DIFFERENT device PK
	privKey, pubPEM := generateTestKeyPair(t)
	keyID := "wrong-device-key"
	f.bioKeyRepo.keys[keyID] = &auth.BiometricKey{
		ID:            uuid.New(),
		UserID:        f.userID,
		DeviceID:      uuid.New(), // different device!
		KeyID:         keyID,
		PublicKey:     pubPEM,
		BiometricType: "FINGERPRINT",
		IsActive:      true,
	}

	challengeBytes, _ := base64.StdEncoding.DecodeString(resp.Challenge)
	sig := signChallenge(t, privKey, challengeBytes)

	_, err = f.svc.LoginByBiometric(ctx, auth.BiometricLoginRequest{
		DeviceID:    f.deviceIDStr,
		KeyID:       keyID,
		ChallengeID: resp.ChallengeID,
		Signature:   sig,
	}, "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for device mismatch")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_BIOMETRIC_NOT_REGISTERED" {
		t.Fatalf("expected AUTH_BIOMETRIC_NOT_REGISTERED, got: %v", err)
	}
}

func TestBiometricLogin_InvalidSignature(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	resp, err := f.svc.CreateChallenge(ctx, auth.BiometricChallengeRequest{DeviceID: f.deviceIDStr})
	if err != nil {
		t.Fatal(err)
	}

	// Register key with one key pair, sign with another
	_, pubPEM := generateTestKeyPair(t)
	differentKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keyID := "wrong-sig-key"
	f.bioKeyRepo.keys[keyID] = &auth.BiometricKey{
		ID:            uuid.New(),
		UserID:        f.userID,
		DeviceID:      f.deviceID,
		KeyID:         keyID,
		PublicKey:     pubPEM,
		BiometricType: "FINGERPRINT",
		IsActive:      true,
	}

	challengeBytes, _ := base64.StdEncoding.DecodeString(resp.Challenge)
	// Sign with a DIFFERENT private key
	sig := signChallenge(t, differentKey, challengeBytes)

	_, err = f.svc.LoginByBiometric(ctx, auth.BiometricLoginRequest{
		DeviceID:    f.deviceIDStr,
		KeyID:       keyID,
		ChallengeID: resp.ChallengeID,
		Signature:   sig,
	}, "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("expected AUTH_TOKEN_INVALID, got: %v", err)
	}
}

func TestRegisterBiometricKey(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	_, pubPEM := generateTestKeyPair(t)
	keyID := "register-key-001"

	err := f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
		KeyID:         keyID,
		PublicKey:     pubPEM,
		BiometricType: "FINGERPRINT",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Key should be stored
	stored := f.bioKeyRepo.keys[keyID]
	if stored == nil {
		t.Fatal("key not found in repo")
	}
	if stored.UserID != f.userID {
		t.Fatal("user_id mismatch")
	}
	if stored.DeviceID != f.deviceID {
		t.Fatal("device_id mismatch")
	}
	if stored.BiometricType != "FINGERPRINT" {
		t.Fatal("biometric_type mismatch")
	}
}

func TestRegisterBiometricKey_InvalidType(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	_, pubPEM := generateTestKeyPair(t)

	err := f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
		KeyID:         "bad-type-key",
		PublicKey:     pubPEM,
		BiometricType: "RETINA_SCAN",
	})
	if err == nil {
		t.Fatal("expected error for invalid biometric type")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected VALIDATION_ERROR, got: %v", err)
	}
}

// --- Helpers ---

func generateTestKeyPair(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	pubPEM, err := auth.ExportPublicKeyPEM(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("export public key: %v", err)
	}
	return privKey, pubPEM
}

func signChallenge(t *testing.T, privKey *ecdsa.PrivateKey, challenge []byte) string {
	t.Helper()
	hash := sha256.Sum256(challenge)
	sig, err := ecdsa.SignASN1(rand.Reader, privKey, hash[:])
	if err != nil {
		t.Fatalf("sign challenge: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}