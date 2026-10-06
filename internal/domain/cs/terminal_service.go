package cs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// TerminalService mengurus pendaftaran terminal, tiga gerbang kesiapan, dan aktivasi.
type TerminalService struct {
	terminals   TerminalRepository
	supervisors SupervisorRepository
	sessions    AgentSessionRepository
	audit       AuditEventRepository

	clock func() time.Time
}

type TerminalServiceConfig struct {
	Terminals   TerminalRepository
	Supervisors SupervisorRepository
	Sessions    AgentSessionRepository
	Audit       AuditEventRepository

	Clock func() time.Time
}

func NewTerminalService(cfg TerminalServiceConfig) *TerminalService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &TerminalService{
		terminals:   cfg.Terminals,
		supervisors: cfg.Supervisors,
		sessions:    cfg.Sessions,
		audit:       cfg.Audit,
		clock:       clock,
	}
}

// Register mendaftarkan terminal baru. Barisnya SEKALIGUS entri allowlist.
func (s *TerminalService) Register(ctx context.Context, req RegisterTerminalRequest, registeredBy, ip, userAgent string) (*Terminal, error) {
	terminalID := strings.TrimSpace(req.TerminalID)
	workstation := strings.TrimSpace(req.Workstation)
	location := strings.TrimSpace(req.Location)
	if terminalID == "" || workstation == "" || location == "" {
		return nil, apperr.ValidationError
	}
	if len(terminalID) > 40 || len(workstation) > 64 || len(location) > 128 {
		return nil, apperr.ValidationError
	}

	t := &Terminal{
		TerminalID:   terminalID,
		Workstation:  workstation,
		Location:     location,
		Status:       TerminalRegistered,
		RegisteredBy: registeredBy,
		RegisteredAt: s.clock(),
		UpdatedAt:    s.clock(),
	}
	if err := s.terminals.Register(ctx, t); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, EventTerminalRegistered, registeredBy, terminalID, nil, map[string]any{
		"workstation": workstation,
		"location":    location,
	}, ip, userAgent)
	return t, nil
}

// Get mengembalikan terminal, atau 404.
func (s *TerminalService) Get(ctx context.Context, terminalID string) (*Terminal, error) {
	t, err := s.terminals.FindByID(ctx, terminalID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, apperr.TerminalNotFound
	}
	return t, nil
}

// ListSupervisors mengembalikan supervisor aktif di sebuah lokasi, TANPA token mereka.
func (s *TerminalService) ListSupervisors(ctx context.Context, location string) ([]Supervisor, error) {
	if s.supervisors == nil {
		return nil, apperr.ProviderNotConfigured
	}
	return s.supervisors.ListActive(ctx, strings.TrimSpace(location))
}

// AuthorizeSupervisor memverifikasi token dual-control dan MELOLOSKAN gerbang
// SUPERVISOR_AUTH untuk sesi petugas.
//
// Mengembalikan TANDA TERIMA (`authorization_ref`), bukan token. `#BCA-AUTH-9942` di
// dokumen alur adalah rujukan ini — bukan kredensialnya.
func (s *TerminalService) AuthorizeSupervisor(
	ctx context.Context, sess *SessionContext, req SupervisorAuthorizeRequest, ip, userAgent string,
) (*SupervisorAuthorizeResponse, error) {
	supervisorID := strings.TrimSpace(req.SupervisorID)
	if supervisorID == "" || req.Token == "" {
		return nil, apperr.ValidationError
	}
	if s.supervisors == nil {
		return nil, apperr.ProviderNotConfigured
	}

	name, ok, err := s.supervisors.Authenticate(ctx, supervisorID, req.Token)
	if err != nil {
		// Postgres tersendat bukan bukti tokennya salah, dan bukan alasan meluluskannya.
		// Pola yang sama dengan AgentAuth: jangan pernah fail-open di jalur yang
		// menentukan siapa bertanggung jawab.
		return nil, fmt.Errorf("authenticate supervisor: %w", err)
	}
	if !ok {
		s.writeAudit(ctx, EventSupervisorAuthFailed, sess.EmployeeID, sess.TerminalID,
			&sess.SessionID, map[string]any{"supervisor_id": supervisorID}, ip, userAgent)
		return nil, apperr.SupervisorTokenInvalid
	}

	now := s.clock()
	ref, err := newAuthorizationRef()
	if err != nil {
		return nil, fmt.Errorf("generate authorization ref: %w", err)
	}

	rec := &GateRecord{
		SessionID:        sess.SessionID,
		Gate:             GateSupervisorAuth,
		PassedAt:         now,
		ExpiresAt:        now.Add(GateTTL),
		AuthorizationRef: ref,
		SupervisorID:     supervisorID,
	}
	if err := s.terminals.UpsertGate(ctx, rec); err != nil {
		return nil, err
	}
	if err := s.promoteIfReady(ctx, sess, now); err != nil {
		return nil, err
	}

	// actor-nya SUPERVISOR, bukan petugas: yang menandatangani adalah dia, dan jejak
	// dual-control yang mencatat petugas sebagai pelaku tidak membuktikan apa pun.
	s.writeAudit(ctx, EventSupervisorAuthorized, supervisorID, sess.TerminalID,
		&sess.SessionID, map[string]any{
			"authorization_ref": ref,
			"authorized_agent":  sess.EmployeeID,
		}, ip, userAgent)

	return &SupervisorAuthorizeResponse{
		AuthorizationRef: ref,
		SupervisorID:     supervisorID,
		SupervisorName:   name,
		AuthorizedAt:     now,
		ExpiresAt:        rec.ExpiresAt,
	}, nil
}

