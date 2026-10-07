package cs

import (
	"context"
	"log/slog"
	"strings"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// Cakupan kewenangan petugas. Cocok dengan CHECK `cs_agents_scopes_valid` di migrasi
// 000027.
//
// Didefinisikan di sini, BUKAN diambil dari paket middleware: domain tidak boleh
// bergantung pada lapisan HTTP, dan jalur pendaftaran petugas harus bisa menolak cakupan
// tak dikenal tanpa sebuah request. Nilainya sengaja sama persis — kalau salah satu
// berubah, CHECK di skema yang akan menangkapnya.
const (
	ScopeVideoCall   = "VIDEO_CALL"
	ScopeCardAdmin   = "CARD_ADMIN"
	ScopeCustomerPII = "CUSTOMER_PII"
	ScopeTicket      = "TICKET"
)

// AllScopes adalah keempat cakupan yang boleh diberikan.
var AllScopes = []string{ScopeVideoCall, ScopeCardAdmin, ScopeCustomerPII, ScopeTicket}

// ValidScope melaporkan apakah sebuah cakupan dikenal.
func ValidScope(s string) bool {
	for _, candidate := range AllScopes {
		if candidate == s {
			return true
		}
	}
	return false
}

// HRISEmployee adalah satu pegawai menurut direktori HRIS.
//
// TIDAK disimpan di database ini. HRIS adalah sistem luar dan pemilik kebenaran soal
// siapa pegawai aktif; menyalin tabel pegawai ke sini akan membuat salinan yang basi
// tepat ketika seseorang berhenti bekerja.
type HRISEmployee struct {
	EmployeeID string `json:"employee_id"`
	Name       string `json:"name"`
	Position   string `json:"position"`
	Branch     string `json:"branch"`

	// Active false berarti pegawainya ADA tapi statusnya dicabut. Dibedakan dari "tidak
	// ditemukan" dengan sengaja: menyamakan keduanya membuat pegawai yang statusnya
	// dicabut mengira ia salah mengetik NPP-nya, lalu mencoba lagi berkali-kali.
	Active bool `json:"active"`
}

// HRISDirectory mencari pegawai di direktori HRIS.
//
// Tiga jawaban yang WAJIB dibedakan:
//   - (pegawai, nil) — ditemukan. Lihat Active untuk tahu ia masih bekerja.
//   - (nil, apperr.EmployeeNotFound) — NPP itu bukan NPP siapa pun.
//   - (nil, apperr.HRISUnavailable) — direktorinya tidak bisa dihubungi. BUKAN penolakan;
//     boleh dicoba lagi.
//
// Yang ketiga sengaja 503 dan bukan 404: 404 akan terbaca sebagai "NPP tidak terdaftar",
// dan petugas yang mendapatkannya saat HRIS mati akan menyimpulkan hal yang salah tentang
// rekannya.
type HRISDirectory interface {
	LookupEmployee(ctx context.Context, employeeID string) (*HRISEmployee, error)
}

// HRISDirectoryFor mengembalikan direktori yang sesuai environment.
//
// Pola yang sama dengan onboarding.ProvidersFor, dan untuk alasan yang sama: mock yang
// terpasang tanpa gerbang environment akan membuat proses produksi menjawab 200 untuk
// NPP yang tidak pernah diperiksa siapa pun.
func HRISDirectoryFor(devMode bool) HRISDirectory {
	if devMode {
		return NewMockHRISDirectory()
	}

	slog.Warn("HRIS directory is not configured; " +
		"NPP lookup will refuse with HRIS_UNAVAILABLE")
	return unconfiguredHRIS{}
}

type unconfiguredHRIS struct{}

func (unconfiguredHRIS) LookupEmployee(context.Context, string) (*HRISEmployee, error) {
	return nil, apperr.HRISUnavailable
}

// MockHRISDirectory adalah direktori tiruan untuk pengembangan lokal.
//
// Isinya sengaja memuat ketiga keadaan yang harus bisa dibedakan — termasuk satu pegawai
// NONAKTIF. Mock yang semua NPP-nya aktif tidak pernah melatih jalur penolakan, dan
// jalur itu baru akan dicoba pertama kali oleh orang sungguhan yang sudah berhenti
// bekerja.
type MockHRISDirectory struct {
	employees map[string]HRISEmployee
}

func NewMockHRISDirectory() *MockHRISDirectory {
	return &MockHRISDirectory{
		employees: map[string]HRISEmployee{
			"CS-1042": {EmployeeID: "CS-1042", Name: "Sarah Adisti",
				Position: "CS Officer", Branch: "KCU Semarang", Active: true},
			"CS-2099": {EmployeeID: "CS-2099", Name: "Dimas Prakoso",
				Position: "CS Officer", Branch: "KCU Jakarta Thamrin", Active: true},
			"OPS-2001": {EmployeeID: "OPS-2001", Name: "Budi Hartono",
				Position: "Operations Staff", Branch: "KCU Semarang", Active: true},
			"SPV-3001": {EmployeeID: "SPV-3001", Name: "Rina Kusuma",
				Position: "CS Supervisor", Branch: "KCU Semarang", Active: true},

			// Pegawai yang statusnya sudah dicabut. Pendaftaran HARUS menolaknya, dan
			// dengan alasan yang berbeda dari "NPP tidak ditemukan".
			"CS-0001": {EmployeeID: "CS-0001", Name: "Agus Setiawan",
				Position: "CS Officer", Branch: "KCU Bandung", Active: false},
		},
	}
}

func (m *MockHRISDirectory) LookupEmployee(_ context.Context, employeeID string) (*HRISEmployee, error) {
	emp, ok := m.employees[strings.ToUpper(strings.TrimSpace(employeeID))]
	if !ok {
		return nil, apperr.EmployeeNotFound
	}
	// Salinan, bukan pointer ke peta: pemanggil yang menyuntingnya tidak boleh mengubah
	// isi direktori untuk pemanggil berikutnya.
	out := emp
	return &out, nil
}
