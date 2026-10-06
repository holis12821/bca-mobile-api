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

	clock func() time.Time
}

type AgentSessionServiceConfig struct {
	Creds     AgentCredentialRepository
	Sessions  AgentSessionRepository
	Terminals TerminalRepository
	Audit     AuditEventRepository
	Hasher    PasswordHasher

	Clock func() time.Time
}

func NewAgentSessionService(cfg AgentSessionServiceConfig) *AgentSessionService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &AgentSessionService{
		creds:     cfg.Creds,
		sessions:  cfg.Sessions,
		terminals: cfg.Terminals,
		audit:     cfg.Audit,
		hasher:    cfg.Hasher,
		clock:     clock,
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
