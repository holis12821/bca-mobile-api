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
	"github.com/holis12821/bca-mobile-api/internal/pkg/pinrule"
)

// TerminalService mengurus pendaftaran terminal, tiga gerbang kesiapan, dan aktivasi.
type TerminalService struct {
	terminals   TerminalRepository
	supervisors SupervisorRepository
	sessions    AgentSessionRepository
	audit       AuditEventRepository

	// creds dan hasher hanya dipakai jalur kunci meja: PIN-nya milik PETUGAS, tapi yang
	// terkunci adalah TERMINAL, jadi keduanya bertemu di service ini. Nil membuat jalur
	// kunci meja menolak, bukan panic.
	creds  AgentCredentialRepository
	hasher PasswordHasher

	clock func() time.Time
}

type TerminalServiceConfig struct {
	Terminals   TerminalRepository
	Supervisors SupervisorRepository
	Sessions    AgentSessionRepository
	Audit       AuditEventRepository

	Creds  AgentCredentialRepository
	Hasher PasswordHasher

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
		creds:       cfg.Creds,
		hasher:      cfg.Hasher,
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
	// TerminalLocked ikut dikecualikan: loket terkunci sudah lewat aktivasi, dan
	// menurunkannya ke READY akan menabrak CHECK cs_terminals_locked_consistent —
	// locked_at-nya masih terisi. Petugas yang mengulang healthcheck dari balik tirai
	// tidak sedang meminta loketnya dibuka.
	if !state.CanActivate || state.Status == TerminalOnline || state.Status == TerminalReady ||
		state.Status == TerminalLocked {
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

// --- Kunci meja petugas (clean desk policy) ---
//
// Apa yang DIJAMIN jalur ini: layar petugas berhenti menampilkan video, nama, dan NIK
// nasabah; loket berhenti menerima panggilan baru; ada baris audit yang mencatat kapan
// dan oleh siapa.
//
// Apa yang TIDAK dijaminnya: kerahasiaan data terhadap orang yang menguasai terminal
// fisiknya. Siapa pun yang memegang PC-nya bisa menutup aplikasi atau membaca memorinya.
// Itu pekerjaan kebijakan perangkat — screen lock OS, disk encryption — bukan API ini.
// Jangan menulis kalimat di response atau dokumen yang terbaca sebagai janji lebih besar.

// LockDesk menurunkan tirai: terminal ONLINE → LOCKED.
//
// Tiga hal yang SENGAJA tidak terjadi di sini, dan ketiganya diuji:
//
//  1. Panggilan video yang sedang berjalan TIDAK diputus. Petugas mengunci layar karena
//     ada orang lain mendekat, bukan karena ingin memutus nasabah yang sedang
//     diverifikasi. onboarding_video_calls tidak disentuh, hub signaling tidak ditutup,
//     dan `call_ended` tidak dikirim.
//  2. Sesi petugas tidak diperpanjang dan tidak dihentikan. expires_at berjalan terus
//     selama terkunci; giliran yang habis di balik tirai berakhir seperti biasa.
//  3. last_seen_at tidak disentuh. Loket terkunci adalah loket yang tidak ditunggui, dan
//     menandainya "baru terlihat" tiap kali tirainya diperiksa akan membuat deteksi sesi
//     terlantar buta.
//
// Antreannya berhenti mengalir tanpa kode tambahan: AssertOnline menuntut status ==
// ONLINE persis, dan dashboard menghitung can_take_calls dari pembacaan yang sama.
func (s *TerminalService) LockDesk(
	ctx context.Context, sess *SessionContext, req LockDeskRequest, ip, userAgent string,
) (*DeskLockState, error) {
	if s.creds == nil || s.hasher == nil {
		return nil, apperr.ProviderNotConfigured
	}

	reason := req.Reason
	if reason == "" {
		// Klien memanggil ini dari dua jalur — tombol dan pintasan papan tik — dan yang
		// satu tidak selalu menyebut alasannya.
		reason = LockReasonAgent
	}
	// FAILED_UNLOCK sengaja TIDAK diterima dari klien: ia hanya boleh lahir dari tiga
	// kali PIN salah, dan loket yang bisa menyatakan dirinya beku atas permintaan sendiri
	// adalah loket yang bisa memanggil supervisor tanpa pernah salah mengetik.
	if reason != LockReasonAgent && reason != LockReasonIdle {
		return nil, apperr.ValidationError
	}

	rec, err := s.creds.FindDeskPIN(ctx, sess.EmployeeID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, apperr.AgentCredentialInvalid
	}
	if rec.DeskPINHash == "" {
		// Mengunci tanpa punya PIN berarti mengunci diri sendiri di luar: tidak ada yang
		// bisa diketik untuk membukanya, dan jalan keluarnya hanya lewat supervisor.
		return nil, apperr.AgentDeskPINNotSet
	}

	t, err := s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}

	// Idempoten. Mengunci loket yang sudah terkunci menjawab keadaan yang sama, bukan
	// 409: dua jalur klien yang berbalapan bukan galat, dan layar kunci yang menampilkan
	// pesan error saat berhasil terkunci akan membuat petugas menekan tombolnya lagi.
	if t.Status == TerminalLocked {
		return s.deskLockState(t, rec, nil), nil
	}
	if t.Status != TerminalOnline || t.ActiveAgentID != sess.EmployeeID {
		return nil, apperr.TerminalNotOnline
	}

	now := s.clock()
	locked, err := s.terminals.Lock(ctx, sess.TerminalID, sess.EmployeeID, reason, now)
	if err != nil {
		return nil, err
	}
	if !locked {
		// Keadaannya berubah di antara SELECT dan UPDATE. Dibaca ulang, bukan ditebak:
		// yang menang balapan mungkin permintaan kunci kedua dari klien yang sama.
		t, err = s.Get(ctx, sess.TerminalID)
		if err != nil {
			return nil, err
		}
		if t.Status == TerminalLocked {
			return s.deskLockState(t, rec, nil), nil
		}
		return nil, apperr.TerminalNotOnline
	}

	s.writeAudit(ctx, EventDeskLocked, sess.EmployeeID, sess.TerminalID, &sess.SessionID,
		map[string]any{"reason": string(reason)}, ip, userAgent)

	t, err = s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}
	return s.deskLockState(t, rec, nil), nil
}

