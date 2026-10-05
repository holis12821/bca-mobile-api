package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

const (
	avgCallDurationSeconds = 180 // 3 minutes estimated per call

	// signalingTokenTTL was an hour, which is how long a token found in a log
	// line stayed usable. Five minutes single-use is the ceiling §0 proposes and
	// is enough to open the socket: the nasabah connects on joining the queue
	// and waits inside the socket, not outside it. A reconnect joins the queue
	// again and gets a fresh token.
	signalingTokenTTL = 5 * time.Minute

	// activeCallMaxAge adalah umur maksimal panggilan ACTIVE sebelum dianggap basi.
	//
	// Panggilan yang agennya hilang tanpa menyubmit hasil tidak punya apa pun yang
	// membereskannya: ia sudah keluar dari sorted set, jadi tidak ada `queue_update` yang
	// menyentuhnya, dan `idx_vc_session_active_unique` membuat sesinya tidak bisa mengantre
	// lagi. Jalan keluarnya satu — nasabah memanggil ulang `POST /video-call/queue`, dan di
	// sanalah baris basi ini dibebaskan (lihat JoinQueue).
	//
	// 15 menit, bukan 3 (avgCallDurationSeconds): angka ini adalah batas "pasti sudah
	// salah", bukan perkiraan durasi normal. Panggilan e-KYC yang bertele-tele tidak boleh
	// dibatalkan di bawah kaki petugas yang masih bicara.
	activeCallMaxAge = 15 * time.Minute
)

// Alasan pembatalan, masuk ke detail audit. Dikumpulkan di sini supaya tidak ada literal
// yang berserak — satu salah ejaan membuat jejak audit tidak bisa dicari.
const (
	// cancelReasonStale: ACTIVE melewati activeCallMaxAge, dibebaskan saat nasabah
	// mengantre lagi.
	cancelReasonStale = "STALE_ACTIVE_RELEASED"
	// cancelReasonSessionCancelled: nasabah membatalkan sesi onboarding-nya.
	cancelReasonSessionCancelled = "SESSION_CANCELLED"
	// cancelReasonSessionGone: sesinya kedaluwarsa atau sudah dihapus, ketahuan saat CS
	// membuka daftar antrean.
	cancelReasonSessionGone = "SESSION_GONE"
)

// SignalingNotifier mengirim pesan server→client ke socket signaling yang hidup.
//
// Antarmuka, bukan tipe konkret, karena paket `websocket` sudah meng-import paket ini —
// menyebut Hub secara langsung akan membuat import cycle. `*websocket.Hub` memenuhinya
// secara struktural.
//
// Kosongnya bagian ini sebelumnya adalah akar kebuntuan protokol: satu-satunya pesan yang
// pernah dikirim ke nasabah adalah `instruction` yang direlai dari agent, jadi
// `agent_assigned`, `queue_update`, dan `call_ended` yang diwajibkan §5b tidak pernah ada.
// Nasabah menunggu `agent_assigned` untuk membuat SDP offer, jadi panggilannya tidak pernah
// bisa dimulai.
type SignalingNotifier interface {
	SendToNasabah(sessionID string, msg SignalMessage)
}

type VideoCallService struct {
	sessions   SessionRepository
	cache      SessionCache
	videoCalls VideoCallRepository
	queueCache VideoCallQueueCache
	jwt        *crypto.JWTManager
	audit      AuditRepository
	sigBaseURL string // e.g. "wss://signal.bcamobile.id"
	iceServers []ICEServer
	clock      func() time.Time
	notifier   SignalingNotifier
}

type VideoCallServiceConfig struct {
	Sessions         SessionRepository
	Cache            SessionCache
	VideoCalls       VideoCallRepository
	QueueCache       VideoCallQueueCache
	JWTManager       *crypto.JWTManager
	Audit            AuditRepository
	SignalingBaseURL string

	// ICEServers berasal dari environment (STUN_URLS, TURN_URLS, ...). Kredensial
	// TURN milik penyedia dan berganti secara berkala, jadi tidak boleh ditanam
	// di kode — lihat config.WebRTC.
	ICEServers []ICEServer

	Clock func() time.Time // optional; defaults to time.Now

	// Notifier opsional: tanpa itu antrean dan panggilan tetap bekerja, tapi nasabah tidak
	// menerima agent_assigned maupun call_ended — dan tanpa agent_assigned panggilan tidak
	// pernah dimulai. Dibiarkan nil hanya di test yang tidak memeriksa emisi.
	Notifier SignalingNotifier
}

