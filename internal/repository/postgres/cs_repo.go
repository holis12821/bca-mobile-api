package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
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

// --- Pendaftaran petugas ---

// CSAgentRegistryRepo mengimplementasikan cs.AgentRegistry.
type CSAgentRegistryRepo struct {
	pool *pgxpool.Pool
}

func NewCSAgentRegistryRepo(pool *pgxpool.Pool) *CSAgentRegistryRepo {
	return &CSAgentRegistryRepo{pool: pool}
}

// Register menyisipkan baris cs_agents baru.
//
// NPP ganda dijawab dari PELANGGARAN PRIMARY KEY (23505), bukan dari SELECT lebih dulu:
// dua pendaftaran bersamaan untuk NPP yang sama akan sama-sama melihat "belum ada", dan
// yang kedua akan menimpa kunci API yang baru saja diserahkan ke orang pertama.
//
// Cakupan kosong tidak perlu dijaga di sini — CHECK cs_agents_scopes_valid di migrasi
// 000027 menuntut cardinality > 0 dan membatasi nilainya ke empat cakupan yang ada.
// Service tetap memeriksanya lebih dulu supaya pesannya bisa menyebut cakupan MANA yang
// tidak dikenal, yang tidak bisa dilakukan sebuah CHECK.
func (r *CSAgentRegistryRepo) Register(ctx context.Context, employeeID, name, apiKeyHash string, scopes []string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs_agents (employee_id, name, api_key_hash, scopes, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, true, $5, $5)`,
		employeeID, name, apiKeyHash, scopes, at,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return apperr.AgentAlreadyRegistered
		}
		return fmt.Errorf("insert cs agent: %w", err)
	}
	return nil
}

// Update mengubah cakupan dan/atau keaktifan petugas yang sudah terdaftar.
//
// Satu transaksi dengan SELECT … FOR UPDATE lebih dulu, dan itu bukan kehati-hatian
// berlebihan: jejak audit perubahan kewenangan menyebut cakupan yang DICABUT, jadi
// keadaan "sebelum" harus dibaca di bawah kunci yang sama dengan penulisnya. Dibaca di
// luar transaksi, dua PATCH bersamaan akan menghasilkan dua jejak yang masing-masing
// mengklaim mencabut cakupan yang sebenarnya sudah dicabut yang lain.
//
// COALESCE dengan cast eksplisit: bidang yang tidak disebut pemanggil dikirim NULL, dan
// `COALESCE($2, scopes)` tanpa `::TEXT[]` membuat Postgres tidak bisa menyimpulkan tipe
// parameter yang selalu NULL.
func (r *CSAgentRegistryRepo) Update(
	ctx context.Context, employeeID string, upd cs.AgentUpdate, at time.Time,
) (*cs.AgentRecord, *cs.AgentRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin update cs agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var before cs.AgentRecord
	err = tx.QueryRow(ctx, `
		SELECT employee_id, name, scopes, is_active, updated_at
		FROM cs_agents
		WHERE employee_id = $1
		FOR UPDATE`, employeeID,
	).Scan(&before.EmployeeID, &before.Name, &before.Scopes, &before.IsActive, &before.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, apperr.AgentNotFound
		}
		return nil, nil, fmt.Errorf("lock cs agent: %w", err)
	}

	// Nil, bukan irisan kosong: `[]string{}` lolos ke Postgres sebagai array kosong dan
	// menabrak `cardinality(scopes) > 0`, yang akan terbaca sebagai kesalahan skema
	// padahal pemanggilnya hanya tidak menyebut cakupan.
	var scopesArg any
	if upd.Scopes != nil {
		scopesArg = upd.Scopes
	}

	var after cs.AgentRecord
	err = tx.QueryRow(ctx, `
		UPDATE cs_agents
		SET scopes     = COALESCE($2::TEXT[], scopes),
		    is_active  = COALESCE($3::BOOLEAN, is_active),
		    updated_at = $4
		WHERE employee_id = $1
		RETURNING employee_id, name, scopes, is_active, updated_at`,
		employeeID, scopesArg, upd.IsActive, at,
	).Scan(&after.EmployeeID, &after.Name, &after.Scopes, &after.IsActive, &after.UpdatedAt)
	if err != nil {
		// 23514 = pelanggaran CHECK. Satu-satunya yang bisa kena di sini adalah
		// cs_agents_scopes_valid, dan service sudah memeriksanya lebih dulu supaya
		// pesannya bisa menyebut cakupan MANA yang salah. Kalau tetap sampai ke sini,
		// itu kesalahan perakitan — dicatat, bukan dibocorkan sebagai 500 tanpa jejak.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			slog.Error("cs agent update violated a schema check",
				"employee_id", employeeID, "constraint", pgErr.ConstraintName)
			return nil, nil, apperr.ValidationError
		}
		return nil, nil, fmt.Errorf("update cs agent: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit update cs agent: %w", err)
	}
	return &before, &after, nil
}
