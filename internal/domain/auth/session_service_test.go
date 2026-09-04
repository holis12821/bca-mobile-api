package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// --- Mocks ---

type mockUserRepo struct {
	user *auth.User
}

func (m *mockUserRepo) FindByDeviceID(_ context.Context, _ string) (*auth.User, error) {
	return m.user, nil
}
func (m *mockUserRepo) IncrementFailedAttempts(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}
func (m *mockUserRepo) ResetFailedAttempts(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockUserRepo) SetLockedUntil(_ context.Context, _ uuid.UUID, _ *time.Time) error {
	return nil
}

type mockDeviceRepo struct{}

func (m *mockDeviceRepo) FindActiveByDeviceID(_ context.Context, _ string) (*auth.Device, error) {
	return &auth.Device{ID: uuid.New()}, nil
}
func (m *mockDeviceRepo) UpdateLastActive(_ context.Context, _ uuid.UUID) error { return nil }

type mockSessionRepo struct {
	mu       sync.Mutex
	sessions map[string]*auth.Session // keyed by refresh_token_hash
	revoked  map[uuid.UUID]bool
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		sessions: make(map[string]*auth.Session),
		revoked:  make(map[uuid.UUID]bool),
	}
}

func (m *mockSessionRepo) Create(_ context.Context, s *auth.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.RefreshTokenHash] = s
	return nil
}

func (m *mockSessionRepo) FindByRefreshTokenHash(_ context.Context, hash string) (*auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hash]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *mockSessionRepo) UpdateRefreshToken(_ context.Context, sessionID uuid.UUID, newHash string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, s := range m.sessions {
		if s.ID == sessionID {
			delete(m.sessions, hash)
			s.RefreshTokenHash = newHash
			s.ExpiresAt = expiresAt
			m.sessions[newHash] = s
			return nil
		}
	}
	return errors.New("session not found")
}

func (m *mockSessionRepo) RevokeByID(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[id] = true
	return nil
}

func (m *mockSessionRepo) RevokeByUserID(_ context.Context, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.UserID == userID {
			m.revoked[s.ID] = true
		}
	}
	return nil
}

func (m *mockSessionRepo) isRevoked(id uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revoked[id]
}

type mockSessionCache struct {
	mu    sync.Mutex
	store map[string]bool
}

func newMockSessionCache() *mockSessionCache {
	return &mockSessionCache{store: make(map[string]bool)}
}

func (m *mockSessionCache) StoreSession(_ context.Context, s *auth.Session, _ string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[s.UserID.String()+":"+s.DeviceID.String()] = true
	return nil
}

func (m *mockSessionCache) AddToUserSessions(_ context.Context, userID uuid.UUID, deviceID string) error {
	return nil
}

func (m *mockSessionCache) DeleteSession(_ context.Context, userID uuid.UUID, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, userID.String()+":"+deviceID)
	return nil
}

func (m *mockSessionCache) InvalidateAllUserSessions(_ context.Context, _ uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = make(map[string]bool)
	return nil
}

type mockTokenRevocation struct {
	mu      sync.Mutex
	revoked map[string]bool
}

func newMockTokenRevocation() *mockTokenRevocation {
	return &mockTokenRevocation{revoked: make(map[string]bool)}
}

func (m *mockTokenRevocation) MarkRevoked(_ context.Context, hash string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[hash] = true
	return nil
}

func (m *mockTokenRevocation) IsRevoked(_ context.Context, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revoked[hash], nil
}

type mockRateLimiter struct{}

func (m *mockRateLimiter) CheckLoginDevice(_ context.Context, _ string) (*auth.RateLimitResult, error) {
	return &auth.RateLimitResult{Allowed: true}, nil
}
func (m *mockRateLimiter) CheckLoginIP(_ context.Context, _ string) (*auth.RateLimitResult, error) {
	return &auth.RateLimitResult{Allowed: true}, nil
}
func (m *mockRateLimiter) CheckPINVerify(_ context.Context, _ string) (*auth.RateLimitResult, error) {
	return &auth.RateLimitResult{Allowed: true}, nil
}
func (m *mockRateLimiter) ResetLoginDevice(_ context.Context, _ string) error { return nil }

