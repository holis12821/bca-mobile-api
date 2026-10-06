package ticket

import (
	"context"

	"github.com/google/uuid"
)

// Repository adalah akses data tiket layanan.
type Repository interface {
	// Create menyisipkan tiket dan MENGISI nomor serta stempel waktunya.
	//
	// Nomornya diterbitkan database dari sequence, bukan dirakit service: dua petugas
	// yang membuat tiket di saat yang sama harus mendapat nomor berbeda, dan satu-satunya
	// tempat yang bisa menjamin itu adalah tempat yang menyimpannya.
	Create(ctx context.Context, t *Ticket) error

	// FindByNumber mengembalikan tiket TANPA catatannya, dicari lewat nomor tiketnya.
	// Mengembalikan nil, nil kalau tidak ada.
	//
	// Dicari lewat nomor, bukan UUID: TKT-20261005-000123 adalah yang tampil di layar
	// petugas dan yang disebutkan nasabah lewat telepon, sementara UUID-nya tidak pernah
	// keluar dari server. Pola yang sama dengan session_id onboarding dan queue_id
	// video call.
	FindByNumber(ctx context.Context, number string) (*Ticket, error)

	// List mengembalikan tiket terurut created_at DESC, id DESC.
	// Mengembalikan limit+1 baris supaya pemanggil bisa menghitung has_more.
	List(ctx context.Context, filter ListFilter) ([]*Ticket, error)

	// Update menyimpan perubahan status, prioritas, kategori, dan penugasan.
	//
	// Menerima tiket yang stempel waktunya SUDAH disesuaikan service — resolved_at dan
	// closed_at punya CHECK yang mengikatnya ke status, jadi keduanya harus bergerak
	// bersama dalam satu tulis.
	Update(ctx context.Context, t *Ticket) error

	// AddNote menyisipkan satu catatan tindak lanjut.
	AddNote(ctx context.Context, ticketID uuid.UUID, note *Note) error

	// ListNotes mengembalikan catatan sebuah tiket, terlama lebih dulu.
	ListNotes(ctx context.Context, ticketID uuid.UUID) ([]Note, error)
}