// RecordHealthcheck mencatat hasil probe perangkat yang dilakukan KLIEN.
//
// Server tidak bisa mengukur kamera atau mikrofon di meja petugas. Yang bisa
// dilakukannya adalah mencatat PERNYATAAN klien beserta waktunya, lalu memakainya
// sebagai gerbang. `passed: false` dicatat juga — percobaan yang gagal adalah justru
// yang perlu terlihat saat petugas mengeluh tidak bisa mulai bertugas.
func (s *TerminalService) RecordHealthcheck(
	ctx context.Context, sess *SessionContext, req HealthcheckRequest, ip, userAgent string,
) error {
	now := s.clock()

	if !req.Passed {
		s.writeAudit(ctx, EventDeviceHealthcheck, sess.EmployeeID, sess.TerminalID,
			&sess.SessionID, map[string]any{
				"passed":  false,
				"details": req.Details,
			}, ip, userAgent)
		return apperr.TerminalNotReady
	}

	rec := &GateRecord{
		SessionID: sess.SessionID,
		Gate:      GateDeviceHealth,
		PassedAt:  now,
		ExpiresAt: now.Add(GateTTL),
		Details:   req.Details,
	}
	if err := s.terminals.UpsertGate(ctx, rec); err != nil {
		return err
	}
	if err := s.promoteIfReady(ctx, sess, now); err != nil {
		return err
	}

	s.writeAudit(ctx, EventDeviceHealthcheck, sess.EmployeeID, sess.TerminalID,
		&sess.SessionID, map[string]any{
			"passed":  true,
			"details": req.Details,
		}, ip, userAgent)
	return nil
}

// AcknowledgePII mencatat pakta integritas data pribadi (SCR-008).
func (s *TerminalService) AcknowledgePII(
	ctx context.Context, sess *SessionContext, req PIIAckRequest, ip, userAgent string,
) error {
	// Harus true secara eksplisit: pakta yang tercatat dari field kosong bukan pakta.
	if !req.Acknowledged {
		return apperr.ValidationError
	}
	version := strings.TrimSpace(req.PactVersion)
	if version == "" {
		// Tanpa versi, jejaknya tidak bisa menjawab "menyetujui APA" setelah teksnya
		// direvisi — dan itu justru pertanyaan yang akan diajukan.
		return apperr.ValidationError
	}

	now := s.clock()
	rec := &GateRecord{
		SessionID: sess.SessionID,
		Gate:      GatePIIAck,
		PassedAt:  now,
		ExpiresAt: now.Add(GateTTL),
		Details:   map[string]any{"pact_version": version},
	}
	if err := s.terminals.UpsertGate(ctx, rec); err != nil {
		return err
	}
	if err := s.promoteIfReady(ctx, sess, now); err != nil {
		return err
	}

	s.writeAudit(ctx, EventPIIAcknowledged, sess.EmployeeID, sess.TerminalID,
		&sess.SessionID, map[string]any{"pact_version": version}, ip, userAgent)
	return nil
}

// Readiness mengembalikan keadaan ketiga gerbang untuk sesi petugas.
//
// `can_activate` dihitung DI SINI, bukan diserahkan klien: Rule 3 adalah aturan server,
// dan klien yang menghitungnya sendiri bisa disunting.
func (s *TerminalService) Readiness(ctx context.Context, sess *SessionContext) (*ReadinessState, error) {
	t, err := s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}

	records, err := s.terminals.ListGates(ctx, sess.SessionID)
	if err != nil {
		return nil, err
	}
	byGate := make(map[Gate]GateRecord, len(records))
	for _, r := range records {
		byGate[r.Gate] = r
	}

	now := s.clock()
	state := &ReadinessState{
		TerminalID:  t.TerminalID,
		Status:      t.Status,
		SessionID:   sess.SessionID.String(),
		Gates:       make([]GateState, 0, len(AllGates)),
		CanActivate: true,
	}

	// Diiterasi dari AllGates, bukan dari baris yang ada: gerbang yang belum pernah
	// dicoba harus muncul sebagai `passed: false`, bukan hilang dari daftar. Layar yang
	// kehilangan satu baris akan terlihat seperti sudah selesai.
	for _, g := range AllGates {
		rec, found := byGate[g]
		gs := GateState{Gate: g}

		switch {
		case !found:
			state.CanActivate = false
		case !rec.ExpiresAt.After(now):
			// Pernah lolos, sudah kedaluwarsa. Dibedakan dari belum pernah lolos: yang
			// pertama diselesaikan dengan mengulang probe, yang kedua mungkin berarti
			// petugas melewatkan satu layar.
			gs.Expired = true
			gs.PassedAt = &rec.PassedAt
			gs.ExpiresAt = &rec.ExpiresAt
			state.CanActivate = false
		default:
			gs.Passed = true
			gs.PassedAt = &rec.PassedAt
			gs.ExpiresAt = &rec.ExpiresAt
			gs.AuthorizationRef = rec.AuthorizationRef
			gs.SupervisorID = rec.SupervisorID
			gs.Details = rec.Details
		}

		state.Gates = append(state.Gates, gs)
	}

	return state, nil
}

