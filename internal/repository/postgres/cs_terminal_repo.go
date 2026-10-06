package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// CSTerminalRepo adalah akses data terminal dan gerbang kesiapannya.
type CSTerminalRepo struct {
	pool *pgxpool.Pool
}

func NewCSTerminalRepo(pool *pgxpool.Pool) *CSTerminalRepo {
	return &CSTerminalRepo{pool: pool}
}

const selectTerminal = `
	SELECT terminal_id, workstation, location, status,
	       registered_by, registered_at,
	       COALESCE(active_agent_id, ''), activated_at, last_seen_at, updated_at
	FROM cs_terminals`

func scanTerminal(row pgx.Row) (*cs.Terminal, error) {
	var t cs.Terminal
	var status string

	err := row.Scan(
		&t.TerminalID, &t.Workstation, &t.Location, &status,
		&t.RegisteredBy, &t.RegisteredAt,
		&t.ActiveAgentID, &t.ActivatedAt, &t.LastSeenAt, &t.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan terminal: %w", err)
	}
	t.Status = cs.TerminalStatus(status)
	return &t, nil
}

func (r *CSTerminalRepo) Register(ctx context.Context, t *cs.Terminal) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs_terminals
			(terminal_id, workstation, location, status, registered_by, registered_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		t.TerminalID, t.Workstation, t.Location, string(t.Status),
		t.RegisteredBy, t.RegisteredAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return apperr.TerminalAlreadyRegistered
		}
		return fmt.Errorf("insert terminal: %w", err)
	}
	return nil
}

func (r *CSTerminalRepo) FindByID(ctx context.Context, terminalID string) (*cs.Terminal, error) {
	return scanTerminal(r.pool.QueryRow(ctx, selectTerminal+` WHERE terminal_id = $1`, terminalID))
}

