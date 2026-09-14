package registration

import (
	"context"
	"time"
)

// RegistrationCache stores registration state in Redis (authoritative).
type RegistrationCache interface {
	Store(ctx context.Context, reg *Registration) error
	Get(ctx context.Context, id string) (*Registration, error)
	Update(ctx context.Context, reg *Registration) error
	Delete(ctx context.Context, id string) error
}

// RegistrationExecutor creates the user + account in a single transaction.
type RegistrationExecutor interface {
	CreateUserAndAccount(ctx context.Context, params CreateUserParams) (*CreateUserResult, error)
}

// CreateUserParams holds everything needed to create a user and account.
type CreateUserParams struct {
	FullName    string
	NIK         string
	PhoneNumber string
	Email       string
	PINHash     string
	DeviceID    string
	DeviceModel string
}

// CreateUserResult is the output of the registration executor.
type CreateUserResult struct {
	UserID        string
	AccountNumber string
}

const (
	OTPCacheTTL      = 5 * time.Minute
	RegistrationTTL  = 30 * time.Minute
	MaxDocSize       = 5 * 1024 * 1024 // 5MB
)