func NewVideoCallService(cfg VideoCallServiceConfig) *VideoCallService {
	baseURL := cfg.SignalingBaseURL
	if baseURL == "" {
		baseURL = "ws://localhost:8080"
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &VideoCallService{
		sessions:   cfg.Sessions,
		cache:      cfg.Cache,
		videoCalls: cfg.VideoCalls,
		queueCache: cfg.QueueCache,
		jwt:        cfg.JWTManager,
		audit:      cfg.Audit,
		sigBaseURL: baseURL,
		iceServers: cfg.ICEServers,
		clock:      clock,
		notifier:   cfg.Notifier,
	}
}

// JoinQueue adds the session to the video call queue.
func (s *VideoCallService) JoinQueue(ctx context.Context, req JoinQueueRequest, ipAddress, userAgent string) (*JoinQueueResponse, error) {
	// 1. Validate session step
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session.CurrentStep != StepVideoCall {
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan VIDEO_CALL.", session.CurrentStep),
		}
	}

	// 2. Check operating hours (06:00-22:00 WIB)
	if !s.isWithinOperatingHours() {
		return nil, apperr.Error{
			Status:  422,
			Code:    "VIDEO_CALL_OUTSIDE_HOURS",
			Message: "Video call hanya tersedia pukul 06:00-22:00 WIB.",
		}
	}

	// 3. Check if already queued
	existing, err := s.videoCalls.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("check existing queue: %w", err)
	}
	// Panggilan ACTIVE yang sudah basi dibebaskan di sini, dan ini satu-satunya tempatnya.
	//
	// Agent yang hilang di tengah panggilan meninggalkan baris ACTIVE yang tidak bisa
	// dibereskan siapa pun: ia sudah ter-ZREM, jadi tidak ada petugas yang melihatnya lagi,
	// dan unique partial index pada (session_id) WHERE status IN ('QUEUED','ACTIVE')
	// menolak baris antrean baru untuk sesi itu. Tanpa pembebasan ini nasabahnya terkurung
	// permanen dan hanya bisa ditolong lewat intervensi manual di database.
	//
	// Sengaja TIDAK memakai worker periodik: repo ini tidak punya satu pun job latar, dan
	// menambahkannya berarti menambah urusan shutdown, kebocoran goroutine di test, serta
	// dua instance yang saling membatalkan panggilan yang sama. Pemulihan yang dijanjikan
	// ATURAN #3 memang berupa nasabah memanggil ulang endpoint ini, jadi di sinilah
	// tempatnya.
	if existing != nil && existing.Status == VCStatusActive && s.isStale(existing) {
		released, relErr := s.videoCalls.Cancel(ctx, existing.QueueID)
		if relErr != nil {
			return nil, fmt.Errorf("release stale video call: %w", relErr)
		}
		if released {
			if remErr := s.queueCache.Remove(ctx, existing.QueueID); remErr != nil {
				slog.Error("remove stale call from queue failed", "queue_id", existing.QueueID, "error", remErr)
			}
			slog.Warn("stale active video call released",
				"queue_id", existing.QueueID,
				"session_id", existing.SessionID,
				"agent_employee_id", existing.AgentEmployeeID,
			)
			s.writeAudit(ctx, req.SessionID, AuditVideoCallEnded, "system", map[string]any{
				"queue_id": existing.QueueID,
				"reason":   cancelReasonStale,
				"agent":    existing.AgentEmployeeID,
			}, ipAddress, userAgent)
		}
		// Baris lamanya sudah bukan penghalang: lanjut ke tiket baru di bawah.
		existing = nil
	}

	if existing != nil && (existing.Status == VCStatusQueued || existing.Status == VCStatusActive) {
		// Already in queue — return current position
		pos, err := s.queueCache.Position(ctx, existing.QueueID)
		if err != nil {
			slog.Error("queue position lookup failed", "queue_id", existing.QueueID, "error", err)
		}

		// Posisi 0 untuk panggilan yang masih QUEUED berarti anggotanya tidak ada di sorted
		// set — Redis-nya hilang, kedaluwarsa, atau ter-flush. Dimasukkan kembali, bukan
		// dibiarkan: `GET /video-call/queued` membaca sorted set itu, jadi nasabah yang
		// hilang dari sana tidak akan pernah terlihat petugas mana pun sementara tiketnya
		// tampak sah di layarnya sendiri. Score-nya waktu masuk yang asli supaya ia kembali
		// ke urutan yang semestinya, bukan ke belakang orang yang datang setelahnya.
		if pos == 0 && existing.Status == VCStatusQueued {
			if addErr := s.queueCache.Add(ctx, existing.QueueID, float64(existing.JoinedAt.UnixMilli())); addErr != nil {
				slog.Error("requeue lost queue member failed", "queue_id", existing.QueueID, "error", addErr)
			} else {
				slog.Warn("queue member was missing from the sorted set, re-added",
					"queue_id", existing.QueueID, "session_id", existing.SessionID)
				if pos, err = s.queueCache.Position(ctx, existing.QueueID); err != nil {
					slog.Error("queue position lookup failed", "queue_id", existing.QueueID, "error", err)
				}
			}
		}

		signalingURL, err := s.buildSignalingURL(req.SessionID, existing.QueueID, RoleNasabah)
		if err != nil {
			return nil, fmt.Errorf("signaling url: %w", err)
		}
		return &JoinQueueResponse{
			QueueID:              existing.QueueID,
			QueueNumber:          existing.QueueNumber,
			Position:             pos,
			EstimatedWaitSeconds: pos * avgCallDurationSeconds,
			OperatingHours:       defaultOperatingHours(),
			SignalingURL:         signalingURL,
			SignalingExpiresIn:   int(signalingTokenTTL.Seconds()),
			ICEServers:           s.iceServers,
		}, nil
	}

	// 4. Generate queue ID and number
	shortID, err := generateShortID()
	if err != nil {
		return nil, fmt.Errorf("generate queue id: %w", err)
	}
	queueID := "q_" + shortID
	seqNum, err := s.queueCache.IncrDailyCounter(ctx)
	if err != nil {
		return nil, fmt.Errorf("incr queue counter: %w", err)
	}
	queueNumber := fmt.Sprintf("A-%03d", seqNum)

	// s.clock(), bukan time.Now(): isWithinOperatingHours sudah memakainya, dan dua sumber
	// waktu di satu service membuat test berjam-palsu tidak konsisten dengan dirinya sendiri.
	now := s.clock().UTC()
	vc := &VideoCall{
		ID:          uuid.New(),
		QueueID:     queueID,
		SessionID:   req.SessionID,
		QueueNumber: queueNumber,
		Status:      VCStatusQueued,
		JoinedAt:    now,
	}

	if err := s.videoCalls.Create(ctx, vc); err != nil {
		return nil, fmt.Errorf("create video call: %w", err)
	}

	// 5. Add to Redis sorted set
	score := float64(now.UnixMilli())
	if err := s.queueCache.Add(ctx, queueID, score); err != nil {
		return nil, fmt.Errorf("add to queue: %w", err)
	}

	// 6. Get position
	pos, err := s.queueCache.Position(ctx, queueID)
	if err != nil {
		slog.Error("queue position lookup failed", "queue_id", queueID, "error", err)
	}

	signalingURL, err := s.buildSignalingURL(req.SessionID, queueID, RoleNasabah)
	if err != nil {
		return nil, fmt.Errorf("signaling url: %w", err)
	}

	// Audit
	s.writeAudit(ctx, req.SessionID, AuditVideoCallQueued, "nasabah:"+session.DeviceID, map[string]any{
		"queue_id":     queueID,
		"queue_number": queueNumber,
		"position":     pos,
	}, ipAddress, userAgent)

	return &JoinQueueResponse{
		QueueID:              queueID,
		QueueNumber:          queueNumber,
		Position:             pos,
		EstimatedWaitSeconds: pos * avgCallDurationSeconds,
		OperatingHours:       defaultOperatingHours(),
		SignalingURL:         signalingURL,
		SignalingExpiresIn:   int(signalingTokenTTL.Seconds()),
		ICEServers:           s.iceServers,
	}, nil
}

