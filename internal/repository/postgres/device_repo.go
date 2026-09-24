package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type DeviceRepo struct {
	pool *pgxpool.Pool
}

func NewDeviceRepo(pool *pgxpool.Pool) *DeviceRepo {
	return &DeviceRepo{pool: pool}
}

// FindActiveByDeviceID finds the active (non-revoked) device row.
func (r *DeviceRepo) FindActiveByDeviceID(ctx context.Context, deviceID string) (*auth.Device, error) {
	query := `
		SELECT id, user_id, device_id, device_name, device_model,
		       os_version, app_version, is_trusted, last_active_at, revoked_at
		FROM devices
		WHERE device_id = $1 AND revoked_at IS NULL`

	var d auth.Device
	err := r.pool.QueryRow(ctx, query, deviceID).Scan(
		&d.ID, &d.UserID, &d.DeviceID,
		&d.DeviceName, &d.DeviceModel,
		&d.OSVersion, &d.AppVersion,
		&d.IsTrusted, &d.LastActiveAt, &d.RevokedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find active device: %w", err)
	}
	return &d, nil
}

// RegisterPushToken stores the FCM token for a device the user owns.
//
// Scoped by user_id as well as device_id: the device id comes from the access
// token, but binding the write to the authenticated user means a token that
// somehow names another user's device still cannot overwrite it.
func (r *DeviceRepo) RegisterPushToken(ctx context.Context, userID uuid.UUID, deviceID, pushToken string) error {
	const query = `
		UPDATE devices
		SET push_token = $3, updated_at = NOW()
		WHERE device_id = $1 AND user_id = $2 AND revoked_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, deviceID, userID, pushToken)
	if err != nil {
		return fmt.Errorf("register push token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// apperr, not a package-local sentinel: a handler that had to import
		// this package to recognise the error would be reaching past the
		// domain layer into a concrete repository.
		return apperr.DeviceNotRecognized
	}
	return nil
}

// ListPushTokens returns the push tokens of a user's active devices.
func (r *DeviceRepo) ListPushTokens(ctx context.Context, userID uuid.UUID) ([]string, error) {
	const query = `
		SELECT push_token FROM devices
		WHERE user_id = $1 AND revoked_at IS NULL AND push_token IS NOT NULL AND push_token <> ''`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list push tokens: %w", err)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan push token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// UpdateLastActive updates last_active_at on the device.
func (r *DeviceRepo) UpdateLastActive(ctx context.Context, deviceID uuid.UUID) error {
	query := `UPDATE devices SET last_active_at = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, deviceID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("update last active: %w", err)
	}
	return nil
}
