package cs

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CustomerRepository membaca nasabah untuk keperluan petugas CS.
//
// Semua pencarian di sini COCOK PERSIS. Tidak ada LIKE, tidak ada prefix, tidak ada
// pencocokan nama — itu keputusan desain, bukan kekurangan. Pencocokan sebagian mengubah
// endpoint pencarian menjadi alat ekspor daftar nasabah: `?q=a` akan menjawab ribuan
// orang, satu halaman per permintaan, dari kredensial petugas mana pun.
type CustomerRepository interface {
	// FindByAccountNumber mencari pemilik sebuah nomor rekening.
	// Mengembalikan nil, nil kalau tidak ada.
	FindByAccountNumber(ctx context.Context, accountNumber string) (*Customer, error)

	// FindByPhoneHashes mencari nasabah yang phone_hash-nya ada di daftar.
	//
	// Menerima BEBERAPA hash, bukan satu, karena phone_hash dihitung dari nomor apa
	// adanya seperti diketik nasabah saat mendaftar — bukan dari bentuk E.164 yang
	// ternormalisasi. Satu orang bisa saja tersimpan sebagai "08123…" sementara petugas
	// mengetik "+62812…", dan keduanya menghasilkan hash yang berbeda. Service menyusun
	// kandidat bentuknya; repository mencocokkan semuanya dalam satu query ber-indeks.
	FindByPhoneHashes(ctx context.Context, phoneHashes []string) ([]*Customer, error)

	// ListAccounts mengembalikan rekening milik seorang nasabah, utama lebih dulu.
	ListAccounts(ctx context.Context, userID uuid.UUID) ([]Account, error)

	// FindByID mencari nasabah berdasarkan id. Mengembalikan nil, nil kalau tidak ada.
	FindByID(ctx context.Context, userID uuid.UUID) (*Customer, error)
}

// AccessLogRepository mencatat setiap sentuhan petugas ke data nasabah.
//
// Append-only di tingkat skema (trigger di migrasi 000029), jadi tidak ada Update
// maupun Delete di sini — bukan karena belum dibutuhkan, tapi karena keduanya akan
// ditolak database.
type AccessLogRepository interface {
	Insert(ctx context.Context, log *AccessLog) error
}

// PhoneHasher menghitung hash pencarian deterministik untuk sebuah nomor.
//
// Dipenuhi *crypto.HMACHasher secara struktural, supaya paket domain ini tidak perlu
// bergantung pada paket crypto — pola yang sama dipakai AgentLookup di middleware.
type PhoneHasher interface {
	Hash(input string) string
}

// --- Terminal, sesi petugas, dan jejak audit ---

// TerminalRepository adalah akses data terminal dan gerbang kesiapannya.
type TerminalRepository interface {
	// Register menyisipkan terminal baru berstatus REGISTERED.
	// Mengembalikan apperr.TerminalAlreadyRegistered kalau terminal_id-nya sudah ada.
	Register(ctx context.Context, t *Terminal) error

	// FindByID mengembalikan terminal, atau nil, nil kalau tidak ada.
	FindByID(ctx context.Context, terminalID string) (*Terminal, error)

	// Activate memindahkan terminal ke ONLINE dan mengikatnya ke petugas.
	//
	// Mengembalikan apperr.TerminalAgentBusy kalau petugasnya SUDAH ONLINE di terminal
	// lain — dijawab dari pelanggaran unique index, bukan dari SELECT lebih dulu: dua
	// permintaan bersamaan akan sama-sama melihat "belum ada yang online".
	Activate(ctx context.Context, terminalID, employeeID string, at time.Time) error

	// Deactivate memindahkan terminal ke OFFLINE dan melepas ikatannya.
	// Mengembalikan false kalau tidak ada yang berubah.
	Deactivate(ctx context.Context, terminalID string, at time.Time) (bool, error)

	// SetStatus memindahkan status terminal tanpa menyentuh ikatan petugas.
	// Dipakai transisi REGISTERED → READY saat ketiga gerbang lolos.
	SetStatus(ctx context.Context, terminalID string, status TerminalStatus, at time.Time) error

	// UpsertGate mencatat satu gerbang kesiapan yang lolos.
	//
	// MENIMPA baris gerbang yang sama untuk sesi itu, tidak menumpuk: healthcheck yang
	// diulang harus menggantikan hasil lama, bukan meninggalkan baris kedaluwarsa yang
	// tetap terbaca sebagai lolos.
	UpsertGate(ctx context.Context, rec *GateRecord) error

	// ListGates mengembalikan gerbang yang tercatat untuk sebuah sesi.
	ListGates(ctx context.Context, sessionID uuid.UUID) ([]GateRecord, error)
}