// UnlockDesk mengangkat tirai dengan PIN meja: LOCKED → ONLINE.
//
// Kembali ke ONLINE, bukan READY: ketiga gerbang kesiapan tidak pernah batal oleh
// penguncian, jadi tidak ada yang perlu diulang.
func (s *TerminalService) UnlockDesk(
	ctx context.Context, sess *SessionContext, req UnlockDeskRequest, ip, userAgent string,
) (*DeskLockState, error) {
	if s.creds == nil || s.hasher == nil {
		return nil, apperr.ProviderNotConfigured
	}
	// PIN kosong ditolak SEBELUM dihitung sebagai percobaan: sebuah klien yang mengirim
	// body kosong karena bug tidak boleh bisa membekukan loket dalam tiga permintaan.
	if strings.TrimSpace(req.PIN) == "" {
		return nil, apperr.ValidationError
	}

	rec, err := s.creds.FindDeskPIN(ctx, sess.EmployeeID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, apperr.AgentCredentialInvalid
	}

	t, err := s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}
	// Terkunci oleh ORANG LAIN dijawab sama dengan tidak terkunci: petugas yang bukan
	// pemegangnya tidak perlu tahu keadaan tirai loket orang.
	if t.Status != TerminalLocked || t.ActiveAgentID != sess.EmployeeID {
		return nil, apperr.TerminalNotLocked
	}
	if rec.FailedUnlockAttempts >= MaxUnlockAttempts {
		return nil, apperr.TerminalDeskFrozen
	}
	if rec.DeskPINHash == "" {
		return nil, apperr.AgentDeskPINNotSet
	}

	now := s.clock()
	ok, err := s.hasher.Verify(ctx, req.PIN, rec.DeskPINHash)
	if err != nil {
		// Argon2id yang gagal bukan bukti PIN-nya salah, dan bukan alasan menaikkan
		// penghitung yang membekukan loket.
		return nil, fmt.Errorf("verify desk pin: %w", err)
	}
	if !ok {
		return nil, s.recordUnlockFailure(ctx, sess, now, ip, userAgent)
	}

	unlocked, err := s.terminals.Unlock(ctx, sess.TerminalID, sess.EmployeeID, now)
	if err != nil {
		return nil, err
	}
	if !unlocked {
		return nil, apperr.TerminalNotLocked
	}
	if err := s.creds.ClearUnlockFailures(ctx, sess.EmployeeID); err != nil {
		return nil, err
	}

	return s.finishUnlock(ctx, sess, t, rec, now, nil, ip, userAgent)
}

