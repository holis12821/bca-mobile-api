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

	"github.com/holis12821/bca-mobile-api/internal/domain/card"
	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// AccountCardRepo reads the cards a customer OWNS (table account_cards,
// migration 000021).
//
// Not to be confused with CardRepo in this same package, which reads the
// onboarding CATALOG (card_products). The two names are close because the
// domain nouns are close; the tables are not related beyond a foreign key.
type AccountCardRepo struct {
	pool *pgxpool.Pool
}

func NewAccountCardRepo(pool *pgxpool.Pool) *AccountCardRepo {
	return &AccountCardRepo{pool: pool}
}

// listColumns is shared by every read in this repo so that a column added to
// one query cannot be forgotten in another.
//
// The join onto card_products is what supplies product name, network, tier,
// style and the replacement fee. Those are NOT duplicated onto account_cards:
// renaming a product would otherwise leave every existing card showing the old
// name forever.
const accountCardColumns = `
		c.id, c.user_id, c.account_id, c.card_type,
		c.masked_number, c.cardholder_name,
		c.valid_thru_month, c.valid_thru_year,
		c.status, c.blocked_reason, c.blocked_at,
		c.debit_online_enabled, c.international_enabled, c.is_primary,
		c.created_at, c.updated_at,
		p.name, p.network, p.tier_key, p.style, p.fee_card_replacement,
		p.delivery_days_min, p.delivery_days_max, p.branch_pickup_available`

