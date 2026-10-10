package onboarding

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// Peninjau Tier 2. BERBEDA dari testAgent (CS-1042) dengan sengaja: four-eyes menolak
// petugas yang menutup perkara ajuannya sendiri, jadi setiap test jalur bahagia di sini
// akan berubah jadi test jalur penolakan kalau keduanya disamakan.
var (
	testReviewer      = AgentInfo{EmployeeID: "SPV-3001", Name: "Rina Kusuma"}
	testOtherReviewer = AgentInfo{EmployeeID: "SPV-3002", Name: "Dewi Lestari"}
)

// setupEscalationService memakai jam TETAP supaya waited_seconds bisa diperiksa dengan
// angka pasti — bukan dengan rentang toleransi yang lolos apa pun.
func setupEscalationService() (*EscalationService, *mockSessionRepo, *mockSessionCache, *mockEscalationRepo, *mockAuditRepo, time.Time) {
	sessionRepo := newMockSessionRepo()
	cache := newMockSessionCache()
	escRepo := newMockEscalationRepo()
	audit := &mockAuditRepo{}

	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	svc := NewEscalationService(EscalationServiceConfig{
		Escalations: escRepo,
		Sessions:    sessionRepo,
		Cache:       cache,
		Audit:       audit,
		Clock:       func() time.Time { return now },
	})
	return svc, sessionRepo, cache, escRepo, audit, now
}

// seedEscalation menanam satu perkara terbuka beserta sesi nasabahnya di VIDEO_CALL.
func seedEscalation(
	sessionRepo *mockSessionRepo, cache *mockSessionCache, escRepo *mockEscalationRepo,
	escalationID, sessionID, queue string, raisedAt time.Time,
) *VideoCallEscalation {
	createVCTestSessionWithID(sessionRepo, cache, sessionID)

	esc := &VideoCallEscalation{
		EscalationID:    escalationID,
		SessionID:       sessionID,
		QueueID:         "vq_" + sessionID,
		EscalationQueue: queue,
		Status:          EscalationStatusPending,
		Reason:          "KTP terlihat berbeda dari wajah nasabah",
		RaisedByAgent:   testAgent.EmployeeID,
		RaisedAt:        raisedAt,
	}
	escRepo.byID[escalationID] = esc
	return esc
}

func TestListEscalations_OpenOnly_OldestFirst(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()

	seedEscalation(sessionRepo, cache, escRepo, "esc_baru", "onb_a", EscalationTier2, now.Add(-10*time.Minute))
	seedEscalation(sessionRepo, cache, escRepo, "esc_lama", "onb_b", EscalationTier2, now.Add(-3*time.Hour))

	// Perkara yang sudah ditutup TIDAK boleh ikut tanpa ?status=: bawaannya antrean
	// kerja, bukan arsip.
	closed := seedEscalation(sessionRepo, cache, escRepo, "esc_tutup", "onb_c", EscalationTier2, now.Add(-5*time.Hour))
	closed.Status = EscalationStatusResolved

	items, err := svc.List(ctx, ListEscalationsFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 open escalations, got %d", len(items))
	}
	if items[0].EscalationID != "esc_lama" {
		t.Errorf("antrean kerja harus terlama dulu, got %s", items[0].EscalationID)
	}

	// waited_seconds diturunkan dari raised_at dan jam service, bukan disimpan.
	if items[0].WaitedSeconds != 3*3600 {
		t.Errorf("waited_seconds = %d, want %d", items[0].WaitedSeconds, 3*3600)
	}
	if items[1].WaitedSeconds != 600 {
		t.Errorf("waited_seconds = %d, want 600", items[1].WaitedSeconds)
	}
}

func TestListEscalations_FilterByQueueAndStatus(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()

	seedEscalation(sessionRepo, cache, escRepo, "esc_t2", "onb_a", EscalationTier2, now.Add(-time.Hour))
	seedEscalation(sessionRepo, cache, escRepo, "esc_fraud", "onb_b", EscalationFraud, now.Add(-time.Hour))

	items, err := svc.List(ctx, ListEscalationsFilter{Queue: EscalationFraud})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].EscalationID != "esc_fraud" {
		t.Fatalf("filter antrean tidak diterapkan: %+v", items)
	}

	// Status RESOLVED mengembalikan arsip, bukan antrean kerja.
	items, err = svc.List(ctx, ListEscalationsFilter{Status: EscalationStatusResolved})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("belum ada perkara RESOLVED, got %d", len(items))
	}
}

