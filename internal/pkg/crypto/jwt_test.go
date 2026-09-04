package crypto_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func newTestJWTManager(t *testing.T) *crypto.JWTManager {
	t.Helper()
	kp := generateTestRSAKeyPair(t)
	return crypto.NewJWTManager(kp, 15*time.Minute, 168*time.Hour)
}

func TestJWT_GenerateAndVerifyAccessToken(t *testing.T) {
	m := newTestJWTManager(t)
	pair, err := m.GenerateTokenPair("user-123", "sess-456", "dev-789")
	require.NoError(t, err)

	claims, err := m.VerifyToken(pair.AccessToken, crypto.TokenTypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "user-123", claims.Subject)
	assert.Equal(t, "sess-456", claims.SessionID)
	assert.Equal(t, "dev-789", claims.DeviceID)
	assert.Equal(t, crypto.TokenTypeAccess, claims.Type)
}

func TestJWT_GenerateAndVerifyRefreshToken(t *testing.T) {
	m := newTestJWTManager(t)
	pair, err := m.GenerateTokenPair("user-123", "sess-456", "dev-789")
	require.NoError(t, err)

	claims, err := m.VerifyToken(pair.RefreshToken, crypto.TokenTypeRefresh)
	require.NoError(t, err)
	assert.Equal(t, "user-123", claims.Subject)
	assert.Equal(t, crypto.TokenTypeRefresh, claims.Type)
	assert.NotEmpty(t, claims.ID, "refresh token must have jti")
}

func TestJWT_AccessTokenRejectedAsRefresh(t *testing.T) {
	m := newTestJWTManager(t)
	pair, err := m.GenerateTokenPair("user-123", "sess-456", "dev-789")
	require.NoError(t, err)

	_, err = m.VerifyToken(pair.AccessToken, crypto.TokenTypeRefresh)
	assert.Error(t, err, "access token must be rejected when verified as refresh")
	assert.Contains(t, err.Error(), "type mismatch")
}

func TestJWT_RefreshTokenRejectedAsAccess(t *testing.T) {
	m := newTestJWTManager(t)
	pair, err := m.GenerateTokenPair("user-123", "sess-456", "dev-789")
	require.NoError(t, err)

	_, err = m.VerifyToken(pair.RefreshToken, crypto.TokenTypeAccess)
	assert.Error(t, err, "refresh token must be rejected when verified as access")
	assert.Contains(t, err.Error(), "type mismatch")
}

func TestJWT_ExpiredToken(t *testing.T) {
	kp := generateTestRSAKeyPair(t)
	m := crypto.NewJWTManager(kp, 1*time.Millisecond, 168*time.Hour)

	pair, err := m.GenerateTokenPair("user-123", "sess-456", "dev-789")
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	_, err = m.VerifyToken(pair.AccessToken, crypto.TokenTypeAccess)
	assert.Error(t, err, "expired token should be rejected")
}

func TestJWT_WrongKeyPair(t *testing.T) {
	m1 := newTestJWTManager(t)
	m2 := newTestJWTManager(t) // different key pair

	pair, err := m1.GenerateTokenPair("user-123", "sess-456", "dev-789")
	require.NoError(t, err)

	_, err = m2.VerifyToken(pair.AccessToken, crypto.TokenTypeAccess)
	assert.Error(t, err, "token from a different key pair should be rejected")
}