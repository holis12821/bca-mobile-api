package cs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// PasswordHasher memverifikasi dan membuat hash kata sandi.
//
// Dipenuhi paket crypto secara struktural, supaya paket domain ini tidak bergantung
// padanya — pola yang sama dipakai [PhoneHasher] dan AgentLookup di middleware.
type PasswordHasher interface {
	Hash(ctx context.Context, plaintext string) (string, error)
	Verify(ctx context.Context, plaintext, encoded string) (bool, error)
}

// AgentSessionService melayani login, logout, dan penyetelan kata sandi petugas.
type AgentSessionService struct {
	creds     AgentCredentialRepository
	sessions  AgentSessionRepository
	terminals TerminalRepository
	audit     AuditEventRepository
	hasher    PasswordHasher

	// registry dan supervisors hanya dipakai jalur pendaftaran (SCR-001). Nil membuat
	// pendaftaran menolak, bukan panic — proses yang tidak merakitnya memang tidak
	// menyediakan pendaftaran petugas.
	registry    AgentRegistry
	supervisors SupervisorRepository

	// hris adalah direktori pegawai. Nil diperlakukan sama dengan direktori yang tidak
	// terkonfigurasi: pendaftaran menolak dengan HRIS_UNAVAILABLE, bukan melewatkan
	// pemeriksaan NPP.
	hris HRISDirectory

	clock func() time.Time
}

type AgentSessionServiceConfig struct {
	Creds     AgentCredentialRepository
	Sessions  AgentSessionRepository
	Terminals TerminalRepository
	Audit     AuditEventRepository
	Hasher    PasswordHasher

	Registry    AgentRegistry
	Supervisors SupervisorRepository
	HRIS        HRISDirectory

	Clock func() time.Time
}

func NewAgentSessionService(cfg AgentSessionServiceConfig) *AgentSessionService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &AgentSessionService{
		creds:       cfg.Creds,
		sessions:    cfg.Sessions,
		terminals:   cfg.Terminals,
		audit:       cfg.Audit,
		hasher:      cfg.Hasher,
		registry:    cfg.Registry,
		supervisors: cfg.Supervisors,
		hris:        cfg.HRIS,
		clock:       clock,
	}
}

