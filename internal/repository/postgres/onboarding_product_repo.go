package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// OnboardingProductRepo membaca katalog jenis rekening tabungan (migrasi 000039).
//
// Hanya baca. Katalog diubah lewat migrasi, bukan lewat endpoint: seeder menolak jalan
// di luar APP_ENV=development, jadi perubahan yang lewat seeder akan membuat staging
// menjawab layar kosong — pelajaran dari 000025.
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
