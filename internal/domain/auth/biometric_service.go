package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

const (
	challengeBytes  = 32
	challengeTTLSec = 60

	AuditBiometricLogin    = "AUTH_BIOMETRIC_LOGIN"
	AuditBiometricRegister = "AUTH_BIOMETRIC_REGISTER"
)

// CreateChallenge generates a 32-byte random challenge and stores it in Redis.
func (s *Service) CreateChallenge(ctx context.Context, req BiometricChallengeRequest) (*BiometricChallengeResponse, error) {
	if req.DeviceID == "" {
		return nil, apperr.ValidationError
	}

	// Verify device exists
	device, err := s.devices.FindActiveByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("find device: %w", err)
	}
	if device == nil {
		return nil, apperr.DeviceNotRecognized
	}

	// Generate 32 random bytes
	challenge := make([]byte, challengeBytes)
	if _, err := rand.Read(challenge); err != nil {
		return nil, fmt.Errorf("generate challenge: %w", err)
	}

	challengeID := uuid.New().String()

	if s.biometricChallenge == nil {
		return nil, apperr.InternalError
	}

	if err := s.biometricChallenge.StoreChallenge(ctx, challengeID, &ChallengeData{
		Challenge: challenge,
		DeviceID:  req.DeviceID,
	}); err != nil {
		return nil, fmt.Errorf("store challenge: %w", err)
	}

	return &BiometricChallengeResponse{
		ChallengeID: challengeID,
		Challenge:   base64.StdEncoding.EncodeToString(challenge),
		ExpiresIn:   challengeTTLSec,
	}, nil
}

// LoginByBiometric verifies a biometric signature against a stored public key.
//
// Flow:
//  1. Consume challenge atomically (GETDEL)
//  2. Verify device_id matches challenge's device_id
//  3. Find biometric key by key_id
//  4. Verify key_id belongs to the SAME device as the challenge
//  5. Verify signature against the stored public key
//  6. Resolve user and create session
func (s *Service) LoginByBiometric(ctx context.Context, req BiometricLoginRequest, clientIP string) (*LoginResponse, error) {
	if req.DeviceID == "" || req.KeyID == "" || req.ChallengeID == "" || req.Signature == "" {
		return nil, apperr.ValidationError
	}

	// 1. Consume challenge atomically via GETDEL
	if s.biometricChallenge == nil {
		return nil, apperr.InternalError
	}
	challengeData, err := s.biometricChallenge.ConsumeChallenge(ctx, req.ChallengeID)
	if err != nil {
		return nil, fmt.Errorf("consume challenge: %w", err)
	}
	if challengeData == nil {
		// Challenge not found — expired or already consumed
		return nil, apperr.TokenInvalid
	}

	// 2. Verify device_id matches
	if challengeData.DeviceID != req.DeviceID {
		return nil, apperr.DeviceNotRecognized
	}

	// 3. Find biometric key
	if s.biometricKeys == nil {
		return nil, apperr.InternalError
	}
	bioKey, err := s.biometricKeys.FindActiveByKeyID(ctx, req.KeyID)
	if err != nil {
		return nil, fmt.Errorf("find biometric key: %w", err)
	}
	if bioKey == nil {
		return nil, apperr.BiometricNotRegistered
	}

	// 4. Verify key_id belongs to the same device as the challenge.
	// This prevents an attacker from using a key registered on device A
	// to authenticate on device B.
	device, err := s.devices.FindActiveByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("find device: %w", err)
	}
	if device == nil {
		return nil, apperr.DeviceNotRecognized
	}
	if bioKey.DeviceID != device.ID {
		slog.Warn("biometric key_id device mismatch",
			"key_device_id", bioKey.DeviceID,
			"request_device_id", device.ID,
		)
		return nil, apperr.BiometricNotRegistered
	}

	// 5. Verify signature
	sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return nil, apperr.TokenInvalid
	}

	if err := verifySignature(bioKey.PublicKey, challengeData.Challenge, sigBytes); err != nil {
		slog.Warn("biometric signature verification failed", "error", err, "key_id", req.KeyID)
		return nil, apperr.TokenInvalid
	}

	// 6. Create session (reuse the successful login path)
	user, err := s.users.FindByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if user == nil {
		return nil, apperr.DeviceNotRecognized
	}

	// Check lockout
	lockStatus, err := s.lockout.IsLocked(ctx, user.ID.String())
	if err != nil {
		return nil, fmt.Errorf("check lockout: %w", err)
	}
	if lockStatus.Locked {
		return nil, apperr.Error{
			Status:  423,
			Code:    apperr.AccountLocked.Code,
			Message: apperr.AccountLocked.Message,
			Details: map[string]any{"locked_until": lockStatus.LockedUntil.UTC().Format(time.RFC3339)},
		}
	}

	resp, err := s.createBiometricSession(ctx, user, device, bioKey.BiometricType, clientIP)
	if err != nil {
		return nil, err
	}

	// Audit
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &user.ID,
			Action:       AuditBiometricLogin,
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata: map[string]any{
				"device_id":      req.DeviceID,
				"key_id":         req.KeyID,
				"biometric_type": bioKey.BiometricType,
			},
		})
	}

	return resp, nil
}