// Login memverifikasi NPP + kata sandi, memeriksa allowlist terminal, lalu membuka sesi.
//
// Urutan pemeriksaannya mengikuti SCR-003 (§12), dan urutannya bukan selera:
//
//  1. Petugas dikenal & tidak terkunci — sebelum menyentuh Argon2id, karena verifikasi
//     kata sandi itu mahal dan tidak boleh bisa dipicu NPP karangan berulang-ulang.
//  2. Kata sandi benar.
//  3. Terminal ada di allowlist (barisnya ada di cs_terminals).
//  4. Sesi dibuat.
//
// Mengembalikan `next_step: TERMINAL_READINESS` SELALU (Rule 2): sesi yang baru
// terautentikasi wajib lewat layar kesiapan. Dikirim server supaya aturan itu tidak hidup
// hanya di routing klien — yang bisa disunting.
func (s *AgentSessionService) Login(ctx context.Context, req LoginRequest, ip, userAgent string) (*LoginResponse, error) {
	employeeID := strings.TrimSpace(req.EmployeeID)
	terminalID := strings.TrimSpace(req.TerminalID)
	if employeeID == "" || req.Password == "" || terminalID == "" {
		return nil, apperr.ValidationError
	}

	rec, err := s.creds.FindForLogin(ctx, employeeID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		// Petugas tak dikenal dan kata sandi salah dijawab SAMA: membedakannya
		// memberi tahu penyerang NPP mana yang terdaftar. Argon2id sengaja tidak
		// dijalankan di sini — jalur ini sudah di belakang X-Internal-API-Key, jadi
		// tidak ada penyerang anonim yang bisa mengukur selisih waktunya.
		s.writeAudit(ctx, EventAgentLoginFailed, employeeID, terminalID, nil, map[string]any{
			"reason": "unknown_agent",
		}, ip, userAgent)
		return nil, apperr.AgentCredentialInvalid
	}

	now := s.clock()
	if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
		return nil, lockedError(rec.LockedUntil.Sub(now))
	}

	if rec.PasswordHash == "" {
		// Dibedakan dari kata sandi salah: petugas yang belum punya kata sandi tidak
		// sedang salah mengetik, dan menjawabnya "salah" akan membuatnya mencoba terus.
		return nil, apperr.AgentPasswordNotSet
	}

	ok, err := s.hasher.Verify(ctx, req.Password, rec.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify agent password: %w", err)
	}
	if !ok {
		lockedUntil, lockErr := s.creds.RecordLoginFailure(
			ctx, employeeID, MaxFailedLogins, LoginLockout, now)
		if lockErr != nil {
			slog.Error("record agent login failure failed",
				"employee_id", employeeID, "error", lockErr)
		}
		s.writeAudit(ctx, EventAgentLoginFailed, employeeID, terminalID, nil, map[string]any{
			"reason": "bad_password",
			"locked": lockedUntil != nil,
		}, ip, userAgent)

		if lockedUntil != nil && lockedUntil.After(now) {
			return nil, lockedError(lockedUntil.Sub(now))
		}
		return nil, apperr.AgentCredentialInvalid
	}

	// Allowlist terminal: barisnya ada di cs_terminals, atau terminalnya tidak diizinkan.
	terminal, err := s.terminals.FindByID(ctx, terminalID)
	if err != nil {
		return nil, err
	}
	if terminal == nil {
		s.writeAudit(ctx, EventAgentLoginFailed, employeeID, terminalID, nil, map[string]any{
			"reason": "terminal_not_allowlisted",
		}, ip, userAgent)
		return nil, apperr.TerminalNotFound
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}

	sess := &AgentSession{
		ID:         uuid.New(),
		EmployeeID: employeeID,
		TerminalID: terminalID,
		Shift:      strings.TrimSpace(req.Shift),
		StartedAt:  now,
		ExpiresAt:  now.Add(SessionTTL),
	}
	if err := s.sessions.Create(ctx, sess, tokenHash); err != nil {
		return nil, err
	}

	if err := s.creds.ClearLoginFailures(ctx, employeeID); err != nil {
		// Tidak menggagalkan login: penghitung yang tidak ter-reset hanya membuat
		// lockout datang lebih cepat, dan itu jauh lebih baik daripada menolak petugas
		// yang kata sandinya benar.
		slog.Error("clear agent login failures failed",
			"employee_id", employeeID, "error", err)
	}

	s.writeAudit(ctx, EventAgentLogin, employeeID, terminalID, &sess.ID, map[string]any{
		"shift": sess.Shift,
	}, ip, userAgent)

	return &LoginResponse{
		SessionToken:   token,
		EmployeeID:     employeeID,
		Name:           rec.Name,
		Scopes:         rec.Scopes,
		TerminalID:     terminalID,
		TerminalStatus: terminal.Status,
		StartedAt:      sess.StartedAt,
		ExpiresAt:      sess.ExpiresAt,
		NextStep:       "TERMINAL_READINESS",
	}, nil
}

// Logout menutup sesi petugas DAN menonaktifkan terminalnya (SCR-019).
//
// Keduanya, bukan hanya sesinya: terminal yang tetap ONLINE setelah petugasnya pergi
// adalah loket yang Rule 4 akan meluluskan tanpa ada orang di depannya.
func (s *AgentSessionService) Logout(ctx context.Context, sessionID uuid.UUID, employeeID, terminalID, ip, userAgent string) error {
	now := s.clock()

	ended, err := s.sessions.End(ctx, sessionID, SessionEndedLogout, now)
	if err != nil {
		return err
	}
	if !ended {
		return apperr.AgentSessionNotFound
	}

	if terminalID != "" {
		if _, err := s.terminals.Deactivate(ctx, terminalID, now); err != nil {
			// Sesi sudah tertutup; terminal yang gagal dinonaktifkan akan tertolak
			// sendiri oleh penjaga Rule 4 karena sesinya tidak ada lagi. Dicatat sebagai
			// error supaya baris yang menggantung terlihat, bukan diam-diam.
			slog.Error("deactivate terminal on logout failed",
				"terminal_id", terminalID, "error", err)
		}
	}

	s.writeAudit(ctx, EventAgentLogout, employeeID, terminalID, &sessionID, nil, ip, userAgent)
	return nil
}

