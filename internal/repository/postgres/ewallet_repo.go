package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/ewallet"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type EWalletProviderRepo struct {
	pool *pgxpool.Pool
}

func NewEWalletProviderRepo(pool *pgxpool.Pool) *EWalletProviderRepo {
	return &EWalletProviderRepo{pool: pool}
}

func (r *EWalletProviderRepo) ListActive(ctx context.Context) ([]ewallet.Provider, error) {
	query := `
		SELECT id, name, icon_url, is_active, min_amount, max_amount,
		       admin_fee, preset_amounts, sort_order
		FROM ewallet_providers
		WHERE is_active = TRUE
		ORDER BY sort_order`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list active providers: %w", err)
	}
	defer rows.Close()

	var providers []ewallet.Provider
	for rows.Next() {
		var p ewallet.Provider
		var presetsJSON []byte
		err := rows.Scan(
			&p.ID, &p.Name, &p.IconURL, &p.IsActive,
			&p.MinAmount, &p.MaxAmount, &p.AdminFee,
			&presetsJSON, &p.SortOrder,
		)
		if err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		_ = json.Unmarshal(presetsJSON, &p.PresetAmounts)
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (r *EWalletProviderRepo) FindByID(ctx context.Context, id string) (*ewallet.Provider, error) {
	query := `
		SELECT id, name, icon_url, is_active, min_amount, max_amount,
		       admin_fee, preset_amounts, sort_order
		FROM ewallet_providers
		WHERE id = $1`

	var p ewallet.Provider
	var presetsJSON []byte
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.Name, &p.IconURL, &p.IsActive,
		&p.MinAmount, &p.MaxAmount, &p.AdminFee,
		&presetsJSON, &p.SortOrder,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find provider: %w", err)
	}
	_ = json.Unmarshal(presetsJSON, &p.PresetAmounts)
	return &p, nil
}

// EWalletExecutorImpl runs the SERIALIZABLE transaction for e-wallet top-up.
// 3-leg posting: DEBIT source, CREDIT EWALLET shard, CREDIT FEE_INCOME shard.
type EWalletExecutorImpl struct {
	pool *pgxpool.Pool
}

func NewEWalletExecutor(pool *pgxpool.Pool) *EWalletExecutorImpl {
	return &EWalletExecutorImpl{pool: pool}
}

func (e *EWalletExecutorImpl) ExecuteTopUp(ctx context.Context, params ewallet.ExecuteTopUpParams) (*transaction.Transaction, error) {
	var txn *transaction.Transaction
	var err error

	for attempt := range maxSerializationRetries {
		txn, err = e.tryExecuteTopUp(ctx, params)
		if err == nil {
			return txn, nil
		}

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40001" {
			if attempt < maxSerializationRetries-1 {
				base := time.Duration(10*(1<<attempt)) * time.Millisecond
				jitter := time.Duration(rand.IntN(int(base / 2)))
				time.Sleep(base + jitter)
				continue
			}
		}
		return nil, err
	}
	return nil, err
}

