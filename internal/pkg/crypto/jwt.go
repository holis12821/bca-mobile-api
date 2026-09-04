package crypto

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenType distinguishes access from refresh tokens.
// Verify MUST check typ — an access token must never validate
// on the refresh endpoint, or vice versa.
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
)

// Claims are the JWT claims for this service.
type Claims struct {
	jwt.RegisteredClaims
	SessionID string    `json:"sid"`
	DeviceID  string    `json:"did"`
	Type      TokenType `json:"typ"`
}

// JWTManager handles RS256 JWT generation and verification.
type JWTManager struct {
	privateKey      *rsa.PrivateKey
	publicKey       *rsa.PublicKey
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

func NewJWTManager(kp *RSAKeyPair, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		privateKey:      kp.PrivateKey,
		publicKey:       kp.PublicKey,
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}
}

// TokenPair is a pair of access + refresh tokens.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

// GenerateTokenPair creates a new access + refresh token pair.
func (m *JWTManager) GenerateTokenPair(userID, sessionID, deviceID string) (*TokenPair, error) {
	now := time.Now().UTC()

	accessToken, err := m.generateToken(Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTokenTTL)),
		},
		SessionID: sessionID,
		DeviceID:  deviceID,
		Type:      TokenTypeAccess,
	})
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", err)
	}

	refreshToken, err := m.generateToken(Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ID:        uuid.New().String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.refreshTokenTTL)),
		},
		SessionID: sessionID,
		DeviceID:  deviceID,
		Type:      TokenTypeRefresh,
	})
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}

	return &TokenPair{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}

// VerifyToken parses and validates a JWT, enforcing the expected typ claim.
func (m *JWTManager) VerifyToken(tokenStr string, expectedType TokenType) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.publicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// typ MUST be checked — an access token must never validate
	// on the refresh endpoint, or vice versa.
	if claims.Type != expectedType {
		return nil, fmt.Errorf("token type mismatch: got %q, want %q", claims.Type, expectedType)
	}

	return claims, nil
}

func (m *JWTManager) generateToken(claims Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(m.privateKey)
}