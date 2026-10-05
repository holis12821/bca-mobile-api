package auth

import (
	"time"

	"github.com/google/uuid"
)

// User represents a user for authentication purposes.
type User struct {
	ID          uuid.UUID
	FullName    string
	DisplayName string
	PINHash     string
	PINSalt     string

	// AccessCodeHash is the "kode akses" the nasabah chose during onboarding —
	// what m-BCA asks for at login, as distinct from the PIN that authorises a
	// transaction. It was collected, hashed, handed to the provisioner and then
	// dropped, so login could only ever check PINHash. Empty for users created
	// before it was persisted (the seeded demo accounts), and LoginCredential
	// falls back to the PIN for those.
	AccessCodeHash string

	Status            string
	LockedUntil       *time.Time
	FailedPINAttempts int
	MaxPINAttempts    int
	LastLoginAt       *time.Time
}

// LoginCredential returns the hash that POST /auth/login/pin must verify
// against: the access code when the user has one, otherwise the PIN.
func (u *User) LoginCredential() string {
	if u.AccessCodeHash != "" {
		return u.AccessCodeHash
	}
	return u.PINHash
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

	// DeviceKey is the client-supplied device_id — the same value the JWT
	// carries as "did". It is not a database column; it exists so the Redis
	// cache can key on the identifier every caller actually has.
	//
	// The cache used to write session:{user}:{devices.id} but delete
	// session:{user}:{client device_id}. Those never matched, so logout left
	// the cached session in place and InvalidateAllUserSessions deleted keys
	// that had never been written.
	DeviceKey string
}

// LoginRequest is the decoded request body for PIN login.
type LoginRequest struct {
	DeviceID     string     `json:"device_id" validate:"required"`
	PINEncrypted string     `json:"pin_encrypted" validate:"required"`
	DeviceInfo   DeviceInfo `json:"device_info"`

	// EncryptionKeyID names the PIN public key the client encrypted with, the
	// same field onboarding already carries. Optional so builds that predate it
	// keep working; when present and stale the request is rejected as
	// AUTH_PIN_KEY_UNKNOWN instead of as a wrong PIN.
	EncryptionKeyID string `json:"encryption_key_id,omitempty"`
}

// PINEncryptionKey is the published half of the PIN transport key, served by
// GET /auth/pin/public-key. It mirrors GET /onboarding/credentials/public-key
// field for field so a client can use one code path for both.
type PINEncryptionKey struct {
	Algorithm    string `json:"algorithm"`
	KeyID        string `json:"key_id"`
	PublicKeyPEM string `json:"public_key_pem"`
	PayloadShape string `json:"payload_shape"`
	Encoding     string `json:"encoding"`
	MaxSkewSec   int    `json:"max_skew_sec"`
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
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    int      `json:"expires_in"`
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
//
// expires_in and expires_at are both present: the spec documents the second,
// the first implementation returned only the first.
type BiometricChallengeResponse struct {
	ChallengeID string    `json:"challenge_id"`
	Challenge   string    `json:"challenge"` // base64-encoded 32 random bytes
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`

	// Algorithm and SignatureFormat tell the client exactly what to produce.
	// They are constants, but shipping them here means a client never has to
	// guess from prose — the guessing is what butir 2 of the handover document
	// was blocked on.
	Algorithm       string `json:"algorithm"`
	SignatureFormat string `json:"signature_format"`
}

// BiometricLoginRequest is the request body for biometric login.
//
// The signature arrives under either name. The spec documents
// `signed_challenge` and the app was built against it; the first
// implementation here read `signature`. Serving both is why neither client
// sees a VALIDATION_ERROR it cannot explain.
type BiometricLoginRequest struct {
	DeviceID    string `json:"device_id" validate:"required"`
	KeyID       string `json:"key_id" validate:"required"`
	ChallengeID string `json:"challenge_id" validate:"required"`
	Signature   string `json:"signature"`        // base64 DER (ECDSA), alias of signed_challenge
	SignedChall string `json:"signed_challenge"` // spec name for the same value

	// BiometricType is accepted and logged but never trusted: which biometric
	// unlocked the key is the phone's business, and the registered key already
	// records it.
	BiometricType string `json:"biometric_type,omitempty"`
}

// SignatureValue returns the signature regardless of which field carried it.
func (r BiometricLoginRequest) SignatureValue() string {
	if r.Signature != "" {
		return r.Signature
	}
	return r.SignedChall
}

// BiometricRegisterRequest is the request to register a biometric key (protected).
type BiometricRegisterRequest struct {
	KeyID string `json:"key_id" validate:"required"`

	// PublicKey is an EC P-256 SubjectPublicKeyInfo, base64 (no PEM header) or
	// PEM. Nothing else is accepted — see registerable().
	PublicKey     string `json:"public_key" validate:"required"`
	BiometricType string `json:"biometric_type" validate:"required"`

	// Attestation is the base64 Android Key Attestation chain. It is stored,
	// not verified against the Google root — see docs/04-SECURITY.md.
	Attestation string `json:"attestation,omitempty"`

	// DeviceID may be sent for symmetry with the spec's example body. The
	// binding always comes from the access token; a device id in the JSON binds
	// nothing, so a mismatch is rejected rather than honoured.
	DeviceID string `json:"device_id,omitempty"`
}

// BiometricRegisterResponse is returned on successful registration.
type BiometricRegisterResponse struct {
	BiometricID  string    `json:"biometric_id"`
	KeyID        string    `json:"key_id"`
	RegisteredAt time.Time `json:"registered_at"`

	// ReplacedKeys counts keys revoked by this registration. Re-enrolling a
	// fingerprint invalidates the Keystore key, so the app registers again;
	// the previous key for this device is revoked in the same step.
	ReplacedKeys int `json:"replaced_keys"`
}

// ChallengeData is the data stored in Redis for a biometric challenge.
type ChallengeData struct {
	Challenge []byte // raw 32 bytes
	DeviceID  string // device_id that requested the challenge
}

// ChangePINRequest is the request body for POST /auth/pin/change.
type ChangePINRequest struct {
	OldPINEncrypted string `json:"old_pin_encrypted" validate:"required"`
	NewPINEncrypted string `json:"new_pin_encrypted" validate:"required"`

	// EncryptionKeyID: see LoginRequest.
	EncryptionKeyID string `json:"encryption_key_id,omitempty"`
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