// Activate memindahkan terminal ke ONLINE. Menuntut ketiga gerbang lolos (Rule 3).
func (s *TerminalService) Activate(ctx context.Context, sess *SessionContext, ip, userAgent string) (*Terminal, error) {
	state, err := s.Readiness(ctx, sess)
	if err != nil {
		return nil, err
	}
	if !state.CanActivate {
		// Gerbang mana yang kurang disertakan: tanpa itu petugas hanya melihat
		// "belum siap" dan harus menebak layar mana yang harus diulang.
		pending := make([]string, 0, len(state.Gates))
		for _, g := range state.Gates {
			if !g.Passed {
				pending = append(pending, string(g.Gate))
			}
		}
		e := apperr.TerminalNotReady
		e.Details = map[string]any{"pending_gates": pending}
		return nil, e
	}

	now := s.clock()
	if err := s.terminals.Activate(ctx, sess.TerminalID, sess.EmployeeID, now); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, EventTerminalActivated, sess.EmployeeID, sess.TerminalID,
		&sess.SessionID, nil, ip, userAgent)

	return s.Get(ctx, sess.TerminalID)
}

// Deactivate memindahkan terminal ke OFFLINE tanpa menutup sesi petugas.
//
// Dipisah dari logout karena keduanya berbeda: petugas yang istirahat menonaktifkan
// loketnya tanpa menutup gilirannya, dan yang pulang melakukan keduanya.
func (s *TerminalService) Deactivate(ctx context.Context, sess *SessionContext, ip, userAgent string) error {
	now := s.clock()
	changed, err := s.terminals.Deactivate(ctx, sess.TerminalID, now)
	if err != nil {
		return err
	}
	if !changed {
		return apperr.TerminalNotFound
	}

	s.writeAudit(ctx, EventTerminalDeactivated, sess.EmployeeID, sess.TerminalID,
		&sess.SessionID, nil, ip, userAgent)
	return nil
}

// AssertOnline menegakkan Rule 4: hanya petugas di terminal ONLINE yang boleh
// mengambil antrean.
//
// Dipanggil `agent-token`. Sebelum ini Rule 4 adalah hiasan — server tidak tahu apa pun
// tentang terminal, jadi petugas yang melewati layar kesiapan dengan menyunting state
// klien tetap bisa mengambil panggilan.
//
// Terminal harus ONLINE **dan** terikat ke petugas yang memanggil: terminal ONLINE milik
// orang lain tidak memberi kewenangan kepada siapa pun selain pemegangnya.
func (s *TerminalService) AssertOnline(ctx context.Context, terminalID, employeeID string) error {
	t, err := s.terminals.FindByID(ctx, terminalID)
	if err != nil {
		return err
	}
	if t == nil {
		return apperr.TerminalNotFound
	}
	if t.Status != TerminalOnline || t.ActiveAgentID != employeeID {
		return apperr.TerminalNotOnline
	}
	return nil
}

// promoteIfReady memindahkan terminal REGISTERED/OFFLINE → READY begitu ketiga gerbang
// lolos, supaya layar kesiapan bisa menampilkan kemajuan tanpa menunggu aktivasi.
//
// TIDAK memindahkan ke ONLINE: itu tindakan eksplisit petugas (SCR-009), dan loket yang
// menyala sendiri karena gerbang terakhir lolos akan mulai menerima nasabah sebelum
// petugasnya siap menerima.
func (s *TerminalService) promoteIfReady(ctx context.Context, sess *SessionContext, now time.Time) error {
	state, err := s.Readiness(ctx, sess)
	if err != nil {
		return err
	}
	if !state.CanActivate || state.Status == TerminalOnline || state.Status == TerminalReady {
		return nil
	}
	return s.terminals.SetStatus(ctx, sess.TerminalID, TerminalReady, now)
}

func (s *TerminalService) writeAudit(ctx context.Context, eventType, actor, terminalID string, sessionID *uuid.UUID, details map[string]any, ip, userAgent string) {
	writeCSAudit(ctx, s.audit, s.clock(), eventType, actor, terminalID, sessionID, details, ip, userAgent)
}

// newAuthorizationRef menerbitkan rujukan tanda terima otorisasi: BCA-AUTH-XXXXXX.
//
// Dari crypto/rand, bukan dari sequence: ia muncul di layar dan di jejak audit, jadi
// nomor berurutan akan memberi tahu berapa banyak otorisasi yang pernah terjadi.
func newAuthorizationRef() (string, error) {
	raw := make([]byte, 3)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "BCA-AUTH-" + strings.ToUpper(hex.EncodeToString(raw)), nil
}
