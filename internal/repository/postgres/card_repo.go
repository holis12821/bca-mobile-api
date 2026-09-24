package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// CardRepo membaca katalog kartu Paspor dari PostgreSQL.
// Implementasi onboarding.CardRepository.
type CardRepo struct {
	pool *pgxpool.Pool
}

func NewCardRepo(pool *pgxpool.Pool) *CardRepo {
	return &CardRepo{pool: pool}
}

// cardSelectColumns dipakai bersama ListCards dan GetCard supaya kedua jalur
// tidak bisa melenceng dalam urutan kolom yang dipindai.
const cardSelectColumns = `
	o.card_type, c.name, c.network, c.tier_key, c.style, c.currency,
	o.badge_key, o.is_popular, o.display_order, o.is_default,
	c.fee_monthly_admin, c.fee_card_issuance, c.fee_card_replacement,
	c.limit_cash_withdrawal, c.limit_transfer_bca,
	c.limit_transfer_interbank, c.limit_debit_purchase,
	o.availability_status, o.availability_reason_key,
	c.physical_card_available, c.delivery_days_min, c.delivery_days_max,
	c.branch_pickup_available,
	c.min_age, c.min_initial_deposit`

func scanCard(row pgx.Row) (*onboarding.CardOption, error) {
	var c onboarding.CardOption
	err := row.Scan(
		&c.CardType, &c.Name, &c.Network, &c.TierKey, &c.Style, &c.Currency,
		&c.BadgeKey, &c.IsPopular, &c.DisplayOrder, &c.IsDefault,
		&c.Fees.MonthlyAdmin, &c.Fees.CardIssuance, &c.Fees.CardReplacement,
		&c.Limits.CashWithdrawal, &c.Limits.TransferBCA,
		&c.Limits.TransferInterbank, &c.Limits.DebitPurchase,
		&c.Availability.Status, &c.Availability.ReasonKey,
		&c.Delivery.PhysicalCardAvailable, &c.Delivery.EstimatedDaysMin,
		&c.Delivery.EstimatedDaysMax, &c.Delivery.BranchPickupAvailable,
		&c.Eligibility.MinAge, &c.Eligibility.MinInitialDeposit,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCards mengembalikan kartu yang ditawarkan untuk satu produk.
//
// DISTINCT ON memilih satu baris per card_type, dan ORDER BY di dalamnya
// mendahulukan baris berwilayah di atas baris nasional (region_code NULL) —
// itulah aturan "override wilayah menang" di Prompt 2. Pengurutan tampilan
// dilakukan di query luar, karena DISTINCT ON memaksa ORDER BY dalam dimulai
// dari kolom yang di-distinct.
func (r *CardRepo) ListCards(ctx context.Context, productType onboarding.ProductType, regionCode string) ([]onboarding.CardOption, error) {
	query := `
		SELECT ` + cardSelectColumns + `
		FROM (
			SELECT DISTINCT ON (o.card_type) o.*
			FROM product_card_options o
			WHERE o.product_type = $1
			  AND (o.region_code IS NULL OR o.region_code = NULLIF($2, ''))
			ORDER BY o.card_type, (o.region_code IS NOT NULL) DESC
		) o
		JOIN card_products c ON c.card_type = o.card_type
		WHERE c.is_active
		ORDER BY o.display_order, o.card_type`

	rows, err := r.pool.Query(ctx, query, productType, regionCode)
	if err != nil {
		return nil, fmt.Errorf("query cards: %w", err)
	}
	defer rows.Close()

	var cards []onboarding.CardOption
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		cards = append(cards, *c)
	}
	return cards, rows.Err()
}

// GetCard mengembalikan satu kartu pada satu produk. nil, nil bila tidak ada.
func (r *CardRepo) GetCard(ctx context.Context, productType onboarding.ProductType, cardType, regionCode string) (*onboarding.CardOption, error) {
	query := `
		SELECT ` + cardSelectColumns + `
		FROM (
			SELECT DISTINCT ON (o.card_type) o.*
			FROM product_card_options o
			WHERE o.product_type = $1
			  AND o.card_type = $2
			  AND (o.region_code IS NULL OR o.region_code = NULLIF($3, ''))
			ORDER BY o.card_type, (o.region_code IS NOT NULL) DESC
		) o
		JOIN card_products c ON c.card_type = o.card_type
		WHERE c.is_active`

	card, err := scanCard(r.pool.QueryRow(ctx, query, productType, cardType, regionCode))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get card: %w", err)
	}
	return card, nil
}

