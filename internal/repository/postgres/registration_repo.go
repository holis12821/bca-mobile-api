package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type RegistrationExecutor struct {
	pool *pgxpool.Pool
}

func NewRegistrationExecutor(pool *pgxpool.Pool) *RegistrationExecutor {
	return &RegistrationExecutor{pool: pool}
}

// CreateUserAndAccount creates a user, account, device binding, and default limits in one transaction.
func (e *RegistrationExecutor) CreateUserAndAccount(ctx context.Context, params registration.CreateUserParams) (*registration.CreateUserResult, error) {
	pgxTx, err := e.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer pgxTx.Rollback(ctx)

	userID := uuid.New()
	now := time.Now().UTC()

	// Create user
	_, err = pgxTx.Exec(ctx, `
		INSERT INTO users (id, full_name, pin_hash, phone_number, email,
			status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $6)`,
		userID, params.FullName, params.PINHash, params.PhoneNumber, params.Email, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, apperr.RegistrationDuplicate
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	// Generate account number (10-digit, starting with 1)
	accountNumber := fmt.Sprintf("1%09d", rand.IntN(1000000000))
	accountID := uuid.New()

	_, err = pgxTx.Exec(ctx, `
		INSERT INTO accounts (id, user_id, account_number, account_type,
			account_label, balance, currency, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'SAVINGS', 'Tabungan', 0, 'IDR', 'ACTIVE', $4, $4)`,
		accountID, userID, accountNumber, now)
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}

	// Register device
	_, err = pgxTx.Exec(ctx, `
		INSERT INTO devices (id, user_id, device_id, device_model, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		uuid.New(), userID, params.DeviceID, params.DeviceModel, now)
	if err != nil {
		return nil, fmt.Errorf("insert device: %w", err)
	}

	// Insert default transaction limits (trigger/migration 000009 may handle this,
	// but we ensure they exist)
	limits := []struct {
		Type  string
		Daily int64
	}{
		{"TRANSFER_INTERNAL", 50000000},
		{"TRANSFER_EXTERNAL", 25000000},
		{"EWALLET", 20000000},
		{"QRIS", 5000000},
	}
	for _, l := range limits {
		_, err = pgxTx.Exec(ctx, `
			INSERT INTO transaction_limits (id, user_id, limit_type, daily_limit, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (user_id, limit_type) DO NOTHING`,
			uuid.New(), userID, l.Type, l.Daily, now)
		if err != nil {
			return nil, fmt.Errorf("insert limit %s: %w", l.Type, err)
		}
	}

	if err := pgxTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &registration.CreateUserResult{
		UserID:        userID.String(),
		AccountNumber: accountNumber,
	}, nil
}