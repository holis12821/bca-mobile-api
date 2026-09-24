package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
)

// DummyPINHash is used for constant-time response on unknown devices.
// Verifying against a dummy hash ensures the response timing is comparable
// to a real PIN check, so an attacker cannot distinguish "device not found"
// from "wrong PIN" by measuring latency.
var DummyPINHash string

func init() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := crypto.HashPassword(ctx, "000000", crypto.DefaultArgon2Params)
	if err != nil {
		panic("generate dummy PIN hash: " + err.Error())
	}
	DummyPINHash = h
}

type Service struct {
	users              UserRepository
	devices            DeviceRepository
	sessions           SessionRepository
	sessionCache       SessionCache
	tokenRevocation    TokenRevocationCache
	biometricKeys      BiometricKeyRepository
	biometricChallenge BiometricChallengeCache
	rateLimiter        RateLimiter
	lockout            LockoutChecker
	pinKeys            *crypto.RSAKeyPair
	jwtMgr             *crypto.JWTManager
	nonceCheck         crypto.NonceChecker
	audit              *AuditService
	notifier           *notify.Notifier
	accessTTL          time.Duration
	refreshTTL         time.Duration
}

type ServiceConfig struct {
	Users              UserRepository
	Devices            DeviceRepository
	Sessions           SessionRepository
	SessionCache       SessionCache
	TokenRevocation    TokenRevocationCache
	BiometricKeys      BiometricKeyRepository
	BiometricChallenge BiometricChallengeCache
	RateLimiter        RateLimiter
	Lockout            LockoutChecker
	PINKeys            *crypto.RSAKeyPair
	JWTManager         *crypto.JWTManager
	NonceChecker       crypto.NonceChecker
	Audit              *AuditService
	Notifier           *notify.Notifier
	AccessTTL          time.Duration
	RefreshTTL         time.Duration
}

func NewService(cfg ServiceConfig) *Service {
	refreshTTL := cfg.RefreshTTL
	if refreshTTL == 0 {
		refreshTTL = 168 * time.Hour // 7 days default
	}
	return &Service{
		users:              cfg.Users,
		devices:            cfg.Devices,
		sessions:           cfg.Sessions,
		sessionCache:       cfg.SessionCache,
		tokenRevocation:    cfg.TokenRevocation,
		biometricKeys:      cfg.BiometricKeys,
		biometricChallenge: cfg.BiometricChallenge,
		rateLimiter:        cfg.RateLimiter,
		lockout:            cfg.Lockout,
		pinKeys:            cfg.PINKeys,
		jwtMgr:             cfg.JWTManager,
		nonceCheck:         cfg.NonceChecker,
		audit:              cfg.Audit,
		notifier:           cfg.Notifier,
		accessTTL:          cfg.AccessTTL,
		refreshTTL:         refreshTTL,
	}
}

// LoginByPIN implements the 7-step login flow from §3 skill.
func (s *Service) LoginByPIN(ctx context.Context, req LoginRequest, clientIP string) (*LoginResponse, error) {
	// Step 1: Rate limit by device + by IP
	if err := s.checkRateLimits(ctx, req.DeviceID, clientIP); err != nil {
		return nil, err
	}

	// Step 2: Resolve user from device_id.
	// device_id is unique among active rows (migration 000009 partial index).
	// No active row → 403 AUTH_DEVICE_NOT_RECOGNIZED.
	user, err := s.users.FindByDeviceID(ctx, req.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("find user by device: %w", err)
	}
	if user == nil {
		// Step 6: Unknown device must have comparable timing.
		// Do the Argon2 work against a dummy hash so response latency
		// doesn't leak whether the device_id is registered.
		s.dummyPINVerify(ctx)
		return nil, apperr.DeviceNotRecognized
	}

	// Step 3: Check lockout (Redis + DB)
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
	// Also check DB locked_until (in case Redis was flushed)
	if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
		return nil, apperr.Error{
			Status:  423,
			Code:    apperr.AccountLocked.Code,
			Message: apperr.AccountLocked.Message,
			Details: map[string]any{"locked_until": user.LockedUntil.UTC().Format(time.RFC3339)},
		}
	}

	// Step 4: Decrypt PIN and verify
	payload, err := s.pinKeys.DecryptPIN(req.PINEncrypted)
	if err != nil {
		slog.Warn("pin decrypt failed", "error", err)
		return nil, apperr.InvalidPIN
	}

	// Validate timestamp skew (max 60s)
	if err := crypto.ValidatePINTimestamp(payload, crypto.MaxPINTimestampSkew); err != nil {
		slog.Warn("pin timestamp skew", "error", err)
		return nil, apperr.InvalidPIN
	}

	// Validate nonce (replay protection)
	if s.nonceCheck != nil {
		fresh, err := s.nonceCheck.CheckAndMark(payload.Nonce)
		if err != nil {
			return nil, fmt.Errorf("nonce check: %w", err)
		}
		if !fresh {
			slog.Warn("pin nonce replay", "nonce", payload.Nonce)
			return nil, apperr.InvalidPIN
		}
	}

	// Verify the LOGIN credential with Argon2id.
	//
	// For a user onboarded through /v1/onboarding that is the kode akses they
	// chose; for users created before access_code_hash existed it falls back to
	// the PIN. The transaction PIN is checked separately, by VerifyPIN.
	match, err := crypto.VerifyPassword(ctx, payload.PIN, user.LoginCredential())
	if err != nil {
		return nil, fmt.Errorf("verify pin: %w", err)
	}

	if !match {
		// Step 5: Increment failed attempts using UPDATE ... RETURNING.
		// Branch on the RETURNED value, not stale in-memory value.
		return nil, s.handleFailedPIN(ctx, user, req.DeviceID, clientIP)
	}

	// Step 7: Success — reset attempts, create session, audit
	return s.handleSuccessfulLogin(ctx, user, req, clientIP)
}

