// Package sms provides the OTP delivery gateway shared by the registration and
// onboarding flows.
//
// There is deliberately no real provider here yet. What matters is that the
// choice between "log it" and "refuse" is made once, from APP_ENV, instead of
// each flow silently wiring a mock that a production deployment would inherit.
package sms

import (
	"context"
	"errors"
	"log/slog"
)

// Gateway sends a one-time password to a phone number.
type Gateway interface {
	SendOTP(ctx context.Context, phone, otp string) error
}

// ErrNotConfigured is returned by the production placeholder. Callers treat a
// send failure as non-fatal (the code is stored; the nasabah can resend), so
// this surfaces as a loud log line and an OTP that never arrives — which is
// the honest outcome when no provider is configured.
var ErrNotConfigured = errors.New("sms gateway is not configured")

// MockGateway logs the OTP instead of sending it. Development and test only.
type MockGateway struct{}

func NewMockGateway() *MockGateway { return &MockGateway{} }

func (MockGateway) SendOTP(_ context.Context, phone, otp string) error {
	slog.Info("mock sms gateway: OTP sent", "phone", phone, "otp", otp)
	return nil
}

// UnconfiguredGateway refuses to send and never logs the code. This is what a
// production process gets until a real provider is wired in: an OTP printed to
// the application log is an OTP in the log aggregator, readable by everyone
// with dashboard access.
type UnconfiguredGateway struct{}

func NewUnconfiguredGateway() *UnconfiguredGateway { return &UnconfiguredGateway{} }

func (UnconfiguredGateway) SendOTP(_ context.Context, phone, _ string) error {
	slog.Error("sms gateway not configured; OTP was generated but not delivered",
		"phone_suffix", suffix(phone),
	)
	return ErrNotConfigured
}

func suffix(phone string) string {
	if len(phone) <= 4 {
		return phone
	}
	return phone[len(phone)-4:]
}

// For returns the gateway appropriate to the environment.
func For(devMode bool) Gateway {
	if devMode {
		return NewMockGateway()
	}
	return NewUnconfiguredGateway()
}