// UnlockOverride membuka loket BEKU dengan dual-control yang sudah ada.
//
// Dua bukti diminta bersamaan dan keduanya perlu: authorization_ref membuktikan
// SUPERVISOR HADIR, password membuktikan PETUGASNYA MASIH ORANG YANG SAMA. Supervisor
// sendirian tidak boleh membuka meja orang lain lalu meninggalkannya terbuka; petugas
// sendirian sudah terbukti tidak ingat PIN-nya.
//
// Rujukan otorisasinya dibaca dari gerbang SUPERVISOR_AUTH sesi ini — mekanisme yang
// SAMA dengan kesiapan terminal, bukan yang kedua. Mockup menyebut "otorisasi FISIK
// supervisor"; tidak ada mekanisme kehadiran fisik di sistem ini, dan endpoint yang
// mengaku memverifikasinya akan berbohong.
func (s *TerminalService) UnlockOverride(
	ctx context.Context, sess *SessionContext, req UnlockOverrideRequest, ip, userAgent string,
) (*DeskLockState, error) {
	if s.creds == nil || s.hasher == nil {
		return nil, apperr.ProviderNotConfigured
	}

	ref := strings.TrimSpace(req.AuthorizationRef)
	if ref == "" || req.Password == "" {
		return nil, apperr.ValidationError
	}
	if req.NewPIN != "" {
		// Divalidasi SEBELUM apa pun dibuka: PIN baru yang ditolak setelah tirainya
		// terangkat akan meninggalkan petugas dengan PIN lama yang ia kira sudah diganti.
		if err := pinrule.ValidatePIN(req.NewPIN); err != nil {
			return nil, err
		}
	}

	t, err := s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}
	if t.Status != TerminalLocked || t.ActiveAgentID != sess.EmployeeID {
		return nil, apperr.TerminalNotLocked
	}

	now := s.clock()
	gate, err := s.findSupervisorGate(ctx, sess.SessionID, ref, now)
	if err != nil {
		return nil, err
	}

	// Kata sandi petugas, lewat jalur lockout yang SUDAH ada. Jalur ini tidak boleh jadi
	// tempat menebak kata sandi tanpa batas hanya karena ia bernama "override".
	login, err := s.creds.FindForLogin(ctx, sess.EmployeeID)
	if err != nil {
		return nil, err
	}
	if login == nil {
		return nil, apperr.AgentCredentialInvalid
	}
	if login.LockedUntil != nil && login.LockedUntil.After(now) {
		return nil, lockedError(login.LockedUntil.Sub(now))
	}
	if login.PasswordHash == "" {
		return nil, apperr.AgentPasswordNotSet
	}
	ok, err := s.hasher.Verify(ctx, req.Password, login.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify agent password: %w", err)
	}
	if !ok {
		lockedUntil, lerr := s.creds.RecordLoginFailure(ctx, sess.EmployeeID, MaxFailedLogins, LoginLockout, now)
		if lerr != nil {
			return nil, lerr
		}
		s.writeAudit(ctx, EventDeskUnlockFailed, sess.EmployeeID, sess.TerminalID,
			&sess.SessionID, map[string]any{
				"override":          true,
				"reason":            "password_invalid",
				"authorization_ref": ref,
			}, ip, userAgent)
		if lockedUntil != nil && lockedUntil.After(now) {
			return nil, lockedError(lockedUntil.Sub(now))
		}
		return nil, apperr.AgentCredentialInvalid
	}
	if err := s.creds.ClearLoginFailures(ctx, sess.EmployeeID); err != nil {
		return nil, err
	}

	rec, err := s.creds.FindDeskPIN(ctx, sess.EmployeeID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, apperr.AgentCredentialInvalid
	}

	// PIN baru disetel SEBELUM tirainya diangkat. Kalau penyetelannya gagal, loketnya
	// tetap beku dan supervisor masih di sampingnya — jauh lebih murah daripada loket
	// terbuka dengan PIN lama yang petugasnya sudah lupa.
	if req.NewPIN != "" {
		hash, herr := s.hasher.Hash(ctx, req.NewPIN)
		if herr != nil {
			return nil, fmt.Errorf("hash desk pin: %w", herr)
		}
		// SetDeskPIN sekaligus mengosongkan failed_unlock_attempts.
		if serr := s.creds.SetDeskPIN(ctx, sess.EmployeeID, hash, now); serr != nil {
			return nil, serr
		}
		s.writeAudit(ctx, EventDeskPINSet, sess.EmployeeID, sess.TerminalID, &sess.SessionID,
			map[string]any{"first_time": rec.DeskPINHash == "", "override": true}, ip, userAgent)
		rec.DeskPINHash = hash
	} else if cerr := s.creds.ClearUnlockFailures(ctx, sess.EmployeeID); cerr != nil {
		return nil, cerr
	}

	unlocked, err := s.terminals.Unlock(ctx, sess.TerminalID, sess.EmployeeID, now)
	if err != nil {
		return nil, err
	}
	if !unlocked {
		return nil, apperr.TerminalNotLocked
	}

	return s.finishUnlock(ctx, sess, t, rec, now, map[string]any{
		"override":          true,
		"authorization_ref": ref,
		"supervisor_id":     gate.SupervisorID,
	}, ip, userAgent)
}

