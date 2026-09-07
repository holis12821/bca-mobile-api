package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
)

// MutationRepo provides data access for account mutations with keyset pagination.
type MutationRepo struct {
	pool *pgxpool.Pool
}

func NewMutationRepo(pool *pgxpool.Pool) *MutationRepo {
	return &MutationRepo{pool: pool}
}

// ListByAccountID returns keyset-paginated mutations.
// Cursor matches ORDER BY transaction_date DESC, created_at DESC, id DESC.
// Returns limit+1 rows so caller can compute has_more.
func (r *MutationRepo) ListByAccountID(ctx context.Context, accountID uuid.UUID, cursor *transaction.MutationCursorValues, limit int, period *transaction.DateRange) ([]transaction.Mutation, error) {
	args := []any{accountID}
	where := "WHERE account_id = $1"
	argIdx := 2

	if period != nil {
		where += fmt.Sprintf(" AND transaction_date >= $%d AND transaction_date <= $%d", argIdx, argIdx+1)
		args = append(args, period.From, period.To)
		argIdx += 2
	}

	if cursor != nil {
		where += fmt.Sprintf(" AND (transaction_date, created_at, id) < ($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2)
		args = append(args, cursor.Date, cursor.CreatedAt, cursor.ID)
		argIdx += 3
	}

	query := fmt.Sprintf(`
		SELECT id, account_id, transaction_id, mutation_type, amount,
		       balance_before, balance_after, description, detail, category,
		       reference_number, transaction_date, transaction_time, created_at
		FROM account_mutations
		%s
		ORDER BY transaction_date DESC, created_at DESC, id DESC
		LIMIT $%d`, where, argIdx)

	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query mutations: %w", err)
	}
	defer rows.Close()

	var mutations []transaction.Mutation
	for rows.Next() {
		var m transaction.Mutation
		if err := rows.Scan(
			&m.ID, &m.AccountID, &m.TransactionID, &m.MutationType, &m.Amount,
			&m.BalanceBefore, &m.BalanceAfter, &m.Description, &m.Detail, &m.Category,
			&m.ReferenceNumber, &m.TransactionDate, &m.TransactionTime, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan mutation: %w", err)
		}
		mutations = append(mutations, m)
	}
	return mutations, rows.Err()
}

// TransactionRepo provides data access for transactions with keyset pagination.
type TransactionRepo struct {
	pool *pgxpool.Pool
}

func NewTransactionRepo(pool *pgxpool.Pool) *TransactionRepo {
	return &TransactionRepo{pool: pool}
}

// ListByUserID returns keyset-paginated transaction history.
func (r *TransactionRepo) ListByUserID(ctx context.Context, userID uuid.UUID, txnType *string, cursor *transaction.HistoryCursorValues, limit int) ([]transaction.Transaction, error) {
	args := []any{userID}
	where := "WHERE user_id = $1"
	argIdx := 2

	if txnType != nil {
		where += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, *txnType)
		argIdx++
	}

	if cursor != nil {
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", argIdx, argIdx+1)
		args = append(args, cursor.CreatedAt, cursor.ID)
		argIdx += 2
	}

	query := fmt.Sprintf(`
		SELECT id, idempotency_key, user_id, source_account_id, destination_account_id,
		       type, status, amount, admin_fee, total_amount, currency,
		       reference_number, description, notes,
		       destination_account, destination_name, destination_bank, destination_bank_code,
		       provider_id, provider_name, provider_ref,
		       created_at, processed_at, completed_at, expired_at
		FROM transactions
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d`, where, argIdx)

	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query transactions: %w", err)
	}
	defer rows.Close()

	var txns []transaction.Transaction
	for rows.Next() {
		var t transaction.Transaction
		if err := rows.Scan(
			&t.ID, &t.IdempotencyKey, &t.UserID, &t.SourceAccountID, &t.DestinationAccountID,
			&t.Type, &t.Status, &t.Amount, &t.AdminFee, &t.TotalAmount, &t.Currency,
			&t.ReferenceNumber, &t.Description, &t.Notes,
			&t.DestinationAccount, &t.DestinationName, &t.DestinationBank, &t.DestinationBankCode,
			&t.ProviderID, &t.ProviderName, &t.ProviderRef,
			&t.CreatedAt, &t.ProcessedAt, &t.CompletedAt, &t.ExpiredAt,
		); err != nil {
			return nil, fmt.Errorf("scan transaction: %w", err)
		}
		txns = append(txns, t)
	}
	return txns, rows.Err()
}