type mockLockout struct{}

func (m *mockLockout) IsLocked(_ context.Context, _ string) (*auth.LockoutStatus, error) {
	return &auth.LockoutStatus{Locked: false}, nil
}
func (m *mockLockout) LockAccount(_ context.Context, _ string) (*auth.LockoutStatus, error) {
	return &auth.LockoutStatus{Locked: true, LockedUntil: time.Now().Add(30 * time.Minute)}, nil
}
func (m *mockLockout) Unlock(_ context.Context, _ string) error { return nil }

type mockAuditRepo struct {
	mu      sync.Mutex
	entries []*auth.AuditEntry
}

func (m *mockAuditRepo) Insert(_ context.Context, entry *auth.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockAuditRepo) getEntries() []*auth.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*auth.AuditEntry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

// --- Test setup ---

func setupService(t *testing.T) (*auth.Service, *mockSessionRepo, *mockTokenRevocation, *mockAuditRepo, *crypto.JWTManager) {
	t.Helper()

	// Generate RSA keys for JWT
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	kp := &crypto.RSAKeyPair{PrivateKey: privateKey, PublicKey: &privateKey.PublicKey}
	jwtMgr := crypto.NewJWTManager(kp, 15*time.Minute, 7*24*time.Hour)

	userID := uuid.New()
	sessionRepo := newMockSessionRepo()
	tokenRevocation := newMockTokenRevocation()
	auditRepo := &mockAuditRepo{}
	auditService := auth.NewAuditService(auditRepo)
	t.Cleanup(func() { auditService.Close() })

	svc := auth.NewService(auth.ServiceConfig{
		Users: &mockUserRepo{user: &auth.User{
			ID:             userID,
			FullName:       "Test User",
			DisplayName:    "Test",
			PINHash:        "dummy",
			MaxPINAttempts: 5,
		}},
		Devices:         &mockDeviceRepo{},
		Sessions:        sessionRepo,
		SessionCache:    newMockSessionCache(),
		TokenRevocation: tokenRevocation,
		RateLimiter:     &mockRateLimiter{},
		Lockout:         &mockLockout{},
		PINKeys:         kp,
		JWTManager:      jwtMgr,
		Audit:           auditService,
		AccessTTL:       15 * time.Minute,
		RefreshTTL:      7 * 24 * time.Hour,
	})

	return svc, sessionRepo, tokenRevocation, auditRepo, jwtMgr
}

