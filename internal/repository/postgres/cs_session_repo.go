package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
)

// CSAgentSessionRepo mengelola sesi petugas CS.
type CSAgentSessionRepo struct {
	pool *pgxpool.Pool
}

func NewCSAgentSessionRepo(pool *pgxpool.Pool) *CSAgentSessionRepo {
	return &CSAgentSessionRepo{pool: pool}
}

// Create menutup sesi hidup petugas lalu membuka yang baru, dalam SATU transaksi.
//
// Keduanya harus atomik: sesi lama yang tertutup tanpa sesi baru terbuka akan membuat
// petugas kehilangan gilirannya karena kegagalan di tengah jalan — dan ia tidak bisa
// masuk lagi sampai tenggat sesi lama lewat.
//
// Sesi lama ditutup SUPERSEDED, bukan ditolak: petugas yang terminalnya mati tanpa logout
// harus bisa masuk dari loket lain.
func (r *CSAgentSessionRepo) Create(ctx context.Context, sess *cs.AgentSession, tokenHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create agent session: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Error("rollback gagal", "op", "create agent session", "error", rbErr)
		}
	}()

	if _, err := tx.Exec(ctx, `
		UPDATE cs_agent_sessions
		SET ended_at = $2, ended_reason = 'SUPERSEDED'
		WHERE employee_id = $1 AND ended_at IS NULL`,
		sess.EmployeeID, sess.StartedAt,
	); err != nil {
		return fmt.Errorf("supersede previous agent session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO cs_agent_sessions
			(id, token_hash, employee_id, terminal_id, shift, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.ID, tokenHash, sess.EmployeeID, sess.TerminalID,
		nullableText(sess.Shift), sess.StartedAt, sess.ExpiresAt,
	); err != nil {
		return fmt.Errorf("insert agent session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create agent session: %w", err)
	}
	return nil
}

// FindByToken mengembalikan identitas pembawa token.
//
// Tenggat diperiksa DI SQL (`expires_at > now()`), bukan di aplikasi: jam aplikasi dan
// jam database bisa berbeda, dan yang memegang barisnya adalah database.
//
// Cakupan ikut diambil lewat JOIN, bukan query terpisah: kewenangan yang dibaca dari
// baris berbeda dengan yang mengautentikasi membuka celah waktu antara keduanya — alasan
// yang sama dengan AuthenticateAgent.
func (r *CSAgentSessionRepo) FindByToken(ctx context.Context, tokenHash string) (*cs.SessionContext, error) {
	var sc cs.SessionContext
	err := r.pool.QueryRow(ctx, `
		SELECT s.id, s.employee_id, a.name, a.scopes, s.terminal_id, s.expires_at
		FROM cs_agent_sessions s
		JOIN cs_agents a ON a.employee_id = s.employee_id
		WHERE s.token_hash = $1
		  AND s.ended_at IS NULL
		  AND s.expires_at > now()
		  AND a.is_active`, tokenHash,
	).Scan(&sc.SessionID, &sc.EmployeeID, &sc.Name, &sc.Scopes, &sc.TerminalID, &sc.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Token tidak dikenal, sudah ditutup, tenggatnya lewat, atau petugasnya
			// dinonaktifkan. Keempatnya dijawab sama: pemanggilnya hanya perlu tahu
			// sesinya tidak berlaku.
			return nil, nil
		}
		return nil, fmt.Errorf("find agent session by token: %w", err)
	}
	return &sc, nil
}

func (r *CSAgentSessionRepo) FindLiveByEmployee(ctx context.Context, employeeID string) (*cs.AgentSession, error) {
	var sess cs.AgentSession
	var shift *string

	err := r.pool.QueryRow(ctx, `
		SELECT id, employee_id, terminal_id, shift, started_at, expires_at, last_seen_at
		FROM cs_agent_sessions
		WHERE employee_id = $1 AND ended_at IS NULL AND expires_at > now()`, employeeID,
	).Scan(&sess.ID, &sess.EmployeeID, &sess.TerminalID, &shift,
		&sess.StartedAt, &sess.ExpiresAt, &sess.LastSeenAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find live agent session: %w", err)
	}
	if shift != nil {
		sess.Shift = *shift
	}
	return &sess, nil
}

