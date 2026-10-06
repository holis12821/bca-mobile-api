package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/ticket"
)

// TicketRepo adalah akses data tiket layanan di Postgres.
type TicketRepo struct {
	pool *pgxpool.Pool
}

func NewTicketRepo(pool *pgxpool.Pool) *TicketRepo {
	return &TicketRepo{pool: pool}
}

// selectTicket adalah kolom yang dipakai FindByNumber dan List. Satu tempat supaya urutan
// Scan tidak pernah berbeda antar keduanya.
const selectTicket = `
	SELECT id, ticket_number, user_id, COALESCE(session_id, ''),
	       category, priority, status,
	       subject, description,
	       created_by_agent, COALESCE(assigned_to_agent, ''),
	       resolved_at, closed_at, created_at, updated_at
	FROM service_tickets`

func scanTicket(row pgx.Row) (*ticket.Ticket, error) {
	var t ticket.Ticket
	var category, priority, status string

	err := row.Scan(
		&t.ID, &t.TicketNumber, &t.UserID, &t.SessionID,
		&category, &priority, &status,
		&t.Subject, &t.Description,
		&t.CreatedByAgent, &t.AssignedToAgent,
		&t.ResolvedAt, &t.ClosedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan ticket: %w", err)
	}

	t.Category = ticket.Category(category)
	t.Priority = ticket.Priority(priority)
	t.Status = ticket.Status(status)
	return &t, nil
}

// Create menyisipkan tiket dan mengembalikan nomor yang diterbitkan database.
//
// ticket_number dirakit DI DALAM INSERT dari service_ticket_number_seq, bukan dihitung
// lebih dulu lalu dikirim: dua petugas yang menekan "buat tiket" pada milidetik yang sama
// harus mendapat nomor berbeda, dan satu-satunya hal yang bisa menjaminnya adalah
// sequence. Formatnya TKT-YYYYMMDD-000123.
func (r *TicketRepo) Create(ctx context.Context, t *ticket.Ticket) error {
	var sessionID *string
	if t.SessionID != "" {
		sessionID = &t.SessionID
	}
	var assignee *string
	if t.AssignedToAgent != "" {
		assignee = &t.AssignedToAgent
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO service_tickets
			(id, ticket_number, user_id, session_id, category, priority, status,
			 subject, description, created_by_agent, assigned_to_agent,
			 created_at, updated_at)
		VALUES ($1,
		        'TKT-' || to_char($11::timestamptz AT TIME ZONE 'Asia/Jakarta', 'YYYYMMDD')
		              || '-' || lpad(nextval('service_ticket_number_seq')::text, 6, '0'),
		        $2, $3, $4::ticket_category, $5::ticket_priority, $6::ticket_status,
		        $7, $8, $9, $10, $11, $11)
		RETURNING ticket_number`,
		t.ID, t.UserID, sessionID,
		string(t.Category), string(t.Priority), string(t.Status),
		t.Subject, t.Description, t.CreatedByAgent, assignee,
		t.CreatedAt,
	).Scan(&t.TicketNumber)
	if err != nil {
		return fmt.Errorf("insert ticket: %w", err)
	}
	return nil
}

func (r *TicketRepo) FindByNumber(ctx context.Context, number string) (*ticket.Ticket, error) {
	return scanTicket(r.pool.QueryRow(ctx, selectTicket+` WHERE ticket_number = $1`, number))
}

// List mengembalikan tiket terurut created_at DESC, id DESC, dengan limit+1 baris.
func (r *TicketRepo) List(ctx context.Context, filter ticket.ListFilter) ([]*ticket.Ticket, error) {
	args := []any{}
	where := []string{"TRUE"}

	// Placeholder dinomori saat argumennya ditambahkan, bukan dihitung manual — pola
	// yang sama dengan ListForCS, dan dengan alasan yang sama.
	add := func(clause string, vals ...any) {
		for i := range vals {
			args = append(args, vals[i])
			clause = strings.Replace(clause, "?", fmt.Sprintf("$%d", len(args)), 1)
		}
		where = append(where, clause)
	}

	if filter.Status != "" {
		add("status = ?::ticket_status", string(filter.Status))
	}
	if filter.Category != "" {
		add("category = ?::ticket_category", string(filter.Category))
	}
	if filter.AssignedTo != "" {
		add("assigned_to_agent = ?", filter.AssignedTo)
	}
	if filter.UserID != nil {
		add("user_id = ?", *filter.UserID)
	}
	if filter.Cursor != nil {
		add("(created_at, id) < (?, ?)", filter.Cursor.CreatedAt, filter.Cursor.ID)
	}

	args = append(args, filter.Limit+1)

	query := fmt.Sprintf(selectTicket+`
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d`, strings.Join(where, " AND "), len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	defer rows.Close()

	var out []*ticket.Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out = append(out, t)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tickets: %w", err)
	}
	return out, nil
}

// Update menyimpan status, prioritas, kategori, penugasan, dan stempel waktunya.
//
// Keempatnya plus kedua stempel dalam SATU UPDATE, bukan beberapa: CHECK
// service_tickets_resolved_consistent menguji status bersama resolved_at, jadi dua
// UPDATE terpisah akan ditolak di yang pertama.
func (r *TicketRepo) Update(ctx context.Context, t *ticket.Ticket) error {
	var assignee *string
	if t.AssignedToAgent != "" {
		assignee = &t.AssignedToAgent
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE service_tickets
		SET status            = $2::ticket_status,
		    priority          = $3::ticket_priority,
		    category          = $4::ticket_category,
		    assigned_to_agent = $5,
		    resolved_at       = $6,
		    closed_at         = $7,
		    updated_at        = $8
		WHERE id = $1`,
		t.ID, string(t.Status), string(t.Priority), string(t.Category),
		assignee, t.ResolvedAt, t.ClosedAt, t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("update ticket: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ticket not found")
	}
	return nil
}

func (r *TicketRepo) AddNote(ctx context.Context, ticketID uuid.UUID, note *ticket.Note) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO service_ticket_notes (id, ticket_id, author, body, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		note.ID, ticketID, note.Author, note.Body, note.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert ticket note: %w", err)
	}

	// updated_at tiket ikut maju: catatan baru adalah pergerakan tiket, dan daftar yang
	// diurut pergerakan terakhir akan menempatkannya salah tanpa ini.
	if _, err := r.pool.Exec(ctx,
		`UPDATE service_tickets SET updated_at = $2 WHERE id = $1`,
		ticketID, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("touch ticket after note: %w", err)
	}
	return nil
}

// ListNotes mengembalikan catatan sebuah tiket, TERLAMA lebih dulu.
//
// Berbeda arah dengan daftar tiket, dan itu sengaja: tiket dibaca sebagai percakapan
// dari awal, sementara daftar tiket dibaca sebagai yang terbaru lebih dulu.
func (r *TicketRepo) ListNotes(ctx context.Context, ticketID uuid.UUID) ([]ticket.Note, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, author, body, created_at
		FROM service_ticket_notes
		WHERE ticket_id = $1
		ORDER BY created_at ASC`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list ticket notes: %w", err)
	}
	defer rows.Close()

	var out []ticket.Note
	for rows.Next() {
		var n ticket.Note
		if err := rows.Scan(&n.ID, &n.Author, &n.Body, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ticket note: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ticket notes: %w", err)
	}
	return out, nil
}
