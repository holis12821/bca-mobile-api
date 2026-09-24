package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
)

// PromotionRepo reads the promo cards shown on the Beranda.
//
// The promotions table has existed since migration 000007 with nothing reading
// it: the dashboard's promo carousel had no source and the spec's
// "promotions" key was simply absent from the response.
type PromotionRepo struct {
	pool *pgxpool.Pool
}

func NewPromotionRepo(pool *pgxpool.Pool) *PromotionRepo {
	return &PromotionRepo{pool: pool}
}

// ListActive returns active promotions whose validity window contains now,
// highest priority first.
func (r *PromotionRepo) ListActive(ctx context.Context, limit int) ([]account.Promotion, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	const query = `
		SELECT id, title, image_url, deep_link, valid_until
		FROM promotions
		WHERE is_active = TRUE
		  AND valid_from <= NOW()
		  AND valid_until >= NOW()
		ORDER BY priority DESC, valid_until ASC
		LIMIT $1`

	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query promotions: %w", err)
	}
	defer rows.Close()

	var promos []account.Promotion
	for rows.Next() {
		var p account.Promotion
		if err := rows.Scan(&p.ID, &p.Title, &p.ImageURL, &p.DeepLink, &p.ValidUntil); err != nil {
			return nil, fmt.Errorf("scan promotion: %w", err)
		}
		promos = append(promos, p)
	}
	return promos, rows.Err()
}