// SetPassword menyetel atau mengganti kata sandi petugas.
//
// Jalur penyetelan PERTAMA, dan satu-satunya yang ada. Dibuktikan dengan kunci API
// petugas yang sudah dipegangnya — handler memasangnya di belakang `AgentIdentity`,
// jadi yang sampai ke sini sudah terbukti sebagai petugas itu.
//
// `current_password` wajib hanya kalau petugas SUDAH punya kata sandi. Tanpa aturan itu,
// siapa pun yang pernah melihat kunci API bisa menimpa kata sandi orang lain kapan saja.
func (s *AgentSessionService) SetPassword(ctx context.Context, employeeID string, req SetPasswordRequest, ip, userAgent string) error {
	if len(req.NewPassword) < MinPasswordLen || len(req.NewPassword) > MaxPasswordLen {
		return apperr.AgentPasswordWeak
	}

	rec, err := s.creds.FindForLogin(ctx, employeeID)
	if err != nil {
		return err
	}
	if rec == nil {
		return apperr.AgentCredentialInvalid
	}

	if rec.PasswordHash != "" {
		if req.CurrentPassword == "" {
			return apperr.AgentCredentialInvalid
		}
		ok, err := s.hasher.Verify(ctx, req.CurrentPassword, rec.PasswordHash)
		if err != nil {
			return fmt.Errorf("verify current password: %w", err)
		}
		if !ok {
			return apperr.AgentCredentialInvalid
		}
	}

	hash, err := s.hasher.Hash(ctx, req.NewPassword)
	if err != nil {
		return fmt.Errorf("hash agent password: %w", err)
	}
	if err := s.creds.SetPassword(ctx, employeeID, hash, s.clock()); err != nil {
		return err
	}

	// Isi kata sandinya TIDAK pernah masuk details — bukan panjangnya, bukan petunjuknya.
	s.writeAudit(ctx, EventAgentPasswordSet, employeeID, "", nil, map[string]any{
		"first_time": rec.PasswordHash == "",
	}, ip, userAgent)
	return nil
}

// Resolve mengembalikan identitas pembawa token sesi, atau error 401.
//
// Dipakai middleware. Tenggat diperiksa di SQL, bukan di sini: jam aplikasi dan jam
// database bisa berbeda, dan yang memegang barisnya adalah database.
func (s *AgentSessionService) Resolve(ctx context.Context, token string) (*SessionContext, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, apperr.AgentSessionInvalid
	}

	sc, err := s.sessions.FindByToken(ctx, hashSessionToken(token))
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, apperr.AgentSessionInvalid
	}
	return sc, nil
}

// Touch memperbarui tanda hidup sesi. Kegagalannya tidak pernah menggagalkan permintaan.
func (s *AgentSessionService) Touch(ctx context.Context, sessionID uuid.UUID) {
	if err := s.sessions.Touch(ctx, sessionID, s.clock()); err != nil {
		slog.Warn("touch agent session failed", "session_id", sessionID, "error", err)
	}
}

func (s *AgentSessionService) writeAudit(ctx context.Context, eventType, actor, terminalID string, sessionID *uuid.UUID, details map[string]any, ip, userAgent string) {
	writeCSAudit(ctx, s.audit, s.clock(), eventType, actor, terminalID, sessionID, details, ip, userAgent)
}

// newSessionToken menerbitkan token sesi beserta hash-nya.
//
// 32 byte dari crypto/rand, base64 URL-safe tanpa padding. Token inilah yang dibawa
// klien; server hanya menyimpan SHA-256-nya, jadi isi tabel yang bocor tidak bisa dipakai
// masuk.
func newSessionToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashSessionToken(token), nil
}

// hashSessionToken memakai SHA-256, dan itu disengaja BUKAN Argon2id.
//
// Tokennya 256 bit dari crypto/rand — bukan kata sandi yang bisa ditebak, jadi tidak ada
// yang perlu diperlambat. Dan jalur ini dilalui SETIAP permintaan ber-sesi: Argon2id
// (64 MB × 4 thread) per permintaan akan membuat verifikasi sesi lebih mahal daripada
// pekerjaan yang dilindunginya.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// lockedError menyertakan sisa waktu kuncian.
//
// Klien butuh angkanya untuk menampilkan hitung mundur; tanpa itu layarnya hanya bisa
// berkata "coba lagi nanti" dan petugas akan menekan tombolnya berulang-ulang.
func lockedError(remaining time.Duration) apperr.Error {
	e := apperr.AgentLocked
	e.Details = map[string]any{
		"retry_after_seconds": int(remaining.Seconds()) + 1,
	}
	return e
}

