package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find biometric key: %w", err)
	}
	return &k, nil
}

// Create inserts a new biometric key row.
//
// key_id is UNIQUE across the whole table, and Android may re-create a key
// under the same Keystore alias after a biometric re-enrollment. A plain INSERT
// therefore failed for exactly the case re-registration exists to handle, so a
// collision on the SAME user and device updates the row instead. A collision
// with anyone else is not resolved silently: it returns an error, because the
// alternative is one nasabah overwriting another nasabah's login key.
func (r *BiometricRepo) Create(ctx context.Context, key *auth.BiometricKey) error {
	query := `
		INSERT INTO biometric_keys
			(id, user_id, device_id, key_id, public_key, biometric_type, attestation)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (key_id) DO UPDATE
		SET public_key     = EXCLUDED.public_key,
		    biometric_type = EXCLUDED.biometric_type,
		    attestation    = EXCLUDED.attestation,
		    is_active      = TRUE,
		    revoked_at     = NULL,
		    created_at     = NOW()
		WHERE biometric_keys.user_id = EXCLUDED.user_id
		  AND biometric_keys.device_id = EXCLUDED.device_id
		RETURNING id`

	var id uuid.UUID
	err := r.pool.QueryRow(ctx, query,
		key.ID, key.UserID, key.DeviceID, key.KeyID, key.PublicKey,
		key.BiometricType, key.Attestation,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The DO UPDATE was filtered out: key_id belongs to another
			// user/device pair.
			return fmt.Errorf("create biometric key: key_id already registered elsewhere")
		}
		return fmt.Errorf("create biometric key: %w", err)
	}
	// An updated row keeps its original id; the caller reports the id that is
	// actually stored.
	key.ID = id
	return nil
}

// RevokeByUserDevice deactivates every active key for one user on one device.
func (r *BiometricRepo) RevokeByUserDevice(ctx context.Context, userID, deviceID uuid.UUID) (int, error) {
	query := `
		UPDATE biometric_keys
		SET is_active = FALSE, revoked_at = NOW()
		WHERE user_id = $1 AND device_id = $2
		  AND is_active = TRUE AND revoked_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, userID, deviceID)
	if err != nil {
		return 0, fmt.Errorf("revoke biometric keys: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