func (e *EWalletExecutorImpl) tryExecuteTopUp(ctx context.Context, params ewallet.ExecuteTopUpParams) (*transaction.Transaction, error) {
	pgxTx, err := e.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer pgxTx.Rollback(ctx)

	txnID := uuid.New()

	// Resolve settlement shards
	var ewalletSettlementID, feeSettlementID uuid.UUID
	err = pgxTx.QueryRow(ctx, `SELECT settlement_account_id('EWALLET', $1)`, txnID).Scan(&ewalletSettlementID)
	if err != nil {
		return nil, fmt.Errorf("resolve ewallet settlement: %w", err)
	}
	err = pgxTx.QueryRow(ctx, `SELECT settlement_account_id('FEE_INCOME', $1)`, txnID).Scan(&feeSettlementID)
	if err != nil {
		return nil, fmt.Errorf("resolve fee settlement: %w", err)
	}

	// Lock all accounts ORDER BY id
	accountIDs := sortedUniqueUUIDs([]uuid.UUID{params.SourceAccountID, ewalletSettlementID, feeSettlementID})

	type lockedAccount struct {
		ID      uuid.UUID
		Balance decimal.Decimal
	}
	balances := make(map[uuid.UUID]lockedAccount)
	for _, accID := range accountIDs {
		var la lockedAccount
		err := pgxTx.QueryRow(ctx, `SELECT id, balance FROM accounts WHERE id = $1 FOR UPDATE`, accID).Scan(&la.ID, &la.Balance)
		if err != nil {
			return nil, fmt.Errorf("lock account %s: %w", accID, err)
		}
		balances[accID] = la
	}

	// Check source balance
	source := balances[params.SourceAccountID]
	if source.Balance.LessThan(params.TotalAmount) {
		return nil, apperr.EWalletInsufficientBalance
	}

	// Check daily limit
	wibDate := params.WIBDate.Format("2006-01-02")
	wibTime := params.WIBTime.Format("15:04:05")

	var usedToday decimal.Decimal
	err = pgxTx.QueryRow(ctx,
		`SELECT COALESCE(total_amount, 0) FROM daily_usage
		 WHERE user_id = $1 AND limit_type = 'EWALLET' AND usage_date = $2`,
		params.UserID, wibDate).Scan(&usedToday)
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("check daily usage: %w", err)
	}

	var dailyLimit decimal.Decimal
	err = pgxTx.QueryRow(ctx,
		`SELECT daily_limit FROM transaction_limits WHERE user_id = $1 AND limit_type = 'EWALLET'`,
		params.UserID).Scan(&dailyLimit)
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("check daily limit: %w", err)
	}
	if !dailyLimit.IsZero() && usedToday.Add(params.TotalAmount).GreaterThan(dailyLimit) {
		return nil, apperr.TransferLimitExceeded
	}

	// Generate reference number
	var refNumber string
	err = pgxTx.QueryRow(ctx, `SELECT next_reference_number($1)`, wibDate).Scan(&refNumber)
	if err != nil {
		return nil, fmt.Errorf("generate ref number: %w", err)
	}

	// Insert transaction
	desc := "Top Up " + params.ProviderName
	now := time.Now().UTC()
	_, err = pgxTx.Exec(ctx, `
		INSERT INTO transactions (id, idempotency_key, user_id, source_account_id,
			type, status, amount, admin_fee, total_amount, currency, reference_number,
			description, destination_account, destination_name,
			provider_id, provider_name,
			created_at, processed_at, completed_at)
		VALUES ($1, $2, $3, $4,
			'EWALLET_TOPUP', 'SUCCESS', $5, $6, $7, 'IDR', $8,
			$9, $10, $11,
			$12, $13,
			$14, $14, $14)`,
		txnID, params.IdempotencyKey, params.UserID, params.SourceAccountID,
		params.Amount, params.AdminFee, params.TotalAmount, refNumber,
		desc, params.PhoneNumber, params.DestinationName,
		params.ProviderID, params.ProviderName, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return e.fetchExistingTransaction(ctx, params.UserID, params.IdempotencyKey)
		}
		return nil, fmt.Errorf("insert transaction: %w", err)
	}

	// Leg 1: DEBIT source (amount + fee)
	newSourceBalance := source.Balance.Sub(params.TotalAmount)
	_, err = pgxTx.Exec(ctx, `
		INSERT INTO account_mutations (account_id, transaction_id, mutation_type,
			amount, balance_before, balance_after, description, detail, category,
			reference_number, transaction_date, transaction_time)
		VALUES ($1, $2, 'DEBIT', $3, $4, $5, $6, $7, 'EWALLET', $8, $9, $10)`,
		params.SourceAccountID, txnID, params.TotalAmount,
		source.Balance, newSourceBalance,
		"TOP UP "+params.ProviderName, "Top Up "+params.PhoneNumber+" "+params.DestinationName,
		refNumber, wibDate, wibTime)
	if err != nil {
		return nil, fmt.Errorf("insert debit mutation: %w", err)
	}
	_, err = pgxTx.Exec(ctx, `UPDATE accounts SET balance = $2 WHERE id = $1`, params.SourceAccountID, newSourceBalance)
	if err != nil {
		return nil, fmt.Errorf("update source balance: %w", err)
	}

	// Leg 2: CREDIT EWALLET settlement shard
	ewalletBal := balances[ewalletSettlementID]
	newEwalletBal := ewalletBal.Balance.Add(params.Amount)
	_, err = pgxTx.Exec(ctx, `
		INSERT INTO account_mutations (account_id, transaction_id, mutation_type,
			amount, balance_before, balance_after, description, category,
			reference_number, transaction_date, transaction_time)
		VALUES ($1, $2, 'CREDIT', $3, $4, $5, $6, 'SETTLEMENT', $7, $8, $9)`,
		ewalletSettlementID, txnID, params.Amount,
		ewalletBal.Balance, newEwalletBal,
		"SETTLEMENT "+params.ProviderName,
		refNumber, wibDate, wibTime)
	if err != nil {
		return nil, fmt.Errorf("insert settlement mutation: %w", err)
	}
	_, err = pgxTx.Exec(ctx, `UPDATE accounts SET balance = $2 WHERE id = $1`, ewalletSettlementID, newEwalletBal)
	if err != nil {
		return nil, fmt.Errorf("update settlement balance: %w", err)
	}

	// Leg 3: CREDIT FEE_INCOME shard (only if fee > 0)
	if params.AdminFee.IsPositive() {
		feeBal := balances[feeSettlementID]
		newFeeBal := feeBal.Balance.Add(params.AdminFee)
		_, err = pgxTx.Exec(ctx, `
			INSERT INTO account_mutations (account_id, transaction_id, mutation_type,
				amount, balance_before, balance_after, description, category,
				reference_number, transaction_date, transaction_time)
			VALUES ($1, $2, 'CREDIT', $3, $4, $5, $6, 'FEE_INCOME', $7, $8, $9)`,
			feeSettlementID, txnID, params.AdminFee,
			feeBal.Balance, newFeeBal,
			"FEE "+params.ProviderName,
			refNumber, wibDate, wibTime)
		if err != nil {
			return nil, fmt.Errorf("insert fee mutation: %w", err)
		}
		_, err = pgxTx.Exec(ctx, `UPDATE accounts SET balance = $2 WHERE id = $1`, feeSettlementID, newFeeBal)
		if err != nil {
			return nil, fmt.Errorf("update fee balance: %w", err)
		}
	}

	// Upsert daily usage
	_, err = pgxTx.Exec(ctx, `
		INSERT INTO daily_usage (user_id, usage_date, limit_type, total_amount, transaction_count)
		VALUES ($1, $2, 'EWALLET', $3, 1)
		ON CONFLICT (user_id, usage_date, limit_type)
		DO UPDATE SET total_amount = daily_usage.total_amount + $3,
		             transaction_count = daily_usage.transaction_count + 1`,
		params.UserID, wibDate, params.TotalAmount)
	if err != nil {
		return nil, fmt.Errorf("upsert daily usage: %w", err)
	}

	if err := pgxTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &transaction.Transaction{
		ID:              txnID,
		IdempotencyKey:  &params.IdempotencyKey,
		UserID:          params.UserID,
		SourceAccountID: &params.SourceAccountID,
		Type:            "EWALLET_TOPUP",
		Status:          "SUCCESS",
		Amount:          params.Amount,
		AdminFee:        params.AdminFee,
		TotalAmount:     params.TotalAmount,
		Currency:        "IDR",
		ReferenceNumber: refNumber,
		ProviderID:      &params.ProviderID,
		ProviderName:    &params.ProviderName,
		CreatedAt:       now,
	}, nil
}

