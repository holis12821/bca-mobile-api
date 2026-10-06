package ticket

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// Batas paginasi dan panjang teks.
const (
	defaultLimit = 20
	maxLimit     = 100

	maxSubjectLen     = 200 // sejalan dengan VARCHAR(200) di migrasi 000030
	maxDescriptionLen = 10_000
	maxNoteLen        = 10_000
)

// Service adalah logika bisnis tiket layanan.
type Service struct {
	repo  Repository
	clock func() time.Time
}

type ServiceConfig struct {
	Repo Repository

	// Clock disuntik test. Nil memakai time.Now.
	Clock func() time.Time
}

func NewService(cfg ServiceConfig) *Service {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{repo: cfg.Repo, clock: clock}
}

// Create membuat tiket baru atas nama petugas.
func (s *Service) Create(ctx context.Context, req CreateRequest, agentEmployeeID string) (*Ticket, error) {
	subject := strings.TrimSpace(req.Subject)
	if subject == "" || len(subject) > maxSubjectLen {
		return nil, apperr.ValidationError
	}

	description := strings.TrimSpace(req.Description)
	if len(description) > maxDescriptionLen {
		return nil, apperr.ValidationError
	}

	category := Category(strings.ToUpper(strings.TrimSpace(req.Category)))
	if category == "" {
		category = CategoryLainnya
	}
	if !ValidCategory(category) {
		return nil, apperr.ValidationError
	}

	priority := Priority(strings.ToUpper(strings.TrimSpace(req.Priority)))
	if priority == "" {
		priority = PriorityNormal
	}
	if !ValidPriority(priority) {
		return nil, apperr.ValidationError
	}

	var userID *uuid.UUID
	if raw := strings.TrimSpace(req.UserID); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return nil, apperr.ValidationError
		}
		userID = &parsed
	}

	now := s.clock()
	t := &Ticket{
		ID:             uuid.New(),
		UserID:         userID,
		SessionID:      strings.TrimSpace(req.SessionID),
		Category:       category,
		Priority:       priority,
		Status:         StatusOpen,
		Subject:        subject,
		Description:    description,
		CreatedByAgent: agentEmployeeID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.repo.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// find memuat tiket lewat nomornya, memetakan "tidak ada" ke 404.
//
// Satu tempat supaya ketiga jalur tulis tidak masing-masing memutuskan arti nil.
func (s *Service) find(ctx context.Context, number string) (*Ticket, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, apperr.ValidationError
	}
	t, err := s.repo.FindByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, apperr.NotFound
	}
	return t, nil
}

// Get mengembalikan satu tiket berikut catatannya.
func (s *Service) Get(ctx context.Context, number string) (*Ticket, error) {
	t, err := s.find(ctx, number)
	if err != nil {
		return nil, err
	}

	notes, err := s.repo.ListNotes(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	t.Notes = notes
	return t, nil
}

// List mengembalikan (tiket, hasMore, cursorBerikutnya).
func (s *Service) List(ctx context.Context, filter ListFilter) ([]*Ticket, bool, *Cursor, error) {
	if filter.Limit <= 0 {
		filter.Limit = defaultLimit
	}
	if filter.Limit > maxLimit {
		filter.Limit = maxLimit
	}

	rows, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, false, nil, err
	}

	hasMore := len(rows) > filter.Limit
	if hasMore {
		rows = rows[:filter.Limit]
	}

	var next *Cursor
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return rows, hasMore, next, nil
}

// Update mengubah status, prioritas, kategori, atau penugasan sebuah tiket.
//
// Stempel waktu disesuaikan DI SINI, bukan diserahkan ke pemanggil: resolved_at dan
// closed_at punya CHECK yang mengikatnya ke status (migrasi 000030), jadi status yang
// berubah tanpa stempelnya akan ditolak database dengan pesan yang tidak menolong
// siapa pun.
func (s *Service) Update(ctx context.Context, number string, req UpdateRequest) (*Ticket, error) {
	t, err := s.find(ctx, number)
	if err != nil {
		return nil, err
	}

	if req.Priority != nil {
		p := Priority(strings.ToUpper(strings.TrimSpace(*req.Priority)))
		if !ValidPriority(p) {
			return nil, apperr.ValidationError
		}
		t.Priority = p
	}

	if req.Category != nil {
		c := Category(strings.ToUpper(strings.TrimSpace(*req.Category)))
		if !ValidCategory(c) {
			return nil, apperr.ValidationError
		}
		t.Category = c
	}

	if req.AssignedToAgent != nil {
		// String kosong berarti melepas penugasan — berbeda dari field yang tidak
		// dikirim sama sekali, yang berarti biarkan apa adanya.
		t.AssignedToAgent = strings.TrimSpace(*req.AssignedToAgent)
	}

	if req.Status != nil {
		next := Status(strings.ToUpper(strings.TrimSpace(*req.Status)))
		if !ValidStatus(next) {
			return nil, apperr.ValidationError
		}
		if !ValidTransition(t.Status, next) {
			return nil, apperr.Error{
				Status:  http.StatusUnprocessableEntity,
				Code:    "TICKET_INVALID_TRANSITION",
				Message: "Tiket yang sudah ditutup tidak bisa dibuka kembali.",
			}
		}
		s.applyStatus(t, next)
	}

	t.UpdatedAt = s.clock()
	if err := s.repo.Update(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// applyStatus memindahkan status berikut stempel waktunya.
//
// resolved_at TIDAK ditimpa kalau sudah ada: tiket yang sempat RESOLVED lalu ditutup
// diselesaikan pada waktu yang pertama, dan laporan waktu penyelesaian yang memakai
// stempel kedua akan melaporkan angka yang lebih lama daripada kenyataannya.
func (s *Service) applyStatus(t *Ticket, next Status) {
	now := s.clock()

	switch next {
	case StatusResolved:
		if t.ResolvedAt == nil {
			t.ResolvedAt = &now
		}
		t.ClosedAt = nil

	case StatusClosed:
		if t.ResolvedAt == nil {
			t.ResolvedAt = &now
		}
		if t.ClosedAt == nil {
			t.ClosedAt = &now
		}

	default:
		// Kembali ke OPEN atau IN_PROGRESS: kedua stempel harus dibersihkan, atau CHECK
		// service_tickets_resolved_consistent akan menolak barisnya.
		t.ResolvedAt = nil
		t.ClosedAt = nil
	}

	t.Status = next
}

// AddNote menambahkan catatan tindak lanjut.
func (s *Service) AddNote(ctx context.Context, number string, req AddNoteRequest, agentEmployeeID string) (*Note, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" || len(body) > maxNoteLen {
		return nil, apperr.ValidationError
	}

	t, err := s.find(ctx, number)
	if err != nil {
		return nil, err
	}

	note := &Note{
		ID:        uuid.New(),
		Author:    agentEmployeeID,
		Body:      body,
		CreatedAt: s.clock(),
	}
	if err := s.repo.AddNote(ctx, t.ID, note); err != nil {
		return nil, err
	}
	return note, nil
}