// Nilai penyaring yang salah ketik DITOLAK, bukan dijawab daftar kosong: nol perkara
// karena salah ketik tidak bisa dibedakan dari antrean yang memang bersih.
func TestListEscalations_UnknownFilterValuesRejected(t *testing.T) {
	svc, _, _, _, _, _ := setupEscalationService()
	ctx := context.Background()

	for name, tc := range map[string]struct {
		filter   ListEscalationsFilter
		wantCode string
	}{
		"antrean tak dikenal": {ListEscalationsFilter{Queue: "TIER_9"}, "ESCALATION_QUEUE_UNKNOWN"},
		"status tak dikenal":  {ListEscalationsFilter{Status: "IN_REVIEWS"}, "ESCALATION_STATUS_UNKNOWN"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.List(ctx, tc.filter)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if got := apperr.From(err).Code; got != tc.wantCode {
				t.Errorf("code = %s, want %s", got, tc.wantCode)
			}
		})
	}
}

func TestListEscalations_NoRepoRefuses(t *testing.T) {
	svc := NewEscalationService(EscalationServiceConfig{})

	// 503, bukan daftar kosong: daftar kosong terbaca sebagai "tidak ada perkara yang
	// menunggu", dan perkara yang menahan nasabah lalu tidak dikerjakan siapa pun.
	_, err := svc.List(context.Background(), ListEscalationsFilter{})
	if got := apperr.From(err).Code; got != apperr.ProviderNotConfigured.Code {
		t.Fatalf("code = %s, want %s", got, apperr.ProviderNotConfigured.Code)
	}
}

func TestClaimEscalation_PendingToInReview(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

	resp, err := svc.Update(ctx, "esc_1",
		UpdateEscalationRequest{Action: EscalationActionClaim},
		testReviewer, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Escalation.Status != EscalationStatusInReview {
		t.Errorf("status = %s, want IN_REVIEW", resp.Escalation.Status)
	}
	if resp.Escalation.ClaimedByAgent != testReviewer.EmployeeID {
		t.Errorf("claimed_by_agent = %q, want %q",
			resp.Escalation.ClaimedByAgent, testReviewer.EmployeeID)
	}
	// Memegang perkara BUKAN memutuskannya: langkah nasabah tidak boleh bergerak.
	if resp.CurrentStep != StepVideoCall {
		t.Errorf("current_step = %s, want VIDEO_CALL", resp.CurrentStep)
	}
}

// Mengambil perkara yang sudah dipegang DIRI SENDIRI dijawab apa adanya: layar yang
// dibuka ulang akan memanggilnya lagi, dan 409 di sana hanya memaksa petugas menebak
// apakah ia masih memegangnya.
func TestClaimEscalation_IdempotentForHolder(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

	req := UpdateEscalationRequest{Action: EscalationActionClaim}
	if _, err := svc.Update(ctx, "esc_1", req, testReviewer, "127.0.0.1", "test"); err != nil {
		t.Fatalf("claim pertama gagal: %v", err)
	}
	resp, err := svc.Update(ctx, "esc_1", req, testReviewer, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("claim ulang oleh pemegangnya harus lolos, got %v", err)
	}
	if resp.Escalation.Status != EscalationStatusInReview {
		t.Errorf("status = %s, want IN_REVIEW", resp.Escalation.Status)
	}
}

func TestClaimEscalation_HeldByOtherRejected(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

	req := UpdateEscalationRequest{Action: EscalationActionClaim}
	if _, err := svc.Update(ctx, "esc_1", req, testReviewer, "127.0.0.1", "test"); err != nil {
		t.Fatalf("claim pertama gagal: %v", err)
	}

	_, err := svc.Update(ctx, "esc_1", req, testOtherReviewer, "127.0.0.1", "test")
	if err == nil {
		t.Fatal("peninjau kedua harus ditolak")
	}
	appErr := apperr.From(err)
	if appErr.Code != "ESCALATION_CLAIMED_BY_OTHER" {
		t.Errorf("code = %s, want ESCALATION_CLAIMED_BY_OTHER", appErr.Code)
	}
	if appErr.Status != 409 {
		t.Errorf("status = %d, want 409", appErr.Status)
	}
}

// Four-eyes, dan diperiksa pada KEDUA tindakan. NEED_REVIEW adalah pernyataan bahwa
// petugas itu tidak sanggup memutuskan; membiarkannya memutus sendiri sesudahnya
// mengubah eskalasi menjadi jalan memutar tanpa pemeriksaan.
func TestUpdateEscalation_RaiserCannotActOnOwnCase(t *testing.T) {
	ctx := context.Background()

	for name, req := range map[string]UpdateEscalationRequest{
		"claim": {Action: EscalationActionClaim},
		"resolve": {
			Action:     EscalationActionResolve,
			Resolution: EscalationResolutionApproved,
			Notes:      "Dokumen sudah diperiksa ulang",
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
			seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

			// testAgent adalah pengaju perkaranya.
			_, err := svc.Update(ctx, "esc_1", req, testAgent, "127.0.0.1", "test")
			if err == nil {
				t.Fatal("pengaju harus ditolak")
			}
			if got := apperr.From(err).Code; got != apperr.EscalationSelfResolve.Code {
				t.Errorf("code = %s, want %s", got, apperr.EscalationSelfResolve.Code)
			}
		})
	}
}

