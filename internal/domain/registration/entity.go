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
	OTPCode     string
	OTPExpiry   time.Time
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

// Valid document types for upload.
var ValidDocumentTypes = map[string]bool{
	"KTP":        true,
	"SELFIE":     true,
	"KTP_SELFIE": true,
}