func (s *Service) checkRateLimits(ctx context.Context, deviceID, clientIP string) error {
	// Per-device: 5 / 15 min
	result, err := s.rateLimiter.CheckLoginDevice(ctx, deviceID)
	if err != nil {
		// Fail-closed: Redis error → deny
		return apperr.InternalError
	}
	if !result.Allowed {
		return apperr.RateLimitExceeded
	}

	// Per-IP: 20 / 15 min
	// NOT optional — blunts the device_id lockout-DoS attack.
	result, err = s.rateLimiter.CheckLoginIP(ctx, clientIP)
	if err != nil {
		return apperr.InternalError
	}
	if !result.Allowed {
		return apperr.RateLimitExceeded
	}

	return nil
}

// dummyPINVerify burns Argon2 CPU time so that "device not found" and
// "wrong PIN" have comparable response latency.
func (s *Service) dummyPINVerify(ctx context.Context) {
	_, _ = crypto.VerifyPassword(ctx, "000000", DummyPINHash)
}

// handleFailedPIN atomically increments failed_pin_attempts via
// UPDATE ... RETURNING and branches on the returned value.
func (s *Service) handleFailedPIN(ctx context.Context, user *User, deviceID, clientIP string) error {
	newCount, err := s.users.IncrementFailedAttempts(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("increment failed attempts: %w", err)
	}

	if newCount >= user.MaxPINAttempts {
		// Lock the account
		lockStatus, lockErr := s.lockout.LockAccount(ctx, user.ID.String())
		if lockErr != nil {
			slog.Error("failed to lock account", "user_id", user.ID, "error", lockErr)
		}

		// Also set locked_until in DB (backup if Redis is flushed)
		if lockStatus != nil {
			_ = s.users.SetLockedUntil(ctx, user.ID, &lockStatus.LockedUntil)
		}

		details := map[string]any{}
		if lockStatus != nil {
			details["locked_until"] = lockStatus.LockedUntil.UTC().Format(time.RFC3339)
		}

		// Audit: account locked
		if s.audit != nil {
			s.audit.Log(&AuditEntry{
				UserID:       &user.ID,
				Action:       AuditAccountLocked,
				ResourceType: "auth",
				IPAddress:    clientIP,
				Metadata: map[string]any{
					"device_id":    deviceID,
					"locked_until": details["locked_until"],
				},
			})
		}

		return apperr.Error{
			Status:  423,
			Code:    apperr.AccountLocked.Code,
			Message: apperr.AccountLocked.Message,
			Details: details,
		}
	}

	remaining := user.MaxPINAttempts - newCount

	// Audit: login failed
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &user.ID,
			Action:       AuditLoginFailed,
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata: map[string]any{
				"device_id":          deviceID,
				"attempt_number":     newCount,
				"attempts_remaining": remaining,
			},
		})
	}

	return apperr.Error{
		Status:  401,
		Code:    apperr.InvalidPIN.Code,
		Message: apperr.InvalidPIN.Message,
		Details: map[string]any{"attempts_remaining": remaining},
	}
}