func TestResolveEscalation_ApprovedAdvancesToCredentials(t *testing.T) {
	svc, sessionRepo, cache, escRepo, audit, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-2*time.Hour))

	resp, err := svc.Update(ctx, "esc_1", UpdateEscalationRequest{
		Action:     EscalationActionResolve,
		Resolution: EscalationResolutionApproved,
		Notes:      "Identitas cocok setelah pemeriksaan dokumen kedua",
	}, testReviewer, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Escalation.Status != EscalationStatusResolved {
		t.Errorf("status = %s, want RESOLVED", resp.Escalation.Status)
	}
	if resp.Escalation.ResolvedByAgent != testReviewer.EmployeeID {
		t.Errorf("resolved_by_agent = %q", resp.Escalation.ResolvedByAgent)
	}
	if resp.CurrentStep != StepCredentials {
		t.Fatalf("current_step = %s, want CREDENTIALS", resp.CurrentStep)
	}

	// Langkahnya benar-benar tersimpan, bukan hanya dilaporkan di response.
	session, _ := sessionRepo.FindBySessionID(ctx, "onb_a")
	if session.CurrentStep != StepCredentials {
		t.Errorf("sesi tersimpan di %s, want CREDENTIALS", session.CurrentStep)
	}
	if !session.StepsCompleted.VideoCallVerified {
		t.Error("video_call_verified harus true setelah persetujuan Tier 2")
	}
	// Cache ikut diperbarui; entri basi akan membuat nasabah dikirim balik ke VIDEO_CALL.
	if cached, _ := cache.Get(ctx, "onb_a"); cached.CurrentStep != StepCredentials {
		t.Errorf("cache di %s, want CREDENTIALS", cached.CurrentStep)
	}

	// Jejaknya ke onboarding_audit_logs, ber-kunci session_id nasabah.
	logs, _ := audit.FindBySessionID(ctx, "onb_a")
	var found *AuditLog
	for _, l := range logs {
		if l.EventType == AuditVideoCallEscalationResolved {
			found = l
		}
	}
	if found == nil {
		t.Fatal("VIDEO_CALL_ESCALATION_RESOLVED tidak tercatat")
	}
	if found.Actor != "agent:"+testReviewer.EmployeeID {
		t.Errorf("actor = %q", found.Actor)
	}
	if found.Details["resolution"] != EscalationResolutionApproved {
		t.Errorf("details.resolution = %v", found.Details["resolution"])
	}
	if found.Details["waited_seconds"] != 2*3600 {
		t.Errorf("details.waited_seconds = %v, want %d", found.Details["waited_seconds"], 2*3600)
	}
}

