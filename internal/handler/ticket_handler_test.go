package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/ticket"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
)

// stubTicketRepo adalah penyimpanan in-memory. Tidak menegakkan CHECK database — itu
// sudah diuji di test service; di sini yang diuji lapisan HTTP.
type stubTicketRepo struct {
	tickets map[uuid.UUID]*ticket.Ticket
	notes   map[uuid.UUID][]ticket.Note
	filters []ticket.ListFilter
}

func newStubTicketRepo() *stubTicketRepo {
	return &stubTicketRepo{
		tickets: make(map[uuid.UUID]*ticket.Ticket),
		notes:   make(map[uuid.UUID][]ticket.Note),
	}
}

func (s *stubTicketRepo) Create(_ context.Context, t *ticket.Ticket) error {
	t.TicketNumber = "TKT-20261005-000001"
	copied := *t
	s.tickets[t.ID] = &copied
	return nil
}

func (s *stubTicketRepo) FindByNumber(_ context.Context, number string) (*ticket.Ticket, error) {
	for _, t := range s.tickets {
		if t.TicketNumber == number {
			copied := *t
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *stubTicketRepo) List(_ context.Context, f ticket.ListFilter) ([]*ticket.Ticket, error) {
	s.filters = append(s.filters, f)
	var out []*ticket.Ticket
	for _, t := range s.tickets {
		out = append(out, t)
	}
	return out, nil
}

func (s *stubTicketRepo) Update(_ context.Context, t *ticket.Ticket) error {
	copied := *t
	s.tickets[t.ID] = &copied
	return nil
}

func (s *stubTicketRepo) AddNote(_ context.Context, id uuid.UUID, n *ticket.Note) error {
	s.notes[id] = append(s.notes[id], *n)
	return nil
}

func (s *stubTicketRepo) ListNotes(_ context.Context, id uuid.UUID) ([]ticket.Note, error) {
	return s.notes[id], nil
}

type stubTicketLookup struct{}

func (stubTicketLookup) AuthenticateAgent(_ context.Context, _, _ string) (string, []string, bool, error) {
	return "Sarah Adisti", []string{middleware.ScopeTicket}, true, nil
}

func newTicketRouter(withAgent bool) (*chi.Mux, *stubTicketRepo) {
	repo := newStubTicketRepo()
	h := handler.NewTicketHandler(ticket.NewService(ticket.ServiceConfig{Repo: repo}))

	r := chi.NewMux()
	r.Route("/tickets", func(r chi.Router) {
		if withAgent {
			r.Use(middleware.AgentAuth(stubTicketLookup{}, middleware.ScopeTicket))
		}
		r.Post("/", h.CreateTicket)
		r.Get("/", h.ListTickets)
		r.Get("/{ticket_number}", h.GetTicket)
		r.Patch("/{ticket_number}", h.UpdateTicket)
		r.Post("/{ticket_number}/notes", h.AddNote)
	})
	return r, repo
}

func ticketRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Employee-ID", "CS-1042")
	req.Header.Set("X-Agent-API-Key", "secret")
	return req
}

func serveTicket(r *chi.Mux, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// --- create ---

func TestTicketCreate_UsesAuthenticatedAgentAsCreator(t *testing.T) {
	r, repo := newTicketRouter(true)

	rr := serveTicket(r, ticketRequest(http.MethodPost, "/tickets", map[string]any{
		"subject":  "Kartu tertelan di ATM Sudirman",
		"category": "KARTU",
	}))

	if rr.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", rr.Code, rr.Body.String())
	}
	if len(repo.tickets) != 1 {
		t.Fatalf("%d tiket tersimpan", len(repo.tickets))
	}
	for _, tk := range repo.tickets {
		// Pembuatnya datang dari kredensial, bukan dari body — tidak ada field body
		// yang bisa mengakuinya.
		if tk.CreatedByAgent != "CS-1042" {
			t.Fatalf("created_by_agent = %q", tk.CreatedByAgent)
		}
	}
}

func TestTicketCreate_RejectsBadBody(t *testing.T) {
	r, _ := newTicketRouter(true)

	cases := []any{
		map[string]any{},                                  // tanpa subject
		map[string]any{"subject": "   "},                  // subject kosong
		map[string]any{"subject": "x", "category": "ZZZ"}, // kategori tak dikenal
		map[string]any{"subject": "x", "priority": "ZZZ"}, // prioritas tak dikenal
	}
	for _, body := range cases {
		rr := serveTicket(r, ticketRequest(http.MethodPost, "/tickets", body))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, mau 400", body, rr.Code)
		}
	}
}

func TestTicketCreate_RejectsMalformedJSON(t *testing.T) {
	r, _ := newTicketRouter(true)

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewBufferString("{bukan json"))
	req.Header.Set("X-Agent-Employee-ID", "CS-1042")
	req.Header.Set("X-Agent-API-Key", "secret")

	if rr := serveTicket(r, req); rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, mau 400", rr.Code)
	}
}

// --- list ---

// assigned_to=me harus diterjemahkan server dari kredensial. Client tidak perlu tahu
// employee_id-nya sendiri untuk melihat tiketnya sendiri.
func TestTicketList_ResolvesAssignedToMe(t *testing.T) {
	r, repo := newTicketRouter(true)

	if rr := serveTicket(r, ticketRequest(http.MethodGet, "/tickets?assigned_to=me", nil)); rr.Code != http.StatusOK {
		t.Fatalf("code = %d", rr.Code)
	}
	if len(repo.filters) != 1 {
		t.Fatalf("%d filter terekam", len(repo.filters))
	}
	if repo.filters[0].AssignedTo != "CS-1042" {
		t.Fatalf("assigned_to = %q, mau diterjemahkan jadi CS-1042", repo.filters[0].AssignedTo)
	}
}

