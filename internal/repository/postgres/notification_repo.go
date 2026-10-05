package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
)

type NotificationRepo struct {
	pool *pgxpool.Pool
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{pool: pool}
}

// Insert writes one notification row. Implements notify.Store.
func (r *NotificationRepo) Insert(ctx context.Context, e notify.Entry) error {
	var metadata []byte
	if len(e.Metadata) > 0 {
		var err error
		metadata, err = json.Marshal(e.Metadata)
		if err != nil {
			return fmt.Errorf("marshal notification metadata: %w", err)
		}
	}

	const query = `
		INSERT INTO notifications (id, user_id, type, title, body, deep_link, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	var deepLink *string
	if e.DeepLink != "" {
		deepLink = &e.DeepLink
	}

	_, err := r.pool.Exec(ctx, query,
		uuid.New(), e.UserID, e.Type, e.Title, e.Body, deepLink, metadata, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert notification: %w", err)
	}
	return nil
}

// ListByUserID returns paginated notifications using keyset pagination,
// optionally narrowed to a set of types.
// Returns limit+1 rows so caller can compute has_more.
func (r *NotificationRepo) ListByUserID(ctx context.Context, userID uuid.UUID, types []string, cursor *uuid.UUID, limit int) ([]account.Notification, error) {
	args := []any{userID}
	where := "WHERE user_id = $1"
	argIdx := 2

	// = ANY($n) rather than an IN list built by hand: the values come from the
	// query string, and a hand-built list is where an injection gets in.
	if len(types) > 0 {
		where += fmt.Sprintf(" AND type = ANY($%d)", argIdx)
		args = append(args, types)
		argIdx++
	}

	if cursor != nil {
		// Keyset pagination: compare against the cursor row's (created_at, id).
		// The subquery is NOT scoped to user_id on purpose — the outer
		// predicate already is, and a cursor naming someone else's row simply
		// finds nothing rather than paging through their notifications.
		where += fmt.Sprintf(` AND (created_at, id) < (
			      SELECT created_at, id FROM notifications WHERE id = $%d
			  )`, argIdx)
		args = append(args, *cursor)
		argIdx++
	}

	query := fmt.Sprintf(`
		SELECT id, user_id, type, title, body, deep_link,
		       is_read, read_at, metadata, created_at
		FROM notifications
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d`, where, argIdx)

	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query notifications: %w", err)
	}
	defer rows.Close()

	var notifications []account.Notification
	for rows.Next() {
		var n account.Notification
		if err := rows.Scan(
			&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body, &n.DeepLink,
			&n.IsRead, &n.ReadAt, &n.Metadata, &n.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		notifications = append(notifications, n)
	}
	return notifications, rows.Err()
}

// CountUnread returns the number of unread notifications for a user.
func (r *NotificationRepo) CountUnread(ctx context.Context, userID uuid.UUID) (int, error) {
	query := `SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND is_read = FALSE`

	var count int
	if err := r.pool.QueryRow(ctx, query, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return count, nil
}

// MarkRead marks a single notification as read.
func (r *NotificationRepo) MarkRead(ctx context.Context, userID uuid.UUID, notifID uuid.UUID) error {
	query := `
		UPDATE notifications
		SET is_read = TRUE, read_at = $3
		WHERE id = $1 AND user_id = $2 AND is_read = FALSE`

	_, err := r.pool.Exec(ctx, query, notifID, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// MarkAllRead marks all unread notifications as read for a user.
func (r *NotificationRepo) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	query := `
		UPDATE notifications
		SET is_read = TRUE, read_at = $2
		WHERE user_id = $1 AND is_read = FALSE`

	_, err := r.pool.Exec(ctx, query, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}