// DeskLockState menjawab `GET /internal/v1/terminals/lock-state`.
//
// Inilah yang membuat tirai selamat dari proses yang mati: renderer yang di-reload atau
// PC yang mati kehilangan tirai lokalnya bersama PIN-nya, dan tanpa endpoint ini aplikasi
// yang dibuka kembali akan menampilkan layar nasabah.
//
// Dipanggil SEKALI saat pemulihan sesi, di tempat yang sama dengan `auth/me`. Bukan
// endpoint polling: keadaan kunci hanya berubah oleh tindakan petugas itu sendiri, dan
// klien sudah tahu tindakannya sendiri.
func (s *TerminalService) DeskLockState(ctx context.Context, sess *SessionContext) (*DeskLockState, error) {
	if s.creds == nil {
		return nil, apperr.ProviderNotConfigured
	}

	t, err := s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}
	rec, err := s.creds.FindDeskPIN(ctx, sess.EmployeeID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, apperr.AgentCredentialInvalid
	}
	return s.deskLockState(t, rec, nil), nil
}

// recordUnlockFailure menaikkan penghitung, membekukan loket pada percobaan ketiga, dan
// mengembalikan galat yang pantas.
//
// DESK_UNLOCK_FAILED ditulis pada SETIAP kegagalan, bukan hanya yang ketiga: pola
// percobaan yang berhenti di dua, berulang tiap hari, di loket yang sama, adalah justru
// yang dicari pemeriksa.
func (s *TerminalService) recordUnlockFailure(
	ctx context.Context, sess *SessionContext, now time.Time, ip, userAgent string,
) error {
	attempts, err := s.creds.RecordUnlockFailure(ctx, sess.EmployeeID, now)
	if err != nil {
		return err
	}

	frozen := attempts >= MaxUnlockAttempts
	if frozen {
		// Statusnya TETAP LOCKED; yang berubah hanya alasannya. Pembekuan adalah alasan
		// terkunci, bukan keadaan baru.
		if err := s.terminals.FreezeLock(ctx, sess.TerminalID, now); err != nil {
			return err
		}
	}

	s.writeAudit(ctx, EventDeskUnlockFailed, sess.EmployeeID, sess.TerminalID, &sess.SessionID,
		map[string]any{"attempts": attempts, "frozen": frozen}, ip, userAgent)

	if frozen {
		return apperr.TerminalDeskFrozen
	}

	// Sisa percobaan ikut dikirim: tanpa angkanya layar kunci hanya bisa berkata "salah",
	// dan petugas tidak tahu ia selangkah dari membekukan loketnya sendiri.
	e := apperr.AgentDeskPINInvalid
	e.Details = map[string]any{"attempts_remaining": MaxUnlockAttempts - attempts}
	return e
}

