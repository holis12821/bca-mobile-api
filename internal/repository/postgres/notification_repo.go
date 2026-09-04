package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
)

type NotificationRepo struct {
	pool *pgxpool.Pool
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{pool: pool}
}

// ListByUserID returns paginated notifications using keyset pagination.
// Returns limit+1 rows so caller can compute has_more.
func (r *NotificationRepo) ListByUserID(ctx context.Context, userID uuid.UUID, cursor *uuid.UUID, limit int) ([]account.Notification, error) {
	var query string
	var args []any

	if cursor != nil {
		// Keyset pagination: get created_at of cursor row, then fetch older
		query = `
			SELECT id, user_id, type, title, body, deep_link,
			       is_read, read_at, metadata, created_at
			FROM notifications
			WHERE user_id = $1
			  AND (created_at, id) < (
			      SELECT created_at, id FROM notifications WHERE id = $2
			  )
			ORDER BY created_at DESC, id DESC
			LIMIT $3`
		args = []any{userID, *cursor, limit}
	} else {
		query = `
			SELECT id, user_id, type, title, body, deep_link,
			       is_read, read_at, metadata, created_at
			FROM notifications
			WHERE user_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2`
		args = []any{userID, limit}
	}

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