func (s *Service) handleSuccessfulLogin(ctx context.Context, user *User, req LoginRequest, clientIP string) (*LoginResponse, error) {
	// Reset failed attempts + update last_login_at
	if err := s.users.ResetFailedAttempts(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("reset failed attempts: %w", err)
	}

	// Find the device row (for FK)
	device, err := s.devices.FindActiveByDeviceID(ctx, req.DeviceID)
	if err != nil || device == nil {
		return nil, fmt.Errorf("find device: %w", err)
	}

	// Update device last_active_at
	_ = s.devices.UpdateLastActive(ctx, device.ID)

	// Create session
	sessionID := uuid.New()
	tokenPair, err := s.jwtMgr.GenerateTokenPair(user.ID.String(), sessionID.String(), req.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("generate tokens: %w", err)
	}

	refreshHash := hashToken(tokenPair.RefreshToken)

	session := &Session{
		ID:               sessionID,
		UserID:           user.ID,
		DeviceID:         device.ID,
		DeviceKey:        req.DeviceID,
		RefreshTokenHash: refreshHash,
		IPAddress:        clientIP,
		AuthMethod:       "PIN",
		// s.refreshTTL, not a hardcoded 168h: the row and the token it belongs
		// to have to expire together, whatever JWT_REFRESH_TOKEN_TTL says.
		ExpiresAt: time.Now().Add(s.refreshTTL),
		CreatedAt: time.Now(),
	}

	// Store in PostgreSQL
	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Store in Redis
	if s.sessionCache != nil {
		if err := s.sessionCache.StoreSession(ctx, session, user.DisplayName, "PIN"); err != nil {
			slog.Error("cache session failed", "error", err)
			// Non-fatal: session exists in DB
		}
		if err := s.sessionCache.AddToUserSessions(ctx, user.ID, req.DeviceID); err != nil {
			slog.Error("add to user sessions failed", "error", err)
		}
		// The live marker the Auth middleware reads. Best-effort: a failure
		// here only means the middleware falls through to Postgres.
		if err := s.sessionCache.MarkActive(ctx, user.ID, sessionID, s.refreshTTL); err != nil {
			slog.Error("mark session active failed", "error", err)
		}
	}

	// Reset device rate limit on success
	_ = s.rateLimiter.ResetLoginDevice(ctx, req.DeviceID)

	// Audit successful login
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &user.ID,
			SessionID:    &sessionID,
			Action:       AuditLoginSuccess,
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata: map[string]any{
				"device_id":   req.DeviceID,
				"auth_method": "PIN",
			},
		})
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

// VerifyPIN verifies the user's PIN for pre-transaction confirmation.
// Returns the user ID on success. Counts toward the lockout counter on failure.
func (s *Service) VerifyPIN(ctx context.Context, userID uuid.UUID, deviceID, pinEncrypted, clientIP string) error {
	// Rate limit PIN verify (shares counter with login)
	if s.rateLimiter != nil {
		result, err := s.rateLimiter.CheckPINVerify(ctx, userID.String())
		if err != nil {
			return apperr.InternalError
		}
		if result != nil && !result.Allowed {
			return apperr.RateLimitExceeded
		}
	}

	// Resolve user
	user, err := s.users.FindByDeviceID(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if user == nil || user.ID != userID {
		s.dummyPINVerify(ctx)
		return apperr.InvalidPIN
	}

	// Check lockout
	lockStatus, err := s.lockout.IsLocked(ctx, user.ID.String())
	if err != nil {
		return fmt.Errorf("check lockout: %w", err)
	}
	if lockStatus.Locked {
		return apperr.AccountLocked
	}

	// Decrypt + verify PIN
	payload, err := s.pinKeys.DecryptPIN(pinEncrypted)
	if err != nil {
		return apperr.InvalidPIN
	}

	if err := crypto.ValidatePINTimestamp(payload, crypto.MaxPINTimestampSkew); err != nil {
		return apperr.InvalidPIN
	}

	if s.nonceCheck != nil {
		fresh, err := s.nonceCheck.CheckAndMark(payload.Nonce)
		if err != nil {
			return fmt.Errorf("nonce check: %w", err)
		}
		if !fresh {
			return apperr.InvalidPIN
		}
	}

	match, err := crypto.VerifyPassword(ctx, payload.PIN, user.PINHash)
	if err != nil {
		return fmt.Errorf("verify pin: %w", err)
	}

	if !match {
		return s.handleFailedPIN(ctx, user, deviceID, clientIP)
	}

	// Audit
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			Action:       "AUTH_PIN_VERIFIED",
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata:     map[string]any{"device_id": deviceID},
		})
	}

	return nil
}