// RegisterBiometricKey registers a new biometric public key for the authenticated user.
func (s *Service) RegisterBiometricKey(ctx context.Context, userID uuid.UUID, deviceID string, req BiometricRegisterRequest) error {
	if req.KeyID == "" || req.PublicKey == "" || req.BiometricType == "" {
		return apperr.ValidationError
	}
	if req.BiometricType != "FINGERPRINT" && req.BiometricType != "FACE_ID" {
		return apperr.ValidationError
	}

	// Validate the public key is parseable
	if _, err := parsePublicKey(req.PublicKey); err != nil {
		return apperr.ValidationError
	}

	// Find device
	device, err := s.devices.FindActiveByDeviceID(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("find device: %w", err)
	}
	if device == nil {
		return apperr.DeviceNotRecognized
	}

	if s.biometricKeys == nil {
		return apperr.InternalError
	}

	var attestation *string
	if req.Attestation != "" {
		attestation = &req.Attestation
	}

	key := &BiometricKey{
		ID:            uuid.New(),
		UserID:        userID,
		DeviceID:      device.ID,
		KeyID:         req.KeyID,
		PublicKey:     req.PublicKey,
		BiometricType: req.BiometricType,
		Attestation:   attestation,
		IsActive:      true,
		CreatedAt:     time.Now(),
	}

	if err := s.biometricKeys.Create(ctx, key); err != nil {
		return fmt.Errorf("create biometric key: %w", err)
	}

	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			Action:       AuditBiometricRegister,
			ResourceType: "biometric_key",
			ResourceID:   key.ID.String(),
			Metadata: map[string]any{
				"device_id":      deviceID,
				"key_id":         req.KeyID,
				"biometric_type": req.BiometricType,
			},
		})
	}

	return nil
}

func (s *Service) createBiometricSession(ctx context.Context, user *User, device *Device, authMethod, clientIP string) (*LoginResponse, error) {
	_ = s.devices.UpdateLastActive(ctx, device.ID)

	sessionID := uuid.New()
	tokenPair, err := s.jwtMgr.GenerateTokenPair(user.ID.String(), sessionID.String(), device.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("generate tokens: %w", err)
	}

	refreshHash := hashBioToken(tokenPair.RefreshToken)

	session := &Session{
		ID:               sessionID,
		UserID:           user.ID,
		DeviceID:         device.ID,
		RefreshTokenHash: refreshHash,
		IPAddress:        clientIP,
		AuthMethod:       authMethod,
		ExpiresAt:        time.Now().Add(s.refreshTTL),
		CreatedAt:        time.Now(),
	}

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	if s.sessionCache != nil {
		if err := s.sessionCache.StoreSession(ctx, session, user.DisplayName, authMethod); err != nil {
			slog.Error("cache biometric session failed", "error", err)
		}
		if err := s.sessionCache.AddToUserSessions(ctx, user.ID, device.DeviceID); err != nil {
			slog.Error("add biometric user session failed", "error", err)
		}
	}

	return &LoginResponse{
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.accessTTL.Seconds()),
		User: UserInfo{
			ID:          user.ID.String(),
			DisplayName: user.DisplayName,
			FullName:    user.FullName,
		},
	}, nil
}

// verifySignature verifies an ECDSA P-256 signature over SHA-256(challenge).
func verifySignature(pubKeyPEM string, challenge, signature []byte) error {
	pubKey, err := parsePublicKey(pubKeyPEM)
	if err != nil {
		return err
	}

	hash := sha256.Sum256(challenge)

	switch key := pubKey.(type) {
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(key, hash[:], signature) {
			// Try raw R||S format (64 bytes for P-256)
			if len(signature) == 64 {
				r := new(big.Int).SetBytes(signature[:32])
				sVal := new(big.Int).SetBytes(signature[32:])
				if !ecdsa.Verify(key, hash[:], r, sVal) {
					return fmt.Errorf("ecdsa signature invalid")
				}
				return nil
			}
			return fmt.Errorf("ecdsa signature invalid")
		}
		return nil
	default:
		return fmt.Errorf("unsupported key type: %T", pubKey)
	}
}

func parsePublicKey(pemStr string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		// Try parsing as raw base64 DER
		der, err := base64.StdEncoding.DecodeString(pemStr)
		if err != nil {
			return nil, fmt.Errorf("invalid public key format")
		}
		return x509.ParsePKIXPublicKey(der)
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

func hashBioToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ExportPublicKeyPEM exports an ECDSA public key to PEM format.
func ExportPublicKeyPEM(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), nil
}