// ResolveSession memenuhi middleware.AgentSessionResolver.
//
// Mengembalikan `any` karena paket middleware tidak boleh meng-import paket ini —
// arah impornya handler → domain, dan middleware dipakai keduanya. Handler
// meng-assert-nya kembali ke *SessionContext.
func (s *AgentSessionService) ResolveSession(ctx context.Context, token string) (any, error) {
	sc, err := s.Resolve(ctx, token)
	if err != nil {
		return nil, err
	}
	return sc, nil
}

// LiveSession mengembalikan sesi hidup petugas, atau nil kalau tidak ada.
//
// Dipakai `auth/me`: pemanggilnya memakai kunci API dan belum tentu punya token sesi,
// jadi ia tidak bisa tahu apakah gilirannya sudah terbuka tanpa menanyakannya.
func (s *AgentSessionService) LiveSession(ctx context.Context, employeeID string) (*AgentSession, error) {
	return s.sessions.FindLiveByEmployee(ctx, employeeID)
}

// --- Pendaftaran petugas (SCR-001) ---

// RegisterAgentRequest adalah body `POST /internal/v1/agents`.
//
// Kunci API petugas baru TIDAK diterima dari pemanggil — ia diterbitkan server. Kunci
// pilihan klien adalah kunci yang bisa dipilih lemah, dipakai ulang dari sistem lain,
// atau sudah pernah bocor, dan tidak ada cara memeriksanya dari sini.
type RegisterAgentRequest struct {
	EmployeeID string   `json:"employee_id"`
	Scopes     []string `json:"scopes"`

	// SupervisorID dan Token adalah otorisasi dual-control. Diminta di titik TINDAKAN,
	// bukan sebagai gerbang sesi: yang perlu ditandatangani adalah pemberian kewenangan
	// ini, bukan kesiapan loket si pendaftar.
	SupervisorID string `json:"supervisor_id"`
	Token        string `json:"token"`
}

// RegisterAgentResponse mengembalikan kunci API petugas baru SEKALI.
//
// Server menyimpan hash-nya; tidak ada endpoint yang bisa mengembalikannya lagi. Sama
// dengan `session_token` di LoginResponse, dan untuk alasan yang sama.
type RegisterAgentResponse struct {
	EmployeeID string   `json:"employee_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`

	// APIKey hanya ada di response ini.
	APIKey string `json:"api_key"`

	// Position dan Branch datang dari HRIS, bukan dari body: nama dan jabatan petugas
	// adalah milik direktori pegawai, dan menerimanya dari pemanggil akan membuat baris
	// cs_agents menyebut orang yang berbeda dari yang ada di HRIS.
	Position string `json:"position,omitempty"`
	Branch   string `json:"branch,omitempty"`

	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
}

// LookupEmployee mencari NPP di direktori HRIS.
//
// Melayani SCR-001 sebelum pendaftaran: layar itu menanyakan NPP lebih dulu dan
// menampilkan nama yang ditemukan untuk dikonfirmasi. Tanpa langkah ini, salah ketik NPP
// baru terlihat setelah petugas terdaftar dengan nama orang lain.
func (s *AgentSessionService) LookupEmployee(ctx context.Context, employeeID string) (*HRISEmployee, error) {
	employeeID = strings.TrimSpace(employeeID)
	if employeeID == "" {
		return nil, apperr.ValidationError
	}
	if s.hris == nil {
		return nil, apperr.HRISUnavailable
	}
	return s.hris.LookupEmployee(ctx, employeeID)
}