// FindByID finds a transaction by ID, scoped to user_id (ownership in WHERE).
func (r *TransactionRepo) FindByID(ctx context.Context, userID, txnID uuid.UUID) (*transaction.Transaction, error) {
	query := `
		SELECT id, idempotency_key, user_id, source_account_id, destination_account_id,
		       type, status, amount, admin_fee, total_amount, currency,
		       reference_number, description, notes,
		       destination_account, destination_name, destination_bank, destination_bank_code,
		       provider_id, provider_name, provider_ref,
		       created_at, processed_at, completed_at, expired_at
		FROM transactions
		WHERE id = $1 AND user_id = $2`

	var t transaction.Transaction
	err := r.pool.QueryRow(ctx, query, txnID, userID).Scan(
		&t.ID, &t.IdempotencyKey, &t.UserID, &t.SourceAccountID, &t.DestinationAccountID,
		&t.Type, &t.Status, &t.Amount, &t.AdminFee, &t.TotalAmount, &t.Currency,
		&t.ReferenceNumber, &t.Description, &t.Notes,
		&t.DestinationAccount, &t.DestinationName, &t.DestinationBank, &t.DestinationBankCode,
		&t.ProviderID, &t.ProviderName, &t.ProviderRef,
		&t.CreatedAt, &t.ProcessedAt, &t.CompletedAt, &t.ExpiredAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find transaction: %w", err)
	}
	return &t, nil
}

// FindByIdempotencyKey finds a transaction by user + idempotency key.
func (r *TransactionRepo) FindByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (*transaction.Transaction, error) {
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
	err := r.pool.QueryRow(ctx, query, userID, key).Scan(
		&t.ID, &t.IdempotencyKey, &t.UserID, &t.SourceAccountID, &t.DestinationAccountID,
		&t.Type, &t.Status, &t.Amount, &t.AdminFee, &t.TotalAmount, &t.Currency,
		&t.ReferenceNumber, &t.Description, &t.Notes,
		&t.DestinationAccount, &t.DestinationName, &t.DestinationBank, &t.DestinationBankCode,
		&t.ProviderID, &t.ProviderName, &t.ProviderRef,
		&t.CreatedAt, &t.ProcessedAt, &t.CompletedAt, &t.ExpiredAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find by idempotency: %w", err)
	}
	return &t, nil
}

// InquiryRepo provides data access for transfer inquiries.
type InquiryRepo struct {
	pool *pgxpool.Pool
}

func NewInquiryRepo(pool *pgxpool.Pool) *InquiryRepo {
	return &InquiryRepo{pool: pool}
}

