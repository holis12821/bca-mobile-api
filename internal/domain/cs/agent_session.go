package cs

import (
	"time"

	"github.com/google/uuid"
)

// Umur dan kebijakan sesi petugas.
const (
	// SessionTTL 9 jam: satu giliran kerja 8 jam plus margin untuk serah terima.
	// Sesi yang lebih panjang berarti terminal yang ditinggalkan tetap ONLINE sampai
	// besok, dan Rule 4 akan meluluskan loket yang tidak ada orangnya.
	SessionTTL = 9 * time.Hour

	// MinPasswordLen 12. Kata sandi ini melindungi kewenangan memutuskan verifikasi
	// identitas yang membuka pembukaan rekening; delapan karakter tidak sepadan.
	MinPasswordLen = 12
	MaxPasswordLen = 128

	// MaxFailedLogins 5, sama dengan users.max_pin_attempts bawaan.
	MaxFailedLogins = 5

	// LoginLockout 15 menit. Cukup lama untuk mematikan penebakan otomatis, cukup
	// pendek supaya petugas yang benar-benar salah ketik tidak kehilangan gilirannya.
	LoginLockout = 15 * time.Minute
)

// AgentSession adalah satu giliran kerja petugas di satu terminal.
type AgentSession struct {
	ID         uuid.UUID `json:"-"`
	EmployeeID string    `json:"employee_id"`
	TerminalID string    `json:"terminal_id"`
	Shift      string    `json:"shift,omitempty"`

	StartedAt time.Time  `json:"started_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`

	EndedReason string `json:"ended_reason,omitempty"`

	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// Alasan berakhirnya sesi. Cocok dengan CHECK di migrasi 000035.
const (
	SessionEndedLogout     = "LOGOUT"
	SessionEndedExpired    = "EXPIRED"
	SessionEndedSuperseded = "SUPERSEDED"
	SessionEndedRevoked    = "REVOKED"
)

// LoginRequest adalah body `POST /internal/v1/auth/login`.
type LoginRequest struct {
	EmployeeID string `json:"employee_id"`
	Password   string `json:"password"`
	TerminalID string `json:"terminal_id"`
	Shift      string `json:"shift"`
}

// LoginResponse dikembalikan setelah login berhasil.
//
// `session_token` adalah SATU-SATUNYA kali token itu terkirim. Server menyimpan
// hash-nya; tidak ada endpoint yang bisa mengembalikannya lagi.
type LoginResponse struct {
	SessionToken string `json:"session_token"`

	EmployeeID string   `json:"employee_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`

	TerminalID     string         `json:"terminal_id"`
	TerminalStatus TerminalStatus `json:"terminal_status"`

	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`

	// NextStep selalu TERMINAL_READINESS. Rule 2: sesi yang baru terautentikasi WAJIB
	// lewat layar kesiapan, tidak boleh langsung ke dashboard. Dikirim server supaya
	// aturan itu tidak hidup hanya di routing klien — yang bisa disunting.
	NextStep string `json:"next_step"`
}

// SetPasswordRequest adalah body `POST /internal/v1/auth/password`.
//
// Jalur penyetelan kata sandi PERTAMA, dan satu-satunya yang ada. Dibuktikan dengan
// kunci API petugas yang sudah dipegangnya (header `X-Agent-API-Key`), bukan dengan
// kata sandi lama yang belum pernah ada.
//
// `current_password` wajib hanya kalau petugas SUDAH punya kata sandi. Tanpa aturan itu,
// siapa pun yang pernah melihat kunci API bisa menimpa kata sandi orang lain kapan saja.
type SetPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// SessionContext adalah identitas yang dibawa sebuah token sesi.
//
// Dikembalikan repository ke middleware; tidak pernah di-encode ke JSON.
type SessionContext struct {
	SessionID  uuid.UUID
	EmployeeID string
	Name       string
	Scopes     []string
	TerminalID string
	ExpiresAt  time.Time
}
