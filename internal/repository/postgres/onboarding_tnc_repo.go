package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// OnboardingTNCRepo membaca dua tabel S&K dari migrasi 000025.
type OnboardingTNCRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingTNCRepo(pool *pgxpool.Pool) *OnboardingTNCRepo {
	return &OnboardingTNCRepo{pool: pool}
}

// tncDocumentColumns dibagi dua query supaya urutan kolomnya tidak bisa
// melenceng antara pembacaan versi aktif dan pembacaan versi tertentu.
const tncDocumentColumns = `
	id, version, heading, subtitle,
	trust_title, trust_subtitle,
	notice_label, notice_body,
	consent_prefix, consent_link, consent_suffix,
	agree_cta, is_active, effective_from`

// ActiveTNC mengembalikan versi S&K yang sedang berlaku.
//
// Tidak ada baris aktif berarti migrasi 000025 belum jalan atau seseorang
// mematikan satu-satunya versi aktif lewat SQL. Keduanya salah konfigurasi
// server, jadi TNCUnavailable (503) — bukan 404 yang akan terbaca di aplikasi
// sebagai "nasabah ini tidak punya S&K".
func (r *OnboardingTNCRepo) ActiveTNC(ctx context.Context) (*onboarding.TNCDocument, error) {
	doc, id, err := r.scanDocument(ctx, `
		SELECT`+tncDocumentColumns+`
		FROM onboarding_tnc_documents
		WHERE is_active
		LIMIT 1`)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.TNCUnavailable
		}
		return nil, fmt.Errorf("query active tnc: %w", err)
	}

	if err := r.attachSections(ctx, doc, id); err != nil {
		return nil, err
	}
	return doc, nil
}

// TNCByVersion mengembalikan satu versi, aktif maupun sudah dicabut.
func (r *OnboardingTNCRepo) TNCByVersion(ctx context.Context, version string) (*onboarding.TNCDocument, error) {
	doc, id, err := r.scanDocument(ctx, `
		SELECT`+tncDocumentColumns+`
		FROM onboarding_tnc_documents
		WHERE version = $1`, version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.TNCVersionUnknown
		}
		return nil, fmt.Errorf("query tnc by version: %w", err)
	}

	if err := r.attachSections(ctx, doc, id); err != nil {
		return nil, err
	}
	return doc, nil
}

// scanDocument membaca baris induk dan mengembalikan id-nya terpisah: id itu
// dipakai untuk mengambil pasal-pasalnya, tapi tidak ikut ke response — nilai
// BIGSERIAL berbeda di tiap environment, jadi tidak ada gunanya bagi client.
func (r *OnboardingTNCRepo) scanDocument(ctx context.Context, query string, args ...any) (*onboarding.TNCDocument, int64, error) {
	var (
		doc onboarding.TNCDocument
		id  int64
	)
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&id,
		&doc.Version,
		&doc.Heading,
		&doc.Subtitle,
		&doc.TrustBanner.Title,
		&doc.TrustBanner.Subtitle,
		&doc.Notice.Label,
		&doc.Notice.Body,
		&doc.Consent.Prefix,
		&doc.Consent.Link,
		&doc.Consent.Suffix,
		&doc.AgreeCTA,
		&doc.IsActive,
		&doc.EffectiveFrom,
	)
	if err != nil {
		return nil, 0, err
	}
	return &doc, id, nil
}

// attachSections mengisi pasal-pasal dokumen.
//
// Query kedua, bukan JOIN. Isinya dibaca sekali lalu di-cache 24 jam, dan
// JOIN akan menggandakan kesebelas kolom induk di setiap baris pasal hanya
// untuk dibuang lagi di Go.
//
// Dokumen tanpa pasal sama sekali dianggap tidak sah: layar S&K yang hanya
// berisi judul dan tombol setuju adalah persetujuan atas ketiadaan. Itu hanya
// bisa terjadi bila seseorang menghapus barisnya lewat SQL.
func (r *OnboardingTNCRepo) attachSections(ctx context.Context, doc *onboarding.TNCDocument, documentID int64) error {
	rows, err := r.pool.Query(ctx, `
		SELECT icon_key, title, body
		FROM onboarding_tnc_sections
		WHERE document_id = $1
		ORDER BY section_order, id`, documentID)
	if err != nil {
		return fmt.Errorf("query tnc sections: %w", err)
	}
	defer rows.Close()

	sections := make([]onboarding.TNCSection, 0, 5)
	for rows.Next() {
		var s onboarding.TNCSection
		if err := rows.Scan(&s.IconKey, &s.Title, &s.Body); err != nil {
			return fmt.Errorf("scan tnc section: %w", err)
		}
		sections = append(sections, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tnc sections: %w", err)
	}
	if len(sections) == 0 {
		return apperr.TNCUnavailable
	}

	doc.Sections = sections
	return nil
}