func (r *CSAgentSessionRepo) End(ctx context.Context, sessionID uuid.UUID, reason string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs_agent_sessions
		SET ended_at = $2, ended_reason = $3
		WHERE id = $1 AND ended_at IS NULL`,
		sessionID, at, reason,
	)
	if err != nil {
		return false, fmt.Errorf("end agent session: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *CSAgentSessionRepo) Touch(ctx context.Context, sessionID uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE cs_agent_sessions SET last_seen_at = $2 WHERE id = $1 AND ended_at IS NULL`,
		sessionID, at,
	)
	if err != nil {
		return fmt.Errorf("touch agent session: %w", err)
	}
	return nil
}

// CSAgentCredentialRepo mengelola kata sandi petugas, terpisah dari kunci API.
type CSAgentCredentialRepo struct {
	pool *pgxpool.Pool
}

func NewCSAgentCredentialRepo(pool *pgxpool.Pool) *CSAgentCredentialRepo {
	return &CSAgentCredentialRepo{pool: pool}
}

func (r *CSAgentCredentialRepo) FindForLogin(ctx context.Context, employeeID string) (*cs.AgentLoginRecord, error) {
	var rec cs.AgentLoginRecord
	var passwordHash *string

	err := r.pool.QueryRow(ctx, `
		SELECT employee_id, name, scopes, password_hash, locked_until
		FROM cs_agents
		WHERE employee_id = $1 AND is_active`, employeeID,
	).Scan(&rec.EmployeeID, &rec.Name, &rec.Scopes, &passwordHash, &rec.LockedUntil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find agent for login: %w", err)
	}
	if passwordHash != nil {
		rec.PasswordHash = *passwordHash
	}
	return &rec, nil
}

// SetPassword menyimpan hash dan MENGOSONGKAN lockout.
//
// Lockout dibersihkan bersamaan: petugas yang baru menyetel kata sandi tidak boleh
// langsung tertolak oleh penghitung gagal dari kata sandi lamanya.
func (r *CSAgentCredentialRepo) SetPassword(ctx context.Context, employeeID, passwordHash string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs_agents
		SET password_hash = $2, password_set_at = $3,
		    failed_login_attempts = 0, locked_until = NULL, updated_at = $3
		WHERE employee_id = $1 AND is_active`,
		employeeID, passwordHash, at,
	)
	if err != nil {
		return fmt.Errorf("set agent password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent not found or inactive")
	}
	return nil
}

// RecordLoginFailure menaikkan penghitung dan memasang lockout saat ambangnya lewat.
//
// Kenaikan dan pemasangan lockout dalam SATU UPDATE: dua pernyataan terpisah akan
// membiarkan dua permintaan bersamaan sama-sama membaca penghitung lama, dan ambangnya
// tidak pernah tercapai.
func (r *CSAgentCredentialRepo) RecordLoginFailure(
	ctx context.Context, employeeID string, max int, lockFor time.Duration, now time.Time,
) (*time.Time, error) {
	var lockedUntil *time.Time
	err := r.pool.QueryRow(ctx, `
		UPDATE cs_agents
		SET failed_login_attempts = failed_login_attempts + 1,
		    locked_until = CASE
		        WHEN failed_login_attempts + 1 >= $2 THEN $3::timestamptz
		        ELSE locked_until
		    END,
		    updated_at = $4
		WHERE employee_id = $1 AND is_active
		RETURNING locked_until`,
		employeeID, max, now.Add(lockFor), now,
	).Scan(&lockedUntil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("record agent login failure: %w", err)
	}
	return lockedUntil, nil
}

func (r *CSAgentCredentialRepo) ClearLoginFailures(ctx context.Context, employeeID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE cs_agents
		SET failed_login_attempts = 0, locked_until = NULL
		WHERE employee_id = $1 AND failed_login_attempts > 0`, employeeID)
	if err != nil {
		return fmt.Errorf("clear agent login failures: %w", err)
	}
	return nil
}
