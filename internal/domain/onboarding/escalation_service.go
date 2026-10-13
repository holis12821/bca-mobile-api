package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// EscalationService melayani Tier 2: membaca antrean perkara NEED_REVIEW dan menutupnya.
//
// Berkas tersendiri, bukan menumpang VideoCallService, karena ia melayani jam kerja yang
// berbeda. VideoCallService adalah satu panggilan dari antrean sampai hasil; ini adalah
// tindak lanjut atas panggilan yang sudah selesai, dilakukan orang lain, mungkin berjam-jam
// kemudian, dan tanpa socket maupun media sama sekali.
//
// Selisih yang ditutupnya: sejak migrasi 000038, baris eskalasi LAHIR tapi tidak ada
// satu pun jalur yang MENUTUPNYA. Nasabah yang perkaranya tidak pernah ditutup dijawab
// `422 VIDEO_CALL_UNDER_REVIEW` setiap kali ia mencoba mengantre — selamanya, dan satu-
// satunya pertolongan adalah seseorang yang mengetik UPDATE di database produksi.
type EscalationService struct {
	escalations VideoCallEscalationRepository
	sessions    SessionRepository
	cache       SessionCache
	audit       AuditRepository
	clock       func() time.Time
}

type EscalationServiceConfig struct {
	// Escalations nil membuat SELURUH jalur ini menolak dengan 503, bukan menjawab
	// daftar kosong: daftar kosong terbaca sebagai "tidak ada perkara", dan itu
	// kesimpulan yang membuat petugas Tier 2 pulang.
	Escalations VideoCallEscalationRepository

	Sessions SessionRepository
	Cache    SessionCache
	Audit    AuditRepository

	Clock func() time.Time
}

func NewEscalationService(cfg EscalationServiceConfig) *EscalationService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &EscalationService{
		escalations: cfg.Escalations,
		sessions:    cfg.Sessions,
		cache:       cfg.Cache,
		audit:       cfg.Audit,
		clock:       clock,
	}
}

const (
	escalationDefaultLimit = 50
	escalationMaxLimit     = 200
)

// List mengembalikan antrean kerja Tier 2.
//
// Penyaring yang salah ketik DITOLAK, bukan dijawab daftar kosong. Alasannya sama dengan
// `GET /cs/audit-events`: nol perkara karena `FRAUD_REVIEWS` tidak bisa dibedakan dari
// nol perkara karena antrean penipuan memang sedang bersih, dan yang kedua adalah
// kesimpulan yang diambil orang untuk tidak mengerjakan apa pun hari itu.
func (s *EscalationService) List(ctx context.Context, filter ListEscalationsFilter) ([]VideoCallEscalation, error) {
	if s.escalations == nil {
		return nil, apperr.ProviderNotConfigured
	}

	filter.Queue = strings.ToUpper(strings.TrimSpace(filter.Queue))
	if filter.Queue != "" && !ValidEscalationQueue(filter.Queue) {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ESCALATION_QUEUE_UNKNOWN",
			Message: "Antrean eskalasi tidak dikenal.",
			Details: map[string]any{
				"queue":   filter.Queue,
				"allowed": []string{EscalationTier2, EscalationFraud, EscalationCompliance},
			},
		}
	}

	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if filter.Status != "" && !ValidEscalationStatus(filter.Status) {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ESCALATION_STATUS_UNKNOWN",
			Message: "Status perkara tidak dikenal.",
			Details: map[string]any{
				"status": filter.Status,
				"allowed": []string{
					EscalationStatusPending, EscalationStatusInReview,
					EscalationStatusResolved, EscalationStatusCancelled,
				},
			},
		}
	}

	if filter.Limit <= 0 {
		filter.Limit = escalationDefaultLimit
	}
	if filter.Limit > escalationMaxLimit {
		filter.Limit = escalationMaxLimit
	}

	rows, err := s.escalations.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	// WaitedSeconds diturunkan di sini, tidak disimpan — seperti `priority` di daftar
	// antrean panggilan. Kolom tersimpan sudah basi sejak baris berikutnya dibaca.
	now := s.clock()
	for i := range rows {
		rows[i].WaitedSeconds = waitedSecondsFor(rows[i], now)
	}
	return rows, nil
}

// waitedSecondsFor menghitung berapa lama sebuah perkara sudah menunggu.
//
// Perkara yang sudah ditutup berhenti menghitung di waktu penutupannya: perkara tahun
// lalu yang terus menghitung akan melaporkan tunggu tiga ratus hari di layar arsip, dan
// angka itu tidak menggambarkan apa pun yang terjadi.
func waitedSecondsFor(esc VideoCallEscalation, now time.Time) int {
	until := now
	if esc.ResolvedAt != nil {
		until = *esc.ResolvedAt
	}
	waited := until.Sub(esc.RaisedAt)
	if waited < 0 {
		return 0
	}
	return int(waited.Seconds())
}

