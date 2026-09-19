package onboarding

import (
	"context"
	"log/slog"
)

// MockSMSGateway logs OTP to console instead of sending SMS.
// Use in development/staging environments.
type MockSMSGateway struct{}

func NewMockSMSGateway() *MockSMSGateway {
	return &MockSMSGateway{}
}

func (m *MockSMSGateway) SendOTP(_ context.Context, phone, otp string) error {
	slog.Info("mock sms gateway: OTP sent",
		"phone", phone,
		"otp", otp,
	)
	return nil
}