// ListQueued mengembalikan panggilan yang menunggu, urut dari yang paling depan.
//
// Tanpa ini sisi CS **tidak punya jalan masuk sama sekali**: `POST /video-call/agent-token`
// mensyaratkan `queue_id`, dan satu-satunya endpoint lain yang menyentuh antrean adalah
// `GET /monitoring` yang hanya mengembalikan `queue_length` — sebuah angka, tanpa satu pun
// pengenal panggilan. Jadi antrean bisa penuh dan tetap tidak ada yang bisa dilayani.
//
// Urutannya diambil dari sorted set Redis, bukan dari `ORDER BY joined_at` di Postgres,
// supaya posisi yang dilihat petugas sama persis dengan yang dilihat nasabah. Dua sumber
// urutan akan menghasilkan dua jawaban yang berbeda saat salah satunya tertinggal.
//
// Daftar ini sekaligus **membersihkan** anggota yang tidak bisa dilayani: yang tidak punya
// rekaman, dan yang sesinya sudah kedaluwarsa atau dibatalkan. Keduanya dulu hanya
// dilewati, dan itu menyisakan dua masalah — `ZRANK` milik nasabah tetap menghitungnya,
// jadi semua yang di belakangnya melihat posisi lebih besar daripada jumlah panggilan yang
// benar-benar ada, dan baris QUEUED-nya tetap menggantung di Postgres selamanya. Anggota
// yang dibuang di sini pasti tidak bisa dilayani: `agent-token` menjawab
// ONBOARDING_NOT_FOUND untuk yang tanpa rekaman, dan sesi yang hilang tidak punya langkah
// berikutnya untuk dituju.
//
// Pembersihan tidak pernah menggagalkan daftar: petugas tetap mendapat antreannya walau
// satu baris gagal dirapikan, dan pemanggilan berikutnya mencoba lagi.
func (s *VideoCallService) ListQueued(ctx context.Context) (*ListQueuedVideoCallsResponse, error) {
	ids, err := s.queueCache.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list queue: %w", err)
	}

	now := s.clock().UTC()
	calls := make([]QueuedVideoCall, 0, len(ids))
	for _, queueID := range ids {
		vc, err := s.videoCalls.FindByQueueID(ctx, queueID)
		if err != nil {
			return nil, fmt.Errorf("find video call %s: %w", queueID, err)
		}
		// Anggota yatim: sorted set menyimpannya tapi rekamannya tidak ada. Dibuang, bukan
		// hanya dilewati — selama ia di sana, ia menggeser posisi semua yang di belakangnya
		// tanpa pernah bisa diambil petugas.
		if vc == nil {
			slog.Warn("queue member without a record, dropping", "queue_id", queueID)
			if remErr := s.queueCache.Remove(ctx, queueID); remErr != nil {
				slog.Error("drop orphan queue member failed", "queue_id", queueID, "error", remErr)
			}
			continue
		}

		// Sesi yang sudah tidak ada tidak bisa dilayani: FindBySessionID menyaring
		// `deleted_at IS NULL`, jadi nil di sini berarti dibatalkan atau tidak pernah ada.
		// Panggilannya dibatalkan supaya barisnya tidak menggantung QUEUED selamanya dan
		// supaya unique index tidak menghalangi sesi lain milik perangkat yang sama.
		session, err := s.sessions.FindBySessionID(ctx, vc.SessionID)
		if err != nil {
			// Kegagalan lookup bukan alasan membuang panggilan yang mungkin sah.
			slog.Error("queue session lookup failed", "queue_id", queueID, "error", err)
		} else if session == nil || session.IsExpired() {
			slog.Warn("queue member whose session is gone, cancelling",
				"queue_id", queueID, "session_id", vc.SessionID)
			s.releaseCall(ctx, vc, cancelReasonSessionGone)
			continue
		}

		// Posisi dihitung dari anggota yang DIPERTAHANKAN, bukan dari indeks daftar awal:
		// yang dibuang di atas sudah keluar dari sorted set, jadi `ZRANK` yang dilihat
		// nasabah sesudah ini sama dengan hitungan di sini. Memakai indeks awal akan
		// membuat angka petugas lebih besar satu untuk setiap anggota yang baru dibuang.
		pos := int64(len(calls) + 1)
		calls = append(calls, QueuedVideoCall{
			QueueID:       vc.QueueID,
			QueueNumber:   vc.QueueNumber,
			SessionID:     vc.SessionID,
			Position:      pos,
			WaitedSeconds: int(now.Sub(vc.JoinedAt).Seconds()),
			Status:        string(vc.Status),
		})
	}

	return &ListQueuedVideoCallsResponse{
		Calls:                calls,
		OperatingHours:       defaultOperatingHours(),
		WithinOperatingHours: s.isWithinOperatingHours(),
	}, nil
}

