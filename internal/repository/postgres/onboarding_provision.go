package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// OnboardingProvisioner turns a completed onboarding session into the rows the
// rest of the API reads: an m-BCA user, the device binding that lets that user
// log in from the phone they onboarded with, the account, and the default
// transaction limits.
//
// Before this existed, a finished onboarding produced an account number and an
// m-BCA user id that were never written anywhere — the nasabah could not log
// in with the credentials they had just chosen.
type OnboardingProvisioner struct {
	pool *pgxpool.Pool
	// piiPassphrase is the pgcrypto key for pgp_sym_encrypt, hex-encoded for
	// the same reason ProfileRepo documents: the parameter is TEXT, and raw
	// key bytes ≥ 0x80 make Postgres reject the statement (SQLSTATE 22021).
	piiPassphrase string
	lookupHasher  *crypto.HMACHasher
}

func NewOnboardingProvisioner(pool *pgxpool.Pool, piiPassphrase string, lookupHasher *crypto.HMACHasher) *OnboardingProvisioner {
	return &OnboardingProvisioner{
		pool:          pool,
		piiPassphrase: piiPassphrase,
		lookupHasher:  lookupHasher,
	}
}

// accountTypeFor maps an onboarding product onto the account_type the accounts
// table accepts (see the CHECK constraint in migration 000002).
func accountTypeFor(p onboarding.ProductType) string {
	switch p {
	case onboarding.ProductTahapanXpre:
		return "TAHAPAN_XPRESI"
	default:
		// TAHAPAN_BCA and TABUNGANKU are both plain savings accounts as far as
		// the ledger is concerned.
		return "TAHAPAN"
	}
}

func accountLabelFor(p onboarding.ProductType) string {
	if info, ok := onboarding.ProductCatalog[p]; ok {
		return info.Name
	}
	return "Tabungan"
}

// ProvisionAccount creates user + device + account + limits in one transaction.
// Either the nasabah can log in afterwards or nothing was written at all.
func (p *OnboardingProvisioner) ProvisionAccount(ctx context.Context, params onboarding.ProvisionParams) (*onboarding.ProvisionResult, error) {
	if p.lookupHasher == nil {
		return nil, errors.New("provisioner requires a lookup hasher (LOOKUP_HMAC_SECRET)")
	}
	if params.PhoneNumber == "" {
		return nil, errors.New("provisioner requires a phone number")
	}

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback setelah commit yang sukses mengembalikan pgx.ErrTxClosed — itu
	// jalur normal, bukan kegagalan. Sisanya dicatat: transaksi yang gagal
	// dilepas menahan koneksi di pool sampai timeout, dan diam-diam adalah cara
	// terburuk untuk mengetahuinya.
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Error("rollback gagal", "op", "onboarding provision", "error", rbErr)
		}
	}()

	userID := uuid.New()
	accountID := uuid.New()
	now := time.Now().UTC()

	var emailHash *string
	if params.Email != "" {
		h := p.lookupHasher.Hash(params.Email)
		emailHash = &h
	}

	// The PIN the nasabah set during onboarding is their m-BCA PIN. The hash is
	// already Argon2id-encoded and carries its own salt; pin_salt is a legacy
	// column that mirrors the salt segment, exactly as the seeder does.
	// access_code_hash is the kode akses the nasabah chose in the credentials
	// step. It used to be passed in here and dropped, which is why the flow
	// could end with "silakan login dengan kode akses Anda" and a login that
	// only ever checked the PIN.
	_, err = tx.Exec(ctx, `
		INSERT INTO users (id, full_name, display_name, nik_encrypted, pin_hash, pin_salt,
			access_code_hash, phone_encrypted, phone_hash, email_encrypted, email_hash,
			status, created_at, updated_at)
		VALUES ($1, $2, $3, pgp_sym_encrypt($4, $5), $6, $7,
			$8, pgp_sym_encrypt($9, $5), $10, pgp_sym_encrypt($11, $5), $12,
			'ACTIVE', $13, $13)`,
		userID, params.FullName, displayNameFrom(params.FullName), params.NIK, p.piiPassphrase,
		params.PINHash, argon2SaltSegment(params.PINHash),
		nullIfEmpty(params.AccessCodeHash),
		params.PhoneNumber, p.lookupHasher.Hash(params.PhoneNumber),
		params.Email, emailHash,
		now,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Same phone or email already has an m-BCA user.
			return nil, apperr.RegistrationDuplicate
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	// Device binding: the phone that completed onboarding is trusted for login.
	//
	// Migration 000009 made device_id globally unique among active rows — one
	// device, one active user. So an existing active binding for this phone is
	// revoked first (the history index keeps the old row visible) rather than
	// conflicting with the insert. Onboarding on a resold or shared handset has
	// to work, and the revocation is logged.
	if params.DeviceID != "" {
		revoked, revErr := tx.Exec(ctx, `
			UPDATE devices SET revoked_at = $2
			WHERE device_id = $1 AND revoked_at IS NULL`,
			params.DeviceID, now)
		if revErr != nil {
			return nil, fmt.Errorf("revoke previous device binding: %w", revErr)
		}
		if n := revoked.RowsAffected(); n > 0 {
			slog.Warn("revoked previous device binding during onboarding",
				"device_id", params.DeviceID,
				"revoked_rows", n,
				"new_user_id", userID,
			)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO devices (id, user_id, device_id, device_name, is_trusted, created_at)
			VALUES ($1, $2, $3, $4, true, $5)`,
			uuid.New(), userID, params.DeviceID, "Onboarding device", now)
		if err != nil {
			return nil, fmt.Errorf("insert device: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO accounts (id, user_id, account_number, account_type, account_label,
			balance, currency, status, is_primary, owner_type, opened_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 0, 'IDR', 'ACTIVE', true, 'CUSTOMER', $6, $6, $6)`,
		accountID, userID, params.AccountNumber,
		accountTypeFor(params.ProductType), accountLabelFor(params.ProductType), now,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("account number %s already exists: %w", params.AccountNumber, err)
		}
		return nil, fmt.Errorf("insert account: %w", err)
	}

	for _, l := range registration.DefaultLimits {
		_, err = tx.Exec(ctx, `
			INSERT INTO transaction_limits (id, user_id, limit_type, daily_limit, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (user_id, limit_type) DO NOTHING`,
			uuid.New(), userID, l.Type, l.Daily, now)
		if err != nil {
			return nil, fmt.Errorf("insert limit %s: %w", l.Type, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &onboarding.ProvisionResult{
		UserID:        userID.String(),
		AccountID:     accountID.String(),
		AccountNumber: params.AccountNumber,
	}, nil
}

// nullIfEmpty keeps an absent credential NULL rather than an empty string, so
// User.LoginCredential can tell "no access code" from "access code is blank".
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// displayNameFrom takes the first word of the full name, title-cased — the
// dashboard greets the nasabah with this.
func displayNameFrom(fullName string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(fullName), " ")
	if first == "" {
		return fullName
	}
	return strings.ToUpper(first[:1]) + strings.ToLower(first[1:])
}

// argon2SaltSegment pulls the salt out of an Argon2id encoded hash
// ($argon2id$v=19$m=..,t=..,p=..$<salt>$<hash>). The column is legacy — the
// encoded hash carries its own salt and VerifyPassword reads only that.
func argon2SaltSegment(encoded string) string {
	parts := strings.Split(encoded, "$")
	if len(parts) < 5 {
		return "embedded"
	}
	return parts[4]
}