// finishUnlock menulis jejak dan menyusun jawaban untuk kedua jalur pembukaan.
func (s *TerminalService) finishUnlock(
	ctx context.Context, sess *SessionContext, before *Terminal, rec *AgentDeskPINRecord,
	now time.Time, extra map[string]any, ip, userAgent string,
) (*DeskLockState, error) {
	details := map[string]any{"reason": string(before.LockedReason)}
	for k, v := range extra {
		details[k] = v
	}

	var duration *int
	if before.LockedAt != nil {
		secs := int(now.Sub(*before.LockedAt).Seconds())
		if secs < 0 {
			secs = 0
		}
		duration = &secs
		details["locked_duration_seconds"] = secs
	}

	s.writeAudit(ctx, EventDeskUnlocked, sess.EmployeeID, sess.TerminalID, &sess.SessionID,
		details, ip, userAgent)

	t, err := s.Get(ctx, sess.TerminalID)
	if err != nil {
		return nil, err
	}
	// Penghitungnya sudah dikosongkan di database; salinan di memori ini menyusul supaya
	// attempts_remaining di jawaban tidak menampilkan angka yang sudah basi.
	rec.FailedUnlockAttempts = 0
	return s.deskLockState(t, rec, duration), nil
}

// findSupervisorGate mencari otorisasi dual-control yang masih berlaku untuk sesi ini.
//
// Membaca gerbang SUPERVISOR_AUTH yang sudah dicatat AuthorizeSupervisor, bukan
// memverifikasi token supervisor lagi: tanda terimanya memang yang dipegang klien, dan
// mekanisme kedua untuk hal yang sama adalah mekanisme kedua untuk disalahgunakan.
func (s *TerminalService) findSupervisorGate(
	ctx context.Context, sessionID uuid.UUID, ref string, now time.Time,
) (*GateRecord, error) {
	records, err := s.terminals.ListGates(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range records {
		r := records[i]
		if r.Gate != GateSupervisorAuth || r.AuthorizationRef != ref {
			continue
		}
		if !r.ExpiresAt.After(now) {
			// Kedaluwarsa dijawab sama dengan tidak ada: keduanya diselesaikan dengan
			// meminta supervisor menandatangani lagi.
			return nil, apperr.SupervisorTokenInvalid
		}
		return &r, nil
	}
	return nil, apperr.SupervisorTokenInvalid
}

// deskLockState menyusun jawaban dari terminal dan kredensial petugas.
func (s *TerminalService) deskLockState(t *Terminal, rec *AgentDeskPINRecord, duration *int) *DeskLockState {
	remaining := MaxUnlockAttempts - rec.FailedUnlockAttempts
	if remaining < 0 {
		remaining = 0
	}

	locked := t.Status == TerminalLocked
	return &DeskLockState{
		TerminalID:            t.TerminalID,
		Status:                t.Status,
		Locked:                locked,
		LockedAt:              t.LockedAt,
		LockedReason:          t.LockedReason,
		LockedDurationSeconds: duration,

		// Beku hanya berarti sesuatu selama tirainya turun. Penghitung yang tertinggal di
		// tiga pada loket yang sudah terbuka bukan pembekuan, dan melaporkannya sebagai
		// beku akan membuat layar menampilkan tombol supervisor yang tidak dibutuhkan.
		Frozen:            locked && rec.FailedUnlockAttempts >= MaxUnlockAttempts,
		AttemptsRemaining: remaining,
		DeskPINSet:        rec.DeskPINHash != "",
	}
}
