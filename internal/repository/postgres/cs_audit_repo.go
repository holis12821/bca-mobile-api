package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
)

// CSAuditEventRepo menulis dan mencari jejak audit petugas/terminal.
//
// Append-only di tingkat skema (trigger di migrasi 000037), jadi tidak ada Update maupun
// Delete di sini — bukan karena belum dibutuhkan, tapi karena keduanya ditolak database.
type CSAuditEventRepo struct {
	pool *pgxpool.Pool
}

func NewCSAuditEventRepo(pool *pgxpool.Pool) *CSAuditEventRepo {
	return &CSAuditEventRepo{pool: pool}
}

func (r *CSAuditEventRepo) Insert(ctx context.Context, ev *cs.AuditEvent) error {
	var detailsJSON []byte
	if ev.Details != nil {
		var err error
		detailsJSON, err = json.Marshal(ev.Details)
		if err != nil {
			return fmt.Errorf("marshal cs audit details: %w", err)
		}
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs_audit_events
			(id, event_type, actor, terminal_id, session_id, details, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ev.ID, ev.EventType, ev.Actor,
		nullableText(ev.TerminalID), ev.SessionID, detailsJSON,
		nullableText(ev.IPAddress), nullableText(ev.UserAgent), ev.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert cs audit event: %w", err)
	}
	return nil
}

// List mengembalikan peristiwa terurut created_at DESC, id DESC, dengan limit+1 baris.
func (r *CSAuditEventRepo) List(ctx context.Context, filter cs.AuditFilter) ([]cs.AuditEvent, error) {
	args := []any{}
	where := []string{"TRUE"}

	// Placeholder dinomori saat argumennya ditambahkan — pola yang sama dengan
	// ListForCS dan daftar tiket, dan dengan alasan yang sama.
	add := func(clause string, vals ...any) {
		for i := range vals {
			args = append(args, vals[i])
			clause = strings.Replace(clause, "?", fmt.Sprintf("$%d", len(args)), 1)
		}
		where = append(where, clause)
	}

	if filter.Actor != "" {
		add("actor = ?", filter.Actor)
	}
	if filter.EventType != "" {
		add("event_type = ?", filter.EventType)
	}
	if filter.TerminalID != "" {
		add("terminal_id = ?", filter.TerminalID)
	}
	if filter.From != nil {
		add("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		add("created_at < ?", *filter.To)
	}
	if filter.Cursor != nil {
		add("(created_at, id) < (?, ?)", filter.Cursor.CreatedAt, filter.Cursor.ID)
	}

	args = append(args, filter.Limit+1)

	query := fmt.Sprintf(`
		SELECT id, event_type, actor, COALESCE(terminal_id, ''), session_id,
		       details, COALESCE(ip_address, ''), COALESCE(user_agent, ''), created_at
		FROM cs_audit_events
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d`, strings.Join(where, " AND "), len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list cs audit events: %w", err)
	}
	defer rows.Close()

	var out []cs.AuditEvent
	for rows.Next() {
		var ev cs.AuditEvent
		var detailsJSON []byte

		if err := rows.Scan(&ev.ID, &ev.EventType, &ev.Actor, &ev.TerminalID,
			&ev.SessionID, &detailsJSON, &ev.IPAddress, &ev.UserAgent, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan cs audit event: %w", err)
		}
		if len(detailsJSON) > 0 {
			if err := json.Unmarshal(detailsJSON, &ev.Details); err != nil {
				return nil, fmt.Errorf("unmarshal cs audit details: %w", err)
			}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cs audit events: %w", err)
	}
	return out, nil
}
