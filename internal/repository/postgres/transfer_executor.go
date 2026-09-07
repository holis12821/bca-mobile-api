package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

const maxSerializationRetries = 3

// TransferExecutor runs transfers in a SERIALIZABLE transaction with 40001 retry.
type TransferExecutor struct {
	pool *pgxpool.Pool
}

func NewTransferExecutor(pool *pgxpool.Pool) *TransferExecutor {
	return &TransferExecutor{pool: pool}
}

// ExecuteTransfer runs the SERIALIZABLE transaction (steps 6-14 of §5).
func (te *TransferExecutor) ExecuteTransfer(ctx context.Context, params transaction.ExecuteParams) (*transaction.Transaction, error) {
	var txn *transaction.Transaction
	var err error

	for attempt := range maxSerializationRetries {
		txn, err = te.tryExecute(ctx, params)
		if err == nil {
			return txn, nil
		}

		// Check for serialization failure (SQLSTATE 40001)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40001" {
			if attempt < maxSerializationRetries-1 {
				// Jittered backoff: ~10ms, ~30ms, ~90ms
				base := time.Duration(10*(1<<attempt)) * time.Millisecond
				jitter := time.Duration(rand.IntN(int(base / 2)))
				time.Sleep(base + jitter)
				continue
			}
		}

		// Non-retryable error or retries exhausted
		return nil, err
	}

	return nil, err
}