// ListByUserID returns all cards owned by a user, primary first.
func (r *AccountCardRepo) ListByUserID(ctx context.Context, userID uuid.UUID) ([]card.Card, error) {
	query := `
		SELECT ` + accountCardColumns + `
		FROM account_cards c
		JOIN card_products p ON p.card_type = c.card_type
		WHERE c.user_id = $1
		ORDER BY c.is_primary DESC, c.created_at ASC`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query account cards: %w", err)
	}
	defer rows.Close()

	// Non-nil so a customer with no cards yields an empty list rather than a
	// nil slice travelling up to the encoder.
	cards := make([]card.Card, 0)
	for rows.Next() {
		var c card.Card
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.AccountID, &c.CardType,
			&c.MaskedNumber, &c.CardholderName,
			&c.ValidThruMonth, &c.ValidThruYear,
			&c.Status, &c.BlockedReason, &c.BlockedAt,
			&c.DebitOnlineEnabled, &c.InternationalEnabled, &c.IsPrimary,
			&c.CreatedAt, &c.UpdatedAt,
			&c.ProductName, &c.Network, &c.TierKey, &c.Style, &c.FeeCardReplacement,
			&c.DeliveryDaysMin, &c.DeliveryDaysMax, &c.BranchPickupAvailable,
		); err != nil {
			return nil, fmt.Errorf("scan account card: %w", err)
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

// FindOwnedByID returns a card only when userID owns it.
//
// The user_id predicate is the whole point of this method. account_cards.id
// arrives straight from the URL, and a query without it would happily hand over
// — and later modify — somebody else's card. The same omission on the money
// path once let one customer debit another's account.
func (r *AccountCardRepo) FindOwnedByID(ctx context.Context, userID, cardID uuid.UUID) (*card.Card, error) {
	query := `
		SELECT ` + accountCardColumns + `
		FROM account_cards c
		JOIN card_products p ON p.card_type = c.card_type
		WHERE c.id = $1 AND c.user_id = $2`

	var c card.Card
	err := r.pool.QueryRow(ctx, query, cardID, userID).Scan(
		&c.ID, &c.UserID, &c.AccountID, &c.CardType,
		&c.MaskedNumber, &c.CardholderName,
		&c.ValidThruMonth, &c.ValidThruYear,
		&c.Status, &c.BlockedReason, &c.BlockedAt,
		&c.DebitOnlineEnabled, &c.InternationalEnabled, &c.IsPrimary,
		&c.CreatedAt, &c.UpdatedAt,
		&c.ProductName, &c.Network, &c.TierKey, &c.Style, &c.FeeCardReplacement,
		&c.DeliveryDaysMin, &c.DeliveryDaysMax, &c.BranchPickupAvailable,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Deliberately the same answer for "no such card" and "not yours".
		return nil, apperr.CardNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find owned card: %w", err)
	}
	return &c, nil
}

// RegisterIssuedCard records a freshly issued card as one the customer owns.
//
// This is the only write path into account_cards outside the seeder. Without it
// the table was populated by `make seed` and nothing else: a customer who had
// just finished onboarding got a card print request in
// onboarding_card_issuance and an empty list on GET /account/cards, because the
// two tables answer different questions and nothing connected them.
//
// Deliberately not idempotency-keyed. The guard is the masked number already on
// the account: the card issuance retry queue may call this more than once for
// the same physical card, and a second row would show the customer two cards
// they do not have. A genuine second card has a different number.
//
// No audit row. account_card_audit_log records what the CUSTOMER does to a card
// (settings, block, replacement) and its action CHECK allows exactly those
// three; issuance is already traceable through onboarding_card_issuance and the
// onboarding audit trail, so widening that constraint would buy nothing.
func (r *AccountCardRepo) RegisterIssuedCard(ctx context.Context, issued onboarding.IssuedCard) error {
	userID, err := uuid.Parse(issued.UserID)
	if err != nil {
		return fmt.Errorf("register issued card: user id %q: %w", issued.UserID, err)
	}
	accountID, err := uuid.Parse(issued.AccountID)
	if err != nil {
		return fmt.Errorf("register issued card: account id %q: %w", issued.AccountID, err)
	}

	// is_primary is claimed only when the customer has no primary card yet.
	// idx_account_cards_primary is a partial unique index, so asserting it
	// unconditionally would fail the insert for anyone opening a second account.
	_, err = r.pool.Exec(ctx, `
		INSERT INTO account_cards (
			user_id, account_id, card_type, masked_number, cardholder_name,
			valid_thru_month, valid_thru_year, is_primary)
		SELECT $1, $2, $3, $4, $5, $6, $7,
		       NOT EXISTS (SELECT 1 FROM account_cards p WHERE p.user_id = $1 AND p.is_primary)
		WHERE NOT EXISTS (
			SELECT 1 FROM account_cards c
			WHERE c.account_id = $2 AND c.masked_number = $4
		)`,
		userID, accountID, issued.CardType, issued.MaskedNumber, issued.CardholderName,
		issued.ValidThruMonth, issued.ValidThruYear)
	if err != nil {
		return fmt.Errorf("register issued card: %w", err)
	}
	return nil
}

// UpdateSettings writes both channel switches and the audit row atomically.
//
// One transaction, not two statements: a settings change with no audit row is
// exactly the pair that must never exist, since the log is what answers "who
// turned overseas purchases back on".
func (r *AccountCardRepo) UpdateSettings(ctx context.Context, cardID uuid.UUID, settings card.CardSettings, audit card.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE account_cards
			SET debit_online_enabled = $2,
			    international_enabled = $3,
			    updated_at = NOW()
			WHERE id = $1`,
			cardID, settings.DebitOnlineEnabled, settings.InternationalEnabled)
		if err != nil {
			return fmt.Errorf("update card settings: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.CardNotFound
		}
		return insertCardAudit(ctx, tx, audit)
	})
}

// Block marks the card blocked, records the reason, and writes the audit row.
func (r *AccountCardRepo) Block(ctx context.Context, cardID uuid.UUID, reason string, blockedAt time.Time, audit card.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE account_cards
			SET status = 'BLOCKED',
			    blocked_reason = $2,
			    blocked_at = $3,
			    updated_at = NOW()
			WHERE id = $1`,
			cardID, reason, blockedAt)
		if err != nil {
			return fmt.Errorf("block card: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.CardNotFound
		}
		return insertCardAudit(ctx, tx, audit)
	})
}

// replacementColumns is shared by the insert's RETURNING and the replay read so
// the two cannot drift into returning different shapes for the same request.
const replacementColumns = `
		id, card_id, user_id, idempotency_key, reason, delivery_method,
		fee, status, COALESCE(masked_number, ''),
		estimated_arrival_from, estimated_arrival_to, created_at`

