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

// UpdateLastActive updates last_active_at on the device.
func (r *DeviceRepo) UpdateLastActive(ctx context.Context, deviceID uuid.UUID) error {
	query := `UPDATE devices SET last_active_at = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, deviceID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("update last active: %w", err)
	}
	return nil
}