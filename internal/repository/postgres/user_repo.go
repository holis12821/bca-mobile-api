package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

// FindByDeviceID resolves a user from an active (non-revoked) device_id.
// The partial unique index idx_devices_device_id_active guarantees at most
// one active row per device_id.
func (r *UserRepo) FindByDeviceID(ctx context.Context, deviceID string) (*auth.User, error) {
	query := `
		SELECT u.id, u.full_name, u.display_name, u.pin_hash, u.pin_salt,
		       u.status, u.locked_until, u.failed_pin_attempts, u.max_pin_attempts,
		       u.last_login_at
		FROM users u
		JOIN devices d ON d.user_id = u.id
		WHERE d.device_id = $1
		  AND d.revoked_at IS NULL
		  AND u.deleted_at IS NULL`

	var user auth.User
	err := r.pool.QueryRow(ctx, query, deviceID).Scan(
		&user.ID, &user.FullName, &user.DisplayName,
		&user.PINHash, &user.PINSalt,
		&user.Status, &user.LockedUntil,
		&user.FailedPINAttempts, &user.MaxPINAttempts,
		&user.LastLoginAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find user by device_id: %w", err)
	}
	return &user, nil
}

// IncrementFailedAttempts atomically increments failed_pin_attempts
// and returns the NEW value. This is the correct approach — branching on
// a stale in-memory value (user.FailedPINAttempts + 1) is a race that
// lets attempt 6 and 7 through.
func (r *UserRepo) IncrementFailedAttempts(ctx context.Context, userID uuid.UUID) (int, error) {
	query := `
		UPDATE users
		SET failed_pin_attempts = failed_pin_attempts + 1
		WHERE id = $1
		RETURNING failed_pin_attempts`

	var newCount int
	err := r.pool.QueryRow(ctx, query, userID).Scan(&newCount)
	if err != nil {
		return 0, fmt.Errorf("increment failed attempts: %w", err)
	}
	return newCount, nil
}

// ResetFailedAttempts sets failed_pin_attempts to 0 and updates last_login_at.
func (r *UserRepo) ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error {
	query := `
		UPDATE users
		SET failed_pin_attempts = 0, last_login_at = $2
		WHERE id = $1`

	_, err := r.pool.Exec(ctx, query, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("reset failed attempts: %w", err)
	}
	return nil
}

// SetLockedUntil sets the locked_until timestamp on the user row.
func (r *UserRepo) SetLockedUntil(ctx context.Context, userID uuid.UUID, lockedUntil *time.Time) error {
	query := `UPDATE users SET locked_until = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, userID, lockedUntil)
	if err != nil {
		return fmt.Errorf("set locked_until: %w", err)
	}
	return nil
}