func TestResolveEscalation_RejectedStaysAtVideoCall(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

	resp, err := svc.Update(ctx, "esc_1", UpdateEscalationRequest{
		Action:          EscalationActionResolve,
		Resolution:      EscalationResolutionRejected,
		RejectionReason: string(RejectIdentityMismatch),
		Notes:           "Wajah dan e-KTP tidak cocok, dokumen diduga milik orang lain",
	}, testReviewer, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepVideoCall {
		t.Errorf("current_step = %s, want VIDEO_CALL", resp.CurrentStep)
	}
	if resp.Escalation.ResolutionReason != string(RejectIdentityMismatch) {
		t.Errorf("resolution_reason = %q", resp.Escalation.ResolutionReason)
	}
	session, _ := sessionRepo.FindBySessionID(ctx, "onb_a")
	if session.CurrentStep != StepVideoCall {
		t.Errorf("sesi tersimpan di %s, want VIDEO_CALL", session.CurrentStep)
	}
}

// Penolakan WAJIB beralasan ber-enum, dan penutupan WAJIB berketerangan. Keduanya juga
// dijaga CHECK di migrasi 000041/000038; yang di service ada supaya penolakannya 422
// dengan sebab yang jelas, bukan 500 dari pelanggaran constraint.
func TestResolveEscalation_IncompleteDecisionsRejected(t *testing.T) {
	ctx := context.Background()

	for name, req := range map[string]UpdateEscalationRequest{
		"tanpa resolution": {Action: EscalationActionResolve, Notes: "sudah diperiksa"},
		"resolution tak dikenal": {
			Action: EscalationActionResolve, Resolution: "ESCALATE_AGAIN", Notes: "sudah diperiksa",
		},
		"REJECTED tanpa alasan ber-enum": {
			Action: EscalationActionResolve, Resolution: EscalationResolutionRejected,
			Notes: "tidak cocok",
		},
		"REJECTED dengan alasan teks bebas": {
			Action: EscalationActionResolve, Resolution: EscalationResolutionRejected,
			RejectionReason: "ktp blur", Notes: "tidak terbaca",
		},
		"APPROVED tanpa keterangan": {
			Action: EscalationActionResolve, Resolution: EscalationResolutionApproved,
		},
		"keterangan hanya spasi": {
			Action: EscalationActionResolve, Resolution: EscalationResolutionApproved, Notes: "   ",
		},
		"action tak dikenal": {Action: "CANCEL"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
			seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

			_, err := svc.Update(ctx, "esc_1", req, testReviewer, "127.0.0.1", "test")
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if got := apperr.From(err).Code; got != apperr.ValidationError.Code {
				t.Errorf("code = %s, want %s", got, apperr.ValidationError.Code)
			}

			// Perkaranya tidak boleh tersentuh oleh permintaan yang ditolak.
			if escRepo.byID["esc_1"].Status != EscalationStatusPending {
				t.Errorf("status berubah menjadi %s", escRepo.byID["esc_1"].Status)
			}
		})
	}
}

func TestUpdateEscalation_NotFoundAndAlreadyClosed(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()

	resolved := seedEscalation(sessionRepo, cache, escRepo, "esc_tutup", "onb_a", EscalationTier2, now.Add(-time.Hour))
	resolved.Status = EscalationStatusResolved

	req := UpdateEscalationRequest{
		Action: EscalationActionResolve, Resolution: EscalationResolutionApproved,
		Notes: "sudah diperiksa",
	}

	// 404 dan 409 DIBEDAKAN: yang pertama berarti escalation_id-nya salah, yang kedua
	// berarti daftar di layar Tier 2 sudah basi dan harus dimuat ulang.
	_, err := svc.Update(ctx, "esc_tidak_ada", req, testReviewer, "127.0.0.1", "test")
	if got := apperr.From(err).Code; got != apperr.EscalationNotFound.Code {
		t.Errorf("code = %s, want %s", got, apperr.EscalationNotFound.Code)
	}

	_, err = svc.Update(ctx, "esc_tutup", req, testReviewer, "127.0.0.1", "test")
	if got := apperr.From(err).Code; got != apperr.EscalationAlreadyClosed.Code {
		t.Errorf("code = %s, want %s", got, apperr.EscalationAlreadyClosed.Code)
	}
}

// Sesi kedaluwarsa TIDAK menggagalkan penutupan perkara.
//
// Perkara yang gagal ditutup karena sesinya basi adalah perkara yang menahan nasabahnya
// selamanya — persis keadaan yang endpoint ini ada untuk mengakhiri. Yang tidak boleh
// terjadi adalah langkah sesi mati ikut dimajukan.
func TestResolveEscalation_ExpiredSessionStillClosesCase(t *testing.T) {
	svc, sessionRepo, cache, escRepo, _, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

	expired := sessionRepo.sessions["onb_a"]
	expired.ExpiresAt = time.Now().Add(-time.Hour)

	resp, err := svc.Update(ctx, "esc_1", UpdateEscalationRequest{
		Action:     EscalationActionResolve,
		Resolution: EscalationResolutionApproved,
		Notes:      "Identitas cocok, tapi sesinya sudah kedaluwarsa",
	}, testReviewer, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("penutupan perkara harus tetap berhasil, got %v", err)
	}
	if resp.Escalation.Status != EscalationStatusResolved {
		t.Errorf("status = %s, want RESOLVED", resp.Escalation.Status)
	}
	if sessionRepo.sessions["onb_a"].CurrentStep != StepVideoCall {
		t.Errorf("langkah sesi kedaluwarsa tidak boleh maju, got %s",
			sessionRepo.sessions["onb_a"].CurrentStep)
	}
}