func (e *EWalletExecutorImpl) fetchExistingTransaction(ctx context.Context, userID uuid.UUID, idemKey string) (*transaction.Transaction, error) {
	query := `
		SELECT id, idempotency_key, user_id, source_account_id,
		       type, status, amount, admin_fee, total_amount, currency,
		       reference_number, description,
		       destination_account, destination_name,
		       provider_id, provider_name,
		       created_at
		FROM transactions
		WHERE user_id = $1 AND idempotency_key = $2`

	var t transaction.Transaction
	err := e.pool.QueryRow(ctx, query, userID, idemKey).Scan(
		&t.ID, &t.IdempotencyKey, &t.UserID, &t.SourceAccountID,
		&t.Type, &t.Status, &t.Amount, &t.AdminFee, &t.TotalAmount, &t.Currency,
		&t.ReferenceNumber, &t.Description,
		&t.DestinationAccount, &t.DestinationName,
		&t.ProviderID, &t.ProviderName,
		&t.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch existing transaction: %w", err)
	}
	return &t, nil
}

func sortedUniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool)
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	// Sort by string to ensure consistent lock order
	for i := 0; i < len(unique)-1; i++ {
		for j := i + 1; j < len(unique); j++ {
			if unique[i].String() > unique[j].String() {
				unique[i], unique[j] = unique[j], unique[i]
			}
		}
	}
	return unique
}