// Update menjalankan satu tindakan Tier 2 atas sebuah perkara: CLAIM atau RESOLVE.
//
// `agent` adalah identitas yang SUDAH diautentikasi middleware, bukan nilai dari body.
// Tanpa itu, kedua penjaga four-eyes di bawah hanya membandingkan satu string payload
// dengan string payload lain yang ditulis pemanggil yang sama.
//
// Urutan pemeriksaannya mengikat:
//
//  1. Bentuk permintaan — tindakan dikenal, dan kelengkapannya sesuai tindakannya.
//  2. Perkaranya ada, dan belum ditutup — 404 dan 409 dibedakan.
//  3. Four-eyes: bukan perkara yang diajukan petugas ini sendiri.
//  4. Pemegang perkara: IN_REVIEW hanya boleh disentuh pemegangnya.
//  5. Baris ditulis — dengan status ada DI DALAM kondisi UPDATE, jadi dua permintaan
//     yang berlomba tidak bisa melewati pemeriksaan di atas.
//  6. BARU SESUDAHNYA langkah sesi nasabah dipindah, dan hanya pada APPROVED.
func (s *EscalationService) Update(
	ctx context.Context,
	escalationID string,
	req UpdateEscalationRequest,
	agent AgentInfo,
	ipAddress, userAgent string,
) (*UpdateEscalationResponse, error) {
	if s.escalations == nil {
		return nil, apperr.ProviderNotConfigured
	}

	escalationID = strings.TrimSpace(escalationID)
	if escalationID == "" || agent.EmployeeID == "" {
		return nil, apperr.ValidationError
	}

	action := strings.ToUpper(strings.TrimSpace(req.Action))
	switch action {
	case EscalationActionClaim, EscalationActionResolve:
	default:
		return nil, apperr.ValidationError
	}

	resolution := strings.ToUpper(strings.TrimSpace(req.Resolution))
	reason := strings.ToUpper(strings.TrimSpace(req.RejectionReason))
	notes := strings.TrimSpace(req.Notes)

	if action == EscalationActionResolve {
		if !ValidEscalationResolution(resolution) {
			return nil, apperr.ValidationError
		}
		// Penolakan Tier 2 memakai enum yang SAMA dengan penolakan Tier 1, dan wajib:
		// alasan penolakan verifikasi identitas dihitung dan dilaporkan, dan dua daftar
		// alasan untuk tindakan yang sama membuat satu laporan harus menjumlahkan dua
		// kategori untuk satu hal.
		if resolution == EscalationResolutionRejected && !ValidRejectionReason(RejectionReason(reason)) {
			return nil, apperr.ValidationError
		}
		// Cermin dari kewajiban `notes` pada NEED_REVIEW: Tier 1 wajib menerangkan supaya
		// Tier 2 tidak mengulang seluruh panggilan, dan Tier 2 wajib menerangkan supaya
		// pemeriksaan berikutnya — atau nasabah yang menyengketakan hasilnya — tahu atas
		// dasar apa rekeningnya akhirnya dibuka atau ditolak.
		if notes == "" {
			return nil, apperr.ValidationError
		}
	}

	esc, err := s.escalations.FindByID(ctx, escalationID)
	if err != nil {
		return nil, fmt.Errorf("find escalation: %w", err)
	}
	if esc == nil {
		return nil, apperr.EscalationNotFound
	}
	if esc.Status == EscalationStatusResolved || esc.Status == EscalationStatusCancelled {
		return nil, apperr.EscalationAlreadyClosed
	}

	// Four-eyes, dan diperiksa pada KEDUA tindakan — bukan hanya pada RESOLVE.
	//
	// NEED_REVIEW adalah pernyataan bahwa petugas itu tidak sanggup memutuskan perkaranya.
	// Membiarkannya memutus sendiri sesudahnya mengubah eskalasi menjadi jalan memutar
	// yang menghasilkan keputusan yang persis sama tanpa diperiksa siapa pun. Dan
	// melarangnya hanya di RESOLVE berarti ia boleh memegang perkaranya lebih dulu, lalu
	// ditolak di langkah terakhir — perkara yang kemudian tertahan atas namanya sampai
	// seseorang menyadarinya.
	if strings.EqualFold(esc.RaisedByAgent, agent.EmployeeID) {
		return nil, apperr.EscalationSelfResolve
	}

	// Perkara yang sedang dipegang orang hanya boleh disentuh pemegangnya. Dua peninjau
	// yang sama-sama menulis keputusan ke satu perkara menghasilkan satu keputusan yang
	// menimpa keputusan lain tanpa jejak bahwa pernah ada dua.
	if esc.Status == EscalationStatusInReview && !strings.EqualFold(esc.ClaimedByAgent, agent.EmployeeID) {
		return nil, apperr.Error{
			Status:  409,
			Code:    "ESCALATION_CLAIMED_BY_OTHER",
			Message: "Perkara ini sedang ditangani petugas lain.",
			Details: map[string]any{"claimed_by_agent": esc.ClaimedByAgent},
		}
	}

	now := s.clock()

	if action == EscalationActionClaim {
		// Mengambil perkara yang sudah dipegang DIRI SENDIRI dijawab apa adanya, bukan
		// 409: layar yang dibuka ulang akan memanggilnya lagi, dan menolaknya di sana
		// hanya memaksa petugas menebak apakah ia masih memegangnya.
		if esc.Status == EscalationStatusInReview {
			return s.respond(ctx, esc, "")
		}

		ok, claimErr := s.escalations.Claim(ctx, escalationID, agent.EmployeeID, now)
		if claimErr != nil {
			return nil, fmt.Errorf("claim escalation: %w", claimErr)
		}
		if !ok {
			// Kondisi di UPDATE yang tidak terpenuhi berarti keadaannya berubah di antara
			// pembacaan dan penulisan. Dibaca ulang supaya jawabannya menyebut keadaan
			// yang SEKARANG, bukan keadaan saat permintaan masuk.
			return nil, s.staleStateErr(ctx, escalationID)
		}

		s.writeAudit(ctx, esc.SessionID, AuditVideoCallEscalationClaimed, agent.EmployeeID,
			map[string]any{
				"escalation_id":    escalationID,
				"escalation_queue": esc.EscalationQueue,
				"raised_by_agent":  esc.RaisedByAgent,
				"waited_seconds":   waitedSecondsFor(*esc, now),
			}, ipAddress, userAgent)

		esc.Status = EscalationStatusInReview
		esc.ClaimedByAgent = agent.EmployeeID
		esc.ClaimedAt = &now
		return s.respond(ctx, esc, "")
	}

	// --- RESOLVE ---

	ok, resErr := s.escalations.Resolve(ctx, escalationID, EscalationResolution{
		ResolvedByAgent:  agent.EmployeeID,
		Resolution:       resolution,
		ResolutionReason: reason,
		Notes:            notes,
	}, now)
	if resErr != nil {
		return nil, fmt.Errorf("resolve escalation: %w", resErr)
	}
	if !ok {
		return nil, s.staleStateErr(ctx, escalationID)
	}

	esc.Status = EscalationStatusResolved
	esc.ResolvedByAgent = agent.EmployeeID
	esc.ResolvedAt = &now
	esc.Resolution = resolution
	esc.ResolutionReason = reason
	esc.ResolutionNotes = notes

	auditDetails := map[string]any{
		"escalation_id":    escalationID,
		"escalation_queue": esc.EscalationQueue,
		"resolution":       resolution,
		"raised_by_agent":  esc.RaisedByAgent,
		"waited_seconds":   waitedSecondsFor(*esc, now),
	}
	if resolution == EscalationResolutionRejected {
		auditDetails["resolution_reason"] = reason
	}
	s.writeAudit(ctx, esc.SessionID, AuditVideoCallEscalationResolved, agent.EmployeeID,
		auditDetails, ipAddress, userAgent)

	// Perkaranya SUDAH tertutup pada titik ini, dan itu disengaja: nasabah yang sesinya
	// kedaluwarsa tetap harus dilepaskan dari tahanan eskalasi. Kalau langkahnya dipindah
	// lebih dulu dan penutupannya gagal, perkaranya tetap menahan nasabah yang langkahnya
	// sudah maju — keadaan yang tidak bisa dijelaskan kepada siapa pun.
	return s.respond(ctx, esc, resolution)
}