// RegisterAgent mendaftarkan petugas baru, dengan otorisasi supervisor.
//
// Urutan pemeriksaannya mengikat, dan alasannya sama dengan Login:
//
//  1. Bentuk permintaan — sebelum menyentuh apa pun yang mahal.
//  2. Cakupan dikenal — kesalahan pemanggil, tidak perlu melibatkan HRIS maupun Argon2id.
//  3. NPP ada di HRIS DAN masih aktif — pegawai yang sudah berhenti tidak boleh diberi
//     kewenangan, dan dua keadaan itu dijawab berbeda.
//  4. Token supervisor benar — Argon2id, paling mahal, dan hanya untuk permintaan yang
//     sudah lolos semuanya.
//  5. Baris ditulis.
//
// Kegagalan otorisasi supervisor ikut tercatat (SUPERVISOR_AUTH_FAILED): percobaan
// pemberian kewenangan yang ditolak adalah hal yang justru paling perlu terbaca di jejak.
func (s *AgentSessionService) RegisterAgent(ctx context.Context, req RegisterAgentRequest, registrar, ip, userAgent string) (*RegisterAgentResponse, error) {
	employeeID := strings.TrimSpace(req.EmployeeID)
	supervisorID := strings.TrimSpace(req.SupervisorID)

	if employeeID == "" || supervisorID == "" || req.Token == "" || len(req.Scopes) == 0 {
		return nil, apperr.ValidationError
	}
	if s.registry == nil || s.supervisors == nil {
		return nil, apperr.ProviderNotConfigured
	}

	// Cakupan dibersihkan dan diperiksa di sini supaya penolakannya bisa menyebut cakupan
	// MANA yang tidak dikenal — sesuatu yang CHECK di skema tidak bisa lakukan.
	scopes := make([]string, 0, len(req.Scopes))
	seen := make(map[string]bool, len(req.Scopes))
	for _, raw := range req.Scopes {
		scope := strings.ToUpper(strings.TrimSpace(raw))
		if !ValidScope(scope) {
			return nil, apperr.Error{
				Status:  422,
				Code:    "SCOPE_UNKNOWN",
				Message: "Cakupan kewenangan tidak dikenal.",
				Details: map[string]any{"scope": scope, "allowed": AllScopes},
			}
		}
		// Duplikat dibuang, bukan ditolak: ["TICKET","TICKET"] adalah permintaan yang
		// maksudnya jelas, dan menolaknya hanya menyusahkan pemanggil.
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}

	if s.hris == nil {
		return nil, apperr.HRISUnavailable
	}
	emp, err := s.hris.LookupEmployee(ctx, employeeID)
	if err != nil {
		return nil, err
	}
	if !emp.Active {
		return nil, apperr.EmployeeInactive
	}

	supervisorName, ok, err := s.supervisors.Authenticate(ctx, supervisorID, req.Token)
	if err != nil {
		// Kegagalan infrastruktur, BUKAN penolakan. Dibedakan supaya Postgres yang
		// tersendat tidak terbaca sebagai token supervisor yang salah.
		return nil, err
	}
	if !ok {
		writeCSAudit(ctx, s.audit, s.clock(), EventSupervisorAuthFailed, "agent:"+registrar, "",
			nil, map[string]any{
				"purpose":       "AGENT_REGISTRATION",
				"supervisor_id": supervisorID,
				"employee_id":   employeeID,
			}, ip, userAgent)
		return nil, apperr.SupervisorTokenInvalid
	}

	apiKey, _, err := newSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate agent api key: %w", err)
	}

	// Kunci API di-hash Argon2id, sama dengan baris cs_agents yang sudah ada — bukan
	// SHA-256 seperti token sesi. Bedanya disengaja: kunci API tidak bertenggat dan
	// dipakai berbulan-bulan, jadi biaya verifikasinya terbayar oleh umurnya.
	apiKeyHash, err := s.hasher.Hash(ctx, apiKey)
	if err != nil {
		return nil, fmt.Errorf("hash agent api key: %w", err)
	}

	now := s.clock()
	if err := s.registry.Register(ctx, emp.EmployeeID, emp.Name, apiKeyHash, scopes, now); err != nil {
		return nil, err
	}

	writeCSAudit(ctx, s.audit, now, EventAgentRegistered, "agent:"+registrar, "",
		nil, map[string]any{
			"employee_id":     emp.EmployeeID,
			"scopes":          scopes,
			"supervisor_id":   supervisorID,
			"supervisor_name": supervisorName,
			"hris_branch":     emp.Branch,
		}, ip, userAgent)

	slog.Info("cs agent registered",
		"employee_id", emp.EmployeeID, "scopes", scopes,
		"registered_by", registrar, "supervisor_id", supervisorID)

	return &RegisterAgentResponse{
		EmployeeID:   emp.EmployeeID,
		Name:         emp.Name,
		Scopes:       scopes,
		APIKey:       apiKey,
		Position:     emp.Position,
		Branch:       emp.Branch,
		RegisteredBy: registrar,
		RegisteredAt: now,
	}, nil
}

