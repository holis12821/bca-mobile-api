package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// RegistrationExecutor creates the user, account, device binding and default
// limits for the legacy /v1/registration flow.
//
// It used to insert into users(phone_number, email) and devices(updated_at) —
// none of which exist — while leaving display_name, phone_hash, phone_encrypted
// and pin_salt (all NOT NULL) unset, and stamping account_type 'SAVINGS' which
// the CHECK constraint rejects. Every call therefore failed. The columns below
// now match migrations 000001/000002 and mirror what OnboardingProvisioner
// writes, so a user created either way looks the same to the rest of the API.
type RegistrationExecutor struct {
	pool *pgxpool.Pool
	// piiPassphrase is the pgcrypto key for pgp_sym_encrypt, hex-encoded —
	// see ProfileRepo for why the hex form is mandatory.
	piiPassphrase string
	lookupHasher  *crypto.HMACHasher
}

func NewRegistrationExecutor(pool *pgxpool.Pool, piiPassphrase string, lookupHasher *crypto.HMACHasher) *RegistrationExecutor {
	return &RegistrationExecutor{
		pool:          pool,
		piiPassphrase: piiPassphrase,
		lookupHasher:  lookupHasher,
	}
}

// CreateUserAndAccount creates a user, account, device binding, and default limits in one transaction.
func (e *RegistrationExecutor) CreateUserAndAccount(ctx context.Context, params registration.CreateUserParams) (*registration.CreateUserResult, error) {
	if e.lookupHasher == nil {
		return nil, errors.New("registration executor requires a lookup hasher (LOOKUP_HMAC_SECRET)")
	}
	if params.PhoneNumber == "" {
		return nil, errors.New("registration executor requires a phone number")
	}

	pgxTx, err := e.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback setelah commit yang sukses mengembalikan pgx.ErrTxClosed — itu
	// jalur normal, bukan kegagalan. Sisanya dicatat: transaksi yang gagal
	// dilepas menahan koneksi di pool sampai timeout, dan diam-diam adalah cara
	// terburuk untuk mengetahuinya.
	defer func() {
		if rbErr := pgxTx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Error("rollback gagal", "op", "registration complete", "error", rbErr)
		}
	}()

	userID := uuid.New()
	now := time.Now().UTC()

	var emailHash *string
	if params.Email != "" {
		h := e.lookupHasher.Hash(params.Email)
		emailHash = &h
	}

	_, err = pgxTx.Exec(ctx, `
		INSERT INTO users (id, full_name, display_name, nik_encrypted, pin_hash, pin_salt,
			phone_encrypted, phone_hash, email_encrypted, email_hash,
			status, created_at, updated_at)
		VALUES ($1, $2, $3, pgp_sym_encrypt($4, $5), $6, $7,
			pgp_sym_encrypt($8, $5), $9, pgp_sym_encrypt($10, $5), $11,
			'ACTIVE', $12, $12)`,
		userID, params.FullName, displayNameFrom(params.FullName), params.NIK, e.piiPassphrase,
		params.PINHash, argon2SaltSegment(params.PINHash),
		params.PhoneNumber, e.lookupHasher.Hash(params.PhoneNumber),
		params.Email, emailHash,
		now,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, apperr.RegistrationDuplicate
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	// Account number: 10 digits from crypto/rand. math/rand seeded per process
	// would hand two concurrent registrations the same number.
	accountNumber, err := randomAccountNumber("1")
	if err != nil {
		return nil, fmt.Errorf("generate account number: %w", err)
	}
	accountID := uuid.New()

	_, err = pgxTx.Exec(ctx, `
		INSERT INTO accounts (id, user_id, account_number, account_type, account_label,
			balance, currency, status, is_primary, owner_type, opened_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'TAHAPAN', 'Tahapan BCA', 0, 'IDR', 'ACTIVE', true, 'CUSTOMER', $4, $4, $4)`,
		accountID, userID, accountNumber, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("account number %s already exists: %w", accountNumber, err)
		}
		return nil, fmt.Errorf("insert account: %w", err)
	}

	// Device binding. device_id is unique among active rows (migration 000009),
	// so an existing active binding is revoked first — the same handset can
	// legitimately be re-registered.
	if params.DeviceID != "" {
		if _, err := pgxTx.Exec(ctx, `
			UPDATE devices SET revoked_at = $2
			WHERE device_id = $1 AND revoked_at IS NULL`,
			params.DeviceID, now); err != nil {
			return nil, fmt.Errorf("revoke previous device binding: %w", err)
		}

		_, err = pgxTx.Exec(ctx, `
			INSERT INTO devices (id, user_id, device_id, device_name, device_model,
				is_trusted, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, true, $6, $6)`,
			uuid.New(), userID, params.DeviceID, "Registration device", params.DeviceModel, now)
		if err != nil {
			return nil, fmt.Errorf("insert device: %w", err)
		}
	}

	for _, l := range registration.DefaultLimits {
		_, err = pgxTx.Exec(ctx, `
			INSERT INTO transaction_limits (id, user_id, limit_type, daily_limit, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (user_id, limit_type) DO NOTHING`,
			uuid.New(), userID, l.Type, l.Daily, now)
		if err != nil {
			return nil, fmt.Errorf("insert limit %s: %w", l.Type, err)
		}
	}

	if err := pgxTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &registration.CreateUserResult{
		UserID:        userID.String(),
		AccountNumber: accountNumber,
	}, nil
}

// randomAccountNumber returns prefix + zero-padded random digits, 10 chars total.
func randomAccountNumber(prefix string) (string, error) {
	digits := 10 - len(prefix)
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%0*d", prefix, digits, n.Int64()), nil
}