// respond merakit jawaban, dan pada APPROVED memindahkan langkah sesi nasabah.
//
// resolution kosong berarti tidak ada keputusan (CLAIM): langkahnya dibaca, tidak diubah.
func (s *EscalationService) respond(
	ctx context.Context, esc *VideoCallEscalation, resolution string,
) (*UpdateEscalationResponse, error) {
	resp := &UpdateEscalationResponse{Escalation: esc}

	if s.sessions == nil {
		return resp, nil
	}

	// FindBySessionID langsung, BUKAN lewat penjaga kedaluwarsa: sesi yang kedaluwarsa
	// tidak boleh menggagalkan penutupan perkaranya. Perkara yang gagal ditutup karena
	// sesinya basi adalah perkara yang menahan nasabahnya selamanya — persis keadaan yang
	// endpoint ini ada untuk mengakhiri.
	session, err := s.sessions.FindBySessionID(ctx, esc.SessionID)
	if err != nil {
		slog.Error("read session after escalation update failed",
			"session_id", esc.SessionID, "escalation_id", esc.EscalationID, "error", err)
		return resp, nil
	}
	if session == nil {
		slog.Warn("escalation session no longer exists",
			"session_id", esc.SessionID, "escalation_id", esc.EscalationID)
		return resp, nil
	}
	resp.CurrentStep = session.CurrentStep

	if resolution != EscalationResolutionApproved {
		return resp, nil
	}
	if session.IsExpired() {
		// Perkaranya tetap tertutup — nasabahnya bebas memulai sesi baru. Yang tidak
		// bisa dilakukan adalah memajukan langkah sesi yang sudah mati.
		slog.Warn("escalation approved for an expired session",
			"session_id", esc.SessionID, "escalation_id", esc.EscalationID)
		return resp, nil
	}

	// Mesin langkahnya yang memutuskan, bukan pemanggil — sama dengan jalur APPROVED di
	// VideoCallService.SubmitResult. Persetujuan atas sesi yang sudah lewat VIDEO_CALL
	// dicatat di perkaranya dan selebihnya dibiarkan.
	if !CanTransition(session.CurrentStep, StepCredentials) {
		slog.Warn("escalation approved for a session past VIDEO_CALL",
			"session_id", esc.SessionID, "current_step", string(session.CurrentStep))
		return resp, nil
	}

	completed := session.StepsCompleted
	completed.VideoCallVerified = true
	if err := s.sessions.UpdateStep(ctx, esc.SessionID, StepCredentials, completed); err != nil {
		return nil, fmt.Errorf("update step after escalation: %w", err)
	}
	if s.cache != nil {
		session.CurrentStep = StepCredentials
		session.StepsCompleted = completed
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update after escalation failed",
				"session_id", esc.SessionID, "error", cacheErr)
		}
	}
	resp.CurrentStep = StepCredentials
	return resp, nil
}

