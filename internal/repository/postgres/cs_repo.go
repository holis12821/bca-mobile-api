package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
)

// CSCustomerRepo membaca nasabah untuk petugas CS.
//
// Mendekripsi PII di sini, bukan di service: kuncinya milik lapisan ini. Service
// menerima teks biasa lalu menyamarkannya — dan karena [cs.Customer] tidak punya tag
// JSON, teks biasa itu tidak bisa lolos ke response tanpa melewati penyamaran.
type CSCustomerRepo struct {
	pool          *pgxpool.Pool
	piiPassphrase string
}

// NewCSCustomerRepo menerima kunci PII mentah dan mengubahnya ke bentuk hex.
//
// Kolom users.nik_encrypted, phone_encrypted, dan email_encrypted ditulis
// pgp_sym_encrypt DI SISI POSTGRES — bukan crypto.AES di sisi Go, yang dipakai tabel
// onboarding_personal_data. Dua skema enkripsi untuk dua tabel yang sama-sama memuat NIK
// adalah jebakan yang nyata: percobaan pertama endpoint ini mendekripsi dengan AES,
// gagal diam-diam, dan menjawab nomor HP kosong tanpa satu pun error.
//
// Passphrase-nya bentuk HEX dari kunci, mengikuti ProfileRepo: parameter key
// pgp_sym_encrypt bertipe TEXT dan pgx mengirim text sebagai UTF-8, jadi 32 byte mentah
// akan ditolak Postgres dengan SQLSTATE 22021 begitu ada byte ≥ 0x80. Yang menulis dan
// yang membaca harus sepakat soal ini.
func NewCSCustomerRepo(pool *pgxpool.Pool, piiKey []byte) *CSCustomerRepo {
	return &CSCustomerRepo{pool: pool, piiPassphrase: hex.EncodeToString(piiKey)}
}

// selectCustomer adalah kolom yang dipakai ketiga pencarian. Satu tempat supaya urutan
// Scan tidak pernah berbeda antar query — penyebab paling sering kolom tertukar di
// repository yang punya beberapa jalur baca untuk tipe yang sama.
//
// COALESCE di ketiganya: nik_encrypted dan email_encrypted nullable, dan
// pgp_sym_decrypt(NULL) adalah NULL — yang akan gagal di-Scan ke string. Nasabah lama
// tanpa NIK tersimpan adalah keadaan yang sah, bukan alasan menggagalkan seluruh profil.
const selectCustomer = `
	SELECT u.id, u.full_name, u.display_name,
	       COALESCE(pgp_sym_decrypt(u.nik_encrypted, $1), '')   AS nik,
	       COALESCE(pgp_sym_decrypt(u.phone_encrypted, $1), '') AS phone,
	       COALESCE(pgp_sym_decrypt(u.email_encrypted, $1), '') AS email,
	       u.tier, u.status,
	       u.biometric_enabled, u.push_notification_enabled,
	       u.locked_until, u.last_login_at, u.created_at
	FROM users u`

func (r *CSCustomerRepo) scanCustomer(row pgx.Row) (*cs.Customer, error) {
	var c cs.Customer

	err := row.Scan(
		&c.UserID, &c.FullName, &c.DisplayName,
		&c.NIK, &c.Phone, &c.Email,
		&c.Tier, &c.Status,
		&c.BiometricEnabled, &c.PushNotificationEnabled,
		&c.LockedUntil, &c.LastLoginAt, &c.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan cs customer: %w", err)
	}
	return &c, nil
}

func (r *CSCustomerRepo) FindByID(ctx context.Context, userID uuid.UUID) (*cs.Customer, error) {
	row := r.pool.QueryRow(ctx,
		selectCustomer+` WHERE u.id = $2 AND u.deleted_at IS NULL`,
		r.piiPassphrase, userID)
	return r.scanCustomer(row)
}

