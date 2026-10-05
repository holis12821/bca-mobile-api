package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/content"
)

// ContentRepo reads the help centre and CS contact tables (migration 000023).
type ContentRepo struct {
	pool *pgxpool.Pool
}

func NewContentRepo(pool *pgxpool.Pool) *ContentRepo {
	return &ContentRepo{pool: pool}
}

// HelpCenter returns the active FAQ grouped into categories.
//
// One flat query, grouped in Go. The alternative — a query per category — turns
// a page of static text into N round trips, and the whole table is a few dozen
// rows.
func (r *ContentRepo) HelpCenter(ctx context.Context) (*content.HelpCenterResponse, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT category_key, category_title, question, answer
		FROM content_help_center
		WHERE is_active
		ORDER BY category_order, item_order, id`)
	if err != nil {
		return nil, fmt.Errorf("query help center: %w", err)
	}
	defer rows.Close()

	// Non-nil so an empty table encodes as [] rather than null; the screen's
	// empty state reads a list, not a missing field.
	categories := make([]content.HelpCategory, 0)

	// index maps a category key to its position in categories, so the ORDER BY
	// above is what decides the order — not map iteration, which has none.
	index := make(map[string]int)

	for rows.Next() {
		var key, title, question, answer string
		if err := rows.Scan(&key, &title, &question, &answer); err != nil {
			return nil, fmt.Errorf("scan help center row: %w", err)
		}

		pos, ok := index[key]
		if !ok {
			categories = append(categories, content.HelpCategory{
				Key:   key,
				Title: title,
				Items: make([]content.HelpItem, 0, 4),
			})
			pos = len(categories) - 1
			index[key] = pos
		}
		categories[pos].Items = append(categories[pos].Items, content.HelpItem{
			Question: question,
			Answer:   answer,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate help center rows: %w", err)
	}

	return &content.HelpCenterResponse{Categories: categories}, nil
}

// ContactCS returns the single contact row.
func (r *ContentRepo) ContactCS(ctx context.Context) (*content.ContactCS, error) {
	var c content.ContactCS
	err := r.pool.QueryRow(ctx, `
		SELECT phone, phone_free, whatsapp, email, chat_url, hours
		FROM content_contact_cs
		WHERE id = 1`).
		Scan(&c.Phone, &c.PhoneFree, &c.WhatsApp, &c.Email, &c.ChatURL, &c.Hours)

	if errors.Is(err, pgx.ErrNoRows) {
		// The migration seeds this row, so its absence means someone deleted it.
		// Answering with an empty object would print a screen with no phone
		// number on it, which is worse than saying the page is unavailable.
		return nil, fmt.Errorf("contact cs row missing")
	}
	if err != nil {
		return nil, fmt.Errorf("query contact cs: %w", err)
	}
	return &c, nil
}