func (te *TransferExecutor) tryExecute(ctx context.Context, params transaction.ExecuteParams) (*transaction.Transaction, error) {
	// Step 6: BEGIN SERIALIZABLE
	pgxTx, err := te.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer pgxTx.Rollback(ctx)

	inquiry := params.Inquiry

	// Collect account IDs that need locking, sorted by UUID for deadlock prevention (step 7)
	accountIDs := []uuid.UUID{params.SourceAccountID}

	var destAccountID *uuid.UUID
	var settlementAccountID *uuid.UUID
	var feeAccountID *uuid.UUID

	if params.TransferType == "INTERNAL" {
		// Resolve destination account inside the transaction
		var dstID uuid.UUID
		err := pgxTx.QueryRow(ctx,
			`SELECT id FROM accounts WHERE account_number = $1 AND owner_type = 'CUSTOMER' AND status = 'ACTIVE'`,
			inquiry.DestinationAccount,
		).Scan(&dstID)
		if err == pgx.ErrNoRows {
			return nil, apperr.TransferAccountNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("resolve destination: %w", err)
		}

		// Self-transfer check (step 5 — also verified here under lock)
		if dstID == params.SourceAccountID {
			return nil, apperr.SelfTransfer
		}

		destAccountID = &dstID
		accountIDs = append(accountIDs, dstID)
	}

	// Determine settlement rail for external or fee leg
	var rail string
	switch params.TxnType {
	case "TRANSFER_EXTERNAL":
		rail = "TRANSFER_EXTERNAL"
	}

	txnID := uuid.New()

	if rail != "" {
		// Resolve settlement shard
		var settID uuid.UUID
		err := pgxTx.QueryRow(ctx,
			`SELECT settlement_account_id($1, $2)`, rail, txnID,
		).Scan(&settID)
		if err != nil {
			return nil, fmt.Errorf("resolve settlement shard: %w", err)
		}
		settlementAccountID = &settID
		accountIDs = append(accountIDs, settID)
	}

	// Fee income shard (if admin fee > 0)
	if params.AdminFee.GreaterThan(decimal.Zero) {
		var feeID uuid.UUID
		err := pgxTx.QueryRow(ctx,
			`SELECT settlement_account_id('FEE_INCOME', $1)`, txnID,
		).Scan(&feeID)
		if err != nil {
			return nil, fmt.Errorf("resolve fee shard: %w", err)
		}
		feeAccountID = &feeID
		accountIDs = append(accountIDs, feeID)
	}

	// Step 7: Lock accounts ORDER BY id ascending
	sort.Slice(accountIDs, func(i, j int) bool {
		return accountIDs[i].String() < accountIDs[j].String()
	})
	// Deduplicate
	unique := accountIDs[:0]
	seen := make(map[uuid.UUID]bool)
	for _, id := range accountIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	accountIDs = unique

	// Lock all accounts with FOR UPDATE in sorted order
	type lockedAccount struct {
		ID      uuid.UUID
		Balance decimal.Decimal
		Hold    decimal.Decimal
	}
	accountBalances := make(map[uuid.UUID]lockedAccount)

	for _, accID := range accountIDs {
		var la lockedAccount
		err := pgxTx.QueryRow(ctx,
			`SELECT id, balance, hold_amount FROM accounts WHERE id = $1 FOR UPDATE`,
			accID,
		).Scan(&la.ID, &la.Balance, &la.Hold)
		if err != nil {
			return nil, fmt.Errorf("lock account %s: %w", accID, err)
		}
		accountBalances[accID] = la
	}

	// Step 8: Check available balance
	source := accountBalances[params.SourceAccountID]
	available := source.Balance.Sub(source.Hold)
	if available.LessThan(params.TotalAmount) {
		return nil, apperr.InsufficientBalance
	}

	// Step 9: Check daily limit
	var usedToday decimal.Decimal
	err = pgxTx.QueryRow(ctx,
		`SELECT COALESCE(total_amount, 0) FROM daily_usage
		 WHERE user_id = $1 AND usage_date = $2 AND limit_type = $3`,
		params.UserID, params.WIBDate.Format("2006-01-02"), params.LimitType,
	).Scan(&usedToday)
	if err == pgx.ErrNoRows {
		usedToday = decimal.Zero
	} else if err != nil {
		return nil, fmt.Errorf("check daily usage: %w", err)
	}

	var dailyLimit decimal.Decimal
	err = pgxTx.QueryRow(ctx,
		`SELECT daily_limit FROM transaction_limits WHERE user_id = $1 AND limit_type = $2`,
		params.UserID, params.LimitType,
	).Scan(&dailyLimit)
	if err == pgx.ErrNoRows {
		// Missing row = 0 (deny), never unlimited
		dailyLimit = decimal.Zero
	} else if err != nil {
		return nil, fmt.Errorf("check daily limit: %w", err)
	}

	remaining := dailyLimit.Sub(usedToday)
	if remaining.LessThan(params.TotalAmount) {
		return nil, apperr.Error{
			Status:  422,
			Code:    apperr.TransferLimitExceeded.Code,
			Message: apperr.TransferLimitExceeded.Message,
			Details: transaction.LimitCheckResult{
				DailyLimit: dailyLimit,
				UsedToday:  usedToday,
				Remaining:  remaining,
			},
		}
	}

	// Step 10: Update balances
	// Debit source: totalAmount (amount + fee)
	newSourceBalance := source.Balance.Sub(params.TotalAmount)
	_, err = pgxTx.Exec(ctx,
		`UPDATE accounts SET balance = $2 WHERE id = $1`,
		params.SourceAccountID, newSourceBalance,
	)
	if err != nil {
		return nil, fmt.Errorf("debit source: %w", err)
	}

	if params.TransferType == "INTERNAL" && destAccountID != nil {
		// Credit destination: amount (not totalAmount — fee goes to fee-income)
		dest := accountBalances[*destAccountID]
		newDestBalance := dest.Balance.Add(params.Amount)
		_, err = pgxTx.Exec(ctx,
			`UPDATE accounts SET balance = $2 WHERE id = $1`,
			*destAccountID, newDestBalance,
		)
		if err != nil {
			return nil, fmt.Errorf("credit destination: %w", err)
		}
	}

	if settlementAccountID != nil {
		// Credit settlement shard: amount
		sett := accountBalances[*settlementAccountID]
		newSettBalance := sett.Balance.Add(params.Amount)
		_, err = pgxTx.Exec(ctx,
			`UPDATE accounts SET balance = $2 WHERE id = $1`,
			*settlementAccountID, newSettBalance,
		)
		if err != nil {
			return nil, fmt.Errorf("credit settlement: %w", err)
		}
	}

	if feeAccountID != nil {
		// Credit fee-income shard: admin_fee
		fee := accountBalances[*feeAccountID]
		newFeeBalance := fee.Balance.Add(params.AdminFee)
		_, err = pgxTx.Exec(ctx,
			`UPDATE accounts SET balance = $2 WHERE id = $1`,
			*feeAccountID, newFeeBalance,
		)
		if err != nil {
			return nil, fmt.Errorf("credit fee-income: %w", err)
		}
	}

	// Step 11: INSERT transaction — call next_reference_number with WIB date
	var refNumber string
	err = pgxTx.QueryRow(ctx,
		`SELECT next_reference_number($1)`,
		params.WIBDate.Format("2006-01-02"),
	).Scan(&refNumber)
	if err != nil {
		return nil, fmt.Errorf("generate ref number: %w", err)
	}

	description := "Transfer ke " + inquiry.DestinationAccount
	if inquiry.DestinationName != nil {
		description = "Transfer ke " + *inquiry.DestinationName
	}

	now := time.Now().UTC()
	txnRow := transaction.Transaction{
		ID:                   txnID,
		IdempotencyKey:       &params.IdempotencyKey,
		UserID:               params.UserID,
		SourceAccountID:      &params.SourceAccountID,
		DestinationAccountID: destAccountID,
		Type:                 params.TxnType,
		Status:               "SUCCESS",
		Amount:               params.Amount,
		AdminFee:             params.AdminFee,
		TotalAmount:          params.TotalAmount,
		Currency:             "IDR",
		ReferenceNumber:      refNumber,
		Description:          &description,
		Notes:                strPtrOrNil(params.Notes),
		DestinationAccount:   &inquiry.DestinationAccount,
		DestinationName:      inquiry.DestinationName,
		DestinationBank:      inquiry.DestinationBank,
		DestinationBankCode:  inquiry.BankCode,
		CreatedAt:            now,
		ProcessedAt:          &now,
		CompletedAt:          &now,
	}

	_, err = pgxTx.Exec(ctx,
		`INSERT INTO transactions (id, idempotency_key, user_id, source_account_id, destination_account_id,
		       type, status, amount, admin_fee, total_amount, currency,
		       reference_number, description, notes,
		       destination_account, destination_name, destination_bank, destination_bank_code,
		       created_at, processed_at, completed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		txnRow.ID, txnRow.IdempotencyKey, txnRow.UserID, txnRow.SourceAccountID, txnRow.DestinationAccountID,
		txnRow.Type, txnRow.Status, txnRow.Amount, txnRow.AdminFee, txnRow.TotalAmount, txnRow.Currency,
		txnRow.ReferenceNumber, txnRow.Description, txnRow.Notes,
		txnRow.DestinationAccount, txnRow.DestinationName, txnRow.DestinationBank, txnRow.DestinationBankCode,
		txnRow.CreatedAt, txnRow.ProcessedAt, txnRow.CompletedAt,
	)
	if err != nil {
		// Check for unique violation on idempotency_key — DB-level fallback after Redis TTL
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Fetch existing transaction and return it
			return te.fetchExistingTransaction(ctx, params.UserID, params.IdempotencyKey)
		}
		return nil, fmt.Errorf("insert transaction: %w", err)
	}

	// Step 12: INSERT account_mutations (double-entry)
	wibDate := params.WIBDate.Format("2006-01-02")
	wibTime := params.WIBTime.Format("15:04:05")

	// Leg 1: DEBIT source (amount + fee)
	_, err = pgxTx.Exec(ctx,
		`INSERT INTO account_mutations (account_id, transaction_id, mutation_type, amount,
		       balance_before, balance_after, description, category,
		       reference_number, transaction_date, transaction_time)
		 VALUES ($1,$2,'DEBIT',$3,$4,$5,$6,$7,$8,$9,$10)`,
		params.SourceAccountID, txnID, params.TotalAmount,
		source.Balance, newSourceBalance,
		description, params.TxnType,
		refNumber, wibDate, wibTime,
	)
	if err != nil {
		return nil, fmt.Errorf("insert debit mutation: %w", err)
	}

	if params.TransferType == "INTERNAL" && destAccountID != nil {
		// Leg 2: CREDIT destination (amount only, not fee)
		dest := accountBalances[*destAccountID]
		newDestBalance := dest.Balance.Add(params.Amount)
		creditDesc := "Transfer dari " + inquiry.DestinationAccount
		_, err = pgxTx.Exec(ctx,
			`INSERT INTO account_mutations (account_id, transaction_id, mutation_type, amount,
			       balance_before, balance_after, description, category,
			       reference_number, transaction_date, transaction_time)
			 VALUES ($1,$2,'CREDIT',$3,$4,$5,$6,$7,$8,$9,$10)`,
			*destAccountID, txnID, params.Amount,
			dest.Balance, newDestBalance,
			creditDesc, params.TxnType,
			refNumber, wibDate, wibTime,
		)
		if err != nil {
			return nil, fmt.Errorf("insert credit mutation: %w", err)
		}
	}

	if settlementAccountID != nil {
		// Settlement leg: CREDIT settlement shard (amount)
		sett := accountBalances[*settlementAccountID]
		newSettBalance := sett.Balance.Add(params.Amount)
		_, err = pgxTx.Exec(ctx,
			`INSERT INTO account_mutations (account_id, transaction_id, mutation_type, amount,
			       balance_before, balance_after, description, category,
			       reference_number, transaction_date, transaction_time)
			 VALUES ($1,$2,'CREDIT',$3,$4,$5,$6,$7,$8,$9,$10)`,
			*settlementAccountID, txnID, params.Amount,
			sett.Balance, newSettBalance,
			"Settlement "+params.TxnType, "SETTLEMENT",
			refNumber, wibDate, wibTime,
		)
		if err != nil {
			return nil, fmt.Errorf("insert settlement mutation: %w", err)
		}
	}

	if feeAccountID != nil {
		// Fee leg: CREDIT fee-income shard (admin_fee)
		fee := accountBalances[*feeAccountID]
		newFeeBalance := fee.Balance.Add(params.AdminFee)
		_, err = pgxTx.Exec(ctx,
			`INSERT INTO account_mutations (account_id, transaction_id, mutation_type, amount,
			       balance_before, balance_after, description, category,
			       reference_number, transaction_date, transaction_time)
			 VALUES ($1,$2,'CREDIT',$3,$4,$5,$6,$7,$8,$9,$10)`,
			*feeAccountID, txnID, params.AdminFee,
			fee.Balance, newFeeBalance,
			"Admin fee "+params.TxnType, "FEE_INCOME",
			refNumber, wibDate, wibTime,
		)
		if err != nil {
			return nil, fmt.Errorf("insert fee mutation: %w", err)
		}
	}

	// Step 13: UPSERT daily_usage
	_, err = pgxTx.Exec(ctx,
		`INSERT INTO daily_usage (user_id, usage_date, limit_type, total_amount, transaction_count)
		 VALUES ($1, $2, $3, $4, 1)
		 ON CONFLICT (user_id, usage_date, limit_type)
		 DO UPDATE SET total_amount = daily_usage.total_amount + $4,
		              transaction_count = daily_usage.transaction_count + 1`,
		params.UserID, wibDate, params.LimitType, params.TotalAmount,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert daily usage: %w", err)
	}

	// Step 14: COMMIT
	if err := pgxTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &txnRow, nil
}

func (te *TransferExecutor) fetchExistingTransaction(ctx context.Context, userID uuid.UUID, idemKey string) (*transaction.Transaction, error) {
	query := `
		SELECT id, idempotency_key, user_id, source_account_id, destination_account_id,
		       type, status, amount, admin_fee, total_amount, currency,
		       reference_number, description, notes,
		       destination_account, destination_name, destination_bank, destination_bank_code,
		       provider_id, provider_name, provider_ref,
		       created_at, processed_at, completed_at, expired_at
		FROM transactions
		WHERE user_id = $1 AND idempotency_key = $2`

	var t transaction.Transaction
	err := te.pool.QueryRow(ctx, query, userID, idemKey).Scan(
		&t.ID, &t.IdempotencyKey, &t.UserID, &t.SourceAccountID, &t.DestinationAccountID,
		&t.Type, &t.Status, &t.Amount, &t.AdminFee, &t.TotalAmount, &t.Currency,
		&t.ReferenceNumber, &t.Description, &t.Notes,
		&t.DestinationAccount, &t.DestinationName, &t.DestinationBank, &t.DestinationBankCode,
		&t.ProviderID, &t.ProviderName, &t.ProviderRef,
		&t.CreatedAt, &t.ProcessedAt, &t.CompletedAt, &t.ExpiredAt,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch existing transaction: %w", err)
	}
	return &t, nil
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}