func scanReplacement(row pgx.Row) (*card.ReplacementRecord, error) {
	var rec card.ReplacementRecord
	err := row.Scan(
		&rec.ID, &rec.CardID, &rec.UserID, &rec.IdempotencyKey,
		&rec.Reason, &rec.DeliveryMethod, &rec.Fee, &rec.Status, &rec.MaskedNumber,
		&rec.EstimatedArrivalFrom, &rec.EstimatedArrivalTo, &rec.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// FindReplacementByIdemKey returns an earlier request made with the same key,
// or nil when there is none.
func (r *AccountCardRepo) FindReplacementByIdemKey(ctx context.Context, userID uuid.UUID, idemKey string) (*card.ReplacementRecord, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+replacementColumns+`
		FROM card_replacement_requests
		WHERE user_id = $1 AND idempotency_key = $2`,
		userID, idemKey)

	rec, err := scanReplacement(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find replacement by idempotency key: %w", err)
	}
	rec.Replayed = true
	return rec, nil
}

// CreateReplacement inserts the request, moves the card to
// REPLACEMENT_PENDING, and writes the audit row — all in one transaction.
func (r *AccountCardRepo) CreateReplacement(ctx context.Context, rec card.ReplacementRecord, audit card.AuditEntry) (*card.ReplacementRecord, error) {
	var saved *card.ReplacementRecord

	err := r.inTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO card_replacement_requests
				(card_id, user_id, idempotency_key, reason, delivery_method,
				 fee, status, masked_number,
				 estimated_arrival_from, estimated_arrival_to)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (user_id, idempotency_key) DO NOTHING
			RETURNING `+replacementColumns,
			rec.CardID, rec.UserID, rec.IdempotencyKey, rec.Reason, rec.DeliveryMethod,
			rec.Fee, rec.Status, rec.MaskedNumber,
			rec.EstimatedArrivalFrom, rec.EstimatedArrivalTo)

		inserted, err := scanReplacement(row)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Lost the race against an identical request that landed between
			// the service's replay check and this insert. Read back what the
			// winner wrote; the answer is the same either way.
			replay, rerr := scanReplacement(tx.QueryRow(ctx, `
				SELECT `+replacementColumns+`
				FROM card_replacement_requests
				WHERE user_id = $1 AND idempotency_key = $2`,
				rec.UserID, rec.IdempotencyKey))
			if rerr != nil {
				return fmt.Errorf("read replayed replacement: %w", rerr)
			}
			replay.Replayed = true
			saved = replay
			return nil
		case err != nil:
			// The partial unique index idx_card_replacement_open fires when
			// another request for this card is still REQUESTED or PRINTING.
			// Enforcing it in the database rather than with a prior SELECT is
			// what stops two concurrent requests from both passing a check.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return apperr.CardReplacementInProgress
			}
			return fmt.Errorf("insert card replacement: %w", err)
		}

		// The card keeps working until the new one is activated; the status is
		// what puts the "Permintaan Penggantian Kartu" row on the screen.
		// Restricted to ACTIVE so that a card blocked as lost stays blocked —
		// that is the state the customer acted on.
		if _, err := tx.Exec(ctx, `
			UPDATE account_cards
			SET status = 'REPLACEMENT_PENDING', updated_at = NOW()
			WHERE id = $1 AND status = 'ACTIVE'`, rec.CardID); err != nil {
			return fmt.Errorf("mark card replacement pending: %w", err)
		}

		if err := insertCardAudit(ctx, tx, audit); err != nil {
			return err
		}
		saved = inserted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// insertCardAudit writes one row of account_card_audit_log inside the caller's
// transaction.
func insertCardAudit(ctx context.Context, tx pgx.Tx, audit card.AuditEntry) error {
	oldJSON, err := marshalCardAuditValue(audit.OldValue)
	if err != nil {
		return err
	}
	newJSON, err := marshalCardAuditValue(audit.NewValue)
	if err != nil {
		return err
	}

	// parseAuditIP is shared with the catalog audit: it drops a port if one
	// came along and yields NULL for anything that is not an address, because
	// ip_address is INET and '' would abort the INSERT — losing the audit row
	// over a cosmetic field.
	ip := parseAuditIP(audit.IP)

	if _, err := tx.Exec(ctx, `
		INSERT INTO account_card_audit_log
			(card_id, user_id, actor, action, old_value, new_value, ip_address)
		VALUES ($1, $2, 'CUSTOMER', $3, $4, $5, $6)`,
		audit.CardID, audit.UserID, audit.Action, oldJSON, newJSON, ip); err != nil {
		return fmt.Errorf("insert card audit: %w", err)
	}
	return nil
}

// marshalCardAuditValue is the any-typed sibling of marshalAuditValue, which
// takes map[string]any. Card settings travel as a struct, and converting them
// to a map just to satisfy a signature would let the two spellings of the same
// audit value drift apart.
func marshalCardAuditValue(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal audit value: %w", err)
	}
	return b, nil
}

// inTx runs fn inside a transaction, rolling back on any error.
func (r *AccountCardRepo) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			// Nothing to do but say so: the caller already has the real error.
			_ = rbErr
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
