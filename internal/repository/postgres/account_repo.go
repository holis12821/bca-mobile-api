package postgres

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type AccountRepo struct {
	pool *pgxpool.Pool
}

func NewAccountRepo(pool *pgxpool.Pool) *AccountRepo {
	return &AccountRepo{pool: pool}
}

// FindActiveByUserID returns all ACTIVE CUSTOMER accounts for a user.
func (r *AccountRepo) FindActiveByUserID(ctx context.Context, userID uuid.UUID) ([]account.Account, error) {
	query := `
		SELECT id, user_id, account_number, account_type, account_label,
		       currency, balance, hold_amount, available_balance,
		       is_primary, status, opened_at
		FROM accounts
		WHERE user_id = $1
		  AND status = 'ACTIVE'
		  AND owner_type = 'CUSTOMER'
		ORDER BY is_primary DESC, opened_at ASC`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	defer rows.Close()

	var accounts []account.Account
	for rows.Next() {
		var a account.Account
		if err := rows.Scan(
			&a.ID, &a.UserID, &a.AccountNumber, &a.AccountType, &a.AccountLabel,
			&a.Currency, &a.Balance, &a.HoldAmount, &a.AvailableBalance,
			&a.IsPrimary, &a.Status, &a.OpenedAt,
		); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// FindOwnedByID returns the account only when userID owns it.
// This is the ownership gate for every debit path — see the interface doc.
func (r *AccountRepo) FindOwnedByID(ctx context.Context, userID, accountID uuid.UUID) (*account.Account, error) {
	query := `
		SELECT id, user_id, account_number, account_type, account_label,
		       currency, balance, hold_amount, available_balance,
		       is_primary, status, opened_at
		FROM accounts
		WHERE id = $1
		  AND user_id = $2
		  AND owner_type = 'CUSTOMER'
		  AND status = 'ACTIVE'`

	var a account.Account
	err := r.pool.QueryRow(ctx, query, accountID, userID).Scan(
		&a.ID, &a.UserID, &a.AccountNumber, &a.AccountType, &a.AccountLabel,
		&a.Currency, &a.Balance, &a.HoldAmount, &a.AvailableBalance,
		&a.IsPrimary, &a.Status, &a.OpenedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find owned account: %w", err)
	}
	return &a, nil
}

// FindByAccountNumber finds a CUSTOMER account by account number.
// Filters owner_type = 'CUSTOMER' to exclude settlement shards (9902000000).
func (r *AccountRepo) FindByAccountNumber(ctx context.Context, accountNumber string) (*account.Account, error) {
	query := `
		SELECT id, user_id, account_number, account_type, account_label,
		       currency, balance, hold_amount, available_balance,
		       is_primary, status, opened_at
		FROM accounts
		WHERE account_number = $1
		  AND owner_type = 'CUSTOMER'
		  AND status = 'ACTIVE'`

	var a account.Account
	err := r.pool.QueryRow(ctx, query, accountNumber).Scan(
		&a.ID, &a.UserID, &a.AccountNumber, &a.AccountType, &a.AccountLabel,
		&a.Currency, &a.Balance, &a.HoldAmount, &a.AvailableBalance,
		&a.IsPrimary, &a.Status, &a.OpenedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find account by number: %w", err)
	}
	return &a, nil
}

// ProfileRepo provides user profile data with PII decryption at repository layer.
type ProfileRepo struct {
	pool          *pgxpool.Pool
	piiPassphrase string // pgcrypto passphrase for pgp_sym_encrypt/decrypt
}

// NewProfileRepo takes the raw PII key and holds it hex-encoded.
//
// pgp_sym_encrypt's key parameter is TEXT, and pgx sends text as UTF-8. The
// raw 32 bytes are arbitrary binary — any byte ≥ 0x80 makes Postgres reject
// the whole statement with SQLSTATE 22021, so this must be the hex form.
// Whatever writes these columns (the API, the seeder) has to agree on it.
func NewProfileRepo(pool *pgxpool.Pool, piiKey []byte) *ProfileRepo {
	return &ProfileRepo{pool: pool, piiPassphrase: hex.EncodeToString(piiKey)}
}

// FindProfile returns the user profile with decrypted PII fields.
func (r *ProfileRepo) FindProfile(ctx context.Context, userID uuid.UUID) (*account.UserProfile, error) {
	query := `
		SELECT id, full_name, display_name,
		       pgp_sym_decrypt(phone_encrypted, $2) AS phone,
		       COALESCE(pgp_sym_decrypt(email_encrypted, $2), '') AS email,
		       last_login_at
		FROM users
		WHERE id = $1
		  AND deleted_at IS NULL`

	var profile account.UserProfile
	var phone, email string
	err := r.pool.QueryRow(ctx, query, userID, r.piiPassphrase).Scan(
		&profile.ID, &profile.FullName, &profile.DisplayName,
		&phone, &email,
		&profile.LastLoginAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find profile: %w", err)
	}
	profile.Phone = phone
	profile.Email = email
	return &profile, nil
}

// UpdateEmail updates the user's encrypted email.
func (r *ProfileRepo) UpdateEmail(ctx context.Context, userID uuid.UUID, email string) error {
	query := `
		UPDATE users
		SET email_encrypted = pgp_sym_encrypt($2, $3),
		    updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, userID, email, r.piiPassphrase)
	if err != nil {
		return fmt.Errorf("update email: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// UpdateSettings updates the user's settings flags.
func (r *ProfileRepo) UpdateSettings(ctx context.Context, userID uuid.UUID, req account.UpdateSettingsRequest) error {
	query := `
		UPDATE users
		SET biometric_enabled = COALESCE($2, biometric_enabled),
		    push_notification_enabled = COALESCE($3, push_notification_enabled),
		    email_statement_enabled = COALESCE($4, email_statement_enabled),
		    updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, userID, req.BiometricEnabled, req.PushNotificationEnabled, req.EmailStatementEnabled)
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// TransactionLimitRepo provides data access for transaction limits.
type TransactionLimitRepo struct {
	pool *pgxpool.Pool
}

func NewTransactionLimitRepo(pool *pgxpool.Pool) *TransactionLimitRepo {
	return &TransactionLimitRepo{pool: pool}
}

// FindByUserID returns all limits for a user.
func (r *TransactionLimitRepo) FindByUserID(ctx context.Context, userID uuid.UUID) ([]account.TransactionLimit, error) {
	query := `
		SELECT id, user_id, limit_type, daily_limit,
		       monthly_limit, per_transaction_limit
		FROM transaction_limits
		WHERE user_id = $1
		ORDER BY limit_type`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query limits: %w", err)
	}
	defer rows.Close()

	var limits []account.TransactionLimit
	for rows.Next() {
		var l account.TransactionLimit
		var monthly, perTxn *decimal.Decimal
		if err := rows.Scan(&l.ID, &l.UserID, &l.LimitType, &l.DailyLimit, &monthly, &perTxn); err != nil {
			return nil, fmt.Errorf("scan limit: %w", err)
		}
		l.MonthlyLimit = monthly
		l.PerTransactionLimit = perTxn
		limits = append(limits, l)
	}
	return limits, rows.Err()
}

// UsedToday returns today's consumed amount per limit type.
// wibDate is a calendar date resolved by the caller in Asia/Jakarta.
func (r *TransactionLimitRepo) UsedToday(ctx context.Context, userID uuid.UUID, wibDate string) (map[string]decimal.Decimal, error) {
	const query = `
		SELECT limit_type, COALESCE(total_amount, 0)
		FROM daily_usage
		WHERE user_id = $1 AND usage_date = $2`

	rows, err := r.pool.Query(ctx, query, userID, wibDate)
	if err != nil {
		return nil, fmt.Errorf("query daily usage: %w", err)
	}
	defer rows.Close()

	used := make(map[string]decimal.Decimal)
	for rows.Next() {
		var limitType string
		var amount decimal.Decimal
		if err := rows.Scan(&limitType, &amount); err != nil {
			return nil, fmt.Errorf("scan daily usage: %w", err)
		}
		used[limitType] = amount
	}
	return used, rows.Err()
}

// UpdateLimit updates a single limit type for a user.
func (r *TransactionLimitRepo) UpdateLimit(ctx context.Context, userID uuid.UUID, limitType string, update account.LimitUpdate) error {
	// Build dynamic SET clause based on which fields are provided
	setClauses := ""
	args := []any{userID, limitType}
	argIdx := 3

	if update.DailyLimit != nil {
		setClauses += fmt.Sprintf("daily_limit = $%d", argIdx)
		args = append(args, *update.DailyLimit)
		argIdx++
	}
	if update.MonthlyLimit != nil {
		if setClauses != "" {
			setClauses += ", "
		}
		setClauses += fmt.Sprintf("monthly_limit = $%d", argIdx)
		args = append(args, *update.MonthlyLimit)
		argIdx++
	}
	if update.PerTransactionLimit != nil {
		if setClauses != "" {
			setClauses += ", "
		}
		setClauses += fmt.Sprintf("per_transaction_limit = $%d", argIdx)
		args = append(args, *update.PerTransactionLimit)
	}

	if setClauses == "" {
		return nil // nothing to update
	}

	query := fmt.Sprintf(`
		UPDATE transaction_limits
		SET %s
		WHERE user_id = $1 AND limit_type = $2`, setClauses)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update limit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound
	}
	return nil
}