// CatalogVersion mengembalikan versi katalog saat ini, format YYYY-MM-DD.n
func (r *CardRepo) CatalogVersion(ctx context.Context) (string, error) {
	const query = `
		SELECT to_char(version_date, 'YYYY-MM-DD') || '.' || counter
		FROM card_catalog_version WHERE id`

	var version string
	if err := r.pool.QueryRow(ctx, query).Scan(&version); err != nil {
		return "", fmt.Errorf("read catalog version: %w", err)
	}
	return version, nil
}

// BumpCatalogVersion menaikkan versi katalog satu langkah.
//
// Counter direset saat tanggalnya berganti. UPDATE tunggal ini atomik, jadi dua
// penulisan katalog yang bersamaan tidak bisa menghasilkan versi yang sama —
// syarat agar ETag dan kunci cache tetap bisa dipercaya.
func (r *CardRepo) BumpCatalogVersion(ctx context.Context) (string, error) {
	const query = `
		UPDATE card_catalog_version
		SET version_date = CURRENT_DATE,
		    counter = CASE WHEN version_date = CURRENT_DATE THEN counter + 1 ELSE 1 END,
		    updated_at = NOW()
		WHERE id
		RETURNING to_char(version_date, 'YYYY-MM-DD') || '.' || counter`

	var version string
	if err := r.pool.QueryRow(ctx, query).Scan(&version); err != nil {
		return "", fmt.Errorf("bump catalog version: %w", err)
	}
	return version, nil
}