func TestTicketList_RejectsUnknownFilters(t *testing.T) {
	r, _ := newTicketRouter(true)

	for _, q := range []string{
		"?status=NGAWUR",
		"?category=NGAWUR",
		"?user_id=bukan-uuid",
		"?limit=0",
		"?limit=99999",
		"?cursor=bukanbase64!!",
	} {
		rr := serveTicket(r, ticketRequest(http.MethodGet, "/tickets"+q, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, mau 400", q, rr.Code)
		}
	}
}

// --- get / update / notes ---

func TestTicketGet_UnknownIs404(t *testing.T) {
	r, _ := newTicketRouter(true)

	rr := serveTicket(r, ticketRequest(http.MethodGet, "/tickets/TKT-tidak-ada", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, mau 404", rr.Code)
	}
}

func TestTicketGet_RejectsMalformedUUID(t *testing.T) {
	r, _ := newTicketRouter(true)

	rr := serveTicket(r, ticketRequest(http.MethodGet, "/tickets/"+strings.Repeat("x", 40), nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, mau 400", rr.Code)
	}
}

func TestTicketUpdate_MovesStatusAndStamps(t *testing.T) {
	r, repo := newTicketRouter(true)
	id := uuid.New()
	repo.tickets[id] = &ticket.Ticket{
		ID: id, TicketNumber: "TKT-20261005-000001", Subject: "x",
		Status: ticket.StatusOpen, Priority: ticket.PriorityNormal,
		Category: ticket.CategoryKartu, CreatedByAgent: "CS-1042",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	rr := serveTicket(r, ticketRequest(http.MethodPatch, ticketPath(repo, id),
		map[string]any{"status": "RESOLVED"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Data ticket.Ticket `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Status != ticket.StatusResolved {
		t.Fatalf("status = %q", body.Data.Status)
	}
	if body.Data.ResolvedAt == nil {
		t.Fatal("resolved_at tidak ikut terisi di response")
	}
}

func TestTicketUpdate_ClosedCannotReopen(t *testing.T) {
	r, repo := newTicketRouter(true)
	id := uuid.New()
	now := time.Now()
	repo.tickets[id] = &ticket.Ticket{
		ID: id, TicketNumber: "TKT-20261005-000002", Subject: "x", Status: ticket.StatusClosed,
		Priority: ticket.PriorityNormal, Category: ticket.CategoryKartu,
		ResolvedAt: &now, ClosedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}

	rr := serveTicket(r, ticketRequest(http.MethodPatch, ticketPath(repo, id),
		map[string]any{"status": "OPEN"}))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, mau 422", rr.Code)
	}
}

func TestTicketAddNote_RecordsAuthorFromCredentials(t *testing.T) {
	r, repo := newTicketRouter(true)
	id := uuid.New()
	repo.tickets[id] = &ticket.Ticket{
		ID: id, TicketNumber: "TKT-20261005-000003", Subject: "x", Status: ticket.StatusOpen,
		Priority: ticket.PriorityNormal, Category: ticket.CategoryKartu,
	}

	rr := serveTicket(r, ticketRequest(http.MethodPost, notesPath(repo, id),
		map[string]any{"body": "Nasabah sudah dihubungi."}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", rr.Code, rr.Body.String())
	}
	if len(repo.notes[id]) != 1 {
		t.Fatalf("%d catatan", len(repo.notes[id]))
	}
	if repo.notes[id][0].Author != "CS-1042" {
		t.Fatalf("author = %q", repo.notes[id][0].Author)
	}
}

func TestTicketAddNote_RejectsBlankBody(t *testing.T) {
	r, repo := newTicketRouter(true)
	id := uuid.New()
	repo.tickets[id] = &ticket.Ticket{ID: id, TicketNumber: "TKT-20261005-000004", Subject: "x", Status: ticket.StatusOpen}

	rr := serveTicket(r, ticketRequest(http.MethodPost, notesPath(repo, id),
		map[string]any{"body": "   "}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, mau 400", rr.Code)
	}
}

// Rute tiket yang terpasang tanpa AgentAuth harus menolak: tiket tanpa pembuat yang
// tercatat tidak bisa dipertanggungjawabkan ke siapa pun.
func TestTicketEndpoints_DenyWhenMountedWithoutAgentAuth(t *testing.T) {
	r, repo := newTicketRouter(false)
	id := uuid.New()
	repo.tickets[id] = &ticket.Ticket{ID: id, TicketNumber: "TKT-20261005-000004", Subject: "x", Status: ticket.StatusOpen}

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/tickets", map[string]any{"subject": "x"}},
		{http.MethodPatch, ticketPath(repo, id), map[string]any{"status": "RESOLVED"}},
		{http.MethodPost, notesPath(repo, id), map[string]any{"body": "x"}},
		{http.MethodGet, "/tickets?assigned_to=me", nil},
	}
	for _, c := range cases {
		rr := serveTicket(r, ticketRequest(c.method, c.path, c.body))
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, mau 403", c.method, c.path, rr.Code)
		}
	}
	if len(repo.tickets) != 1 {
		t.Fatal("tiket dibuat tanpa pembuat yang tercatat")
	}
}

// ticketPath dan notesPath menyusun URL dari NOMOR tiket, bukan UUID-nya — sama dengan
// yang dilakukan aplikasi desktop, yang tidak pernah melihat UUID itu.
func ticketPath(repo *stubTicketRepo, id uuid.UUID) string {
	return "/tickets/" + repo.tickets[id].TicketNumber
}

func notesPath(repo *stubTicketRepo, id uuid.UUID) string {
	return ticketPath(repo, id) + "/notes"
}
