package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

type BiometricRepo struct {
	pool *pgxpool.Pool
}

func NewBiometricRepo(pool *pgxpool.Pool) *BiometricRepo {
	return &BiometricRepo{pool: pool}
}

// FindActiveByKeyID finds an active biometric key by its key_id.
// Returns nil, nil if not found.
func (r *BiometricRepo) FindActiveByKeyID(ctx context.Context, keyID string) (*auth.BiometricKey, error) {
	query := `
		SELECT id, user_id, device_id, key_id, public_key,
		       biometric_type, attestation, is_active, created_at
		FROM biometric_keys
		WHERE key_id = $1 AND is_active = TRUE AND revoked_at IS NULL`

	var k auth.BiometricKey
	err := r.pool.QueryRow(ctx, query, keyID).Scan(
		&k.ID, &k.UserID, &k.DeviceID, &k.KeyID, &k.PublicKey,
		&k.BiometricType, &k.Attestation, &k.IsActive, &k.CreatedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("find biometric key: %w", err)
	}
	return &k, nil
}

// Create inserts a new biometric key row.
func (r *BiometricRepo) Create(ctx context.Context, key *auth.BiometricKey) error {
	query := `
		INSERT INTO biometric_keys
			(id, user_id, device_id, key_id, public_key, biometric_type, attestation)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query,
		key.ID, key.UserID, key.DeviceID, key.KeyID, key.PublicKey,
		key.BiometricType, key.Attestation,
	)
	if err != nil {
		return fmt.Errorf("create biometric key: %w", err)
	}
	return nil
}