// LogCardSelection menulis jejak audit pemilihan kartu.
func (r *CardRepo) LogCardSelection(ctx context.Context, entry onboarding.CardSelectionLogEntry) error {
	const query = `
		INSERT INTO onboarding_card_selection_log
			(session_id, from_card_type, to_card_type, catalog_version,
			 monthly_admin_fee_shown, actor, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query,
		entry.SessionID, entry.FromCardType, entry.ToCardType, entry.CatalogVersion,
		entry.MonthlyAdminFeeShown, entry.Actor, parseAuditIP(entry.IPAddress))
	if err != nil {
		return fmt.Errorf("insert card selection log: %w", err)
	}
	return nil
}

// parseAuditIP mengubah alamat menjadi nilai yang aman untuk kolom INET.
//
// Sebelumnya kolom ini diisi NULLIF($7,”)::inet, yang hanya menangani string
// kosong: alamat cacat — dan X-Forwarded-For dari proxy tak dikenal bisa berisi
// apa saja — membuat SELURUH INSERT gagal dengan
//
//	invalid input syntax for type inet
//
// Pemanggilnya best-effort dan hanya mencatat log, jadi jejak audit hilang
// diam-diam. §11 menyebut audit ini syarat kepatuhan; kehilangan alamat jauh
// lebih ringan daripada kehilangan catatannya.
func parseAuditIP(raw string) *string {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return nil
	}
	// Buang port bila ikut terbawa ("203.0.113.7:54321").
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	if net.ParseIP(addr) == nil {
		slog.Warn("alamat IP audit tidak valid; jejak tetap ditulis tanpa alamat",
			"raw", raw)
		return nil
	}
	return &addr
}

// --- Antrean permintaan cetak kartu (§10) ---

// CardIssuanceRepo menyimpan antrean permintaan cetak kartu ke core banking.
// Implementasi onboarding.CardIssuanceRepository.
type CardIssuanceRepo struct {
	pool *pgxpool.Pool
}

func NewCardIssuanceRepo(pool *pgxpool.Pool) *CardIssuanceRepo {
	return &CardIssuanceRepo{pool: pool}
}

// Claim menyisipkan permintaan cetak untuk satu sesi, sekali saja.
//
// ON CONFLICT DO NOTHING pada UNIQUE(session_id) adalah penjaga sesungguhnya
// terhadap dua kali cetak. Idempotency-Key menjaga di Redis, dan Redis bisa
// kehilangan kuncinya karena eviction; baris ini tidak bisa.
func (r *CardIssuanceRepo) Claim(ctx context.Context, iss onboarding.CardIssuance) (bool, error) {
	query := `
		INSERT INTO onboarding_card_issuance
			(session_id, account_number, card_type, core_banking_code, status,
			 delivery_method, estimated_arrival_from, estimated_arrival_to)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (session_id) DO NOTHING`

	tag, err := r.pool.Exec(ctx, query,
		iss.SessionID, iss.AccountNumber, iss.CardType, iss.CoreBankingCode,
		string(iss.Status), string(iss.DeliveryMethod),
		iss.EstimatedFrom, iss.EstimatedTo,
	)
	if err != nil {
		return false, fmt.Errorf("claim card issuance: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkResult menyimpan hasil penerbitan yang berhasil dan mengosongkan jadwal
// retry — baris yang sudah selesai tidak boleh terbaca pekerja retry lagi.
func (r *CardIssuanceRepo) MarkResult(ctx context.Context, sessionID string, result onboarding.CardIssuanceResult) error {
	query := `
		UPDATE onboarding_card_issuance
		SET masked_number = $2,
		    status = $3,
		    tracking_number = $4,
		    last_error = NULL,
		    next_retry_at = NULL,
		    updated_at = NOW()
		WHERE session_id = $1`

	if _, err := r.pool.Exec(ctx, query, sessionID,
		nullableText(result.MaskedNumber), string(result.Status), result.TrackingNumber,
	); err != nil {
		return fmt.Errorf("mark card issuance result: %w", err)
	}
	return nil
}

// MarkFailed mencatat kegagalan dan menjadwalkan percobaan berikutnya.
//
// Status baris menjadi FAILED, tapi yang DILIHAT nasabah tetap REQUESTED:
// rekeningnya sudah jadi dan kartunya masih dalam antrean, jadi FAILED di sini
// adalah keadaan operasional, bukan kabar untuk nasabah (§10).
func (r *CardIssuanceRepo) MarkFailed(ctx context.Context, sessionID, reason string, nextRetryAt time.Time) error {
	query := `
		UPDATE onboarding_card_issuance
		SET status = 'FAILED',
		    attempts = attempts + 1,
		    last_error = $2,
		    next_retry_at = $3,
		    updated_at = NOW()
		WHERE session_id = $1`

	if _, err := r.pool.Exec(ctx, query, sessionID, reason, nextRetryAt); err != nil {
		return fmt.Errorf("mark card issuance failed: %w", err)
	}
	return nil
}

const cardIssuanceColumns = `
	session_id, account_number, card_type, core_banking_code, status,
	masked_number, delivery_method, estimated_arrival_from, estimated_arrival_to,
	tracking_number, attempts, last_error, next_retry_at`

func scanCardIssuance(row pgx.Row) (*onboarding.CardIssuance, error) {
	var iss onboarding.CardIssuance
	var status, delivery string
	err := row.Scan(
		&iss.SessionID, &iss.AccountNumber, &iss.CardType, &iss.CoreBankingCode, &status,
		&iss.MaskedNumber, &delivery, &iss.EstimatedFrom, &iss.EstimatedTo,
		&iss.TrackingNumber, &iss.Attempts, &iss.LastError, &iss.NextRetryAt,
	)
	if err != nil {
		return nil, err
	}
	iss.Status = onboarding.CardIssuanceStatus(status)
	iss.DeliveryMethod = onboarding.CardDeliveryMethod(delivery)
	return &iss, nil
}

func (r *CardIssuanceRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.CardIssuance, error) {
	query := `SELECT ` + cardIssuanceColumns + `
		FROM onboarding_card_issuance WHERE session_id = $1`

	iss, err := scanCardIssuance(r.pool.QueryRow(ctx, query, sessionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find card issuance: %w", err)
	}
	return iss, nil
}

// DueForRetry mengembalikan permintaan yang sudah waktunya diulang, tertua dulu.
func (r *CardIssuanceRepo) DueForRetry(ctx context.Context, now time.Time, limit int) ([]onboarding.CardIssuance, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + cardIssuanceColumns + `
		FROM onboarding_card_issuance
		WHERE next_retry_at IS NOT NULL AND next_retry_at <= $1
		ORDER BY next_retry_at
		LIMIT $2`

	rows, err := r.pool.Query(ctx, query, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list card issuance retries: %w", err)
	}
	defer rows.Close()

	var out []onboarding.CardIssuance
	for rows.Next() {
		iss, err := scanCardIssuance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card issuance: %w", err)
		}
		out = append(out, *iss)
	}
	return out, rows.Err()
}

// --- Admin katalog kartu (§3, §13) ---

// ListAllCards mengembalikan seluruh kartu beserta penempatannya per produk.
//
// Berbeda dari ListCards: admin melihat kartu yang TIDAK aktif juga, dan melihat
// setiap baris wilayah apa adanya tanpa aturan "override wilayah menang" —
// aturan itu untuk menyajikan satu katalog ke nasabah, sementara admin justru
// perlu melihat baris-baris yang menjadi bahan aturan tersebut.
func (r *CardRepo) ListAllCards(ctx context.Context) ([]onboarding.AdminCardRow, error) {
	cardQuery := `
		SELECT card_type, name, network, tier_key, style, currency,
		       fee_monthly_admin, fee_card_issuance, fee_card_replacement,
		       limit_cash_withdrawal, limit_transfer_bca,
		       limit_transfer_interbank, limit_debit_purchase,
		       physical_card_available, delivery_days_min, delivery_days_max,
		       branch_pickup_available, min_age, min_initial_deposit, is_active
		FROM card_products
		ORDER BY card_type`

	rows, err := r.pool.Query(ctx, cardQuery)
	if err != nil {
		return nil, fmt.Errorf("list all cards: %w", err)
	}
	defer rows.Close()

	byType := map[string]*onboarding.AdminCardRow{}
	var out []onboarding.AdminCardRow
	for rows.Next() {
		var c onboarding.AdminCardRow
		if err := rows.Scan(
			&c.CardType, &c.Name, &c.Network, &c.TierKey, &c.Style, &c.Currency,
			&c.Fees.MonthlyAdmin, &c.Fees.CardIssuance, &c.Fees.CardReplacement,
			&c.Limits.CashWithdrawal, &c.Limits.TransferBCA,
			&c.Limits.TransferInterbank, &c.Limits.DebitPurchase,
			&c.Delivery.PhysicalCardAvailable, &c.Delivery.EstimatedDaysMin,
			&c.Delivery.EstimatedDaysMax, &c.Delivery.BranchPickupAvailable,
			&c.Eligibility.MinAge, &c.Eligibility.MinInitialDeposit, &c.IsActive,
		); err != nil {
			return nil, fmt.Errorf("scan admin card: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin cards: %w", err)
	}
	for i := range out {
		byType[out[i].CardType] = &out[i]
	}

	placementQuery := `
		SELECT card_type, product_type, region_code, display_order,
		       is_default, is_popular, badge_key,
		       availability_status, availability_reason_key
		FROM product_card_options
		ORDER BY product_type, display_order, card_type`

	pRows, err := r.pool.Query(ctx, placementQuery)
	if err != nil {
		return nil, fmt.Errorf("list card placements: %w", err)
	}
	defer pRows.Close()

	for pRows.Next() {
		var cardType string
		var p onboarding.AdminCardPlacement
		if err := pRows.Scan(
			&cardType, &p.ProductType, &p.RegionCode, &p.DisplayOrder,
			&p.IsDefault, &p.IsPopular, &p.BadgeKey,
			&p.AvailabilityStatus, &p.AvailabilityReasonKey,
		); err != nil {
			return nil, fmt.Errorf("scan card placement: %w", err)
		}
		// Penempatan yang menunjuk kartu di luar hasil di atas tidak mungkin:
		// foreign key menjaminnya. Pemeriksaan nil tetap ada supaya perubahan
		// skema di masa depan tidak berujung panic.
		if card := byType[cardType]; card != nil {
			card.Placements = append(card.Placements, p)
		}
	}
	return out, pRows.Err()
}

// UpdateCardProduct menulis satu baris card_products.
//
// Seluruhnya dalam SATU transaksi: baca nilai lama, tulis nilai baru, naikkan
// catalog_version sekali, tulis audit. Memecahnya menjadi beberapa transaksi
// membuka dua keadaan yang tidak boleh ada — katalog sudah berubah tapi versinya
// belum naik (nasabah terus dilayani cache lama), dan perubahan tanpa jejak
// audit, yang §13 sebut sebagai syarat.
func (r *CardRepo) UpdateCardProduct(
	ctx context.Context,
	cardType string,
	w onboarding.CardProductWrite,
	actor, ipAddress string,
) (*onboarding.CardCatalogWriteResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin card update: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Error("rollback card update gagal", "card_type", cardType, "error", rbErr)
		}
	}()

	// FOR UPDATE menahan dua admin yang menulis kartu yang sama: tanpa itu,
	// nilai "lama" di audit bisa berasal dari keadaan yang sudah ditimpa.
	oldValue, err := scanCardProductRow(tx.QueryRow(ctx, `
		SELECT name, tier_key, style, is_active,
		       fee_monthly_admin, fee_card_issuance, fee_card_replacement,
		       limit_cash_withdrawal, limit_transfer_bca,
		       limit_transfer_interbank, limit_debit_purchase,
		       physical_card_available, delivery_days_min, delivery_days_max,
		       branch_pickup_available, min_age, min_initial_deposit
		FROM card_products WHERE card_type = $1 FOR UPDATE`, cardType))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.CardTypeInvalid
		}
		return nil, fmt.Errorf("read card for update: %w", err)
	}

	// Menonaktifkan kartu yang sedang menjadi default harus menyertakan
	// penggantinya dalam permintaan yang sama, kalau tidak produk itu kehilangan
	// pilihan awal dan layar pilih kartu berangkat tanpa kartu tersorot.
	if !w.IsActive {
		if err := reassignDefaultsForCard(ctx, tx, cardType, w.NewDefaultCardType); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE card_products
		SET name = $2, tier_key = $3, style = $4, is_active = $5,
		    fee_monthly_admin = $6, fee_card_issuance = $7, fee_card_replacement = $8,
		    limit_cash_withdrawal = $9, limit_transfer_bca = $10,
		    limit_transfer_interbank = $11, limit_debit_purchase = $12,
		    physical_card_available = $13, delivery_days_min = $14, delivery_days_max = $15,
		    branch_pickup_available = $16, min_age = $17, min_initial_deposit = $18,
		    updated_at = NOW()
		WHERE card_type = $1`,
		cardType, w.Name, w.TierKey, string(w.Style), w.IsActive,
		w.Fees.MonthlyAdmin, w.Fees.CardIssuance, w.Fees.CardReplacement,
		w.Limits.CashWithdrawal, w.Limits.TransferBCA,
		w.Limits.TransferInterbank, w.Limits.DebitPurchase,
		w.Delivery.PhysicalCardAvailable, w.Delivery.EstimatedDaysMin,
		w.Delivery.EstimatedDaysMax, w.Delivery.BranchPickupAvailable,
		w.Eligibility.MinAge, w.Eligibility.MinInitialDeposit,
	); err != nil {
		return nil, fmt.Errorf("update card product: %w", err)
	}

	newValue := cardProductValue(w)
	version, err := bumpVersionTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := insertCatalogAuditTx(ctx, tx, onboarding.CardCatalogAuditEntry{
		Actor:          actor,
		Action:         onboarding.AuditActionCardUpdated,
		CardType:       cardType,
		OldValue:       oldValue,
		NewValue:       newValue,
		CatalogVersion: version,
		IPAddress:      ipAddress,
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card update: %w", err)
	}

	return &onboarding.CardCatalogWriteResult{
		CatalogVersion: version,
		CardType:       cardType,
		OldValue:       oldValue,
		NewValue:       newValue,
	}, nil
}

