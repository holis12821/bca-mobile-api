package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
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

func (m *mockBiometricKeyRepo) RevokeByUserDevice(_ context.Context, userID, deviceID uuid.UUID) (int, error) {
	revoked := 0
	for keyID, k := range m.keys {
		if k.UserID == userID && k.DeviceID == deviceID && k.IsActive {
			k.IsActive = false
			m.keys[keyID] = k
			revoked++
		}
	}
	return revoked, nil
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

	resp, err := f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
		KeyID:         keyID,
		PublicKey:     pubPEM,
		BiometricType: "FINGERPRINT",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if resp == nil || resp.BiometricID == "" || resp.RegisteredAt.IsZero() {
		t.Fatalf("register response must carry biometric_id and registered_at, got %+v", resp)
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

	_, err := f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
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

// TestBiometricLogin_PublishedTestVector pins the interop vector published in
// docs/01-API-SPECIFICATION.md §2. The Android side verifies its own
// implementation against the same numbers without waiting for a server, which is
// what butir 2 of docs/10-HANDOVER-BLOCKER-BACKEND.md asks for.
//
// If this test fails, the published vector and the server no longer agree —
// update both, together, or the client has been given a wrong answer.
func TestBiometricLogin_PublishedTestVector(t *testing.T) {
	const (
		vectorPublicKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEo6b/4hWQuDCQW3mrgm1MT3+R4IN/" +
			"KBMlOIfCncd+r0AELAmBaWsMxOp0AyRkxsK+8LWUeLjiCHmiO0IQiWJa3Q=="
		vectorChallengeB64 = "dF8Omhv+NpbgVq+JhUxtMqU408OSzYke75J1RTwH5d0="
		vectorSignatureB64 = "MEUCIQDDEuePW+/ce5/R/9MySwoCKr4MhcNv3B+MqbscMMkYaQIgGZUUEYi2qyw/" +
			"UJKTGFepdsLBlijYeSxpEwOINsK5bTc="
	)

	f := setupBiometricService(t)
	ctx := context.Background()

	// The vector fixes the challenge, so it is planted directly rather than
	// drawn at random by CreateChallenge.
	challenge, err := base64.StdEncoding.DecodeString(vectorChallengeB64)
	if err != nil {
		t.Fatalf("decode vector challenge: %v", err)
	}
	challengeID := "vector-challenge"
	f.challengeCache.store[challengeID] = &auth.ChallengeData{
		Challenge: challenge,
		DeviceID:  f.deviceIDStr,
	}

	keyID := "vector-key"
	f.bioKeyRepo.keys[keyID] = &auth.BiometricKey{
		ID:            uuid.New(),
		UserID:        f.userID,
		DeviceID:      f.deviceID,
		KeyID:         keyID,
		PublicKey:     vectorPublicKeyB64, // base64 SPKI, no PEM header
		BiometricType: "FINGERPRINT",
		IsActive:      true,
	}

	// signed_challenge, the field name the spec documents — not `signature`.
	resp, err := f.svc.LoginByBiometric(ctx, auth.BiometricLoginRequest{
		DeviceID:    f.deviceIDStr,
		KeyID:       keyID,
		ChallengeID: challengeID,
		SignedChall: vectorSignatureB64,
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("published vector must verify: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("expected a session for the published vector")
	}
}

// A re-enrolled fingerprint invalidates the Keystore key, so the app registers a
// new one. The old key must stop working in the same step, or a key that can
// never sign again stays a valid credential on that device.
func TestRegisterBiometricKey_ReplacesPreviousKeyOnSameDevice(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	_, firstPub := generateTestKeyPair(t)
	if _, err := f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
		KeyID:         "old-key",
		PublicKey:     firstPub,
		BiometricType: "FINGERPRINT",
	}); err != nil {
		t.Fatalf("first register failed: %v", err)
	}

	_, secondPub := generateTestKeyPair(t)
	resp, err := f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
		KeyID:         "new-key",
		PublicKey:     secondPub,
		BiometricType: "FINGERPRINT",
	})
	if err != nil {
		t.Fatalf("re-register failed: %v", err)
	}
	if resp.ReplacedKeys != 1 {
		t.Fatalf("expected 1 replaced key, got %d", resp.ReplacedKeys)
	}
	if old := f.bioKeyRepo.keys["old-key"]; old == nil || old.IsActive {
		t.Fatal("the previous key must be revoked by re-registration")
	}
	if fresh := f.bioKeyRepo.keys["new-key"]; fresh == nil || !fresh.IsActive {
		t.Fatal("the new key must be active")
	}
}

// An RSA key parses fine and then fails every login, where it reads as a broken
// fingerprint. It is rejected at registration instead.
func TestRegisterBiometricKey_RejectsNonP256Key(t *testing.T) {
	f := setupBiometricService(t)
	ctx := context.Background()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal rsa public key: %v", err)
	}

	_, err = f.svc.RegisterBiometricKey(ctx, f.userID, f.deviceIDStr, auth.BiometricRegisterRequest{
		KeyID:         "rsa-key",
		PublicKey:     base64.StdEncoding.EncodeToString(der),
		BiometricType: "FINGERPRINT",
	})
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != apperr.BiometricKeyUnsupported.Code {
		t.Fatalf("expected AUTH_BIOMETRIC_KEY_UNSUPPORTED, got: %v", err)
	}
}

// The challenge response carries the contract the client has to implement, so a
// change there is a change to a published contract.
func TestCreateChallenge_PublishesAlgorithm(t *testing.T) {
	f := setupBiometricService(t)

	resp, err := f.svc.CreateChallenge(context.Background(), auth.BiometricChallengeRequest{
		DeviceID: f.deviceIDStr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Algorithm != auth.BiometricSignatureAlgorithm {
		t.Fatalf("expected algorithm %s, got %s", auth.BiometricSignatureAlgorithm, resp.Algorithm)
	}
	if resp.ExpiresAt.IsZero() || resp.ExpiresIn != 60 {
		t.Fatalf("expected expires_in 60 and a non-zero expires_at, got %d / %v", resp.ExpiresIn, resp.ExpiresAt)
	}
	if raw, err := base64.StdEncoding.DecodeString(resp.Challenge); err != nil || len(raw) != 32 {
		t.Fatalf("challenge must be base64 of 32 bytes, got %d bytes (err=%v)", len(raw), err)
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
