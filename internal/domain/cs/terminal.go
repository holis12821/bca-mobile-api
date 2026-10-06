package cs

import (
	"time"

	"github.com/google/uuid"
)

// TerminalStatus adalah siklus hidup terminal yang DISIMPAN server.
//
// Dokumen alur juga menyebut BUSY, CALL_ACTIVE, dan PROCESSING. Ketiganya TIDAK ada di
// sini: mereka diturunkan dari panggilan aktif petugasnya, bukan disimpan. Status
// tersimpan yang tidak punya satu-satunya sumber kebenaran akan melenceng dari tabel
// panggilan — dan yang melenceng akan dipercaya.
//
// ERROR juga tidak ada, karena tidak ada mekanisme yang menyetelnya.
type TerminalStatus string

const (
	// TerminalRegistered: barisnya ada, belum ada yang bertugas di atasnya.
	TerminalRegistered TerminalStatus = "REGISTERED"

	// TerminalReady: tiga gerbang kesiapan lolos, belum diaktifkan.
	TerminalReady TerminalStatus = "READY"

	// TerminalOnline: boleh mengambil antrean (Rule 4).
	TerminalOnline TerminalStatus = "ONLINE"

	// TerminalOffline: giliran ditutup.
	TerminalOffline TerminalStatus = "OFFLINE"
)

func ValidTerminalStatus(s TerminalStatus) bool {
	switch s {
	case TerminalRegistered, TerminalReady, TerminalOnline, TerminalOffline:
		return true
	}
	return false
}

// Terminal adalah satu loket petugas CS.
type Terminal struct {
	TerminalID  string         `json:"terminal_id"`
	Workstation string         `json:"workstation"`
	Location    string         `json:"location"`
	Status      TerminalStatus `json:"status"`

	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`

	ActiveAgentID string     `json:"active_agent_id,omitempty"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Gate adalah satu dari tiga gerbang kesiapan (Rule 3).
type Gate string

const (
	GateSupervisorAuth Gate = "SUPERVISOR_AUTH"
	GateDeviceHealth   Gate = "DEVICE_HEALTHCHECK"
	GatePIIAck         Gate = "PII_ACK"
)

// AllGates adalah ketiga gerbang yang HARUS lolos sebelum aktivasi.
//
// Satu tempat, supaya `activate` dan layar kesiapan tidak pernah berbeda pendapat soal
// apa saja yang diperiksa. Menambah gerbang di sini otomatis mengetatkan aktivasi.
var AllGates = []Gate{GateSupervisorAuth, GateDeviceHealth, GatePIIAck}

func ValidGate(g Gate) bool {
	for _, candidate := range AllGates {
		if candidate == g {
			return true
		}
	}
	return false
}

// GateState adalah keadaan satu gerbang untuk sebuah sesi.
type GateState struct {
	Gate   Gate `json:"gate"`
	Passed bool `json:"passed"`

	PassedAt  *time.Time `json:"passed_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Expired true berarti gerbangnya PERNAH lolos tapi sudah kedaluwarsa. Dibedakan
	// dari belum pernah lolos: yang pertama diselesaikan dengan mengulang probe, yang
	// kedua mungkin berarti petugas melewatkan satu layar.
	Expired bool `json:"expired"`

	AuthorizationRef string `json:"authorization_ref,omitempty"`
	SupervisorID     string `json:"supervisor_id,omitempty"`

	// Details hanya pada DEVICE_HEALTHCHECK: laporan KLIEN apa adanya.
	//
	// Server tidak pernah mengukur kamera atau mikrofon di meja petugas. Perlakukan
	// isinya sebagai PERNYATAAN, bukan pengukuran.
	Details map[string]any `json:"details,omitempty"`
}

// ReadinessState adalah jawaban `GET /terminals/{id}/readiness`.
type ReadinessState struct {
	TerminalID string         `json:"terminal_id"`
	Status     TerminalStatus `json:"status"`

	// SessionID sesi petugas yang gerbangnya ditampilkan.
	SessionID string `json:"session_id"`

	Gates []GateState `json:"gates"`

	// CanActivate true hanya kalau KETIGA gerbang lolos dan belum kedaluwarsa.
	// Dihitung server, bukan diserahkan ke klien: Rule 3 adalah aturan server.
	CanActivate bool `json:"can_activate"`
}

// GateRecord adalah satu baris cs_terminal_readiness.
type GateRecord struct {
	SessionID uuid.UUID
	Gate      Gate
	PassedAt  time.Time
	ExpiresAt time.Time

	AuthorizationRef string
	SupervisorID     string
	Details          map[string]any
}

// Umur gerbang kesiapan.
//
// Healthcheck 8 jam: satu giliran kerja. Lebih pendek akan memaksa petugas mengulang
// probe di tengah shift tanpa alasan; lebih panjang membuat healthcheck kemarin
// menyatakan sesuatu tentang kamera hari ini.
//
// Otorisasi supervisor dan pakta PII juga 8 jam, dengan alasan yang sama: keduanya
// menyatakan sesuatu tentang GILIRAN INI, bukan tentang orangnya secara permanen.
const GateTTL = 8 * time.Hour

// Supervisor adalah penandatangan otorisasi dual-control.
//
// TIDAK memuat token-nya. Tipe ini di-encode ke JSON di `GET /supervisors`, dan token
// dual-control yang ikut terbaca di sana akan diserahkan ke setiap petugas yang membuka
// daftarnya.
type Supervisor struct {
	SupervisorID string `json:"supervisor_id"`
	Name         string `json:"name"`
	Location     string `json:"location"`
}

// RegisterTerminalRequest adalah body `POST /internal/v1/terminals`.
type RegisterTerminalRequest struct {
	TerminalID  string `json:"terminal_id"`
	Workstation string `json:"workstation"`
	Location    string `json:"location"`
}

// HealthcheckRequest adalah body `POST /terminals/{id}/healthcheck`.
//
// `details` sengaja bebas: klien melaporkan apa yang ada di mejanya, dan daftar bidangnya
// akan berubah tanpa rilis server. Yang WAJIB hanya pernyataan lolos/tidaknya.
type HealthcheckRequest struct {
	Passed  bool           `json:"passed"`
	Details map[string]any `json:"details"`
}

// SupervisorAuthorizeRequest adalah body `POST /internal/v1/supervisors/authorize`.
type SupervisorAuthorizeRequest struct {
	SupervisorID string `json:"supervisor_id"`
	Token        string `json:"token"`
}

// SupervisorAuthorizeResponse mengembalikan TANDA TERIMA, bukan token.
type SupervisorAuthorizeResponse struct {
	AuthorizationRef string    `json:"authorization_ref"`
	SupervisorID     string    `json:"supervisor_id"`
	SupervisorName   string    `json:"supervisor_name"`
	AuthorizedAt     time.Time `json:"authorized_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// PIIAckRequest adalah body `POST /terminals/{id}/pii-ack`.
type PIIAckRequest struct {
	// Acknowledged harus true secara eksplisit. Pakta integritas yang tercatat dari
	// field kosong bukan pakta.
	Acknowledged bool `json:"acknowledged"`

	// PactVersion menyebut versi teks pakta yang disetujui petugas. Tanpa itu, jejaknya
	// tidak bisa menjawab "menyetujui APA" setelah teksnya direvisi.
	PactVersion string `json:"pact_version"`
}
