package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

type SessionRepo struct {
	pool *pgxpool.Pool
}

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{pool: pool}
}

// Create inserts a new session row.
func (r *SessionRepo) Create(ctx context.Context, session *auth.Session) error {
	query := `
		INSERT INTO sessions (id, user_id, device_id, refresh_token_hash,
		                      ip_address, auth_method, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.pool.Exec(ctx, query,
		session.ID, session.UserID, session.DeviceID,
		session.RefreshTokenHash,
		session.IPAddress, session.AuthMethod,
		session.ExpiresAt, session.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// FindByRefreshTokenHash finds an active session by its refresh token hash.
// Returns nil, nil if not found.
func (r *SessionRepo) FindByRefreshTokenHash(ctx context.Context, hash string) (*auth.Session, error) {
	query := `
		SELECT id, user_id, device_id, refresh_token_hash,
		       ip_address, auth_method, expires_at, created_at
		FROM sessions
		WHERE refresh_token_hash = $1 AND revoked_at IS NULL`

	var s auth.Session
	var ip *string
	err := r.pool.QueryRow(ctx, query, hash).Scan(
		&s.ID, &s.UserID, &s.DeviceID, &s.RefreshTokenHash,
		&ip, &s.AuthMethod, &s.ExpiresAt, &s.CreatedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("find session by hash: %w", err)
	}
	if ip != nil {
		s.IPAddress = *ip
	}
	return &s, nil
}

// UpdateRefreshToken atomically swaps the refresh token hash and resets expires_at.
func (r *SessionRepo) UpdateRefreshToken(ctx context.Context, sessionID uuid.UUID, newHash string, expiresAt time.Time) error {
	query := `
		UPDATE sessions
		SET refresh_token_hash = $2, expires_at = $3
		WHERE id = $1 AND revoked_at IS NULL`
	tag, err := r.pool.Exec(ctx, query, sessionID, newHash, expiresAt)
	if err != nil {
		return fmt.Errorf("update refresh token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found or already revoked")
	}
	return nil
}

// RevokeByID revokes a single session.
func (r *SessionRepo) RevokeByID(ctx context.Context, sessionID uuid.UUID) error {
	query := `UPDATE sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, query, sessionID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeByUserID revokes all active sessions for a user.
func (r *SessionRepo) RevokeByUserID(ctx context.Context, userID uuid.UUID) error {
	query := `UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, query, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}