// UpdateAgentRequest adalah body `PATCH /internal/v1/agents/{employee_id}`.
//
// Kedua bidang perubahan boleh kosong, dan kosong berarti TIDAK DIUBAH. Kalau keduanya
// kosong permintaannya ditolak, bukan dijawab "berhasil tanpa perubahan": PATCH yang
// tidak mengubah apa pun tapi dijawab 200 akan membuat klien yang salah menamai
// bidangnya mengira perubahannya tersimpan.
type UpdateAgentRequest struct {
	// Scopes MENGGANTI seluruh daftar, bukan menambah. Dipilih begitu supaya pencabutan
	// punya bentuk yang jelas: "kirim daftar barunya" bisa mencabut, sementara
	// "tambahkan cakupan ini" tidak pernah bisa.
	Scopes []string `json:"scopes"`

	// IsActive pointer karena `false` dan "tidak disebut" adalah dua permintaan berbeda.
	IsActive *bool `json:"is_active"`

	// Otorisasi dual-control, sama dengan RegisterAgentRequest dan untuk alasan yang
	// sama: yang ditandatangani adalah PERUBAHAN KEWENANGAN, bukan kesiapan loket si
	// pengubah.
	SupervisorID string `json:"supervisor_id"`
	Token        string `json:"token"`
}