func createTestSession(t *testing.T, jwtMgr *crypto.JWTManager, sessionRepo *mockSessionRepo, userID uuid.UUID) (string, uuid.UUID) {
	t.Helper()
	sessionID := uuid.New()
	deviceID := "test-device-001"

	tokenPair, err := jwtMgr.GenerateTokenPair(userID.String(), sessionID.String(), deviceID)
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}

	refreshHash := hashToken(tokenPair.RefreshToken)
	session := &auth.Session{
		ID:               sessionID,
		UserID:           userID,
		DeviceID:         uuid.New(), // FK to devices table
		RefreshTokenHash: refreshHash,
		AuthMethod:       "PIN",
		ExpiresAt:        time.Now().Add(7 * 24 * time.Hour),
		CreatedAt:        time.Now(),
	}
	if err := sessionRepo.Create(context.Background(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	return tokenPair.RefreshToken, sessionID
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// --- Tests ---

func TestRefreshToken_HappyPath(t *testing.T) {
	svc, sessionRepo, tokenRevocation, auditRepo, jwtMgr := setupService(t)
	ctx := context.Background()

	// Get the user ID from the mock
	userID := uuid.UUID{}
	for _, s := range sessionRepo.sessions {
		userID = s.UserID
		break
	}
	// No sessions yet, create via JWT
	claims := getClaims(t, jwtMgr)
	userID = uuid.MustParse(claims)

	refreshToken, sessionID := createTestSession(t, jwtMgr, sessionRepo, userID)
	oldHash := hashToken(refreshToken)

	resp, err := svc.RefreshToken(ctx, refreshToken, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.AccessToken == "" || resp.RefreshToken == "" {
		t.Fatal("expected non-empty tokens")
	}
	if resp.RefreshToken == refreshToken {
		t.Fatal("new refresh token should differ from old")
	}
	if resp.TokenType != "Bearer" {
		t.Fatalf("expected Bearer, got %s", resp.TokenType)
	}

	// Old hash should be revoked
	if revoked, _ := tokenRevocation.IsRevoked(ctx, oldHash); !revoked {
		t.Fatal("old token hash should be marked revoked")
	}

	// Session should have new hash
	newHash := hashToken(resp.RefreshToken)
	s, _ := sessionRepo.FindByRefreshTokenHash(ctx, newHash)
	if s == nil {
		t.Fatal("session should exist with new hash")
	}
	if s.ID != sessionID {
		t.Fatal("session ID should not change on rotation")
	}

	// Audit should record AUTH_TOKEN_REFRESH
	time.Sleep(50 * time.Millisecond) // let audit worker process
	entries := auditRepo.getEntries()
	found := false
	for _, e := range entries {
		if e.Action == auth.AuditTokenRefresh {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected AUTH_TOKEN_REFRESH audit entry")
	}
}

func TestRefreshToken_AccessTokenRejected(t *testing.T) {
	svc, sessionRepo, _, _, jwtMgr := setupService(t)
	ctx := context.Background()

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)
	createTestSession(t, jwtMgr, sessionRepo, userID)

	// Generate an access token and try to use it for refresh
	accessTokenPair, err := jwtMgr.GenerateTokenPair(userID.String(), uuid.New().String(), "device")
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.RefreshToken(ctx, accessTokenPair.AccessToken, "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for access token on refresh endpoint")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("expected AUTH_TOKEN_INVALID, got: %v", err)
	}
}

func TestRefreshToken_ReuseDetection(t *testing.T) {
	svc, sessionRepo, _, auditRepo, jwtMgr := setupService(t)
	ctx := context.Background()

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)
	refreshToken, sessionID := createTestSession(t, jwtMgr, sessionRepo, userID)

	// First refresh — should succeed
	resp1, err := svc.RefreshToken(ctx, refreshToken, "127.0.0.1")
	if err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
	if resp1 == nil {
		t.Fatal("expected response from first refresh")
	}

	// Second refresh with OLD token — reuse detection
	_, err = svc.RefreshToken(ctx, refreshToken, "192.168.1.100")
	if err == nil {
		t.Fatal("expected error on token reuse")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("expected AUTH_TOKEN_INVALID, got: %v", err)
	}

	// All sessions for this user should be revoked
	if !sessionRepo.isRevoked(sessionID) {
		t.Fatal("session should be revoked after reuse detection")
	}

	// Audit should have SECURITY_SUSPICIOUS_LOGIN
	time.Sleep(50 * time.Millisecond)
	entries := auditRepo.getEntries()
	found := false
	for _, e := range entries {
		if e.Action == auth.AuditSuspiciousLogin {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected SECURITY_SUSPICIOUS_LOGIN audit entry")
	}
}

func TestRefreshToken_ExpiredToken(t *testing.T) {
	svc, sessionRepo, _, _, jwtMgr := setupService(t)
	ctx := context.Background()

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)

	// Create session that's already expired
	sessionID := uuid.New()
	deviceID := "test-device-001"
	tokenPair, _ := jwtMgr.GenerateTokenPair(userID.String(), sessionID.String(), deviceID)
	refreshHash := hashToken(tokenPair.RefreshToken)

	session := &auth.Session{
		ID:               sessionID,
		UserID:           userID,
		DeviceID:         uuid.New(),
		RefreshTokenHash: refreshHash,
		AuthMethod:       "PIN",
		ExpiresAt:        time.Now().Add(-1 * time.Hour), // expired
		CreatedAt:        time.Now().Add(-8 * 24 * time.Hour),
	}
	_ = sessionRepo.Create(ctx, session)

	_, err := svc.RefreshToken(ctx, tokenPair.RefreshToken, "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for expired session")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_TOKEN_EXPIRED" {
		t.Fatalf("expected AUTH_TOKEN_EXPIRED, got: %v", err)
	}
}

