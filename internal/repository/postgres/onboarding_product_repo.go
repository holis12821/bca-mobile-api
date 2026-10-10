package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// OnboardingProductRepo membaca DAN menulis katalog jenis rekening tabungan
// (migrasi 000039).
//
// Jalur bacanya (ActiveCatalog) melayani nasabah; jalur tulisnya (ListAllProducts,
// WriteProducts) hanya dipanggil admin ber-cakupan CARD_ADMIN lewat /internal/v1.
// Keduanya dipisah di tingkat ANTARMUKA, bukan di sini: domain mendefinisikan
// ProductCatalogRepository (baca) dan ProductCatalogAdminRepository (tulis) secara
// terpisah, jadi service nasabah tidak pernah memegang kemampuan menulis meski satu
// struct ini mengimplementasikan keduanya.
//
// Nilai awal katalog tetap datang dari migrasi, dan seeder tetap menolak jalan di luar
// APP_ENV=development — pelajaran dari 000025.
type OnboardingProductRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingProductRepo(pool *pgxpool.Pool) *OnboardingProductRepo {
	return &OnboardingProductRepo{pool: pool}
}

// ActiveCatalog merakit katalog aktif: versi, copy halaman, produk, dan fiturnya.
//
// Tiga query, bukan satu JOIN besar. Alasannya bentuk datanya: produk dan fitur adalah
// satu-ke-banyak, jadi JOIN akan mengembalikan baris produk sebanyak jumlah fiturnya dan
// perakitannya harus membuang duplikat sendiri. Tiga query yang masing-masing jelas lebih
// murah dibaca daripada satu query yang perlu dijelaskan — dan seluruh hasilnya di-cache
// 24 jam, jadi jalur ini hanya dilalui saat cache miss.
func (r *OnboardingProductRepo) ActiveCatalog(ctx context.Context) (*onboarding.SavingsProductCatalog, error) {
	version, err := r.catalogVersion(ctx)
	if err != nil {
		return nil, err
	}

	page, err := r.page(ctx)
	if err != nil {
		return nil, err
	}

	products, err := r.activeProducts(ctx)
	if err != nil {
		return nil, err
	}

	// Katalog tanpa produk dikembalikan apa adanya, bukan sebagai error: yang memutuskan
	// bahwa itu berarti 503 adalah service, dan repository yang ikut memutuskannya akan
	// membuat dua tempat memegang kebijakan yang sama.
	if len(products) == 0 {
		return &onboarding.SavingsProductCatalog{
			CatalogVersion: version,
			Page:           page,
		}, nil
	}

	if err := r.attachFeatures(ctx, products); err != nil {
		return nil, err
	}

	out := make([]onboarding.ProductOption, 0, len(products))
	for _, p := range products {
		out = append(out, *p)
	}

	return &onboarding.SavingsProductCatalog{
		CatalogVersion: version,
		Page:           page,
		Products:       out,
	}, nil
}

// catalogVersion merakit "YYYY-MM-DD.counter" — bentuk yang sama dengan
// card_catalog_version, supaya dua ETag di flow yang sama tidak punya dua bentuk.
func (r *OnboardingProductRepo) catalogVersion(ctx context.Context) (string, error) {
	var version string
	err := r.pool.QueryRow(ctx, `
		SELECT to_char(version_date, 'YYYY-MM-DD') || '.' || counter
		FROM onboarding_product_catalog_version WHERE id`,
	).Scan(&version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Barisnya hilang berarti migrasi 000039 belum jalan. Versi kosong, bukan
			// error: service yang memutuskan itu 503, dan pesannya akan sama dengan
			// katalog kosong — keduanya memang salah konfigurasi server yang sama.
			return "", nil
		}
		return "", fmt.Errorf("read product catalog version: %w", err)
	}
	return version, nil
}