// staleStateErr menjelaskan mengapa sebuah UPDATE berkondisi tidak mengubah baris.
//
// Dibaca ulang dari database, bukan disimpulkan: keadaan yang membuat kondisi gagal
// terjadi SETELAH pembacaan pertama, jadi satu-satunya jawaban yang benar datang dari
// pembacaan kedua. Kegagalan pembacaan itu sendiri dijawab 409 — permintaannya memang
// tidak terjadi, dan itu bagian yang perlu diketahui pemanggil.
func (s *EscalationService) staleStateErr(ctx context.Context, escalationID string) error {
	esc, err := s.escalations.FindByID(ctx, escalationID)
	if err != nil || esc == nil {
		return apperr.EscalationAlreadyClosed
	}
	if esc.Status == EscalationStatusInReview {
		return apperr.Error{
			Status:  409,
			Code:    "ESCALATION_CLAIMED_BY_OTHER",
			Message: "Perkara ini sedang ditangani petugas lain.",
			Details: map[string]any{"claimed_by_agent": esc.ClaimedByAgent},
		}
	}
	return apperr.EscalationAlreadyClosed
}

// writeAudit menulis satu baris onboarding_audit_logs.
//
// Ke jejak SESI NASABAH, bukan cs_audit_events: pertanyaan yang dijawabnya adalah
// "mengapa pembukaan rekening orang ini tertahan, dan siapa yang melepaskannya" — dan itu
// pertanyaan tentang sesi, bukan tentang giliran kerja petugas.
func (s *EscalationService) writeAudit(
	ctx context.Context, sessionID string, eventType AuditEventType,
	employeeID string, details map[string]any, ip, ua string,
) {
	if s.audit == nil {
		return
	}
	entry := &AuditLog{
		// ID WAJIB diisi di sini: OnboardingAuditRepo.Insert mengirim log.ID ke kolom
		// `id` secara eksplisit, jadi entry tanpa ID menulis UUID NOL — dan karena `id`
		// adalah primary key, hanya baris PERTAMA yang lolos. Semua baris audit eskalasi
		// sesudahnya ditolak 23505, dan kegagalannya cuma dicatat di log, tidak
		// menggagalkan permintaan. Akibatnya jejak yang paling perlu dibaca — siapa yang
		// melepaskan pengajuan rekening yang tertahan — hilang tanpa ada yang tahu.
		// Ketahuan dari uji nyata: CLAIMED tertulis, RESOLVED tidak.
		ID:        uuid.New(),
		SessionID: sessionID,
		EventType: eventType,
		Actor:     "agent:" + employeeID,
		Details:   details,
		IPAddress: ip,
		UserAgent: ua,
		CreatedAt: s.clock(),
	}
	if err := s.audit.Insert(ctx, entry); err != nil {
		slog.Error("escalation audit failed",
			"session_id", sessionID, "event_type", string(eventType), "error", err)
	}
}