// SupervisorRepository memverifikasi dan mendaftar supervisor.
type SupervisorRepository interface {
	// ListActive mengembalikan supervisor aktif, tanpa token-nya.
	ListActive(ctx context.Context, location string) ([]Supervisor, error)

	// Authenticate memverifikasi token dual-control.
	//
	// Tiga keadaan dibedakan seperti AuthenticateAgent: (nama, true, nil) sah,
	// ("", false, nil) token salah atau supervisor nonaktif, dan ("", false, err) hanya
	// kegagalan infrastruktur — yang TIDAK boleh diperlakukan sebagai penolakan.
	Authenticate(ctx context.Context, supervisorID, token string) (name string, ok bool, err error)
}

// AgentSessionRepository mengelola sesi petugas.
type AgentSessionRepository interface {
	// Create membuka sesi baru, menutup sesi hidup petugas itu sebagai SUPERSEDED.
	//
	// Keduanya dalam SATU transaksi: sesi lama yang tertutup tanpa sesi baru terbuka
	// akan membuat petugas kehilangan gilirannya karena kegagalan di tengah jalan.
	Create(ctx context.Context, sess *AgentSession, tokenHash string) error

	// FindByToken mengembalikan identitas pembawa token, atau nil, nil kalau tokennya
	// tidak dikenal, sudah ditutup, atau tenggatnya lewat.
	FindByToken(ctx context.Context, tokenHash string) (*SessionContext, error)

	// FindLiveByEmployee mengembalikan sesi hidup petugas, atau nil, nil.
	FindLiveByEmployee(ctx context.Context, employeeID string) (*AgentSession, error)

	// End menutup sesi. Mengembalikan false kalau sudah tertutup.
	End(ctx context.Context, sessionID uuid.UUID, reason string, at time.Time) (bool, error)

	// Touch memperbarui last_seen_at. Kegagalannya tidak pernah menggagalkan permintaan.
	Touch(ctx context.Context, sessionID uuid.UUID, at time.Time) error
}

// AgentCredentialRepository mengelola kata sandi petugas, terpisah dari kunci API.
type AgentCredentialRepository interface {
	// FindForLogin mengembalikan data yang dibutuhkan login: hash kata sandi, nama,
	// cakupan, dan keadaan lockout. Mengembalikan nil, nil kalau petugasnya tidak ada
	// atau tidak aktif.
	FindForLogin(ctx context.Context, employeeID string) (*AgentLoginRecord, error)

	// SetPassword menyimpan hash kata sandi baru dan mengosongkan penghitung gagal.
	SetPassword(ctx context.Context, employeeID, passwordHash string, at time.Time) error

	// RecordLoginFailure menaikkan penghitung dan memasang lockout saat ambangnya lewat.
	// Mengembalikan keadaan lockout SETELAH kenaikan.
	RecordLoginFailure(ctx context.Context, employeeID string, max int, lockFor time.Duration, now time.Time) (lockedUntil *time.Time, err error)

	// ClearLoginFailures dipanggil setelah login berhasil.
	ClearLoginFailures(ctx context.Context, employeeID string) error
}

// AgentLoginRecord adalah baris cs_agents yang dibutuhkan jalur login.
//
// Tidak pernah di-encode ke JSON: ia memuat hash kata sandi.
type AgentLoginRecord struct {
	EmployeeID   string
	Name         string
	Scopes       []string
	PasswordHash string
	LockedUntil  *time.Time
}

// AgentRegistry mendaftarkan petugas baru.
//
// Terpisah dari AgentCredentialRepository: yang itu melayani login petugas yang SUDAH
// ada, sementara ini menambah barisnya. Jalur tulis yang menciptakan kewenangan tidak
// semestinya menumpang antarmuka yang dipanggil setiap login.
type AgentRegistry interface {
	// Register menyisipkan baris cs_agents baru.
	//
	// Mengembalikan apperr.AgentAlreadyRegistered kalau NPP-nya sudah ada — dijawab dari
	// pelanggaran primary key, bukan SELECT lebih dulu: dua pendaftaran bersamaan akan
	// sama-sama melihat "NPP belum ada".
	Register(ctx context.Context, employeeID, name, apiKeyHash string, scopes []string, at time.Time) error
}

// AuditEventRepository menulis dan mencari jejak petugas/terminal.
type AuditEventRepository interface {
	// Insert menulis satu peristiwa.
	Insert(ctx context.Context, ev *AuditEvent) error

	// List mengembalikan peristiwa terurut created_at DESC, id DESC, limit+1 baris.
	List(ctx context.Context, filter AuditFilter) ([]AuditEvent, error)
}
