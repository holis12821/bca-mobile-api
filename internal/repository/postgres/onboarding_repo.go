package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

type OnboardingSessionRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingSessionRepo(pool *pgxpool.Pool) *OnboardingSessionRepo {
	return &OnboardingSessionRepo{pool: pool}
}

func (r *OnboardingSessionRepo) Create(ctx context.Context, session *onboarding.Session) error {
	stepsJSON, err := json.Marshal(session.StepsCompleted)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}

	// Kolom kartu ikut ditulis di sini. Migrasi 000019 membuat ketiganya, tapi
	// INSERT ini sempat tidak menyebutnya sama sekali: card_type yang dikirim
	// nasabah tersimpan di memori, hilang begitu baris ditulis, dan submit
	// menemukan sesi tanpa kartu. Kolom yang ada di migrasi harus ada di sini.
	query := `
		INSERT INTO onboarding_sessions
			(id, session_id, device_id, product_type, current_step, tnc_version,
			 steps_completed, created_at, updated_at, expires_at,
			 card_type, card_selected_at, card_catalog_version,
			 product_catalog_version, min_initial_deposit_shown)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

	// product_catalog_version dan min_initial_deposit_shown ditulis NULL saat kosong,
	// bukan "" dan 0: kolomnya nullable dengan sengaja, dan 0 di kolom nominal terbaca
	// sebagai "setoran awalnya Rp 0" — keadaan yang berbeda dari "tidak tercatat".
	var depositShown *int64
	if session.MinInitialDepositShown > 0 {
		d := session.MinInitialDepositShown
		depositShown = &d
	}

	_, err = r.pool.Exec(ctx, query,
		session.ID, session.SessionID, session.DeviceID,
		string(session.ProductType), string(session.CurrentStep), session.TNCVersion,
		stepsJSON,
		session.CreatedAt, session.UpdatedAt, session.ExpiresAt,
		nullableText(session.CardType), session.CardSelectedAt,
		nullableText(session.CardCatalogVersion),
		nullableText(session.ProductCatalogVersion), depositShown,
	)
	if err != nil {
		return fmt.Errorf("insert onboarding session: %w", err)
	}
	return nil
}

func (r *OnboardingSessionRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.Session, error) {
	query := `
		SELECT id, session_id, device_id, product_type, current_step, tnc_version,
		       steps_completed, created_at, updated_at, expires_at, deleted_at,
		       card_type, card_selected_at, card_catalog_version
		FROM onboarding_sessions
		WHERE session_id = $1 AND deleted_at IS NULL`

	var s onboarding.Session
	var pt, step string
	var stepsJSON []byte
	var cardType, cardCatalogVersion, tncVersion *string

	// tnc_version lewat *string, bukan langsung ke field string-nya — alasan yang SAMA
	// dengan ListForCS di bawah, dan di sini akibatnya lebih luas: jalur ini dipanggil
	// oleh deviceOwnsSession, jadi satu baris dengan tnc_version NULL membuat SETIAP
	// endpoint onboarding untuk sesi itu menjawab 500 permanen — bukan hanya satu daftar.
	//
	// Kolomnya nullable sejak migrasi 000010 dan sesi yang dibuat lewat API selalu
	// mengisinya, jadi kegagalannya hanya muncul pada baris lama atau baris yang ditulis
	// di luar jalur biasa. Itu tepat baris yang sedang ditelusuri orang ketika ia butuh
	// endpointnya bekerja.
	err := r.pool.QueryRow(ctx, query, sessionID).Scan(
		&s.ID, &s.SessionID, &s.DeviceID,
		&pt, &step, &tncVersion,
		&stepsJSON,
		&s.CreatedAt, &s.UpdatedAt, &s.ExpiresAt, &s.DeletedAt,
		&cardType, &s.CardSelectedAt, &cardCatalogVersion,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find onboarding session: %w", err)
	}

	s.ProductType = onboarding.ProductType(pt)
	s.CurrentStep = onboarding.Step(step)
	if tncVersion != nil {
		s.TNCVersion = *tncVersion
	}
	if cardType != nil {
		s.CardType = *cardType
	}
	if cardCatalogVersion != nil {
		s.CardCatalogVersion = *cardCatalogVersion
	}

	if err := json.Unmarshal(stepsJSON, &s.StepsCompleted); err != nil {
		return nil, fmt.Errorf("unmarshal steps: %w", err)
	}

	return &s, nil
}

func (r *OnboardingSessionRepo) SoftDelete(ctx context.Context, sessionID string) error {
	query := `UPDATE onboarding_sessions SET deleted_at = $2, updated_at = $2 WHERE session_id = $1 AND deleted_at IS NULL`
	tag, err := r.pool.Exec(ctx, query, sessionID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("soft delete session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found or already deleted")
	}
	return nil
}

func (r *OnboardingSessionRepo) UpdateStep(ctx context.Context, sessionID string, step onboarding.Step, completed onboarding.StepsCompleted) error {
	stepsJSON, err := json.Marshal(completed)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}

	now := time.Now().UTC()

	// expires_at is deliberately untouched: a session lives 24h from creation.
	// Refreshing it on every step produced a session that never expired as
	// long as the client kept moving, contradicting ONBOARDING_SESSION_EXPIRED.
	query := `
		UPDATE onboarding_sessions
		SET current_step = $2, steps_completed = $3, updated_at = $4
		WHERE session_id = $1 AND deleted_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, sessionID, string(step), stepsJSON, now)
	if err != nil {
		return fmt.Errorf("update step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

// UpdateCard menyimpan pilihan kartu bersama langkah sesi dalam satu UPDATE.
//
// Kolom langkah lain tidak disebut sama sekali: mengganti kartu tidak boleh
// menyentuh hasil OCR, biometrik, maupun kredensial yang sudah tersimpan (§8).
func (r *OnboardingSessionRepo) UpdateCard(ctx context.Context, sessionID string, upd onboarding.SessionCardUpdate) error {
	stepsJSON, err := json.Marshal(upd.StepsCompleted)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}

	query := `
		UPDATE onboarding_sessions
		SET card_type = $2,
		    card_selected_at = $3,
		    card_catalog_version = $4,
		    current_step = $5,
		    steps_completed = $6,
		    updated_at = $7
		WHERE session_id = $1 AND deleted_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, sessionID,
		upd.CardType, upd.SelectedAt, nullableText(upd.CatalogVersion),
		string(upd.CurrentStep), stepsJSON, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("update session card: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

// nullableText memetakan string kosong ke NULL.
//
// card_type punya foreign key ke card_products(card_type): string kosong akan
// ditolak sebagai pelanggaran FK, sementara NULL adalah keadaan yang memang
// dimaksud — sesi yang belum memilih kartu.
func nullableText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func (r *OnboardingSessionRepo) CountActiveByDevice(ctx context.Context, deviceID string, since time.Time) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM onboarding_sessions
		WHERE device_id = $1
		  AND deleted_at IS NULL
		  AND expires_at > now()
		  AND created_at >= $2`

	var count int
	err := r.pool.QueryRow(ctx, query, deviceID, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active sessions: %w", err)
	}
	return count, nil
}

// ListForCS mengembalikan sesi untuk layar pemantauan petugas.
//
// Keyset, bukan OFFSET: daftar ini diurut created_at DESC dan sesi baru masuk terus
// sepanjang hari, jadi OFFSET akan membuat halaman kedua melewatkan baris yang bergeser.
// Pola dan alasannya sama dengan mutasi rekening.
//
// Mengembalikan limit+1 baris. Pemanggilnya memotong kelebihannya dan memakai
// keberadaannya sebagai has_more — satu query, bukan query plus COUNT yang jawabannya
// sudah basi sebelum terkirim.
func (r *OnboardingSessionRepo) ListForCS(ctx context.Context, filter onboarding.ListCSSessionsFilter) ([]*onboarding.Session, error) {
	// Argumen dirakit berurutan supaya nomor placeholder tidak pernah dihitung manual —
	// salah satu penyebab paling sering query filter opsional yang menunjuk kolom salah.
	args := []any{}
	where := []string{"deleted_at IS NULL"}

	add := func(clause string, vals ...any) {
		for i := range vals {
			args = append(args, vals[i])
			clause = strings.Replace(clause, "?", fmt.Sprintf("$%d", len(args)), 1)
		}
		where = append(where, clause)
	}

	if filter.Step != "" {
		add("current_step = ?", string(filter.Step))
	}
	if !filter.IncludeExpired {
		add("expires_at > ?", time.Now().UTC())
	}
	if filter.StalledFor > 0 {
		add("updated_at <= ?", time.Now().UTC().Add(-filter.StalledFor))
	}
	if filter.Cursor != nil {
		// Baris dengan created_at sama dibedakan id, supaya tidak ada yang terlewat
		// maupun muncul dua kali di batas halaman.
		add("(created_at, id) < (?, ?)", filter.Cursor.CreatedAt, filter.Cursor.ID)
	}

	args = append(args, filter.Limit+1)

	query := fmt.Sprintf(`
		SELECT id, session_id, device_id, product_type, current_step, tnc_version,
		       steps_completed, created_at, updated_at, expires_at, deleted_at,
		       card_type, card_selected_at, card_catalog_version
		FROM onboarding_sessions
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d`, strings.Join(where, " AND "), len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions for cs: %w", err)
	}
	defer rows.Close()

	var out []*onboarding.Session
	for rows.Next() {
		var sess onboarding.Session
		var pt, step string
		var stepsJSON []byte
		var cardType, cardCatalogVersion, tncVersion *string

		// tnc_version lewat *string, bukan langsung ke field string-nya: kolomnya
		// nullable, dan satu baris NULL di tengah daftar akan menggagalkan SELURUH
		// permintaan dengan "cannot scan NULL into *string". Sesi yang dibuat lewat API
		// selalu mengisinya, jadi kegagalannya hanya muncul pada baris lama atau baris
		// yang ditulis di luar jalur biasa — tepat baris yang paling perlu dilihat
		// petugas saat sesuatu tidak beres.
		if err := rows.Scan(
			&sess.ID, &sess.SessionID, &sess.DeviceID,
			&pt, &step, &tncVersion,
			&stepsJSON,
			&sess.CreatedAt, &sess.UpdatedAt, &sess.ExpiresAt, &sess.DeletedAt,
			&cardType, &sess.CardSelectedAt, &cardCatalogVersion,
		); err != nil {
			return nil, fmt.Errorf("scan session for cs: %w", err)
		}

		sess.ProductType = onboarding.ProductType(pt)
		sess.CurrentStep = onboarding.Step(step)
		if tncVersion != nil {
			sess.TNCVersion = *tncVersion
		}
		if cardType != nil {
			sess.CardType = *cardType
		}
		if cardCatalogVersion != nil {
			sess.CardCatalogVersion = *cardCatalogVersion
		}
		if err := json.Unmarshal(stepsJSON, &sess.StepsCompleted); err != nil {
			return nil, fmt.Errorf("unmarshal steps: %w", err)
		}

		out = append(out, &sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions for cs: %w", err)
	}
	return out, nil
}

// OnboardingAuditRepo handles append-only audit log inserts.
type OnboardingAuditRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingAuditRepo(pool *pgxpool.Pool) *OnboardingAuditRepo {
	return &OnboardingAuditRepo{pool: pool}
}

func (r *OnboardingAuditRepo) Insert(ctx context.Context, log *onboarding.AuditLog) error {
	query := `
		INSERT INTO onboarding_audit_logs
			(id, session_id, event_type, actor, details, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	var detailsJSON []byte
	if log.Details != nil {
		var err error
		detailsJSON, err = json.Marshal(log.Details)
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
	}

	_, err := r.pool.Exec(ctx, query,
		log.ID, log.SessionID, string(log.EventType), log.Actor,
		detailsJSON, log.IPAddress, log.UserAgent, log.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func (r *OnboardingAuditRepo) FindBySessionID(ctx context.Context, sessionID string) ([]*onboarding.AuditLog, error) {
	query := `
		SELECT id, session_id, event_type, actor, details, ip_address, user_agent, created_at
		FROM onboarding_audit_logs
		WHERE session_id = $1
		ORDER BY created_at ASC`

	rows, err := r.pool.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()

	var logs []*onboarding.AuditLog
	for rows.Next() {
		var log onboarding.AuditLog
		var eventType string
		var detailsJSON []byte

		if err := rows.Scan(
			&log.ID, &log.SessionID, &eventType, &log.Actor,
			&detailsJSON, &log.IPAddress, &log.UserAgent, &log.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}

		log.EventType = onboarding.AuditEventType(eventType)
		if len(detailsJSON) > 0 {
			_ = json.Unmarshal(detailsJSON, &log.Details)
		}
		logs = append(logs, &log)
	}
	return logs, rows.Err()
}

// OnboardingOCRResultRepo persists OCR extraction results.
type OnboardingOCRResultRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingOCRResultRepo(pool *pgxpool.Pool) *OnboardingOCRResultRepo {
	return &OnboardingOCRResultRepo{pool: pool}
}

func (r *OnboardingOCRResultRepo) Create(ctx context.Context, result *onboarding.OCRResult) error {
	query := `
		INSERT INTO onboarding_ocr_results
			(id, ocr_id, session_id, photo_path, accuracy_pct,
			 nik_enc, nama_enc, tempat_lahir, tanggal_lahir, jenis_kelamin,
			 alamat_enc, rt_rw, kelurahan, kecamatan, kota, provinsi,
			 agama, status_perkawinan,
			 dukcapil_match, sharpness, glare_detected, corners_visible,
			 created_at, auto_delete_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`

	// OCR is best-effort, so an unreadable date is NOT an error here.
	//
	// ParseKTPFromText hands over whatever it found: normalizeDateString only
	// recognises ISO and DD-MM-YYYY and returns anything else unchanged, so
	// "21/04/1995" or a smudged line reaches this point verbatim. Failing the
	// save would block the nasabah at the OCR step over a field they are about
	// to confirm by hand anyway — SavePersonalData is where the date is required
	// and strictly validated. Dropped to NULL with a warning, which is what this
	// call site did before parseBirthDate existed.
	tanggalLahir, err := parseBirthDate(result.Extracted.TanggalLahir)
	if err != nil {
		slog.Warn("ocr birth date is not an ISO date; stored as NULL",
			"session_id", result.SessionID, "ocr_id", result.OCRID)
		tanggalLahir = nil
	}

	_, err = r.pool.Exec(ctx, query,
		result.ID, result.OCRID, result.SessionID, result.PhotoPath, result.AccuracyPct,
		result.Extracted.NIK, result.Extracted.NamaLengkap,
		result.Extracted.TempatLahir, tanggalLahir, result.Extracted.JenisKelamin,
		result.Extracted.Alamat, result.Extracted.RTRW,
		result.Extracted.Kelurahan, result.Extracted.Kecamatan,
		result.Extracted.Kota, result.Extracted.Provinsi,
		result.Extracted.Agama, result.Extracted.StatusPerkawinan,
		result.DukcapilMatch,
		result.PhotoQuality.Sharpness, result.PhotoQuality.GlareDetected, result.PhotoQuality.AllCornersVisible,
		result.CreatedAt, result.AutoDeleteAt,
	)
	if err != nil {
		return fmt.Errorf("insert ocr result: %w", err)
	}
	return nil
}

func (r *OnboardingOCRResultRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.OCRResult, error) {
	query := `
		SELECT id, ocr_id, session_id, photo_path, accuracy_pct,
		       nik_enc, nama_enc, tempat_lahir, tanggal_lahir, jenis_kelamin,
		       alamat_enc, rt_rw, kelurahan, kecamatan, kota, provinsi,
		       agama, status_perkawinan,
		       dukcapil_match, sharpness, glare_detected, corners_visible,
		       created_at, auto_delete_at
		FROM onboarding_ocr_results
		WHERE session_id = $1
		ORDER BY created_at DESC
		LIMIT 1`

	var result onboarding.OCRResult
	var tanggalLahir *time.Time

	err := r.pool.QueryRow(ctx, query, sessionID).Scan(
		&result.ID, &result.OCRID, &result.SessionID, &result.PhotoPath, &result.AccuracyPct,
		&result.Extracted.NIK, &result.Extracted.NamaLengkap,
		&result.Extracted.TempatLahir, &tanggalLahir, &result.Extracted.JenisKelamin,
		&result.Extracted.Alamat, &result.Extracted.RTRW,
		&result.Extracted.Kelurahan, &result.Extracted.Kecamatan,
		&result.Extracted.Kota, &result.Extracted.Provinsi,
		&result.Extracted.Agama, &result.Extracted.StatusPerkawinan,
		&result.DukcapilMatch,
		&result.PhotoQuality.Sharpness, &result.PhotoQuality.GlareDetected, &result.PhotoQuality.AllCornersVisible,
		&result.CreatedAt, &result.AutoDeleteAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find ocr result: %w", err)
	}

	if tanggalLahir != nil {
		result.Extracted.TanggalLahir = tanggalLahir.Format("2006-01-02")
	}

	return &result, nil
}

// OnboardingPersonalDataRepo persists personal data for onboarding.
type OnboardingPersonalDataRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingPersonalDataRepo(pool *pgxpool.Pool) *OnboardingPersonalDataRepo {
	return &OnboardingPersonalDataRepo{pool: pool}
}

func (r *OnboardingPersonalDataRepo) Create(ctx context.Context, data *onboarding.PersonalData) error {
	query := `
		INSERT INTO onboarding_personal_data
			(id, personal_data_id, session_id,
			 nik_enc, nama_enc, tempat_lahir, tanggal_lahir, jenis_kelamin,
			 alamat_lengkap_enc, rt_rw, kode_pos, kelurahan, kecamatan, kota, provinsi,
			 alamat_domisili_sama,
			 pekerjaan, penghasilan_per_bulan, sumber_dana_utama,
			 nomor_hp_enc, email_enc,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`

	tanggalLahir, err := parseBirthDate(data.TanggalLahir)
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, query,
		data.ID, data.PersonalDataID, data.SessionID,
		data.NIK, data.NamaLengkap,
		data.TempatLahir, tanggalLahir, data.JenisKelamin,
		data.AlamatKTP.AlamatLengkap, data.AlamatKTP.RTRW, data.AlamatKTP.KodePos,
		data.AlamatKTP.Kelurahan, data.AlamatKTP.Kecamatan, data.AlamatKTP.Kota, data.AlamatKTP.Provinsi,
		data.AlamatDomisiliSama,
		string(data.Pekerjaan), string(data.PenghasilanPerBulan), string(data.SumberDanaUtama),
		data.NomorHP, data.Email,
		data.CreatedAt, data.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert personal data: %w", err)
	}
	return nil
}

func (r *OnboardingPersonalDataRepo) Update(ctx context.Context, data *onboarding.PersonalData) error {
	query := `
		UPDATE onboarding_personal_data
		SET nik_enc = $2, nama_enc = $3, tempat_lahir = $4, tanggal_lahir = $5, jenis_kelamin = $6,
		    alamat_lengkap_enc = $7, rt_rw = $8, kode_pos = $9, kelurahan = $10, kecamatan = $11, kota = $12, provinsi = $13,
		    alamat_domisili_sama = $14,
		    pekerjaan = $15, penghasilan_per_bulan = $16, sumber_dana_utama = $17,
		    nomor_hp_enc = $18, email_enc = $19, updated_at = $20
		WHERE session_id = $1`

	tanggalLahir, err := parseBirthDate(data.TanggalLahir)
	if err != nil {
		return err
	}

	tag, err := r.pool.Exec(ctx, query,
		data.SessionID,
		data.NIK, data.NamaLengkap,
		data.TempatLahir, tanggalLahir, data.JenisKelamin,
		data.AlamatKTP.AlamatLengkap, data.AlamatKTP.RTRW, data.AlamatKTP.KodePos,
		data.AlamatKTP.Kelurahan, data.AlamatKTP.Kecamatan, data.AlamatKTP.Kota, data.AlamatKTP.Provinsi,
		data.AlamatDomisiliSama,
		string(data.Pekerjaan), string(data.PenghasilanPerBulan), string(data.SumberDanaUtama),
		data.NomorHP, data.Email,
		data.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("update personal data: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("personal data not found for session")
	}
	return nil
}

func (r *OnboardingPersonalDataRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.PersonalData, error) {
	query := `
		SELECT id, personal_data_id, session_id,
		       nik_enc, nama_enc, tempat_lahir, tanggal_lahir, jenis_kelamin,
		       alamat_lengkap_enc, rt_rw, kode_pos, kelurahan, kecamatan, kota, provinsi,
		       alamat_domisili_sama,
		       pekerjaan, penghasilan_per_bulan, sumber_dana_utama,
		       nomor_hp_enc, email_enc,
		       created_at, updated_at
		FROM onboarding_personal_data
		WHERE session_id = $1
		ORDER BY created_at DESC
		LIMIT 1`

	var data onboarding.PersonalData
	var tanggalLahir *time.Time
	var pekerjaan, penghasilan, sumberDana string

	err := r.pool.QueryRow(ctx, query, sessionID).Scan(
		&data.ID, &data.PersonalDataID, &data.SessionID,
		&data.NIK, &data.NamaLengkap,
		&data.TempatLahir, &tanggalLahir, &data.JenisKelamin,
		&data.AlamatKTP.AlamatLengkap, &data.AlamatKTP.RTRW, &data.AlamatKTP.KodePos,
		&data.AlamatKTP.Kelurahan, &data.AlamatKTP.Kecamatan, &data.AlamatKTP.Kota, &data.AlamatKTP.Provinsi,
		&data.AlamatDomisiliSama,
		&pekerjaan, &penghasilan, &sumberDana,
		&data.NomorHP, &data.Email,
		&data.CreatedAt, &data.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find personal data: %w", err)
	}

	if tanggalLahir != nil {
		data.TanggalLahir = tanggalLahir.Format("2006-01-02")
	}
	data.Pekerjaan = onboarding.Pekerjaan(pekerjaan)
	data.PenghasilanPerBulan = onboarding.Penghasilan(penghasilan)
	data.SumberDanaUtama = onboarding.SumberDana(sumberDana)

	return &data, nil
}

// OnboardingBiometricRepo persists biometric verification results.
type OnboardingBiometricRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingBiometricRepo(pool *pgxpool.Pool) *OnboardingBiometricRepo {
	return &OnboardingBiometricRepo{pool: pool}
}

func (r *OnboardingBiometricRepo) Create(ctx context.Context, result *onboarding.BiometricResult) error {
	query := `
		INSERT INTO onboarding_biometrics
			(id, biometric_id, session_id, face_photo_path,
			 liveness_verified, liveness_score,
			 face_match_verified, face_match_score,
			 iso_compliant, spoof_detected, frame_count,
			 created_at, auto_delete_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`

	_, err := r.pool.Exec(ctx, query,
		result.ID, result.BiometricID, result.SessionID, result.FacePhotoPath,
		result.LivenessVerified, result.LivenessScore,
		result.FaceMatchVerified, result.FaceMatchScore,
		result.ISOCompliant, result.SpoofDetected, result.FrameCount,
		result.CreatedAt, result.AutoDeleteAt,
	)
	if err != nil {
		return fmt.Errorf("insert biometric result: %w", err)
	}
	return nil
}

func (r *OnboardingBiometricRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.BiometricResult, error) {
	query := `
		SELECT id, biometric_id, session_id, face_photo_path,
		       liveness_verified, liveness_score,
		       face_match_verified, face_match_score,
		       iso_compliant, spoof_detected, frame_count,
		       created_at, auto_delete_at
		FROM onboarding_biometrics
		WHERE session_id = $1
		ORDER BY created_at DESC
		LIMIT 1`

	var result onboarding.BiometricResult
	err := r.pool.QueryRow(ctx, query, sessionID).Scan(
		&result.ID, &result.BiometricID, &result.SessionID, &result.FacePhotoPath,
		&result.LivenessVerified, &result.LivenessScore,
		&result.FaceMatchVerified, &result.FaceMatchScore,
		&result.ISOCompliant, &result.SpoofDetected, &result.FrameCount,
		&result.CreatedAt, &result.AutoDeleteAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find biometric result: %w", err)
	}
	return &result, nil
}

// OnboardingVideoCallRepo persists video call records.
type OnboardingVideoCallRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingVideoCallRepo(pool *pgxpool.Pool) *OnboardingVideoCallRepo {
	return &OnboardingVideoCallRepo{pool: pool}
}

func (r *OnboardingVideoCallRepo) Create(ctx context.Context, vc *onboarding.VideoCall) error {
	query := `
		INSERT INTO onboarding_video_calls
			(id, queue_id, session_id, queue_number, status, joined_at)
		VALUES ($1,$2,$3,$4,$5,$6)`

	_, err := r.pool.Exec(ctx, query,
		vc.ID, vc.QueueID, vc.SessionID, vc.QueueNumber,
		string(vc.Status), vc.JoinedAt,
	)
	if err != nil {
		return fmt.Errorf("insert video call: %w", err)
	}
	return nil
}

func (r *OnboardingVideoCallRepo) FindByQueueID(ctx context.Context, queueID string) (*onboarding.VideoCall, error) {
	query := `
		SELECT id, queue_id, session_id, queue_number, status,
		       agent_employee_id, agent_name, result,
		       ktp_shown_live, identity_confirmed, notes,
		       call_duration_seconds, recording_id,
		       joined_at, started_at, ended_at
		FROM onboarding_video_calls
		WHERE queue_id = $1`

	return r.scanVideoCall(ctx, query, queueID)
}

func (r *OnboardingVideoCallRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.VideoCall, error) {
	query := `
		SELECT id, queue_id, session_id, queue_number, status,
		       agent_employee_id, agent_name, result,
		       ktp_shown_live, identity_confirmed, notes,
		       call_duration_seconds, recording_id,
		       joined_at, started_at, ended_at
		FROM onboarding_video_calls
		WHERE session_id = $1
		ORDER BY joined_at DESC
		LIMIT 1`

	return r.scanVideoCall(ctx, query, sessionID)
}

func (r *OnboardingVideoCallRepo) scanVideoCall(ctx context.Context, query, param string) (*onboarding.VideoCall, error) {
	var vc onboarding.VideoCall
	var status string
	var agentID, agentName, result, notes, recordingID *string

	err := r.pool.QueryRow(ctx, query, param).Scan(
		&vc.ID, &vc.QueueID, &vc.SessionID, &vc.QueueNumber, &status,
		&agentID, &agentName, &result,
		&vc.KTPShownLive, &vc.IdentityConfirmed, &notes,
		&vc.CallDurationSeconds, &recordingID,
		&vc.JoinedAt, &vc.StartedAt, &vc.EndedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan video call: %w", err)
	}

	vc.Status = onboarding.VideoCallStatus(status)
	if agentID != nil {
		vc.AgentEmployeeID = *agentID
	}
	if agentName != nil {
		vc.AgentName = *agentName
	}
	if result != nil {
		vc.Result = onboarding.VideoCallResult(*result)
	}
	if notes != nil {
		vc.Notes = *notes
	}
	if recordingID != nil {
		vc.RecordingID = *recordingID
	}
	return &vc, nil
}

func (r *OnboardingVideoCallRepo) UpdateResult(ctx context.Context, queueID string, result onboarding.VideoCallResult, agentEmployeeID, agentName, notes, recordingID string, ktpShown, identityConfirmed bool, durationSeconds int) error {
	now := time.Now().UTC()
	query := `
		UPDATE onboarding_video_calls
		SET status = 'COMPLETED', result = $2,
		    -- Yang sudah tercatat MENANG, bukan ditimpa pelapor.
		    --
		    -- Urutan COALESCE-nya dulu terbalik, jadi satu permintaan hasil bisa menulis
		    -- ulang kolom ini menjadi siapa pun — termasuk petugas yang tidak pernah
		    -- menangani panggilannya. Jejak audit lalu bertentangan dengan nama yang sudah
		    -- dilihat nasabah di agent_assigned. Service juga menolak pelapor yang bukan
		    -- petugas yang mengambil panggilan; ini pertahanan kedua di lapisan SQL.
		    agent_employee_id = COALESCE(agent_employee_id, NULLIF($3, '')),
		    -- NULLIF supaya pemanggil yang mengirim nama kosong tidak menghapus nama yang
		    -- sudah tercatat saat agent mengambil panggilan. Itu persis bug yang membuat
		    -- kolom ini selalu berakhir NULL.
		    agent_name = COALESCE(NULLIF($4, ''), agent_name),
		    notes = $5, recording_id = $6,
		    ktp_shown_live = $7, identity_confirmed = $8,
		    call_duration_seconds = $9, ended_at = $10
		WHERE queue_id = $1`

	tag, err := r.pool.Exec(ctx, query, queueID,
		string(result), agentEmployeeID, agentName,
		notes, recordingID,
		ktpShown, identityConfirmed,
		durationSeconds, now,
	)
	if err != nil {
		return fmt.Errorf("update video call result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video call not found: %s", queueID)
	}
	return nil
}

// MarkActive records that an agent has picked the call up.
//
// COALESCE pada agent_name menjaga nama yang sudah tercatat: CS yang meminta token agent
// dua kali (mis. menyambung ulang setelah socketnya putus) tidak boleh menghapus nama yang
// sudah ada hanya karena permintaan kedua mengirimnya kosong.
//
// started_at dipasang sekali lewat COALESCE juga, supaya durasi panggilan dihitung dari
// agent masuk yang pertama, bukan dari upaya menyambung ulang yang terakhir.
func (r *OnboardingVideoCallRepo) MarkActive(ctx context.Context, queueID, agentEmployeeID, agentName string) error {
	now := time.Now().UTC()
	query := `
		UPDATE onboarding_video_calls
		SET status = 'ACTIVE',
		    agent_employee_id = COALESCE(NULLIF($2, ''), agent_employee_id),
		    agent_name = COALESCE(NULLIF($3, ''), agent_name),
		    started_at = COALESCE(started_at, $4)
		WHERE queue_id = $1 AND status <> 'COMPLETED' AND status <> 'CANCELLED'`

	tag, err := r.pool.Exec(ctx, query, queueID, agentEmployeeID, agentName, now)
	if err != nil {
		return fmt.Errorf("mark video call active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video call not active: %s", queueID)
	}
	return nil
}

// Cancel membebaskan panggilan yang masih QUEUED atau ACTIVE.
//
// Alasan pembatalan masuk ke audit trail lewat service, tempat seluruh riwayat sesi memang
// dibaca. Menambah kolom di sini akan menduplikasi jejak yang sudah ada tanpa ada yang
// membacanya.
//
// Status akhir COMPLETED tidak pernah disentuh: hasil verifikasi yang sudah tercatat tidak
// boleh bisa dibatalkan oleh jalur pembersihan apa pun.
func (r *OnboardingVideoCallRepo) Cancel(ctx context.Context, queueID string) (bool, error) {
	query := `
		UPDATE onboarding_video_calls
		SET status = 'CANCELLED', ended_at = COALESCE(ended_at, $2)
		WHERE queue_id = $1 AND status IN ('QUEUED', 'ACTIVE')`

	tag, err := r.pool.Exec(ctx, query, queueID, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("cancel video call: %w", err)
	}
	// Nol baris bukan error: semua pemanggilnya jalur pembersihan yang harus idempoten,
	// dan panggilan yang sudah selesai atau sudah dibatalkan memang tidak perlu disentuh.
	return tag.RowsAffected() > 0, nil
}

// CSAgentRepo mengautentikasi petugas CS untuk endpoint internal video call.
type CSAgentRepo struct {
	pool *pgxpool.Pool
}

func NewCSAgentRepo(pool *pgxpool.Pool) *CSAgentRepo {
	return &CSAgentRepo{pool: pool}
}

// AuthenticateAgent memverifikasi kredensial petugas dan mengembalikan namanya.
//
// Hash Argon2 ber-salt tidak bisa dicari balik, jadi pemanggil menyebut dirinya lebih dulu
// (`employeeID`) dan membuktikannya dengan `apiKey`. Pola yang sama dipakai login nasabah;
// yang berbeda hanya dari mana identitasnya datang.
//
// Tiga keadaan dibedakan dengan sengaja: (name, scopes, true, nil) berhasil,
// ("", nil, false, nil) kredensial salah atau petugas tidak aktif, dan
// ("", nil, false, err) kegagalan infrastruktur. Pemanggilnya TIDAK boleh memperlakukan
// yang ketiga sebagai penolakan — Postgres yang tersendat bukan bukti bahwa petugasnya
// tidak berwenang.
//
// Cakupan ikut dikembalikan, bukan ditanya terpisah: kewenangan yang dibaca dari baris
// yang berbeda dengan yang mengautentikasi membuka celah waktu antara keduanya.
func (r *CSAgentRepo) AuthenticateAgent(ctx context.Context, employeeID, apiKey string) (string, []string, bool, error) {
	if employeeID == "" || apiKey == "" {
		return "", nil, false, nil
	}

	var name, hash string
	var scopes []string
	err := r.pool.QueryRow(ctx,
		`SELECT name, api_key_hash, scopes FROM cs_agents WHERE employee_id = $1 AND is_active`,
		employeeID,
	).Scan(&name, &hash, &scopes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Petugas tidak dikenal. Argon2 sengaja tidak dijalankan di sini: jalur ini
			// hanya terbuka bagi pemanggil yang sudah lolos X-Internal-API-Key, jadi
			// tidak ada penyerang anonim yang bisa mengukur selisih waktunya.
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("find cs agent: %w", err)
	}

	ok, err := crypto.VerifyPassword(ctx, apiKey, hash)
	if err != nil {
		return "", nil, false, fmt.Errorf("verify cs agent key: %w", err)
	}
	if !ok {
		return "", nil, false, nil
	}
	return name, scopes, true, nil
}

// OnboardingCredentialRepo persists hashed credentials for onboarding.
type OnboardingCredentialRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingCredentialRepo(pool *pgxpool.Pool) *OnboardingCredentialRepo {
	return &OnboardingCredentialRepo{pool: pool}
}

func (r *OnboardingCredentialRepo) Create(ctx context.Context, cred *onboarding.Credential) error {
	query := `
		INSERT INTO onboarding_credentials
			(id, credential_id, session_id, access_code_hash, pin_hash, encryption_key_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`

	_, err := r.pool.Exec(ctx, query,
		cred.ID, cred.CredentialID, cred.SessionID,
		cred.AccessCodeHash, cred.PINHash, cred.EncryptionKeyID,
		cred.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert credential: %w", err)
	}
	return nil
}

func (r *OnboardingCredentialRepo) FindBySessionID(ctx context.Context, sessionID string) (*onboarding.Credential, error) {
	query := `
		SELECT id, credential_id, session_id, access_code_hash, pin_hash, encryption_key_id, created_at
		FROM onboarding_credentials
		WHERE session_id = $1
		ORDER BY created_at DESC
		LIMIT 1`

	var cred onboarding.Credential
	err := r.pool.QueryRow(ctx, query, sessionID).Scan(
		&cred.ID, &cred.CredentialID, &cred.SessionID,
		&cred.AccessCodeHash, &cred.PINHash, &cred.EncryptionKeyID,
		&cred.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find credential: %w", err)
	}
	return &cred, nil
}

// parseBirthDate converts the ISO date the domain carries as a string into the
// DATE column's value. Empty stays NULL, which is legitimate: OCR does not
// always find the field.
//
// An unparseable non-empty value is an ERROR, not NULL. The two personal-data
// writers used to swallow it, so a typo dropped a KYC field while the response
// said 200 and the account went on to core banking without a date of birth.
// SavePersonalData validates the format now; this is the backstop for every
// other writer of nasabah-confirmed data.
//
// The OCR writer deliberately does NOT propagate this error — see the comment at
// that call site. Machine-read text is allowed to be wrong; what the nasabah
// confirmed is not.
func parseBirthDate(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, fmt.Errorf("tanggal_lahir %q is not an ISO date: %w", v, err)
	}
	return &t, nil
}

// OnboardingVideoCallScheduleRepo menyimpan janji video call.
type OnboardingVideoCallScheduleRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingVideoCallScheduleRepo(pool *pgxpool.Pool) *OnboardingVideoCallScheduleRepo {
	return &OnboardingVideoCallScheduleRepo{pool: pool}
}

// Create menyisipkan jadwal baru.
//
// Jadwal ganda dijawab dari PELANGGARAN UNIQUE INDEX (23505), bukan dari SELECT lebih
// dulu: idx_vc_schedules_one_active hanya mengizinkan satu baris SCHEDULED per sesi, dan
// dua permintaan bersamaan akan sama-sama melihat "belum ada jadwal" kalau diperiksa di
// aplikasi. Yang benar-benar menahannya adalah index.
func (r *OnboardingVideoCallScheduleRepo) Create(ctx context.Context, sch *onboarding.VideoCallSchedule) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO onboarding_video_call_schedules
			(id, schedule_id, session_id, scheduled_at, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::video_call_schedule_status, $6, $6)`,
		sch.ID, sch.ScheduleID, sch.SessionID, sch.ScheduledAt,
		string(sch.Status), sch.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return apperr.VideoCallAlreadyScheduled
		}
		return fmt.Errorf("insert video call schedule: %w", err)
	}
	return nil
}

func (r *OnboardingVideoCallScheduleRepo) FindActiveBySessionID(ctx context.Context, sessionID string) (*onboarding.VideoCallSchedule, error) {
	var sch onboarding.VideoCallSchedule
	var status string

	err := r.pool.QueryRow(ctx, `
		SELECT id, schedule_id, session_id, scheduled_at, status, created_at
		FROM onboarding_video_call_schedules
		WHERE session_id = $1 AND status = 'SCHEDULED'`, sessionID,
	).Scan(&sch.ID, &sch.ScheduleID, &sch.SessionID, &sch.ScheduledAt, &status, &sch.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find active video call schedule: %w", err)
	}

	sch.Status = onboarding.VideoCallScheduleStatus(status)
	return &sch, nil
}

// CancelBySessionID membatalkan jadwal aktif. Nol baris bukan error — pemanggilnya yang
// memutuskan apakah itu berarti 404.
func (r *OnboardingVideoCallScheduleRepo) CancelBySessionID(ctx context.Context, sessionID string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE onboarding_video_call_schedules
		SET status = 'CANCELLED', updated_at = $2
		WHERE session_id = $1 AND status = 'SCHEDULED'`,
		sessionID, time.Now().UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("cancel video call schedule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// --- Eskalasi NEED_REVIEW (migrasi 000038) ---

// OnboardingVideoCallEscalationRepo mengimplementasikan
// onboarding.VideoCallEscalationRepository di atas onboarding_video_call_escalations.
type OnboardingVideoCallEscalationRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingVideoCallEscalationRepo(pool *pgxpool.Pool) *OnboardingVideoCallEscalationRepo {
	return &OnboardingVideoCallEscalationRepo{pool: pool}
}

// Create menyisipkan eskalasi baru.
//
// `id` dibiarkan DEFAULT gen_random_uuid(): entitas domainnya tidak punya kolom itu, dan
// menambahkannya hanya supaya aplikasi bisa mengisinya akan membuat satu field yang tidak
// pernah dibaca siapa pun.
//
// Eskalasi ganda dijawab dari PELANGGARAN UNIQUE INDEX (23505), bukan dari SELECT lebih
// dulu — alasannya sama dengan jadwal video call di atas: idx_vc_escalations_one_open
// hanya mengizinkan satu baris PENDING/IN_REVIEW per sesi, dan dua petugas yang menyubmit
// NEED_REVIEW bersamaan akan sama-sama melihat "belum ada eskalasi" kalau diperiksa di
// aplikasi. Yang benar-benar menahannya adalah index.
func (r *OnboardingVideoCallEscalationRepo) Create(ctx context.Context, esc *onboarding.VideoCallEscalation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO onboarding_video_call_escalations
			(escalation_id, session_id, queue_id, escalation_queue,
			 status, reason, raised_by_agent, raised_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		esc.EscalationID, esc.SessionID, esc.QueueID, esc.EscalationQueue,
		esc.Status, esc.Reason, esc.RaisedByAgent, esc.RaisedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return apperr.VideoCallEscalationExists
		}
		return fmt.Errorf("insert video call escalation: %w", err)
	}
	return nil
}

// FindOpenBySessionID mengembalikan eskalasi yang masih terbuka, atau nil, nil.
//
// Daftar statusnya SAMA dengan predikat idx_vc_escalations_one_open. Dua daftar yang
// boleh berbeda akan berbeda: kalau query ini membaca lebih sedikit status daripada yang
// ditahan index, penjaga JoinQueue akan meluluskan nasabah yang Create-nya justru akan
// ditolak index — nasabah mengantre, lalu petugas tidak bisa menuntaskan perkaranya.
func (r *OnboardingVideoCallEscalationRepo) FindOpenBySessionID(ctx context.Context, sessionID string) (*onboarding.VideoCallEscalation, error) {
	row := r.pool.QueryRow(ctx, selectEscalation+`
		WHERE session_id = $1 AND status IN ('PENDING', 'IN_REVIEW')`, sessionID)

	esc, err := scanEscalation(row)
	if err != nil {
		return nil, fmt.Errorf("find open video call escalation: %w", err)
	}
	return esc, nil
}

// selectEscalation adalah SATU daftar kolom untuk ketiga jalur baca perkara eskalasi.
//
// Satu tempat supaya urutan Scan tidak pernah berbeda antar query — penyebab paling
// sering kolom tertukar di repository yang punya beberapa pembaca untuk satu tipe. Pola
// yang sama dengan selectCustomer di cs_repo.go.
//
// COALESCE pada kolom teks yang nullable: perkara yang belum dipegang dan belum ditutup
// punya NULL di lima kolom terakhir, dan Scan ke string biasa akan gagal. Kosong adalah
// jawaban yang benar untuk "belum ada", dan entitasnya memakai omitempty.
const selectEscalation = `
	SELECT escalation_id, session_id, queue_id, escalation_queue,
	       status, reason, raised_by_agent, raised_at,
	       COALESCE(claimed_by_agent, '')   AS claimed_by_agent,
	       claimed_at,
	       COALESCE(resolved_by_agent, '')  AS resolved_by_agent,
	       resolved_at,
	       COALESCE(resolution, '')         AS resolution,
	       COALESCE(resolution_reason, '')  AS resolution_reason,
	       COALESCE(resolution_notes, '')   AS resolution_notes
	FROM onboarding_video_call_escalations`

// scanEscalation mengembalikan nil, nil saat tidak ada baris — miss BUKAN error, sama
// seperti pembaca lain di berkas ini.
func scanEscalation(row pgx.Row) (*onboarding.VideoCallEscalation, error) {
	var esc onboarding.VideoCallEscalation
	err := row.Scan(
		&esc.EscalationID, &esc.SessionID, &esc.QueueID, &esc.EscalationQueue,
		&esc.Status, &esc.Reason, &esc.RaisedByAgent, &esc.RaisedAt,
		&esc.ClaimedByAgent, &esc.ClaimedAt,
		&esc.ResolvedByAgent, &esc.ResolvedAt,
		&esc.Resolution, &esc.ResolutionReason, &esc.ResolutionNotes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan video call escalation: %w", err)
	}
	return &esc, nil
}

// FindByID membaca satu perkara APA PUN statusnya.
func (r *OnboardingVideoCallEscalationRepo) FindByID(ctx context.Context, escalationID string) (*onboarding.VideoCallEscalation, error) {
	row := r.pool.QueryRow(ctx, selectEscalation+`
		WHERE escalation_id = $1`, escalationID)

	esc, err := scanEscalation(row)
	if err != nil {
		return nil, fmt.Errorf("find video call escalation: %w", err)
	}
	return esc, nil
}

// List membaca antrean kerja Tier 2, terlama dulu.
//
// Status kosong berarti yang TERBUKA saja, dan daftar statusnya SAMA dengan predikat
// idx_vc_escalations_one_open serta dengan FindOpenBySessionID. Tiga daftar yang boleh
// berbeda akan berbeda, dan yang berbeda di sini berarti perkara yang menahan nasabah
// tidak muncul di layar siapa pun.
func (r *OnboardingVideoCallEscalationRepo) List(ctx context.Context, filter onboarding.ListEscalationsFilter) ([]onboarding.VideoCallEscalation, error) {
	args := []any{}
	where := ""
	add := func(clause string, arg any) {
		args = append(args, arg)
		where += fmt.Sprintf(" AND %s$%d", clause, len(args))
	}

	if filter.Status != "" {
		add("status = ", filter.Status)
	} else {
		where += " AND status IN ('PENDING', 'IN_REVIEW')"
	}
	if filter.Queue != "" {
		add("escalation_queue = ", filter.Queue)
	}
	if filter.ClaimedBy != "" {
		add("claimed_by_agent = ", filter.ClaimedBy)
	}
	if filter.SessionID != "" {
		add("session_id = ", filter.SessionID)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)

	// WHERE TRUE supaya setiap penyaring di atas bisa menempel sebagai " AND ..." tanpa
	// seseorang harus melacak mana yang pertama. Rencana kuerinya tidak terpengaruh.
	rows, err := r.pool.Query(ctx, selectEscalation+`
		WHERE TRUE`+where+`
		ORDER BY raised_at ASC
		LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list video call escalations: %w", err)
	}
	defer rows.Close()

	out := make([]onboarding.VideoCallEscalation, 0, limit)
	for rows.Next() {
		esc, err := scanEscalation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *esc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate video call escalations: %w", err)
	}
	return out, nil
}

// Claim memindahkan PENDING → IN_REVIEW.
//
// Status PENDING ada DI DALAM kondisi UPDATE, bukan diperiksa lebih dulu di aplikasi:
// dua peninjau yang menekan tombolnya pada detik yang sama akan sama-sama melihat
// "masih PENDING", dan yang kedua menimpa nama pemegang yang pertama — lalu keduanya
// mengerjakan perkara yang sama sambil masing-masing yakin memegangnya.
func (r *OnboardingVideoCallEscalationRepo) Claim(ctx context.Context, escalationID, agentID string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE onboarding_video_call_escalations
		SET status = 'IN_REVIEW', claimed_by_agent = $2, claimed_at = $3
		WHERE escalation_id = $1 AND status = 'PENDING'`,
		escalationID, agentID, at,
	)
	if err != nil {
		return false, fmt.Errorf("claim video call escalation: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Resolve menutup perkara.
//
// Dua kondisi yang keduanya penting:
//
//   - status IN ('PENDING','IN_REVIEW') — perkara yang sudah ditutup tidak boleh
//     ditutup ulang dengan keputusan yang berbeda.
//   - claimed_by_agent IS NULL OR = penutupnya — perkara yang sedang dipegang orang
//     hanya boleh ditutup pemegangnya. Service sudah memeriksanya untuk bisa menjawab
//     409 dengan pesan yang tepat, tapi pemeriksaan di aplikasi saja akan dilewati oleh
//     dua permintaan yang berlomba.
//
// resolution_reason dikirim NULL saat kosong, bukan ”: CHECK
// vc_escalations_resolution_reason_valid hanya mengizinkan NULL atau salah satu dari
// enam alasan, dan ” bukan salah satunya.
func (r *OnboardingVideoCallEscalationRepo) Resolve(ctx context.Context, escalationID string, res onboarding.EscalationResolution, at time.Time) (bool, error) {
	var reason *string
	if res.ResolutionReason != "" {
		reason = &res.ResolutionReason
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE onboarding_video_call_escalations
		SET status            = 'RESOLVED',
		    resolved_by_agent = $2,
		    resolved_at       = $3,
		    resolution        = $4,
		    resolution_reason = $5,
		    resolution_notes  = $6
		WHERE escalation_id = $1
		  AND status IN ('PENDING', 'IN_REVIEW')
		  AND (claimed_by_agent IS NULL OR claimed_by_agent = $2)`,
		escalationID, res.ResolvedByAgent, at, res.Resolution, reason, res.Notes,
	)
	if err != nil {
		return false, fmt.Errorf("resolve video call escalation: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// --- Liveness attempts ---

// OnboardingLivenessAttemptRepo writes the per-attempt audit trail.
type OnboardingLivenessAttemptRepo struct {
	pool *pgxpool.Pool
}

func NewOnboardingLivenessAttemptRepo(pool *pgxpool.Pool) *OnboardingLivenessAttemptRepo {
	return &OnboardingLivenessAttemptRepo{pool: pool}
}

// Insert appends one attempt row.
//
// No frames are written — see the comment on the migration. What goes in is the
// decision, the reason label, and the signals, which is enough to review an
// attempt without keeping the face.
func (r *OnboardingLivenessAttemptRepo) Insert(
	ctx context.Context,
	attempt *onboarding.LivenessAttempt,
) error {
	signals, err := json.Marshal(attempt.RiskSignals)
	if err != nil {
		return fmt.Errorf("marshal liveness risk signals: %w", err)
	}

	query := `
		INSERT INTO liveness_attempts
			(id, session_id, challenge_id, device_id, outcome, reason,
			 liveness_score, face_match_score, frame_count, integrity_ok,
			 risk_signals, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	_, err = r.pool.Exec(ctx, query,
		attempt.ID, attempt.SessionID, attempt.ChallengeID, attempt.DeviceID,
		string(attempt.Outcome), attempt.Reason,
		attempt.LivenessScore, attempt.FaceMatchScore, attempt.FrameCount,
		attempt.IntegrityOK, signals,
		attempt.IPAddress, attempt.UserAgent, attempt.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert liveness attempt: %w", err)
	}
	return nil
}

// CountFailuresSince backs the 24-hour window when Redis has been flushed.
//
// Escalated attempts count as failures: the applicant was routed to an agent
// precisely because self-service liveness did not succeed.
func (r *OnboardingLivenessAttemptRepo) CountFailuresSince(
	ctx context.Context,
	sessionID string,
	since time.Time,
) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM liveness_attempts
		WHERE session_id = $1
		  AND created_at >= $2
		  AND outcome IN ('FAILED', 'BLOCKED', 'ESCALATED')`

	var count int
	if err := r.pool.QueryRow(ctx, query, sessionID, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count liveness failures: %w", err)
	}
	return count, nil
}