// Inilah alasan seluruh fase ini ada: sebelum perkaranya ditutup, nasabah ditolak
// VIDEO_CALL_UNDER_REVIEW setiap kali ia mencoba mengantre — selamanya, karena tidak ada
// satu pun jalur yang menutupnya. Sesudah ditolak Tier 2 ia boleh mengantre lagi.
func TestResolveEscalation_RejectedReleasesQueueGuard(t *testing.T) {
	vcSvc, sessionRepo, cache, _, escRepo, audit := setupVCServiceEscalated()
	ctx := context.Background()

	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	escSvc := NewEscalationService(EscalationServiceConfig{
		Escalations: escRepo,
		Sessions:    sessionRepo,
		Cache:       cache,
		Audit:       audit,
		Clock:       func() time.Time { return now },
	})

	sessionID := createVCTestSessionWithID(sessionRepo, cache, "onb_guard")
	escRepo.byID["esc_guard"] = &VideoCallEscalation{
		EscalationID:    "esc_guard",
		SessionID:       sessionID,
		QueueID:         "vq_guard",
		EscalationQueue: EscalationTier2,
		Status:          EscalationStatusPending,
		Reason:          "perlu tinjauan Tier 2",
		RaisedByAgent:   testAgent.EmployeeID,
		RaisedAt:        now.Add(-time.Hour),
	}

	// Sebelum: tertahan.
	_, err := vcSvc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test")
	if got := apperr.From(err).Code; got != apperr.VideoCallUnderReview.Code {
		t.Fatalf("code = %s, want %s", got, apperr.VideoCallUnderReview.Code)
	}

	if _, err := escSvc.Update(ctx, "esc_guard", UpdateEscalationRequest{
		Action:          EscalationActionResolve,
		Resolution:      EscalationResolutionRejected,
		RejectionReason: string(RejectIncompleteInformation),
		Notes:           "Nasabah tidak bisa dihubungi, perlu panggilan ulang",
	}, testReviewer, "127.0.0.1", "test"); err != nil {
		t.Fatalf("resolve gagal: %v", err)
	}

	// Sesudah: boleh mengantre lagi. Hasil akhir yang tidak bisa diperbaiki sama sekali
	// bukan hasil verifikasi, ia pemblokiran permanen tanpa jalan banding.
	if _, err := vcSvc.JoinQueue(ctx, JoinQueueRequest{SessionID: sessionID}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("nasabah masih tertahan setelah perkaranya ditutup: %v", err)
	}
}

// Setiap baris audit WAJIB ber-ID sendiri, dan ini regresi dari bug yang nyata.
//
// OnboardingAuditRepo.Insert mengirim log.ID ke kolom `id` secara eksplisit, dan `id`
// adalah primary key. Entry yang lahir tanpa ID menulis UUID NOL, jadi hanya baris
// PERTAMA yang lolos — setiap baris audit eskalasi sesudahnya ditolak 23505, dan
// kegagalannya hanya dicatat di log tanpa menggagalkan permintaan. Ketahuan dari uji
// langsung ke server: CLAIMED tertulis, RESOLVED hilang tanpa jejak.
func TestEscalationAudit_RowsCarryDistinctIDs(t *testing.T) {
	svc, sessionRepo, cache, escRepo, audit, now := setupEscalationService()
	ctx := context.Background()
	seedEscalation(sessionRepo, cache, escRepo, "esc_1", "onb_a", EscalationTier2, now.Add(-time.Hour))

	if _, err := svc.Update(ctx, "esc_1",
		UpdateEscalationRequest{Action: EscalationActionClaim},
		testReviewer, "127.0.0.1", "test"); err != nil {
		t.Fatalf("claim gagal: %v", err)
	}
	if _, err := svc.Update(ctx, "esc_1", UpdateEscalationRequest{
		Action:     EscalationActionResolve,
		Resolution: EscalationResolutionApproved,
		Notes:      "Identitas cocok",
	}, testReviewer, "127.0.0.1", "test"); err != nil {
		t.Fatalf("resolve gagal: %v", err)
	}

	logs, _ := audit.FindBySessionID(ctx, "onb_a")
	if len(logs) != 2 {
		t.Fatalf("mau 2 baris audit, dapat %d", len(logs))
	}

	seen := map[string]bool{}
	for _, l := range logs {
		if l.ID == uuid.Nil {
			t.Fatalf("%s ber-ID nol: baris kedua akan ditolak primary key", l.EventType)
		}
		if seen[l.ID.String()] {
			t.Fatalf("ID audit terulang: %s", l.ID)
		}
		seen[l.ID.String()] = true
	}
}
