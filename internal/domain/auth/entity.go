package auth

import (
	"time"

	"github.com/google/uuid"
)

// User represents a user for authentication purposes.
type User struct {
	ID                uuid.UUID
	FullName          string
	DisplayName       string
	PINHash           string
	PINSalt           string
	Status            string
	LockedUntil       *time.Time
	FailedPINAttempts int
	MaxPINAttempts    int
	LastLoginAt       *time.Time
}

// Device represents a registered device.
type Device struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	DeviceID     string
	DeviceName   *string
	DeviceModel  *string
	OSVersion    *string
	AppVersion   *string
	IsTrusted    bool
	LastActiveAt *time.Time
	RevokedAt    *time.Time
}

// Session represents a server-side session.
type Session struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	DeviceID         uuid.UUID // FK to devices.id
	RefreshTokenHash string
	IPAddress        string
	UserAgent        string
	AuthMethod       string
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

// LoginRequest is the decoded request body for PIN login.
type LoginRequest struct {
	DeviceID     string     `json:"device_id" validate:"required"`
	PINEncrypted string     `json:"pin_encrypted" validate:"required"`
	DeviceInfo   DeviceInfo `json:"device_info"`
}

// DeviceInfo contains metadata about the client device.
type DeviceInfo struct {
	DeviceName  string `json:"device_name"`
	DeviceModel string `json:"device_model"`
	OSVersion   string `json:"os_version"`
	AppVersion  string `json:"app_version"`
}

// LoginResponse is returned on successful PIN login.
type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	User         UserInfo `json:"user"`
}

// UserInfo is the user profile subset included in login response.
type UserInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	FullName    string `json:"full_name"`
}

// RefreshRequest is the decoded request for token refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// LogoutRequest is the decoded request for logout.
type LogoutRequest struct {
	AllDevices bool `json:"all_devices"`
}

// BiometricKey represents a registered biometric public key.
type BiometricKey struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	DeviceID      uuid.UUID // FK to devices.id
	KeyID         string
	PublicKey     string
	BiometricType string // FINGERPRINT or FACE_ID
	Attestation   *string
	IsActive      bool
	CreatedAt     time.Time
}

// BiometricChallengeRequest is the request to create a biometric challenge.
type BiometricChallengeRequest struct {
	DeviceID string `json:"device_id" validate:"required"`
}

// BiometricChallengeResponse is returned when a challenge is created.
type BiometricChallengeResponse struct {
	ChallengeID string `json:"challenge_id"`
	Challenge   string `json:"challenge"` // base64-encoded 32 random bytes
	ExpiresIn   int    `json:"expires_in"`
}

// BiometricLoginRequest is the request body for biometric login.
type BiometricLoginRequest struct {
	DeviceID    string `json:"device_id" validate:"required"`
	KeyID       string `json:"key_id" validate:"required"`
	ChallengeID string `json:"challenge_id" validate:"required"`
	Signature   string `json:"signature" validate:"required"` // base64-encoded
}

// BiometricRegisterRequest is the request to register a biometric key (protected).
type BiometricRegisterRequest struct {
	KeyID         string `json:"key_id" validate:"required"`
	PublicKey     string `json:"public_key" validate:"required"` // PEM-encoded
	BiometricType string `json:"biometric_type" validate:"required"`
	Attestation   string `json:"attestation,omitempty"`
}

// ChallengeData is the data stored in Redis for a biometric challenge.
type ChallengeData struct {
	Challenge []byte // raw 32 bytes
	DeviceID  string // device_id that requested the challenge
}

// AuditEntry represents a single audit log entry to be written.
type AuditEntry struct {
	UserID       *uuid.UUID
	SessionID    *uuid.UUID
	Action       string
	ResourceType string
	ResourceID   string
	IPAddress    string
	UserAgent    string
	RequestID    string
	OldValues    map[string]any
	NewValues    map[string]any
	Metadata     map[string]any
}