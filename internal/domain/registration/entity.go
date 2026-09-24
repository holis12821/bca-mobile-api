package registration

import (
	"time"

	"github.com/google/uuid"
)

// Registration represents an in-progress registration (buka rekening).
type Registration struct {
	ID          uuid.UUID
	FullName    string
	NIK         string
	PhoneNumber string
	Email       string
	Status      string // OTP_PENDING, OTP_VERIFIED, DOCUMENTS_UPLOADED, COMPLETED
	// OTPHash is a SHA-256 of the code, never the code itself: this struct is
	// serialised into Redis, and an OTP at rest there is a bearer credential
	// for somebody else's registration.
	OTPHash   string
	OTPExpiry time.Time
	// OTPAttempts counts failures against MaxOTPAttempts. Without it a
	// six-digit code is a 10^6 guess space with no ceiling.
	OTPAttempts int
	Documents   map[string]string // type → file path
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// InitiateRequest is the request for POST /registration/initiate.
type InitiateRequest struct {
	FullName    string `json:"full_name" validate:"required"`
	NIK         string `json:"nik" validate:"required"`
	PhoneNumber string `json:"phone_number" validate:"required"`
	Email       string `json:"email" validate:"required"`
}

// InitiateResponse is returned from POST /registration/initiate.
type InitiateResponse struct {
	RegistrationID string `json:"registration_id"`
	Status         string `json:"status"`
	OTPDestination string `json:"otp_destination"`
	// OTPDebug carries the code itself ONLY when APP_ENV=development. The SMS
	// gateway is a stub that logs, so without this an Android build has no way
	// to finish the flow against a local server. It is omitted entirely
	// everywhere else — see Service.devMode.
	OTPDebug string `json:"otp_debug,omitempty"`
}

// VerifyOTPRequest is the request for POST /registration/verify-otp.
type VerifyOTPRequest struct {
	RegistrationID string `json:"registration_id" validate:"required"`
	OTPCode        string `json:"otp_code" validate:"required"`
}

// VerifyOTPResponse is returned from POST /registration/verify-otp.
type VerifyOTPResponse struct {
	RegistrationID    string `json:"registration_id"`
	Status            string `json:"status"`
	RegistrationToken string `json:"registration_token"`
}

// CompleteRequest is the request for POST /registration/complete.
type CompleteRequest struct {
	PINEncrypted string `json:"pin_encrypted" validate:"required"`
	DeviceID     string `json:"device_id" validate:"required"`
	DeviceModel  string `json:"device_model,omitempty"`
}

// CompleteResponse is returned from POST /registration/complete.
type CompleteResponse struct {
	UserID        string `json:"user_id"`
	AccountNumber string `json:"account_number"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}

// DefaultLimits are the transaction limits every new user starts with.
// Kept here so the registration and onboarding provisioners cannot drift.
var DefaultLimits = []struct {
	Type  string
	Daily int64
}{
	{"TRANSFER_INTERNAL", 50_000_000},
	{"TRANSFER_EXTERNAL", 25_000_000},
	{"EWALLET", 20_000_000},
	{"QRIS", 5_000_000},
}

// MaxOTPAttempts is how many wrong codes a registration tolerates before the
// OTP is burned and the nasabah has to start over.
const MaxOTPAttempts = 5

// Valid document types for upload.
var ValidDocumentTypes = map[string]bool{
	"KTP":        true,
	"SELFIE":     true,
	"KTP_SELFIE": true,
}
