// Package ticket mengurus tiket layanan Halo BCA: keluhan dan permintaan nasabah yang
// tidak selesai dalam satu panggilan.
//
// Seluruhnya ditulis petugas. Tidak ada endpoint nasabah yang menyentuh paket ini —
// tiket lahir saat petugas mengangkat telepon, bukan saat nasabah menekan tombol.
package ticket

import (
	"time"

	"github.com/google/uuid"
)

// Status adalah siklus hidup tiket.
//
// OPEN → IN_PROGRESS → RESOLVED → CLOSED, dan boleh melompat maju. Yang tidak boleh
// adalah mundur dari CLOSED — lihat [ValidTransition].
type Status string

const (
	StatusOpen       Status = "OPEN"
	StatusInProgress Status = "IN_PROGRESS"
	StatusResolved   Status = "RESOLVED"
	StatusClosed     Status = "CLOSED"
)

// Priority menentukan urutan tangan, bukan SLA. Tidak ada mekanisme yang memaksa URGENT
// dikerjakan lebih dulu; ia penanda untuk manusia.
type Priority string

const (
	PriorityLow    Priority = "LOW"
	PriorityNormal Priority = "NORMAL"
	PriorityHigh   Priority = "HIGH"
	PriorityUrgent Priority = "URGENT"
)

// Category mengikuti ENUM di migrasi 000030. Menambah satu di sini tanpa menambahnya di
// sana menghasilkan kategori yang selalu ditolak database.
type Category string

const (
	CategoryKartu        Category = "KARTU"
	CategoryTransaksi    Category = "TRANSAKSI"
	CategoryAkun         Category = "AKUN"
	CategoryBukaRekening Category = "BUKA_REKENING"
	CategoryAplikasi     Category = "APLIKASI"
	CategoryLainnya      Category = "LAINNYA"
)

var (
	validStatuses = map[Status]bool{
		StatusOpen: true, StatusInProgress: true,
		StatusResolved: true, StatusClosed: true,
	}
	validPriorities = map[Priority]bool{
		PriorityLow: true, PriorityNormal: true,
		PriorityHigh: true, PriorityUrgent: true,
	}
	validCategories = map[Category]bool{
		CategoryKartu: true, CategoryTransaksi: true, CategoryAkun: true,
		CategoryBukaRekening: true, CategoryAplikasi: true, CategoryLainnya: true,
	}
)

func ValidStatus(s Status) bool     { return validStatuses[s] }
func ValidPriority(p Priority) bool { return validPriorities[p] }
func ValidCategory(c Category) bool { return validCategories[c] }

// ValidTransition melaporkan apakah perpindahan status diizinkan.
//
// Satu-satunya yang dilarang adalah membuka kembali tiket yang sudah CLOSED. Tiket yang
// ditutup punya closed_at, dan menghidupkannya kembali membuat stempel itu berbohong —
// sementara keluhan yang muncul lagi memang layak jadi tiket baru yang merujuk yang lama.
//
// Perpindahan ke status yang sama diizinkan: menyimpan perubahan prioritas tanpa
// mengubah status adalah hal biasa, dan menolaknya hanya memaksa pemanggil menebak.
func ValidTransition(from, to Status) bool {
	if !ValidStatus(from) || !ValidStatus(to) {
		return false
	}
	if from == StatusClosed && to != StatusClosed {
		return false
	}
	return true
}

// Ticket adalah satu tiket layanan.
type Ticket struct {
	ID           uuid.UUID `json:"-"`
	TicketNumber string    `json:"ticket_number"`

	// UserID dan SessionID keduanya opsional, dan keduanya bisa terisi. Penelepon yang
	// belum punya rekening hanya punya session_id; nasabah lama hanya punya user_id.
	UserID    *uuid.UUID `json:"user_id,omitempty"`
	SessionID string     `json:"session_id,omitempty"`

	Category Category `json:"category"`
	Priority Priority `json:"priority"`
	Status   Status   `json:"status"`

	Subject     string `json:"subject"`
	Description string `json:"description"`

	CreatedByAgent  string `json:"created_by_agent"`
	AssignedToAgent string `json:"assigned_to_agent,omitempty"`

	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	// Notes hanya terisi di endpoint detail. Daftar tiket tidak memuatnya: riwayat
	// penanganan sepuluh tiket sekaligus adalah muatan besar yang tidak terbaca siapa pun.
	Notes []Note `json:"notes,omitempty"`
}

// Note adalah satu catatan tindak lanjut.
type Note struct {
	ID        uuid.UUID `json:"-"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateRequest adalah body POST /internal/v1/tickets.
type CreateRequest struct {
	UserID      string `json:"user_id"`
	SessionID   string `json:"session_id"`
	Category    string `json:"category"`
	Priority    string `json:"priority"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
}

// UpdateRequest adalah body PATCH /internal/v1/tickets/{id}.
//
// Semua field pointer supaya "tidak dikirim" terbedakan dari "dikosongkan". Tanpa itu,
// permintaan yang hanya mengubah status akan diam-diam menghapus penugasan.
type UpdateRequest struct {
	Status          *string `json:"status"`
	Priority        *string `json:"priority"`
	Category        *string `json:"category"`
	AssignedToAgent *string `json:"assigned_to_agent"`
}

// AddNoteRequest adalah body POST /internal/v1/tickets/{id}/notes.
type AddNoteRequest struct {
	Body string `json:"body"`
}

// ListFilter menyaring daftar tiket.
type ListFilter struct {
	Status     Status
	Category   Category
	AssignedTo string

	// UserID menyaring tiket milik satu nasabah: "dia pernah mengadu apa saja".
	UserID *uuid.UUID

	Limit  int
	Cursor *Cursor
}

// Cursor memegang nilai keyset untuk ORDER BY created_at DESC, id DESC.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}