// SubmitResult handles the CS backend submitting the result of a video call.
//
// `agent` adalah identitas petugas yang **sudah diautentikasi** middleware, bukan nilai
// dari body. Itu yang membuat dua penjaga di bawah berarti: tanpanya, pemeriksaan "petugas
// yang mengirim hasil harus petugas yang mengambil panggilan" hanya membandingkan satu
// string payload dengan string payload lain yang ditulis pemanggil yang sama.
func (s *VideoCallService) SubmitResult(ctx context.Context, req SubmitVideoCallResultRequest, agent AgentInfo, ipAddress, userAgent string) (*SubmitVideoCallResultResponse, error) {
	// 1. Find the video call
	vc, err := s.videoCalls.FindByQueueID(ctx, req.QueueID)
	if err != nil {
		return nil, fmt.Errorf("find video call: %w", err)
	}
	if vc == nil {
		return nil, apperr.OnboardingNotFound
	}
	if vc.SessionID != req.SessionID {
		return nil, apperr.ValidationError
	}

	// 2. Validate result
	result := VideoCallResult(req.Result)
	if result != VCResultApproved && result != VCResultRejected {
		return nil, apperr.ValidationError
	}
	if agent.EmployeeID == "" {
		return nil, apperr.ValidationError
	}

	// 3. Replay guard. A result that has already been recorded is returned as
	// it stands: re-applying it would re-run the step transition below and drag
	// a session that has since reached REVIEW or COMPLETED back to CREDENTIALS.
	if vc.Status == VCStatusCompleted {
		session, err := s.resolveSession(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		slog.Warn("video call result replayed, ignoring",
			"queue_id", req.QueueID,
			"session_id", req.SessionID,
			"stored_result", string(vc.Result),
			"submitted_result", req.Result,
		)
		return &SubmitVideoCallResultResponse{
			SessionID:   req.SessionID,
			Result:      string(vc.Result),
			CurrentStep: session.CurrentStep,
		}, nil
	}

	// 3b. Hasil hanya sah untuk panggilan yang benar-benar berlangsung.
	//
	// QUEUED berarti belum ada petugas yang mengambilnya, CANCELLED berarti sudah
	// dibebaskan — keduanya tidak punya panggilan yang hasilnya bisa dilaporkan. Dulu
	// status apa pun diterima, jadi satu permintaan bisa menyelesaikan panggilan yang
	// belum pernah terjadi dan memindahkan sesinya ke CREDENTIALS tanpa ada verifikasi
	// tatap muka sama sekali.
	//
	// Ini aman justru karena `AgentSignalingURL` sekarang MENOLAK menerbitkan token saat
	// `MarkActive` gagal. Sebelumnya kegagalan itu hanya dicatat, dan desainnya menitipkan
	// pembetulan status ke langkah ini — penjaga seperti ini akan membuang hasil kerja
	// petugas yang panggilannya sah.
	if vc.Status != VCStatusActive {
		return nil, apperr.Error{
			Status:  422,
			Code:    "VIDEO_CALL_NOT_ACTIVE",
			Message: "Panggilan tidak sedang berlangsung.",
		}
	}

	// 3c. Yang melaporkan hasil harus yang mengambil panggilannya.
	//
	// Tanpa ini petugas mana pun bisa menandatangani verifikasi milik orang lain, dan
	// `UpdateResult` bahkan MENIMPA `agent_employee_id` yang sudah tercatat — jadi jejak
	// auditnya ikut berubah menjadi nama pelapor terakhir. Nasabah sudah melihat nama
	// petugas yang asli di `agent_assigned`; rekaman yang menyimpang dari itu membuat
	// audit trail bertentangan dengan apa yang dialami nasabah.
	if vc.AgentEmployeeID != "" && vc.AgentEmployeeID != agent.EmployeeID {
		slog.Warn("video call result submitted by a different agent",
			"queue_id", req.QueueID,
			"assigned_agent", vc.AgentEmployeeID,
			"submitting_agent", agent.EmployeeID,
		)
		return nil, apperr.Error{
			Status:  409,
			Code:    "VIDEO_CALL_AGENT_MISMATCH",
			Message: "Panggilan ini ditangani petugas lain.",
		}
	}

	// 4. Update video call record
	//
	// Nama agent diteruskan dari rekaman, bukan dikirim "" seperti sebelumnya: argumen
	// kelima itu agentName, dan string kosong di sana **menimpa** nama yang baru dicatat
	// saat agent mengambil panggilan. Akibatnya kolom agent_name selalu berakhir kosong,
	// dan `call_ended` kehilangan satu-satunya sumber nama petugasnya.
	//
	// Permintaan §5c sendiri tidak membawa nama — hanya identitas terautentikasi petugas —
	// jadi rekaman panggilan adalah satu-satunya tempat nama itu ada.
	if err := s.videoCalls.UpdateResult(ctx, req.QueueID,
		result, agent.EmployeeID, vc.AgentName,
		req.Notes, req.RecordingID,
		req.KTPShownLive, req.IdentityConfirmed,
		req.CallDurationSeconds,
	); err != nil {
		return nil, fmt.Errorf("update video call result: %w", err)
	}

	// 5. Remove from queue
	if remErr := s.queueCache.Remove(ctx, req.QueueID); remErr != nil {
		slog.Error("remove from video call queue failed", "queue_id", req.QueueID, "error", remErr)
	}

	// 6. If approved, transition session step
	nextStep := StepVideoCall // stay if rejected
	if result == VCResultApproved {
		session, err := s.resolveSession(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		nextStep = session.CurrentStep

		// The step machine decides, not the caller: an approval for a session
		// that has already moved past VIDEO_CALL is recorded on the call record
		// and otherwise left alone.
		if CanTransition(session.CurrentStep, StepCredentials) {
			completed := session.StepsCompleted
			completed.VideoCallVerified = true
			if err := s.sessions.UpdateStep(ctx, req.SessionID, StepCredentials, completed); err != nil {
				return nil, fmt.Errorf("update step: %w", err)
			}
			if s.cache != nil {
				session.CurrentStep = StepCredentials
				session.StepsCompleted = completed
				if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
					slog.Error("cache step update failed", "session_id", req.SessionID, "error", cacheErr)
				}
			}
			nextStep = StepCredentials
		} else {
			slog.Warn("video call approved for a session past VIDEO_CALL",
				"session_id", req.SessionID,
				"current_step", string(session.CurrentStep),
			)
		}
	}

	// Nasabah diberi tahu panggilannya berakhir. Tanpa ini satu-satunya petunjuk yang dia
	// punya adalah socket yang tiba-tiba sunyi, dan langkah berikutnya baru terlihat kalau
	// aplikasinya kebetulan menanyakan sesi lagi.
	s.notifyNasabah(req.SessionID, SignalMessage{
		Type:            SignalCallEnded,
		SessionID:       req.SessionID,
		QueueID:         req.QueueID,
		Result:          req.Result,
		AgentName:       agentDisplayName(vc, agent),
		DurationSeconds: req.CallDurationSeconds,
	})
	s.broadcastQueuePositions(ctx)

	// Audit
	s.writeAudit(ctx, req.SessionID, AuditVideoCallEnded, "agent:"+agent.EmployeeID, map[string]any{
		"queue_id":           req.QueueID,
		"result":             req.Result,
		"ktp_shown_live":     req.KTPShownLive,
		"identity_confirmed": req.IdentityConfirmed,
		"duration_seconds":   req.CallDurationSeconds,
	}, ipAddress, userAgent)

	return &SubmitVideoCallResultResponse{
		SessionID:   req.SessionID,
		Result:      req.Result,
		CurrentStep: nextStep,
	}, nil
}

// buildSignalingURL mints a short-lived signaling token carrying the caller's
// role. The role is in the token, never in the query string, so a nasabah
// cannot connect as the agent side of their own call.
func (s *VideoCallService) buildSignalingURL(sessionID, queueID string, role string) (string, error) {
	if s.jwt == nil {
		return "", fmt.Errorf("signaling requires a JWT manager")
	}
	token, err := s.jwt.GenerateSignalingToken(sessionID, queueID, role, signalingTokenTTL)
	if err != nil {
		return "", fmt.Errorf("generate signaling token: %w", err)
	}
	return fmt.Sprintf("%s/v1/onboarding/video-call/signal?token=%s", s.sigBaseURL, url.QueryEscape(token)), nil
}

// AgentSignalingURL issues an agent-side signaling URL for a queued call. Only
// the internal CS endpoints reach this — see the internal route group.
//
// `agent` sudah diautentikasi middleware; service ini tidak pernah menerima identitas
// petugas dari body.
func (s *VideoCallService) AgentSignalingURL(ctx context.Context, queueID string, agent AgentInfo, ipAddress, userAgent string) (*AgentSignalingResponse, error) {
	if agent.EmployeeID == "" {
		return nil, apperr.ValidationError
	}

	vc, err := s.videoCalls.FindByQueueID(ctx, queueID)
	if err != nil {
		return nil, fmt.Errorf("find video call: %w", err)
	}
	if vc == nil {
		return nil, apperr.OnboardingNotFound
	}
	if vc.Status == VCStatusCompleted || vc.Status == VCStatusCancelled {
		return nil, apperr.Error{
			Status:  422,
			Code:    "VIDEO_CALL_NOT_ACTIVE",
			Message: "Sesi video call sudah selesai.",
		}
	}

	// Satu panggilan, satu petugas.
	//
	// ZREM saat pengambilan memang menyembunyikan panggilan dari daftar, tapi itu
	// mempersempit jendelanya, bukan menutupnya: dua petugas yang menekan tombol hampir
	// bersamaan dua-duanya melihat panggilan yang sama di daftar. Dulu keduanya mendapat
	// token, dan akibatnya tidak kelihatan tapi serius — `Hub.Register` menutup socket
	// petugas pertama saat yang kedua mendaftar, sementara `MarkActive` ber-COALESCE
	// mempertahankan identitas yang PERTAMA. Jadi yang berbicara dengan nasabah adalah
	// petugas kedua, yang tercatat di audit adalah yang pertama, dan nama di layar nasabah
	// adalah nama yang salah.
	//
	// Pengambilan ulang oleh petugas yang SAMA tetap diizinkan: itu jalur menyambung ulang
	// yang sah setelah socketnya putus, dan tokennya memang sekali pakai.
	if vc.Status == VCStatusActive && vc.AgentEmployeeID != "" && vc.AgentEmployeeID != agent.EmployeeID {
		slog.Warn("video call pickup rejected, already taken",
			"queue_id", queueID,
			"assigned_agent", vc.AgentEmployeeID,
			"requesting_agent", agent.EmployeeID,
		)
		return nil, apperr.Error{
			Status:  409,
			Code:    "VIDEO_CALL_ALREADY_TAKEN",
			Message: "Panggilan ini sudah diambil petugas lain.",
		}
	}

	// Status dipindahkan SEBELUM token diterbitkan, dan kegagalannya menolak permintaan.
	//
	// Dulu kegagalan ini hanya dicatat lalu token tetap keluar, dengan alasan statusnya
	// bisa dibereskan saat hasil disubmit. Akibatnya panggilan berjalan tanpa satu pun
	// catatan siapa yang menanganinya — persis lubang yang membuat jejak audit verifikasi
	// identitas tidak bisa dipertanggungjawabkan. Menolak lebih baik: petugas bisa menekan
	// tombolnya lagi, sementara panggilan yang tidak tercatat tidak bisa diperbaiki
	// belakangan.
	if err := s.videoCalls.MarkActive(ctx, queueID, agent.EmployeeID, agent.Name); err != nil {
		return nil, fmt.Errorf("mark video call active: %w", err)
	}

	signalingURL, err := s.buildSignalingURL(vc.SessionID, queueID, RoleAgent)
	if err != nil {
		return nil, err
	}

	// Pemicu seluruh panggilan. Nasabah menunggu pesan ini untuk membuat SDP offer;
	// tanpa itu tidak ada offer, tidak ada answer, dan tidak ada media.
	s.notifyNasabah(vc.SessionID, SignalMessage{
		Type:      SignalAgentAssigned,
		SessionID: vc.SessionID,
		QueueID:   queueID,
		Agent: &AgentInfo{
			Name:       agent.Name,
			EmployeeID: agent.EmployeeID,
		},
	})

	// VIDEO_CALL_STARTED: konstantanya sudah ada sejak migrasi 000010 dan tidak pernah
	// ditulis siapa pun, jadi jejak audit melompat dari VIDEO_CALL_QUEUED langsung ke
	// VIDEO_CALL_ENDED. Untuk verifikasi e-KYC, "petugas mana yang memulai dan kapan"
	// bukan detail yang bisa disimpulkan dari baris akhir — panggilan yang berakhir tanpa
	// hasil tidak meninggalkan baris itu sama sekali.
	s.writeAudit(ctx, vc.SessionID, AuditVideoCallStarted, "agent:"+agent.EmployeeID, map[string]any{
		"queue_id":     queueID,
		"queue_number": vc.QueueNumber,
		"agent_name":   agent.Name,
	}, ipAddress, userAgent)

	// Keluar dari antrean begitu dilayani, supaya posisi yang di belakangnya maju. Tanpa
	// ini seluruh antrean membeku sampai hasilnya disubmit.
	if remErr := s.queueCache.Remove(ctx, queueID); remErr != nil {
		slog.Error("remove from video call queue failed", "queue_id", queueID, "error", remErr)
	}
	s.broadcastQueuePositions(ctx)

	return &AgentSignalingResponse{
		QueueID:      queueID,
		SessionID:    vc.SessionID,
		QueueNumber:  vc.QueueNumber,
		SignalingURL: signalingURL,
		ExpiresAt:    time.Now().UTC().Add(signalingTokenTTL),
		ICEServers:   s.iceServers,
	}, nil
}

// OnNasabahConnected dipanggil handler signaling tepat setelah socket nasabah terdaftar.
//
// Posisi antrean dikirim di sini, bukan ditunggu sampai ada perubahan: nasabah yang baru
// menyambung tidak tahu apa pun tentang antrean, dan `queue_update` berikutnya baru datang
// saat ada panggilan lain yang selesai — bisa menit-menit kemudian.
func (s *VideoCallService) OnNasabahConnected(ctx context.Context, sessionID, queueID string) {
	if s.notifier == nil || queueID == "" {
		return
	}

	// Panggilan yang sudah dilayani harus mengumumkan agennya **lagi**.
	//
	// Token signaling sekali pakai, jadi nasabah yang terputus menyambung ulang dengan
	// tiket baru — dan socket baru itu tidak pernah menerima `agent_assigned` yang dikirim
	// saat agent mengambil panggilan. Client menggantungkan pembuatan SDP offer pada pesan
	// itu, jadi tanpa pengumuman ulang penyambungan ulang di tengah panggilan berakhir diam:
	// tersambung, tapi tidak ada yang memulai negosiasi.
	//
	// Panggilan ACTIVE juga sudah keluar dari antrean, jadi `queue_update` tidak berarti
	// apa pun untuknya.
	vc, err := s.videoCalls.FindByQueueID(ctx, queueID)
	if err != nil {
		slog.Error("video call lookup failed", "queue_id", queueID, "error", err)
		return
	}
	if vc != nil && vc.Status == VCStatusActive {
		s.notifyNasabah(sessionID, SignalMessage{
			Type:      SignalAgentAssigned,
			SessionID: sessionID,
			QueueID:   queueID,
			Agent: &AgentInfo{
				Name:       vc.AgentName,
				EmployeeID: vc.AgentEmployeeID,
			},
		})
		return
	}

	pos, err := s.queueCache.Position(ctx, queueID)
	if err != nil {
		slog.Error("queue position lookup failed", "queue_id", queueID, "error", err)
		return
	}
	s.notifyNasabah(sessionID, SignalMessage{
		Type:                 SignalQueueUpdate,
		SessionID:            sessionID,
		QueueID:              queueID,
		Position:             pos,
		EstimatedWaitSeconds: pos * avgCallDurationSeconds,
	})
}

// broadcastQueuePositions memberi tahu setiap yang masih mengantre posisi barunya.
//
// Dipanggil setiap kali satu panggilan meninggalkan antrean — diambil agent atau selesai —
// karena saat itulah posisi semua yang di belakangnya bergeser. Kegagalan satu nasabah
// tidak menghentikan yang lain: yang luput hanya kehilangan satu pembaruan posisi, dan
// pembaruan berikutnya tetap datang.
func (s *VideoCallService) broadcastQueuePositions(ctx context.Context) {
	if s.notifier == nil {
		return
	}
	ids, err := s.queueCache.List(ctx)
	if err != nil {
		slog.Error("queue list failed", "error", err)
		return
	}
	for i, queueID := range ids {
		vc, err := s.videoCalls.FindByQueueID(ctx, queueID)
		if err != nil || vc == nil {
			slog.Error("queue broadcast lookup failed", "queue_id", queueID, "error", err)
			continue
		}
		pos := int64(i + 1) // ZRANGE sudah urut; posisi 1-based seperti Position()
		s.notifyNasabah(vc.SessionID, SignalMessage{
			Type:                 SignalQueueUpdate,
			SessionID:            vc.SessionID,
			QueueID:              queueID,
			Position:             pos,
			EstimatedWaitSeconds: pos * avgCallDurationSeconds,
		})
	}
}

func (s *VideoCallService) notifyNasabah(sessionID string, msg SignalMessage) {
	if s.notifier == nil {
		return
	}
	s.notifier.SendToNasabah(sessionID, msg)
}

// agentDisplayName memilih nama yang paling berarti untuk `call_ended`.
//
// Rekaman panggilan menang atas identitas pelapor: nama itulah yang sudah dilihat nasabah
// di `agent_assigned`, dan mengubahnya di pesan penutup hanya akan membingungkan. Kalau
// keduanya kosong, ID pegawai lebih berguna daripada string hampa — layar nasabah
// menampilkannya sebagai identitas petugas.
func agentDisplayName(vc *VideoCall, agent AgentInfo) string {
	if vc != nil && vc.AgentName != "" {
		return vc.AgentName
	}
	if agent.Name != "" {
		return agent.Name
	}
	return agent.EmployeeID
}

// isStale melaporkan apakah panggilan ACTIVE sudah melewati activeCallMaxAge.
//
// Umurnya dihitung dari `started_at`. Kalau kosong — yang seharusnya mustahil, karena
// MarkActive selalu mengisinya lewat COALESCE dan kegagalannya sekarang menolak
// pengambilan — `joined_at` dipakai sebagai cadangan. Memilih "tidak basi" di situ akan
// mengurung sesinya selamanya pada baris yang jelas-jelas sudah rusak, dan itu hanya bisa
// ditolong intervensi manual di database.
func (s *VideoCallService) isStale(vc *VideoCall) bool {
	if vc == nil {
		return false
	}
	since := vc.StartedAt
	if since == nil {
		slog.Warn("active video call without started_at; dating it from joined_at",
			"queue_id", vc.QueueID, "session_id", vc.SessionID)
		since = &vc.JoinedAt
	}
	return s.clock().UTC().Sub(*since) > activeCallMaxAge
}

// releaseCall membatalkan satu panggilan dan mengeluarkannya dari antrean.
//
// Jalur pembersihan, jadi tidak ada yang di sini boleh menggagalkan pemanggilnya:
// pembatalan sesi harus tetap berhasil walau Redis sedang tersendat, dan daftar antrean
// harus tetap sampai ke petugas walau satu baris gagal dirapikan.
func (s *VideoCallService) releaseCall(ctx context.Context, vc *VideoCall, reason string) {
	released, err := s.videoCalls.Cancel(ctx, vc.QueueID)
	if err != nil {
		slog.Error("cancel video call failed", "queue_id", vc.QueueID, "reason", reason, "error", err)
		return
	}
	if !released {
		return
	}
	if err := s.queueCache.Remove(ctx, vc.QueueID); err != nil {
		slog.Error("remove cancelled call from queue failed", "queue_id", vc.QueueID, "error", err)
	}
	s.writeAudit(ctx, vc.SessionID, AuditVideoCallEnded, "system", map[string]any{
		"queue_id": vc.QueueID,
		"reason":   reason,
		"agent":    vc.AgentEmployeeID,
	}, "", "")
}

// CancelForSession membatalkan panggilan hidup milik sebuah sesi. Memenuhi
// [VideoCallCanceller].
//
// Dipanggil saat nasabah membatalkan sesi onboarding-nya. Tanpa ini barisnya tetap QUEUED
// dan tetap menjadi anggota sorted set: ia menggeser posisi semua yang di belakangnya dan
// muncul di daftar petugas sebagai panggilan yang bisa diambil, padahal sesinya sudah
// di-soft-delete dan tidak punya langkah berikutnya. `CANCELLED` dulu hanya sebuah
// konstanta yang tidak pernah ditulis siapa pun.
func (s *VideoCallService) CancelForSession(ctx context.Context, sessionID string) error {
	vc, err := s.videoCalls.FindBySessionID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("find video call by session: %w", err)
	}
	if vc == nil || (vc.Status != VCStatusQueued && vc.Status != VCStatusActive) {
		return nil
	}

	s.releaseCall(ctx, vc, cancelReasonSessionCancelled)

	// Posisi yang di belakangnya baru saja maju. Tanpa ini mereka menunggu sampai ada
	// panggilan lain yang selesai untuk mengetahuinya.
	s.broadcastQueuePositions(ctx)
	return nil
}