// UpdateProductCard menulis satu baris product_card_options.
func (r *CardRepo) UpdateProductCard(
	ctx context.Context,
	productType onboarding.ProductType,
	cardType string,
	w onboarding.ProductCardWrite,
	actor, ipAddress string,
) (*onboarding.CardCatalogWriteResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin product card update: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Error("rollback product card update gagal",
				"product_type", productType, "card_type", cardType, "error", rbErr)
		}
	}()

	var oldValue map[string]any
	old, err := scanProductCardRow(tx.QueryRow(ctx, `
		SELECT display_order, is_default, is_popular, badge_key,
		       availability_status, availability_reason_key, region_code
		FROM product_card_options
		WHERE product_type = $1 AND card_type = $2
		  AND COALESCE(region_code, '') = COALESCE($3, '')
		FOR UPDATE`, productType, cardType, w.RegionCode))
	switch {
	case err == nil:
		oldValue = old
	case errors.Is(err, pgx.ErrNoRows):
		// Baris belum ada: penempatan baru. Kartunya sendiri tetap harus ada —
		// foreign key di bawah yang menegakkannya.
		oldValue = nil
	default:
		return nil, fmt.Errorf("read product card for update: %w", err)
	}

	// Mencabut default (langsung, atau dengan membuat kartunya tidak AVAILABLE)
	// hanya boleh bila penggantinya disertakan.
	wasDefault := oldValue != nil && oldValue["is_default"] == true
	if wasDefault && !w.IsDefault {
		if err := promoteDefault(ctx, tx, productType, w.RegionCode, w.NewDefaultCardType, cardType); err != nil {
			return nil, err
		}
	}

	// Satu default per (produk, wilayah) dijaga unique index idx_product_card_default.
	// Default yang lama harus dilepas SEBELUM yang baru dipasang, kalau tidak
	// penulisannya gagal dengan pelanggaran unique.
	if w.IsDefault {
		if _, err := tx.Exec(ctx, `
			UPDATE product_card_options
			SET is_default = FALSE, updated_at = NOW()
			WHERE product_type = $1
			  AND COALESCE(region_code, '') = COALESCE($2, '')
			  AND card_type <> $3
			  AND is_default`, productType, w.RegionCode, cardType); err != nil {
			return nil, fmt.Errorf("clear previous default: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO product_card_options
			(product_type, card_type, display_order, is_default, is_popular,
			 badge_key, availability_status, availability_reason_key, region_code)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (product_type, card_type, COALESCE(region_code, ''))
		DO UPDATE SET
			display_order = EXCLUDED.display_order,
			is_default = EXCLUDED.is_default,
			is_popular = EXCLUDED.is_popular,
			badge_key = EXCLUDED.badge_key,
			availability_status = EXCLUDED.availability_status,
			availability_reason_key = EXCLUDED.availability_reason_key,
			updated_at = NOW()`,
		productType, cardType, w.DisplayOrder, w.IsDefault, w.IsPopular,
		w.BadgeKey, string(w.AvailabilityStatus), w.AvailabilityReasonKey, w.RegionCode,
	); err != nil {
		return nil, fmt.Errorf("upsert product card: %w", err)
	}

	newValue := productCardValue(w)
	version, err := bumpVersionTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	product := string(productType)
	if err := insertCatalogAuditTx(ctx, tx, onboarding.CardCatalogAuditEntry{
		Actor:          actor,
		Action:         onboarding.AuditActionProductCardUpdated,
		CardType:       cardType,
		ProductType:    &product,
		OldValue:       oldValue,
		NewValue:       newValue,
		CatalogVersion: version,
		IPAddress:      ipAddress,
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit product card update: %w", err)
	}

	return &onboarding.CardCatalogWriteResult{
		CatalogVersion: version,
		CardType:       cardType,
		ProductType:    &productType,
		OldValue:       oldValue,
		NewValue:       newValue,
	}, nil
}

// reassignDefaultsForCard memindahkan status default setiap (produk, wilayah)
// di mana cardType sedang menjadi default.
func reassignDefaultsForCard(ctx context.Context, tx pgx.Tx, cardType, replacement string) error {
	rows, err := tx.Query(ctx, `
		SELECT product_type, region_code
		FROM product_card_options
		WHERE card_type = $1 AND is_default`, cardType)
	if err != nil {
		return fmt.Errorf("cari default yang terdampak: %w", err)
	}

	type placement struct {
		product onboarding.ProductType
		region  *string
	}
	var affected []placement
	for rows.Next() {
		var p placement
		if err := rows.Scan(&p.product, &p.region); err != nil {
			rows.Close()
			return fmt.Errorf("scan default terdampak: %w", err)
		}
		affected = append(affected, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate default terdampak: %w", err)
	}

	for _, p := range affected {
		if err := promoteDefault(ctx, tx, p.product, p.region, replacement, cardType); err != nil {
			return err
		}
	}
	return nil
}

// promoteDefault menjadikan replacement sebagai default baru pada satu
// (produk, wilayah), menggantikan outgoing.
//
// Menolak bila replacement tidak disertakan, tidak ada di produk itu, atau tidak
// bisa dipilih: default yang menunjuk kartu mati sama buruknya dengan tidak ada
// default sama sekali.
func promoteDefault(
	ctx context.Context,
	tx pgx.Tx,
	productType onboarding.ProductType,
	region *string,
	replacement, outgoing string,
) error {
	regionLabel := "nasional"
	if region != nil && *region != "" {
		regionLabel = *region
	}

	if strings.TrimSpace(replacement) == "" {
		return apperr.Error{
			Status:  apperr.CardCatalogInvalidValue.Status,
			Code:    apperr.CardCatalogInvalidValue.Code,
			Message: "kartu ini sedang menjadi default; sertakan new_default_card_type pada permintaan yang sama",
			Details: map[string]any{
				"field":           "new_default_card_type",
				"product_type":    string(productType),
				"region":          regionLabel,
				"current_default": outgoing,
			},
		}
	}
	if replacement == outgoing {
		return apperr.Error{
			Status:  apperr.CardCatalogInvalidValue.Status,
			Code:    apperr.CardCatalogInvalidValue.Code,
			Message: "new_default_card_type tidak boleh kartu yang sedang dinonaktifkan",
			Details: map[string]any{"field": "new_default_card_type"},
		}
	}

	var status string
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT o.availability_status, c.is_active
		FROM product_card_options o
		JOIN card_products c ON c.card_type = o.card_type
		WHERE o.product_type = $1 AND o.card_type = $2
		  AND COALESCE(o.region_code, '') = COALESCE($3, '')`,
		productType, replacement, region).Scan(&status, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.Error{
			Status:  apperr.CardCatalogInvalidValue.Status,
			Code:    apperr.CardCatalogInvalidValue.Code,
			Message: "new_default_card_type tidak ditawarkan untuk produk dan wilayah ini",
			Details: map[string]any{
				"field": "new_default_card_type", "product_type": string(productType),
				"region": regionLabel,
			},
		}
	}
	if err != nil {
		return fmt.Errorf("periksa default pengganti: %w", err)
	}
	if !active || status != string(onboarding.CardAvailable) {
		return apperr.Error{
			Status:  apperr.CardCatalogInvalidValue.Status,
			Code:    apperr.CardCatalogInvalidValue.Code,
			Message: "new_default_card_type harus kartu aktif dengan status AVAILABLE",
			Details: map[string]any{"field": "new_default_card_type", "availability_status": status},
		}
	}

	// Lepas dulu, pasang kemudian: unique index idx_product_card_default hanya
	// mengizinkan satu default per (produk, wilayah).
	if _, err := tx.Exec(ctx, `
		UPDATE product_card_options SET is_default = FALSE, updated_at = NOW()
		WHERE product_type = $1 AND card_type = $2
		  AND COALESCE(region_code, '') = COALESCE($3, '')`,
		productType, outgoing, region); err != nil {
		return fmt.Errorf("lepas default lama: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE product_card_options SET is_default = TRUE, updated_at = NOW()
		WHERE product_type = $1 AND card_type = $2
		  AND COALESCE(region_code, '') = COALESCE($3, '')`,
		productType, replacement, region); err != nil {
		return fmt.Errorf("pasang default baru: %w", err)
	}
	return nil
}

// bumpVersionTx menaikkan catalog_version di dalam transaksi pemanggil.
//
// Sekali per transaksi, bukan per baris: §12 menyandarkan invalidasi cache pada
// versi ini, dan menaikkannya dua kali untuk satu perubahan hanya membuang
// cache yang masih sah.
func bumpVersionTx(ctx context.Context, tx pgx.Tx) (string, error) {
	const query = `
		UPDATE card_catalog_version
		SET version_date = CURRENT_DATE,
		    counter = CASE WHEN version_date = CURRENT_DATE THEN counter + 1 ELSE 1 END,
		    updated_at = NOW()
		WHERE id
		RETURNING to_char(version_date, 'YYYY-MM-DD') || '.' || counter`

	var version string
	if err := tx.QueryRow(ctx, query).Scan(&version); err != nil {
		return "", fmt.Errorf("bump catalog version: %w", err)
	}
	return version, nil
}

func insertCatalogAuditTx(ctx context.Context, tx pgx.Tx, e onboarding.CardCatalogAuditEntry) error {
	oldJSON, err := marshalAuditValue(e.OldValue)
	if err != nil {
		return err
	}
	newJSON, err := marshalAuditValue(e.NewValue)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO card_catalog_audit_log
			(actor, action, card_type, product_type, old_value, new_value,
			 catalog_version, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.Actor, e.Action, e.CardType, e.ProductType, oldJSON, newJSON,
		e.CatalogVersion, parseAuditIP(e.IPAddress),
	); err != nil {
		return fmt.Errorf("insert catalog audit: %w", err)
	}
	return nil
}

// marshalAuditValue mengubah nilai audit menjadi JSONB. nil tetap NULL —
// penempatan yang baru dibuat memang tidak punya nilai lama.
func marshalAuditValue(v map[string]any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal audit value: %w", err)
	}
	return data, nil
}

func scanCardProductRow(row pgx.Row) (map[string]any, error) {
	var (
		name, tierKey, style                string
		isActive, physical, branchPickup    bool
		monthly, issuance, replacement      int64
		cash, transferBCA, interbank, debit int64
		daysMin, daysMax                    *int
		minAge                              int
		minDeposit                          int64
	)
	if err := row.Scan(&name, &tierKey, &style, &isActive,
		&monthly, &issuance, &replacement,
		&cash, &transferBCA, &interbank, &debit,
		&physical, &daysMin, &daysMax, &branchPickup,
		&minAge, &minDeposit); err != nil {
		return nil, err
	}

	return map[string]any{
		"name":      name,
		"tier_key":  tierKey,
		"style":     style,
		"is_active": isActive,
		"fees": map[string]any{
			"monthly_admin":    monthly,
			"card_issuance":    issuance,
			"card_replacement": replacement,
		},
		"limits": map[string]any{
			"cash_withdrawal":    cash,
			"transfer_bca":       transferBCA,
			"transfer_interbank": interbank,
			"debit_purchase":     debit,
		},
		"delivery": map[string]any{
			"physical_card_available": physical,
			"estimated_days_min":      daysMin,
			"estimated_days_max":      daysMax,
			"branch_pickup_available": branchPickup,
		},
		"eligibility": map[string]any{
			"min_age":             minAge,
			"min_initial_deposit": minDeposit,
		},
	}, nil
}

func cardProductValue(w onboarding.CardProductWrite) map[string]any {
	return map[string]any{
		"name":      w.Name,
		"tier_key":  w.TierKey,
		"style":     string(w.Style),
		"is_active": w.IsActive,
		"fees": map[string]any{
			"monthly_admin":    w.Fees.MonthlyAdmin,
			"card_issuance":    w.Fees.CardIssuance,
			"card_replacement": w.Fees.CardReplacement,
		},
		"limits": map[string]any{
			"cash_withdrawal":    w.Limits.CashWithdrawal,
			"transfer_bca":       w.Limits.TransferBCA,
			"transfer_interbank": w.Limits.TransferInterbank,
			"debit_purchase":     w.Limits.DebitPurchase,
		},
		"delivery": map[string]any{
			"physical_card_available": w.Delivery.PhysicalCardAvailable,
			"estimated_days_min":      w.Delivery.EstimatedDaysMin,
			"estimated_days_max":      w.Delivery.EstimatedDaysMax,
			"branch_pickup_available": w.Delivery.BranchPickupAvailable,
		},
		"eligibility": map[string]any{
			"min_age":             w.Eligibility.MinAge,
			"min_initial_deposit": w.Eligibility.MinInitialDeposit,
		},
	}
}

func scanProductCardRow(row pgx.Row) (map[string]any, error) {
	var (
		displayOrder         int
		isDefault, isPopular bool
		badgeKey, reasonKey  *string
		status               string
		regionCode           *string
	)
	if err := row.Scan(&displayOrder, &isDefault, &isPopular, &badgeKey,
		&status, &reasonKey, &regionCode); err != nil {
		return nil, err
	}
	return map[string]any{
		"display_order":           displayOrder,
		"is_default":              isDefault,
		"is_popular":              isPopular,
		"badge_key":               badgeKey,
		"availability_status":     status,
		"availability_reason_key": reasonKey,
		"region_code":             regionCode,
	}, nil
}

func productCardValue(w onboarding.ProductCardWrite) map[string]any {
	return map[string]any{
		"display_order":           w.DisplayOrder,
		"is_default":              w.IsDefault,
		"is_popular":              w.IsPopular,
		"badge_key":               w.BadgeKey,
		"availability_status":     string(w.AvailabilityStatus),
		"availability_reason_key": w.AvailabilityReasonKey,
		"region_code":             w.RegionCode,
	}
}