func (r *OnboardingProductRepo) page(ctx context.Context) (onboarding.ProductPage, error) {
	var p onboarding.ProductPage
	err := r.pool.QueryRow(ctx, `
		SELECT heading, subtitle, deposit_label, cta_label,
		       notice_icon_key, notice_title, notice_body,
		       consent_prefix, consent_link, consent_suffix
		FROM onboarding_product_page WHERE id`,
	).Scan(&p.Heading, &p.Subtitle, &p.DepositLabel, &p.CTALabel,
		&p.Notice.IconKey, &p.Notice.Title, &p.Notice.Body,
		&p.Consent.Prefix, &p.Consent.Link, &p.Consent.Suffix,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Page kosong tidak menggagalkan katalog: client punya teks bawaannya di
			// strings.xml dan akan memakai itu untuk field yang kosong. Yang benar-benar
			// tidak bisa ditebak client adalah angka setoran awal dan daftar produknya.
			return onboarding.ProductPage{}, nil
		}
		return onboarding.ProductPage{}, fmt.Errorf("read product page: %w", err)
	}
	return p, nil
}

// activeProducts mengembalikan produk is_active, terurut display_order.
//
// Produk yang tutup (availability_status bukan AVAILABLE) TETAP ikut — yang disaring di
// sini hanya is_active = FALSE, yaitu produk yang sudah dihentikan dan tidak lagi
// ditawarkan sama sekali.
func (r *OnboardingProductRepo) activeProducts(ctx context.Context) ([]*onboarding.ProductOption, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT product_type, name, description, min_initial_deposit, currency,
		       icon_key, style, badge_key, is_popular, is_default,
		       display_order, availability_status, availability_reason_key
		FROM onboarding_products
		WHERE is_active
		ORDER BY display_order, product_type`)
	if err != nil {
		return nil, fmt.Errorf("list onboarding products: %w", err)
	}
	defer rows.Close()

	var out []*onboarding.ProductOption
	for rows.Next() {
		var p onboarding.ProductOption
		var productType string

		if err := rows.Scan(
			&productType, &p.Name, &p.Description, &p.MinInitialDeposit, &p.Currency,
			&p.IconKey, &p.Style, &p.BadgeKey, &p.IsPopular, &p.IsDefault,
			&p.DisplayOrder, &p.AvailabilityStatus, &p.AvailabilityReasonKey,
		); err != nil {
			return nil, fmt.Errorf("scan onboarding product: %w", err)
		}

		p.ProductType = onboarding.ProductType(productType)

		// Fitur selalu array, tidak pernah null: client yang menerima null akan
		// mengiterasinya sebagai daftar.
		p.Features = []string{}

		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate onboarding products: %w", err)
	}
	return out, nil
}

// attachFeatures mengisi fitur untuk produk yang sudah terbaca.
//
// Satu query untuk seluruh produk, bukan satu per produk: jumlah produknya kecil, tapi
// pola N+1 di jalur yang dibaca setiap cache miss adalah pola yang akan dicontoh
// endpoint berikutnya.
func (r *OnboardingProductRepo) attachFeatures(ctx context.Context, products []*onboarding.ProductOption) error {
	byType := make(map[onboarding.ProductType]*onboarding.ProductOption, len(products))
	for _, p := range products {
		byType[p.ProductType] = p
	}

	rows, err := r.pool.Query(ctx, `
		SELECT product_type, label
		FROM onboarding_product_features
		ORDER BY product_type, feature_order`)
	if err != nil {
		return fmt.Errorf("list onboarding product features: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var productType, label string
		if err := rows.Scan(&productType, &label); err != nil {
			return fmt.Errorf("scan onboarding product feature: %w", err)
		}

		// Fitur milik produk yang tidak aktif dilewati tanpa suara: barisnya memang
		// masih ada (produk tidak pernah dihapus), tapi produknya tidak ikut dilayani.
		if p, ok := byType[onboarding.ProductType(productType)]; ok {
			p.Features = append(p.Features, label)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate onboarding product features: %w", err)
	}
	return nil
}

// --- Admin katalog produk (Fase 6) ---

// ListAllProducts mengembalikan SELURUH baris katalog, termasuk is_active = FALSE.
//
// Dua query, bukan JOIN: alasannya sama dengan ActiveCatalog — produk dan fitur adalah
// satu-ke-banyak. Jalur ini tidak di-cache: layar admin yang menyunting katalog harus
// melihat keadaan database, bukan salinan yang mungkin dibuat sebelum penulisan
// terakhir.
func (r *OnboardingProductRepo) ListAllProducts(ctx context.Context) (*onboarding.AdminProductCatalog, error) {
	version, err := r.catalogVersion(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT product_type, name, description, min_initial_deposit, currency,
		       icon_key, style, badge_key, is_popular, is_default, is_active,
		       display_order, availability_status, availability_reason_key, updated_at
		FROM onboarding_products
		ORDER BY display_order, product_type`)
	if err != nil {
		return nil, fmt.Errorf("list all onboarding products: %w", err)
	}
	defer rows.Close()

	out := make([]onboarding.AdminProductRow, 0, 8)
	byType := make(map[onboarding.ProductType]int, 8)

	for rows.Next() {
		var p onboarding.AdminProductRow
		var productType string

		if err := rows.Scan(
			&productType, &p.Name, &p.Description, &p.MinInitialDeposit, &p.Currency,
			&p.IconKey, &p.Style, &p.BadgeKey, &p.IsPopular, &p.IsDefault, &p.IsActive,
			&p.DisplayOrder, &p.AvailabilityStatus, &p.AvailabilityReasonKey, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan admin onboarding product: %w", err)
		}

		p.ProductType = onboarding.ProductType(productType)

		// Array kosong, bukan nil: klien admin yang melakukan .length pada null akan
		// meledak tanpa sebab yang jelas. Alasan yang sama dengan ListCards.
		p.Features = []string{}

		byType[p.ProductType] = len(out)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin onboarding products: %w", err)
	}

	// Fitur SELURUH produk, termasuk yang tidak aktif — berbeda dari attachFeatures yang
	// melewatinya. Admin yang menyalakan kembali produk perlu melihat teks fitur yang
	// akan ikut tayang, bukan daftar kosong yang mengundangnya menulis ulang.
	featRows, err := r.pool.Query(ctx, `
		SELECT product_type, label
		FROM onboarding_product_features
		ORDER BY product_type, feature_order`)
	if err != nil {
		return nil, fmt.Errorf("list all onboarding product features: %w", err)
	}
	defer featRows.Close()

	for featRows.Next() {
		var productType, label string
		if err := featRows.Scan(&productType, &label); err != nil {
			return nil, fmt.Errorf("scan admin onboarding product feature: %w", err)
		}
		if idx, ok := byType[onboarding.ProductType(productType)]; ok {
			out[idx].Features = append(out[idx].Features, label)
		}
	}
	if err := featRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin onboarding product features: %w", err)
	}

	return &onboarding.AdminProductCatalog{CatalogVersion: version, Products: out}, nil
}