// Create inserts a new inquiry row.
func (r *InquiryRepo) Create(ctx context.Context, inquiry *transaction.Inquiry) error {
	query := `
		INSERT INTO transfer_inquiries (id, user_id, inquiry_type,
		       destination_account, destination_name, destination_bank, bank_code,
		       provider_id, amount, admin_fee, metadata, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

	_, err := r.pool.Exec(ctx, query,
		inquiry.ID, inquiry.UserID, inquiry.InquiryType,
		inquiry.DestinationAccount, inquiry.DestinationName, inquiry.DestinationBank, inquiry.BankCode,
		inquiry.ProviderID, inquiry.Amount, inquiry.AdminFee, inquiry.Metadata,
		inquiry.ExpiresAt, inquiry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create inquiry: %w", err)
	}
	return nil
}

// FindByID finds an inquiry by ID, scoped to user_id.
func (r *InquiryRepo) FindByID(ctx context.Context, userID, inquiryID uuid.UUID) (*transaction.Inquiry, error) {
	query := `
		SELECT id, user_id, inquiry_type, destination_account, destination_name,
		       destination_bank, bank_code, provider_id, amount, admin_fee,
		       metadata, expires_at, used_at, created_at
		FROM transfer_inquiries
		WHERE id = $1 AND user_id = $2`

	var inq transaction.Inquiry
	err := r.pool.QueryRow(ctx, query, inquiryID, userID).Scan(
		&inq.ID, &inq.UserID, &inq.InquiryType,
		&inq.DestinationAccount, &inq.DestinationName,
		&inq.DestinationBank, &inq.BankCode,
		&inq.ProviderID, &inq.Amount, &inq.AdminFee,
		&inq.Metadata, &inq.ExpiresAt, &inq.UsedAt, &inq.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find inquiry: %w", err)
	}
	return &inq, nil
}

// MarkUsed marks the inquiry as used. Returns false if already used or not found.
func (r *InquiryRepo) MarkUsed(ctx context.Context, inquiryID uuid.UUID) (bool, error) {
	query := `
		UPDATE transfer_inquiries
		SET used_at = $2
		WHERE id = $1 AND used_at IS NULL
		RETURNING id`

	var id uuid.UUID
	err := r.pool.QueryRow(ctx, query, inquiryID, time.Now().UTC()).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark inquiry used: %w", err)
	}
	return true, nil
}

// VerificationTokenRepo provides data access for verification tokens in PostgreSQL.
type VerificationTokenRepo struct {
	pool *pgxpool.Pool
}

func NewVerificationTokenRepo(pool *pgxpool.Pool) *VerificationTokenRepo {
	return &VerificationTokenRepo{pool: pool}
}

// Create inserts a new verification token row.
func (r *VerificationTokenRepo) Create(ctx context.Context, token *transaction.VerificationToken) error {
	query := `
		INSERT INTO verification_tokens (id, user_id, token_hash, purpose, transaction_id, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query,
		token.ID, token.UserID, token.TokenHash, token.Purpose,
		token.TransactionID, token.ExpiresAt, token.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create vtoken: %w", err)
	}
	return nil
}

// MarkUsed marks the token as used.
func (r *VerificationTokenRepo) MarkUsed(ctx context.Context, tokenHash string) error {
	query := `
		UPDATE verification_tokens
		SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL`

	_, err := r.pool.Exec(ctx, query, tokenHash, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark vtoken used: %w", err)
	}
	return nil
}

// FavoriteTransferRepo provides data access for favorite transfers.
type FavoriteTransferRepo struct {
	pool *pgxpool.Pool
}

func NewFavoriteTransferRepo(pool *pgxpool.Pool) *FavoriteTransferRepo {
	return &FavoriteTransferRepo{pool: pool}
}

// ListByUserID returns recent transfers, ordered by last_transfer_at DESC.
func (r *FavoriteTransferRepo) ListByUserID(ctx context.Context, userID uuid.UUID, limit int) ([]transaction.FavoriteTransfer, error) {
	query := `
		SELECT id, user_id, destination_account, destination_name,
		       destination_bank, bank_code, transfer_type,
		       transfer_count, last_transfer_at
		FROM favorite_transfers
		WHERE user_id = $1
		ORDER BY last_transfer_at DESC NULLS LAST
		LIMIT $2`

	rows, err := r.pool.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query favorites: %w", err)
	}
	defer rows.Close()

	var favs []transaction.FavoriteTransfer
	for rows.Next() {
		var f transaction.FavoriteTransfer
		if err := rows.Scan(
			&f.ID, &f.UserID, &f.DestinationAccount, &f.DestinationName,
			&f.DestinationBank, &f.BankCode, &f.TransferType,
			&f.TransferCount, &f.LastTransferAt,
		); err != nil {
			return nil, fmt.Errorf("scan favorite: %w", err)
		}
		favs = append(favs, f)
	}
	return favs, rows.Err()
}

// Upsert inserts or updates a favorite transfer, incrementing transfer_count.
func (r *FavoriteTransferRepo) Upsert(ctx context.Context, fav *transaction.FavoriteTransfer) error {
	query := `
		INSERT INTO favorite_transfers (user_id, destination_account, destination_name,
		       destination_bank, bank_code, transfer_type, transfer_count, last_transfer_at)
		VALUES ($1, $2, $3, $4, $5, $6, 1, $7)
		ON CONFLICT (user_id, destination_account, bank_code)
		DO UPDATE SET transfer_count = favorite_transfers.transfer_count + 1,
		             last_transfer_at = $7,
		             destination_name = $3`

	_, err := r.pool.Exec(ctx, query,
		fav.UserID, fav.DestinationAccount, fav.DestinationName,
		fav.DestinationBank, fav.BankCode, fav.TransferType,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert favorite: %w", err)
	}
	return nil
}