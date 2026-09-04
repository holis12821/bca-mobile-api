package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
)

type AuditRepo struct {
	pool *pgxpool.Pool
}

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{pool: pool}
}

func (r *AuditRepo) Insert(ctx context.Context, entry *auth.AuditEntry) error {
	query := `
		INSERT INTO audit_logs
			(user_id, session_id, action, resource_type, resource_id,
			 ip_address, user_agent, request_id, old_values, new_values, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	oldVals, _ := jsonOrNil(entry.OldValues)
	newVals, _ := jsonOrNil(entry.NewValues)
	meta, _ := jsonOrNil(entry.Metadata)

	_, err := r.pool.Exec(ctx, query,
		entry.UserID, entry.SessionID, entry.Action, entry.ResourceType,
		nilIfEmpty(entry.ResourceID),
		nilIfEmpty(entry.IPAddress), nilIfEmpty(entry.UserAgent), nilIfEmpty(entry.RequestID),
		oldVals, newVals, meta,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func jsonOrNil(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}