// WriteProducts menulis beberapa produk dan menaikkan catalog_version SEKALI.
//
// Satu transaksi untuk seluruh penulisan, dengan tiga akibat yang semuanya disengaja:
//
//  1. Penulisan yang gagal di produk kedua tidak meninggalkan produk pertama tertulis.
//     Katalog setengah tertulis adalah keadaan yang tidak diputuskan siapa pun, dan
//     nasabah yang membukanya akan melihat harga baru pada satu produk dan harga lama
//     pada produk yang seharusnya berubah bersamaan.
//  2. catalog_version naik SEKALI, di akhir, bukan per baris. Dua versi untuk satu
//     keputusan membuat ETag berganti dua kali padahal tidak ada katalog perantara
//     yang pernah tayang — dan client yang menyimpan versi di antaranya menyimpan
//     versi yang tidak pernah dilayani.
//  3. Produk dikunci dalam urutan product_type yang tetap. Dua penulisan bersamaan
//     yang menyentuh dua produk yang sama dalam urutan berbeda akan saling menunggu;
//     mengurutkannya lebih dulu membuat yang kedua menunggu di baris pertama dan
//     selesai, bukan gagal dengan deadlock. Pola yang sama dengan kunci rekening urut
//     UUID di jalur uang.
//
// Penulisannya DUA LANGKAH, dan itu bukan pilihan gaya — tanpa itu memindahkan badge
// "Paling Populer" dari satu produk ke produk lain MUSTAHIL dalam satu permintaan.
//
// Sebabnya: idx_onboarding_products_one_popular dan _one_default adalah unique index
// berekspresi, dan unique index tidak bisa DEFERRABLE (hanya unique CONSTRAINT bisa,
// dan index parsial berekspresi tidak bisa menjadi constraint). Jadi pelanggarannya
// terdeteksi pada statement itu juga, bukan saat commit. Karena produk ditulis urut
// product_type, TABUNGANKU mendapat badge sebelum TAHAPAN_BCA melepasnya — dan
// penulisan yang seharusnya sah ditolak 409.
//
// Maka: langkah pertama menulis SEMUANYA dengan is_popular dan is_default dipaksa
// FALSE, langkah kedua menyalakan yang memang diminta. Di antara keduanya tidak ada
// baris yang memegang flag itu, jadi tidak ada yang bisa bertabrakan.
//
// Tabrakan dengan produk yang TIDAK ikut dikirim tetap ditolak 409, dan itu memang
// benar: pemegang lama tidak disentuh permintaan ini, jadi menurunkan flag-nya berarti
// mengubah baris yang tidak diminta siapa pun.
func (r *OnboardingProductRepo) WriteProducts(ctx context.Context, writes []onboarding.ProductWrite, actor, ipAddress string) (*onboarding.ProductCatalogWriteResult, error) {
	if len(writes) == 0 {
		return nil, apperr.ValidationError
	}

	ordered := make([]onboarding.ProductWrite, len(writes))
	copy(ordered, writes)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ProductType < ordered[j].ProductType
	})

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin product catalog write: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result := &onboarding.ProductCatalogWriteResult{
		Updated:          make([]onboarding.ProductType, 0, len(ordered)),
		FeaturesReplaced: make([]onboarding.ProductType, 0, len(ordered)),
	}

	// Langkah 1: semua kolom, dengan is_popular dan is_default dipaksa FALSE.
	for _, w := range ordered {
		tag, err := tx.Exec(ctx, `
			UPDATE onboarding_products
			SET name                    = $2,
			    description             = $3,
			    min_initial_deposit     = $4,
			    currency                = $5,
			    icon_key                = $6,
			    style                   = $7,
			    badge_key               = $8,
			    is_popular              = FALSE,
			    is_default              = FALSE,
			    is_active               = $9,
			    display_order           = $10,
			    availability_status     = $11,
			    availability_reason_key = $12,
			    updated_at              = now()
			WHERE product_type = $1`,
			string(w.ProductType), w.Name, w.Description, w.MinInitialDeposit,
			strings.ToUpper(strings.TrimSpace(w.Currency)), w.IconKey, w.Style, w.BadgeKey,
			w.IsActive, w.DisplayOrder,
			w.AvailabilityStatus, w.AvailabilityReasonKey,
		)
		if err != nil {
			return nil, mapProductWriteError(err)
		}

		// Nol baris berarti enum onboarding_product_type mengenal nilainya tapi
		// barisnya belum ada. UPDATE, bukan UPSERT: produk baru menuntut nilai enum
		// baru, dan nilai enum baru menuntut migrasi — jadi INSERT di sini akan
		// berhasil hanya untuk produk yang enum-nya sudah ada, yaitu keadaan yang
		// hanya mungkin terjadi kalau seseorang menghapus barisnya secara manual.
		// Tabel ini melarang penghapusan; melaporkannya lebih baik daripada
		// menambalnya diam-diam.
		if tag.RowsAffected() == 0 {
			return nil, apperr.OnboardingProductUnknown
		}

		result.Updated = append(result.Updated, w.ProductType)

		if w.Features == nil {
			continue
		}

		// Hapus-lalu-sisipkan, bukan merge: daftar fitur adalah daftar berurut, dan
		// merge atas daftar berurut menuntut pemanggil mengirim feature_order yang
		// benar untuk setiap baris. Mengganti seluruhnya membuat urutan di badan
		// permintaan menjadi urutan yang tayang, tanpa field tambahan.
		if _, err := tx.Exec(ctx,
			`DELETE FROM onboarding_product_features WHERE product_type = $1`,
			string(w.ProductType),
		); err != nil {
			return nil, fmt.Errorf("delete product features: %w", err)
		}

		for i, label := range *w.Features {
			if _, err := tx.Exec(ctx, `
				INSERT INTO onboarding_product_features (product_type, feature_order, label)
				VALUES ($1, $2, $3)`,
				string(w.ProductType), i+1, label,
			); err != nil {
				return nil, mapProductWriteError(err)
			}
		}

		result.FeaturesReplaced = append(result.FeaturesReplaced, w.ProductType)
	}

	// Langkah 2: nyalakan flag tunggal yang diminta.
	//
	// Validasi domain sudah memastikan maksimum satu is_popular dan satu is_default per
	// permintaan, jadi loop ini menyentuh paling banyak satu baris per flag — dan
	// barisnya sudah terkunci langkah pertama, jadi tidak ada urutan kunci baru yang
	// bisa menimbulkan deadlock.
	for _, w := range ordered {
		if !w.IsPopular && !w.IsDefault {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE onboarding_products
			SET is_popular = $2, is_default = $3, updated_at = now()
			WHERE product_type = $1`,
			string(w.ProductType), w.IsPopular, w.IsDefault,
		); err != nil {
			return nil, mapProductWriteError(err)
		}
	}

	// Versi dinaikkan SEKALI, di akhir, di dalam transaksi yang sama. Di dalam, bukan
	// sesudah commit: versi yang naik tanpa perubahan yang ikut tersimpan membuat client
	// membuang cache-nya lalu menerima katalog yang identik, dan perubahan yang
	// tersimpan tanpa versi yang naik membuat client menyajikan katalog lama sampai TTL
	// habis. Yang kedua jauh lebih buruk, dan keduanya hilang kalau keduanya satu
	// transaksi.
	var version string
	if err := tx.QueryRow(ctx, `
		UPDATE onboarding_product_catalog_version
		SET version_date = CURRENT_DATE,
		    counter = CASE WHEN version_date = CURRENT_DATE THEN counter + 1 ELSE 1 END,
		    updated_at = now()
		WHERE id
		RETURNING to_char(version_date, 'YYYY-MM-DD') || '.' || counter`,
	).Scan(&version); err != nil {
		return nil, fmt.Errorf("bump product catalog version: %w", err)
	}
	result.CatalogVersion = version

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit product catalog write: %w", err)
	}

	// Aktor dan IP dicatat di log, bukan di tabel audit: katalog produk belum punya
	// tabel jejak seperti card_catalog_audit_log, dan membuatnya menuntut migrasi
	// tersendiri. Dicatat di sini supaya perubahan setoran awal masih bisa ditanyakan
	// ke orangnya lewat log terstruktur.
	slog.Info("katalog produk ditulis admin",
		"actor", actor, "ip", ipAddress,
		"catalog_version", version,
		"updated", result.Updated,
		"features_replaced", result.FeaturesReplaced)

	return result, nil
}

// mapProductWriteError menerjemahkan pelanggaran constraint menjadi error yang menyebut
// sebabnya.
//
// Tanpa ini, menandai produk kedua sebagai is_popular sementara produk pertama masih
// memegangnya dan tidak ikut dikirim akan menjadi 500 dengan pesan "duplicate key value
// violates unique constraint idx_onboarding_products_one_popular" — benar, tapi tidak
// bisa ditindaklanjuti siapa pun yang tidak membaca migrasinya.
func mapProductWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("write product catalog: %w", err)
	}

	switch pgErr.Code {
	case "23505": // unique_violation — dua is_popular / dua is_default / feature_order ganda
		return apperr.OnboardingProductCatalogConflict
	case "23514": // check_violation — cermin validasi domain yang terlewat
		return apperr.Error{
			Status:  apperr.OnboardingProductInvalidValue.Status,
			Code:    apperr.OnboardingProductInvalidValue.Code,
			Message: "Nilai katalog produk ditolak database.",
			Details: map[string]any{"constraint": pgErr.ConstraintName},
		}
	}
	return fmt.Errorf("write product catalog: %w", err)
}