// ChangePIN verifies the old PIN and sets a new one.
//
// The old-PIN check is a PIN check like any other, so it goes through the same
// rate limiter, lockout and nonce replay guards as login. Without them a
// stolen access token gave an attacker an unlimited oracle for guessing the
// PIN, and a captured pin_encrypted blob could be replayed within the 60s
// timestamp window.
func (s *Service) ChangePIN(ctx context.Context, userID uuid.UUID, sessionID *uuid.UUID, req ChangePINRequest, clientIP string) error {
	if s.rateLimiter != nil {
		result, err := s.rateLimiter.CheckPINVerify(ctx, userID.String())
		if err != nil {
			return apperr.InternalError
		}
		if result != nil && !result.Allowed {
			return apperr.RateLimitExceeded
		}
	}

	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if user == nil {
		return apperr.NotFound
	}

	if s.lockout != nil {
		lockStatus, err := s.lockout.IsLocked(ctx, user.ID.String())
		if err != nil {
			return fmt.Errorf("check lockout: %w", err)
		}
		if lockStatus.Locked {
			return apperr.AccountLocked
		}
	}

	// Decrypt old PIN
	oldPayload, err := s.pinKeys.DecryptPIN(req.OldPINEncrypted)
	if err != nil {
		return apperr.OldPINMismatch
	}
	if err := crypto.ValidatePINTimestamp(oldPayload, crypto.MaxPINTimestampSkew); err != nil {
		return apperr.OldPINMismatch
	}
	if s.nonceCheck != nil {
		fresh, err := s.nonceCheck.CheckAndMark(oldPayload.Nonce)
		if err != nil {
			return fmt.Errorf("nonce check: %w", err)
		}
		if !fresh {
			return apperr.OldPINMismatch
		}
	}

	// Verify old PIN
	match, err := crypto.VerifyPassword(ctx, oldPayload.PIN, user.PINHash)
	if err != nil {
		return fmt.Errorf("verify old pin: %w", err)
	}
	if !match {
		// Counts toward the lockout budget, exactly as a failed login does.
		if err := s.handleFailedPIN(ctx, user, "", clientIP); err != nil {
			var appErr apperr.Error
			if errors.As(err, &appErr) && appErr.Status == 423 {
				return err
			}
		}
		return apperr.OldPINMismatch
	}

	// Decrypt new PIN
	newPayload, err := s.pinKeys.DecryptPIN(req.NewPINEncrypted)
	if err != nil {
		return apperr.ValidationError
	}
	if err := crypto.ValidatePINTimestamp(newPayload, crypto.MaxPINTimestampSkew); err != nil {
		return apperr.ValidationError
	}

	// New PIN must differ from old
	if newPayload.PIN == oldPayload.PIN {
		return apperr.Error{
			Status:  422,
			Code:    "VALIDATION_ERROR",
			Message: "PIN baru tidak boleh sama dengan PIN lama.",
		}
	}

	// Hash new PIN
	newHash, err := crypto.HashPassword(ctx, newPayload.PIN, crypto.DefaultArgon2Params)
	if err != nil {
		return fmt.Errorf("hash new pin: %w", err)
	}

	if err := s.users.UpdatePINHash(ctx, userID, newHash); err != nil {
		return fmt.Errorf("update pin hash: %w", err)
	}

	// A successful change clears the failure budget.
	if err := s.users.ResetFailedAttempts(ctx, userID); err != nil {
		slog.Error("reset failed attempts after pin change failed", "error", err)
	}

	// Changing the PIN ends every other session. If the change was made by
	// somebody who should not have been there, the legitimate owner's next
	// action is a re-login; if it was the owner, the other devices re-auth
	// with the new PIN. Either way a stolen token does not outlive the change.
	if err := s.sessions.RevokeByUserID(ctx, userID); err != nil {
		slog.Error("revoke sessions after pin change failed", "error", err)
	}
	if s.sessionCache != nil {
		if err := s.sessionCache.InvalidateAllUserSessions(ctx, userID); err != nil {
			slog.Error("invalidate session cache after pin change failed", "error", err)
		}
		if err := s.sessionCache.RevokeAllActive(ctx, userID); err != nil {
			slog.Error("revoke active sessions after pin change failed", "error", err)
		}
	}

	// Audit
	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			SessionID:    sessionID,
			Action:       "PIN_CHANGED",
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata:     map[string]any{"sessions_revoked": true},
		})
	}

	if s.notifier != nil {
		s.notifier.Security(ctx, userID,
			"Kode akses berhasil diubah",
			"Kode akses m-BCA Anda baru saja diubah. Semua perangkat lain telah dikeluarkan. Jika ini bukan Anda, segera hubungi Halo BCA.",
		)
	}

	return nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ChangeAccessCode replaces the kode akses used at login.
