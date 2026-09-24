package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
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
			 card_type, card_selected_at, card_catalog_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

	_, err = r.pool.Exec(ctx, query,
		session.ID, session.SessionID, session.DeviceID,
		string(session.ProductType), string(session.CurrentStep), session.TNCVersion,
		stepsJSON,
		session.CreatedAt, session.UpdatedAt, session.ExpiresAt,
		nullableText(session.CardType), session.CardSelectedAt,
		nullableText(session.CardCatalogVersion),
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
	var cardType, cardCatalogVersion *string

	err := r.pool.QueryRow(ctx, query, sessionID).Scan(
		&s.ID, &s.SessionID, &s.DeviceID,
		&pt, &step, &s.TNCVersion,
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

	var tanggalLahir *time.Time
	if result.Extracted.TanggalLahir != "" {
		if t, err := time.Parse("2006-01-02", result.Extracted.TanggalLahir); err == nil {
			tanggalLahir = &t
		}
	}

	_, err := r.pool.Exec(ctx, query,
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

	var tanggalLahir *time.Time
	if data.TanggalLahir != "" {
		if t, err := time.Parse("2006-01-02", data.TanggalLahir); err == nil {
			tanggalLahir = &t
		}
	}

	_, err := r.pool.Exec(ctx, query,
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

	var tanggalLahir *time.Time
	if data.TanggalLahir != "" {
		if t, err := time.Parse("2006-01-02", data.TanggalLahir); err == nil {
			tanggalLahir = &t
		}
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
		    agent_employee_id = $3, agent_name = $4,
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