// Activate memindahkan terminal ke ONLINE dan mengikatnya ke petugas.
//
// Petugas yang sudah ONLINE di terminal lain dijawab dari pelanggaran
// idx_cs_terminals_one_online_per_agent (23505), BUKAN dari SELECT lebih dulu: dua
// permintaan bersamaan akan sama-sama melihat "belum ada yang online".
func (r *CSTerminalRepo) Activate(ctx context.Context, terminalID, employeeID string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs_terminals
		SET status = 'ONLINE', active_agent_id = $2, activated_at = $3, updated_at = $3
		WHERE terminal_id = $1`,
		terminalID, employeeID, at,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return apperr.TerminalAgentBusy
		}
		return fmt.Errorf("activate terminal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.TerminalNotFound
	}
	return nil
}

// Deactivate melepas ikatan petugas bersama statusnya dalam satu UPDATE.
//
// Harus bersamaan: CHECK cs_terminals_online_has_agent menolak baris ONLINE tanpa
// petugas, jadi mengosongkan active_agent_id lebih dulu akan gagal.
func (r *CSTerminalRepo) Deactivate(ctx context.Context, terminalID string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs_terminals
		SET status = 'OFFLINE', active_agent_id = NULL, activated_at = NULL, updated_at = $2
		WHERE terminal_id = $1 AND status <> 'OFFLINE'`,
		terminalID, at,
	)
	if err != nil {
		return false, fmt.Errorf("deactivate terminal: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *CSTerminalRepo) SetStatus(ctx context.Context, terminalID string, status cs.TerminalStatus, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs_terminals SET status = $2, updated_at = $3 WHERE terminal_id = $1`,
		terminalID, string(status), at,
	)
	if err != nil {
		return fmt.Errorf("set terminal status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.TerminalNotFound
	}
	return nil
}

// UpsertGate mencatat gerbang yang lolos, MENIMPA baris gerbang yang sama.
//
// Upsert, bukan insert: healthcheck yang diulang harus menggantikan hasil lama. Insert
// biasa akan ditolak idx_cs_readiness_session_gate, dan insert tanpa index itu akan
// meninggalkan baris kedaluwarsa yang tetap terbaca sebagai lolos.
func (r *CSTerminalRepo) UpsertGate(ctx context.Context, rec *cs.GateRecord) error {
	var detailsJSON []byte
	if rec.Details != nil {
		var err error
		detailsJSON, err = json.Marshal(rec.Details)
		if err != nil {
			return fmt.Errorf("marshal gate details: %w", err)
		}
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs_terminal_readiness
			(session_id, gate, passed_at, expires_at, authorization_ref, supervisor_id, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (session_id, gate) DO UPDATE
		SET passed_at         = EXCLUDED.passed_at,
		    expires_at        = EXCLUDED.expires_at,
		    authorization_ref = EXCLUDED.authorization_ref,
		    supervisor_id     = EXCLUDED.supervisor_id,
		    details           = EXCLUDED.details`,
		rec.SessionID, string(rec.Gate), rec.PassedAt, rec.ExpiresAt,
		nullableText(rec.AuthorizationRef), nullableText(rec.SupervisorID), detailsJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert readiness gate: %w", err)
	}
	return nil
}

func (r *CSTerminalRepo) ListGates(ctx context.Context, sessionID uuid.UUID) ([]cs.GateRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT gate, passed_at, expires_at,
		       COALESCE(authorization_ref, ''), COALESCE(supervisor_id, ''), details
		FROM cs_terminal_readiness
		WHERE session_id = $1`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list readiness gates: %w", err)
	}
	defer rows.Close()

	var out []cs.GateRecord
	for rows.Next() {
		var rec cs.GateRecord
		var gate string
		var detailsJSON []byte

		if err := rows.Scan(&gate, &rec.PassedAt, &rec.ExpiresAt,
			&rec.AuthorizationRef, &rec.SupervisorID, &detailsJSON); err != nil {
			return nil, fmt.Errorf("scan readiness gate: %w", err)
		}
		rec.SessionID = sessionID
		rec.Gate = cs.Gate(gate)
		if len(detailsJSON) > 0 {
			if err := json.Unmarshal(detailsJSON, &rec.Details); err != nil {
				return nil, fmt.Errorf("unmarshal gate details: %w", err)
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate readiness gates: %w", err)
	}
	return out, nil
}

// CSSupervisorRepo memverifikasi token dual-control supervisor.
type CSSupervisorRepo struct {
	pool *pgxpool.Pool
}

func NewCSSupervisorRepo(pool *pgxpool.Pool) *CSSupervisorRepo {
	return &CSSupervisorRepo{pool: pool}
}

// ListActive mengembalikan supervisor aktif. TIDAK pernah menyertakan token_hash.
//
// location kosong berarti semua lokasi — dipakai pengawas; layar SCR-006 selalu
// menyebut lokasinya.
func (r *CSSupervisorRepo) ListActive(ctx context.Context, location string) ([]cs.Supervisor, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT supervisor_id, name, location
		FROM cs_supervisors
		WHERE is_active AND ($1 = '' OR location = $1)
		ORDER BY name ASC`, location)
	if err != nil {
		return nil, fmt.Errorf("list supervisors: %w", err)
	}
	defer rows.Close()

	out := make([]cs.Supervisor, 0, 8)
	for rows.Next() {
		var sv cs.Supervisor
		if err := rows.Scan(&sv.SupervisorID, &sv.Name, &sv.Location); err != nil {
			return nil, fmt.Errorf("scan supervisor: %w", err)
		}
		out = append(out, sv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate supervisors: %w", err)
	}
	return out, nil
}

// Authenticate memverifikasi token dual-control dengan Argon2id.
//
// Pola yang sama dengan AuthenticateAgent: pemanggil menyebut dirinya lewat
// supervisor_id lalu membuktikannya dengan token, karena hash ber-salt tidak bisa
// dicari balik.
func (r *CSSupervisorRepo) Authenticate(ctx context.Context, supervisorID, token string) (string, bool, error) {
	if supervisorID == "" || token == "" {
		return "", false, nil
	}

	var name, hash string
	err := r.pool.QueryRow(ctx,
		`SELECT name, token_hash FROM cs_supervisors WHERE supervisor_id = $1 AND is_active`,
		supervisorID,
	).Scan(&name, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("find supervisor: %w", err)
	}

	ok, err := crypto.VerifyPassword(ctx, token, hash)
	if err != nil {
		return "", false, fmt.Errorf("verify supervisor token: %w", err)
	}
	if !ok {
		return "", false, nil
	}
	return name, true, nil
}