// UpdateAgentResponse adalah keadaan petugas SESUDAH perubahan, beserta selisihnya.
//
// Selisihnya disertakan supaya layar supervisor bisa menampilkan "CUSTOMER_PII dicabut"
// tanpa menyimpan keadaan sebelumnya sendiri — dan supaya yang menekan tombolnya melihat
// apa yang sebenarnya berubah, bukan hanya daftar akhir yang ia sendiri kirim.
type UpdateAgentResponse struct {
	EmployeeID string   `json:"employee_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	IsActive   bool     `json:"is_active"`

	ScopesAdded   []string `json:"scopes_added"`
	ScopesRemoved []string `json:"scopes_removed"`

	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpdateAgent mengubah cakupan atau mencabut hak seorang petugas.
//
// Menutup selisih yang sebelumnya diselesaikan dengan SQL langsung: mendaftarkan petugas
// sudah punya endpoint, mengubah kewenangannya belum — dan jalur yang hanya bisa
// dijalankan lewat psql tidak menghasilkan jejak audit, tidak menuntut otorisasi
// supervisor, dan tidak bisa diserahkan ke siapa pun di luar yang memegang kredensial
// database.
//
// Urutan pemeriksaannya mengikuti RegisterAgent, dengan satu tambahan di depan:
//
//  1. Bentuk permintaan — ada yang diubah, dan otorisasinya disebut.
//  2. Bukan diri sendiri. Diperiksa SEBELUM apa pun yang mahal, dan sebelum NPP-nya
//     dicari: petugas yang menaikkan kewenangannya sendiri hanya butuh satu orang, dan
//     mencabut haknya sendiri bisa dipakai menutup giliran tanpa jejak logout.
//  3. Cakupan dikenal — kesalahan pemanggil, tidak perlu melibatkan Argon2id.
//  4. Token supervisor benar — paling mahal, hanya untuk yang sudah lolos semuanya.
//  5. Baris ditulis, selisihnya dicatat.
func (s *AgentSessionService) UpdateAgent(
	ctx context.Context, employeeID string, req UpdateAgentRequest, updater, ip, userAgent string,
) (*UpdateAgentResponse, error) {
	employeeID = strings.TrimSpace(employeeID)
	supervisorID := strings.TrimSpace(req.SupervisorID)

	if employeeID == "" || supervisorID == "" || req.Token == "" {
		return nil, apperr.ValidationError
	}
	if req.Scopes == nil && req.IsActive == nil {
		return nil, apperr.ValidationError
	}
	if s.registry == nil || s.supervisors == nil {
		return nil, apperr.ProviderNotConfigured
	}

	if strings.EqualFold(employeeID, strings.TrimSpace(updater)) {
		return nil, apperr.AgentSelfUpdate
	}

	// Cakupan dibersihkan di sini, bukan diserahkan ke CHECK skema, supaya penolakannya
	// bisa menyebut cakupan MANA yang tidak dikenal.
	var scopes []string
	if req.Scopes != nil {
		if len(req.Scopes) == 0 {
			// Daftar kosong berbeda dari "tidak disebut": ia meminta petugas tanpa
			// kewenangan apa pun — baris yang terlihat sah tapi ditolak di setiap jalur.
			// Yang dimaksud hampir selalu `is_active: false`, dan itulah yang disarankan.
			return nil, apperr.Error{
				Status:  422,
				Code:    "SCOPE_EMPTY",
				Message: "Petugas harus punya minimal satu cakupan. Untuk mencabut hak, kirim is_active: false.",
			}
		}
		scopes = make([]string, 0, len(req.Scopes))
		seen := make(map[string]bool, len(req.Scopes))
		for _, raw := range req.Scopes {
			scope := strings.ToUpper(strings.TrimSpace(raw))
			if !ValidScope(scope) {
				return nil, apperr.Error{
					Status:  422,
					Code:    "SCOPE_UNKNOWN",
					Message: "Cakupan kewenangan tidak dikenal.",
					Details: map[string]any{"scope": scope, "allowed": AllScopes},
				}
			}
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}

	supervisorName, ok, err := s.supervisors.Authenticate(ctx, supervisorID, req.Token)
	if err != nil {
		// Kegagalan infrastruktur, BUKAN penolakan — sama dengan RegisterAgent.
		return nil, err
	}
	if !ok {
		writeCSAudit(ctx, s.audit, s.clock(), EventSupervisorAuthFailed, "agent:"+updater, "",
			nil, map[string]any{
				"purpose":       "AGENT_UPDATE",
				"supervisor_id": supervisorID,
				"employee_id":   employeeID,
			}, ip, userAgent)
		return nil, apperr.SupervisorTokenInvalid
	}

	now := s.clock()
	before, after, err := s.registry.Update(ctx, employeeID, AgentUpdate{
		Scopes:   scopes,
		IsActive: req.IsActive,
	}, now)
	if err != nil {
		return nil, err
	}

	added, removed := diffScopes(before.Scopes, after.Scopes)

	details := map[string]any{
		"employee_id":     employeeID,
		"supervisor_id":   supervisorID,
		"supervisor_name": supervisorName,
		"scopes_before":   before.Scopes,
		"scopes_after":    after.Scopes,
	}
	// Selisih keaktifan hanya dicatat kalau memang berubah: jejak yang menyebut
	// "is_active tetap true" pada setiap perubahan cakupan membuat pencabutan hak yang
	// sungguhan lebih sulit ditemukan di antara baris yang tidak mengubah apa pun.
	if before.IsActive != after.IsActive {
		details["is_active_before"] = before.IsActive
		details["is_active_after"] = after.IsActive
	}
	if len(added) > 0 {
		details["scopes_added"] = added
	}
	if len(removed) > 0 {
		details["scopes_removed"] = removed
	}
	writeCSAudit(ctx, s.audit, now, EventAgentUpdated, "agent:"+updater, "",
		nil, details, ip, userAgent)

	slog.Info("cs agent updated",
		"employee_id", employeeID,
		"scopes_added", added, "scopes_removed", removed,
		"is_active", after.IsActive,
		"updated_by", updater, "supervisor_id", supervisorID)

	return &UpdateAgentResponse{
		EmployeeID:    after.EmployeeID,
		Name:          after.Name,
		Scopes:        after.Scopes,
		IsActive:      after.IsActive,
		ScopesAdded:   added,
		ScopesRemoved: removed,
		UpdatedBy:     updater,
		UpdatedAt:     after.UpdatedAt,
	}, nil
}

// diffScopes melaporkan cakupan yang bertambah dan yang dicabut.
//
// Keduanya selalu irisan tak-nil, termasuk saat kosong: bidangnya di-encode ke JSON
// tanpa `omitempty`, dan `null` di sana akan membuat klien melakukan `.length` pada nilai
// yang bukan array.
func diffScopes(before, after []string) (added, removed []string) {
	inBefore := make(map[string]bool, len(before))
	for _, s := range before {
		inBefore[s] = true
	}
	inAfter := make(map[string]bool, len(after))
	for _, s := range after {
		inAfter[s] = true
	}

	added = make([]string, 0)
	for _, s := range after {
		if !inBefore[s] {
			added = append(added, s)
		}
	}
	removed = make([]string, 0)
	for _, s := range before {
		if !inAfter[s] {
			removed = append(removed, s)
		}
	}
	return added, removed
}