func TestRefreshToken_SessionBindingMismatch(t *testing.T) {
	svc, sessionRepo, _, _, jwtMgr := setupService(t)
	ctx := context.Background()

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)

	// Create token with one user, but session belongs to different user
	differentUserID := uuid.New()
	sessionID := uuid.New()
	deviceID := "test-device-001"
	tokenPair, _ := jwtMgr.GenerateTokenPair(userID.String(), sessionID.String(), deviceID)
	refreshHash := hashToken(tokenPair.RefreshToken)

	session := &auth.Session{
		ID:               sessionID,
		UserID:           differentUserID, // different user!
		DeviceID:         uuid.New(),
		RefreshTokenHash: refreshHash,
		AuthMethod:       "PIN",
		ExpiresAt:        time.Now().Add(7 * 24 * time.Hour),
		CreatedAt:        time.Now(),
	}
	_ = sessionRepo.Create(ctx, session)

	_, err := svc.RefreshToken(ctx, tokenPair.RefreshToken, "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for user_id mismatch")
	}
	var appErr apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("expected AUTH_TOKEN_INVALID, got: %v", err)
	}
}

func TestRefreshToken_ConcurrentRace(t *testing.T) {
	svc, sessionRepo, _, _, jwtMgr := setupService(t)

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)
	refreshToken, _ := createTestSession(t, jwtMgr, sessionRepo, userID)

	// Two goroutines race to refresh with the same token
	var wg sync.WaitGroup
	results := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.RefreshToken(context.Background(), refreshToken, "127.0.0.1")
			results <- err
		}()
	}

	wg.Wait()
	close(results)

	var successCount, failCount int
	for err := range results {
		if err == nil {
			successCount++
		} else {
			failCount++
		}
	}

	// At least one should succeed, and at least one might fail
	// (depends on timing — the second sees the hash in revoked or misses it in DB)
	if successCount == 0 {
		t.Fatal("at least one refresh should succeed")
	}
}

func TestLogout(t *testing.T) {
	svc, sessionRepo, _, auditRepo, jwtMgr := setupService(t)
	ctx := context.Background()

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)
	_, sessionID := createTestSession(t, jwtMgr, sessionRepo, userID)

	err := svc.Logout(ctx, sessionID, userID, "test-device-001")
	if err != nil {
		t.Fatalf("logout failed: %v", err)
	}

	if !sessionRepo.isRevoked(sessionID) {
		t.Fatal("session should be revoked")
	}

	time.Sleep(50 * time.Millisecond)
	entries := auditRepo.getEntries()
	found := false
	for _, e := range entries {
		if e.Action == auth.AuditLogout {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected AUTH_LOGOUT audit entry")
	}
}

func TestLogoutAll(t *testing.T) {
	svc, sessionRepo, _, auditRepo, jwtMgr := setupService(t)
	ctx := context.Background()

	claims := getClaims(t, jwtMgr)
	userID := uuid.MustParse(claims)

	// Create multiple sessions
	_, sid1 := createTestSession(t, jwtMgr, sessionRepo, userID)
	_, sid2 := createTestSession(t, jwtMgr, sessionRepo, userID)

	err := svc.LogoutAll(ctx, userID, &sid1)
	if err != nil {
		t.Fatalf("logout all failed: %v", err)
	}

	if !sessionRepo.isRevoked(sid1) || !sessionRepo.isRevoked(sid2) {
		t.Fatal("all sessions should be revoked")
	}

	time.Sleep(50 * time.Millisecond)
	entries := auditRepo.getEntries()
	found := false
	for _, e := range entries {
		if e.Action == auth.AuditLogoutAll {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected AUTH_LOGOUT_ALL audit entry")
	}
}

// getClaims generates a token pair just to extract a consistent userID string
func getClaims(t *testing.T, jwtMgr *crypto.JWTManager) string {
	t.Helper()
	userID := uuid.New()
	return userID.String()
}