//
// POST /auth/pin/change changes the transaction PIN; this changes the
// credential the app asks for when it opens. They are separate secrets in
// m-BCA, and before access_code_hash was persisted there was no way to change
// the second one at all.
func (s *Service) ChangeAccessCode(ctx context.Context, userID uuid.UUID, sessionID *uuid.UUID, req ChangePINRequest, clientIP string) error {
	if s.rateLimiter != nil {
		result, err := s.rateLimiter.CheckPINVerify(ctx, userID.String())
		if err != nil {
			return apperr.InternalError
		}
		if result != nil && !result.Allowed {
			return apperr.RateLimitExceeded
		}
	}

	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if user == nil {
		return apperr.NotFound
	}

	if s.lockout != nil {
		lockStatus, err := s.lockout.IsLocked(ctx, user.ID.String())
		if err != nil {
			return fmt.Errorf("check lockout: %w", err)
		}
		if lockStatus.Locked {
			return apperr.AccountLocked
		}
	}

	oldPayload, err := s.pinKeys.DecryptPIN(req.OldPINEncrypted)
	if err != nil {
		return apperr.OldPINMismatch
	}
	if err := crypto.ValidatePINTimestamp(oldPayload, crypto.MaxPINTimestampSkew); err != nil {
		return apperr.OldPINMismatch
	}
	if s.nonceCheck != nil {
		fresh, err := s.nonceCheck.CheckAndMark(oldPayload.Nonce)
		if err != nil {
			return fmt.Errorf("nonce check: %w", err)
		}
		if !fresh {
			return apperr.OldPINMismatch
		}
	}

	match, err := crypto.VerifyPassword(ctx, oldPayload.PIN, user.LoginCredential())
	if err != nil {
		return fmt.Errorf("verify old access code: %w", err)
	}
	if !match {
		if err := s.handleFailedPIN(ctx, user, "", clientIP); err != nil {
			var appErr apperr.Error
			if errors.As(err, &appErr) && appErr.Status == 423 {
				return err
			}
		}
		return apperr.OldPINMismatch
	}

	newPayload, err := s.pinKeys.DecryptPIN(req.NewPINEncrypted)
	if err != nil {
		return apperr.ValidationError
	}
	if err := crypto.ValidatePINTimestamp(newPayload, crypto.MaxPINTimestampSkew); err != nil {
		return apperr.ValidationError
	}
	if newPayload.PIN == oldPayload.PIN {
		return apperr.Error{
			Status:  422,
			Code:    "VALIDATION_ERROR",
			Message: "Kode akses baru tidak boleh sama dengan yang lama.",
		}
	}

	newHash, err := crypto.HashPassword(ctx, newPayload.PIN, crypto.DefaultArgon2Params)
	if err != nil {
		return fmt.Errorf("hash new access code: %w", err)
	}
	if err := s.users.UpdateAccessCodeHash(ctx, userID, newHash); err != nil {
		return fmt.Errorf("update access code: %w", err)
	}
	if err := s.users.ResetFailedAttempts(ctx, userID); err != nil {
		slog.Error("reset failed attempts after access code change failed", "error", err)
	}

	// Same reasoning as ChangePIN: the login credential changed, so every
	// session established with the old one ends.
	if err := s.sessions.RevokeByUserID(ctx, userID); err != nil {
		slog.Error("revoke sessions after access code change failed", "error", err)
	}
	if s.sessionCache != nil {
		if err := s.sessionCache.InvalidateAllUserSessions(ctx, userID); err != nil {
			slog.Error("invalidate session cache after access code change failed", "error", err)
		}
		if err := s.sessionCache.RevokeAllActive(ctx, userID); err != nil {
			slog.Error("revoke active sessions after access code change failed", "error", err)
		}
	}

	if s.audit != nil {
		s.audit.Log(&AuditEntry{
			UserID:       &userID,
			SessionID:    sessionID,
			Action:       "ACCESS_CODE_CHANGED",
			ResourceType: "auth",
			IPAddress:    clientIP,
			Metadata:     map[string]any{"sessions_revoked": true},
		})
	}

	if s.notifier != nil {
		s.notifier.Security(ctx, userID,
			"Kode akses berhasil diubah",
			"Kode akses m-BCA Anda baru saja diubah. Semua perangkat lain telah dikeluarkan. Jika ini bukan Anda, segera hubungi Halo BCA.",
		)
	}

	return nil
}