func (s *VideoCallService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
	if s.cache != nil {
		session, err := s.cache.Get(ctx, sessionID)
		if err != nil {
			slog.Error("cache get session failed", "error", err)
		}
		if session != nil {
			if session.IsExpired() {
				return nil, apperr.OnboardingSessionExpired
			}
			return session, nil
		}
	}
	session, err := s.sessions.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if session == nil {
		return nil, apperr.OnboardingNotFound
	}
	if session.IsExpired() {
		return nil, apperr.OnboardingSessionExpired
	}
	if s.cache != nil {
		_ = s.cache.Store(ctx, session)
	}
	return session, nil
}

func (s *VideoCallService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
	if s.audit == nil {
		return
	}
	entry := &AuditLog{
		ID:        uuid.New(),
		SessionID: sessionID,
		EventType: eventType,
		Actor:     actor,
		Details:   details,
		IPAddress: ip,
		UserAgent: ua,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.audit.Insert(ctx, entry); err != nil {
		slog.Error("video call audit failed", "session_id", sessionID, "error", err)
	}
}

// isWithinOperatingHours checks if the current clock time is between 06:00-22:00 WIB.
func (s *VideoCallService) isWithinOperatingHours() bool {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return true // fallback: allow
	}
	now := s.clock().In(loc)
	hour := now.Hour()
	return hour >= 6 && hour < 22
}

func defaultOperatingHours() OperatingHours {
	return OperatingHours{
		Start:    "06:00",
		End:      "22:00",
		Timezone: "Asia/Jakarta",
	}
}