// FindByAccountNumber mencari PEMILIK sebuah nomor rekening.
//
// owner_type = 'CUSTOMER' bukan hiasan: tabel accounts juga memuat rekening internal
// (settlement, suspense) yang tidak punya nasabah, dan pencarian tanpa syarat itu akan
// menjawab rekening sistem sebagai kalau ia milik seseorang.
func (r *CSCustomerRepo) FindByAccountNumber(ctx context.Context, accountNumber string) (*cs.Customer, error) {
	if accountNumber == "" {
		return nil, nil
	}
	row := r.pool.QueryRow(ctx, selectCustomer+`
		JOIN accounts a ON a.user_id = u.id
		WHERE a.account_number = $2
		  AND a.owner_type = 'CUSTOMER'
		  AND u.deleted_at IS NULL`, r.piiPassphrase, accountNumber)
	return r.scanCustomer(row)
}

// FindByPhoneHashes mencari nasabah yang phone_hash-nya ada di daftar.
//
// = ANY($1) memakai idx_users_phone_hash untuk setiap kandidat dalam satu kali jalan;
// menjalankan satu query per kandidat akan mengalikan beban jalur yang di-polling petugas.
func (r *CSCustomerRepo) FindByPhoneHashes(ctx context.Context, phoneHashes []string) ([]*cs.Customer, error) {
	if len(phoneHashes) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, selectCustomer+`
		WHERE u.phone_hash = ANY($2) AND u.deleted_at IS NULL`,
		r.piiPassphrase, phoneHashes)
	if err != nil {
		return nil, fmt.Errorf("find customers by phone hash: %w", err)
	}
	defer rows.Close()

	var out []*cs.Customer
	for rows.Next() {
		c, err := r.scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		if c != nil {
			out = append(out, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate customers by phone hash: %w", err)
	}
	return out, nil
}

// ListAccounts mengembalikan rekening nasabah, yang utama lebih dulu.
//
// Rekening tertutup ikut: "rekening saya kok hilang" adalah keluhan yang tidak bisa
// dijawab kalau yang tertutup disembunyikan. Status-nya yang membedakan, bukan
// keberadaannya di daftar.
func (r *CSCustomerRepo) ListAccounts(ctx context.Context, userID uuid.UUID) ([]cs.Account, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT account_number, account_type, account_label, currency,
		       is_primary, status, opened_at, closed_at
		FROM accounts
		WHERE user_id = $1 AND owner_type = 'CUSTOMER'
		ORDER BY is_primary DESC, opened_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list cs accounts: %w", err)
	}
	defer rows.Close()

	var out []cs.Account
	for rows.Next() {
		var a cs.Account
		if err := rows.Scan(
			&a.AccountNumber, &a.AccountType, &a.AccountLabel, &a.Currency,
			&a.IsPrimary, &a.Status, &a.OpenedAt, &a.ClosedAt,
		); err != nil {
			return nil, fmt.Errorf("scan cs account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cs accounts: %w", err)
	}
	return out, nil
}

// CSAccessLogRepo menulis jejak akses petugas. Append-only di tingkat skema.
type CSAccessLogRepo struct {
	pool *pgxpool.Pool
}

func NewCSAccessLogRepo(pool *pgxpool.Pool) *CSAccessLogRepo {
	return &CSAccessLogRepo{pool: pool}
}

func (r *CSAccessLogRepo) Insert(ctx context.Context, log *cs.AccessLog) error {
	// query_kind kosong ditulis NULL, bukan string kosong: CHECK di migrasi 000029 hanya
	// menerima NULL atau salah satu nilai yang dikenal.
	var queryKind *string
	if log.QueryKind != "" {
		k := string(log.QueryKind)
		queryKind = &k
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs_access_logs
			(id, agent_employee_id, action, subject_user_id, query_kind,
			 result_count, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		log.ID, log.AgentEmployeeID, log.Action, log.SubjectUserID, queryKind,
		log.ResultCount, log.IPAddress, log.UserAgent, log.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert cs access log: %w", err)
	}
	return nil
}
