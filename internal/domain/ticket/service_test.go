package ticket

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// mockRepo meniru Postgres, termasuk CHECK yang mengikat stempel waktu ke status.
//
// CHECK-nya ditegakkan di sini juga, bukan hanya di database: tanpa itu, service yang
// lupa membersihkan resolved_at akan lulus seluruh test unit dan baru gagal saat
// menyentuh Postgres sungguhan.
type mockRepo struct {
	tickets map[uuid.UUID]*Ticket
	notes   map[uuid.UUID][]Note
	seq     int
	err     error

	// lastFilter merekam apa yang BENAR-BENAR diteruskan service. Batas limit yang
	// hanya diuji lewat konstantanya bukan batas yang diuji.
	lastFilter ListFilter
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		tickets: make(map[uuid.UUID]*Ticket),
		notes:   make(map[uuid.UUID][]Note),
	}
}

func (m *mockRepo) checkTimestamps(t *Ticket) error {
	resolvedSet := t.ResolvedAt != nil
	closedSet := t.ClosedAt != nil

	if (t.Status == StatusResolved || t.Status == StatusClosed) != resolvedSet {
		return errors.New("service_tickets_resolved_consistent violated")
	}
	if (t.Status == StatusClosed) != closedSet {
		return errors.New("service_tickets_closed_consistent violated")
	}
	return nil
}

func (m *mockRepo) Create(_ context.Context, t *Ticket) error {
	if m.err != nil {
		return m.err
	}
	if strings.TrimSpace(t.Subject) == "" {
		return errors.New("service_tickets_subject_not_blank violated")
	}
	if err := m.checkTimestamps(t); err != nil {
		return err
	}
	// Nomor harus unik: sejak rute memakai ticket_number, nomor yang tabrakan membuat
	// test memuat tiket yang salah dan gagal dengan cara yang membingungkan.
	m.seq++
	t.TicketNumber = fmt.Sprintf("TKT-20261005-%06d", m.seq)
	copied := *t
	m.tickets[t.ID] = &copied
	return nil
}

func (m *mockRepo) FindByNumber(_ context.Context, number string) (*Ticket, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, t := range m.tickets {
		if t.TicketNumber == number {
			copied := *t
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *mockRepo) List(_ context.Context, f ListFilter) ([]*Ticket, error) {
	m.lastFilter = f
	if m.err != nil {
		return nil, m.err
	}

	var out []*Ticket
	for _, t := range m.tickets {
		if f.Status != "" && t.Status != f.Status {
			continue
		}
		if f.Category != "" && t.Category != f.Category {
			continue
		}
		if f.AssignedTo != "" && t.AssignedToAgent != f.AssignedTo {
			continue
		}
		if f.UserID != nil && (t.UserID == nil || *t.UserID != *f.UserID) {
			continue
		}
		if f.Cursor != nil {
			if t.CreatedAt.After(f.Cursor.CreatedAt) {
				continue
			}
			if t.CreatedAt.Equal(f.Cursor.CreatedAt) && t.ID.String() >= f.Cursor.ID.String() {
				continue
			}
		}
		copied := *t
		out = append(out, &copied)
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID.String() > out[j].ID.String()
	})

	if f.Limit > 0 && len(out) > f.Limit+1 {
		out = out[:f.Limit+1]
	}
	return out, nil
}

func (m *mockRepo) Update(_ context.Context, t *Ticket) error {
	if m.err != nil {
		return m.err
	}
	if _, ok := m.tickets[t.ID]; !ok {
		return errors.New("ticket not found")
	}
	if err := m.checkTimestamps(t); err != nil {
		return err
	}
	copied := *t
	m.tickets[t.ID] = &copied
	return nil
}

func (m *mockRepo) AddNote(_ context.Context, id uuid.UUID, n *Note) error {
	if m.err != nil {
		return m.err
	}
	m.notes[id] = append(m.notes[id], *n)
	return nil
}

func (m *mockRepo) ListNotes(_ context.Context, id uuid.UUID) ([]Note, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.notes[id], nil
}

func setupTicket() (*Service, *mockRepo) {
	repo := newMockRepo()
	return NewService(ServiceConfig{Repo: repo}), repo
}

func mustCreate(t *testing.T, svc *Service, subject string) *Ticket {
	t.Helper()
	tk, err := svc.Create(context.Background(), CreateRequest{Subject: subject}, "CS-1042")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return tk
}

// --- create ---

func TestCreate_AppliesDefaults(t *testing.T) {
	svc, _ := setupTicket()

	tk := mustCreate(t, svc, "Kartu tertelan di ATM")

	if tk.Status != StatusOpen {
		t.Errorf("status = %q, mau OPEN", tk.Status)
	}
	if tk.Priority != PriorityNormal {
		t.Errorf("priority = %q, mau NORMAL", tk.Priority)
	}
	if tk.Category != CategoryLainnya {
		t.Errorf("category = %q, mau LAINNYA", tk.Category)
	}
	if tk.CreatedByAgent != "CS-1042" {
		t.Errorf("created_by_agent = %q", tk.CreatedByAgent)
	}
	if tk.TicketNumber == "" {
		t.Error("ticket_number kosong; nomor diterbitkan database dan harus kembali ke pemanggil")
	}
}

func TestCreate_RejectsBlankSubject(t *testing.T) {
	svc, _ := setupTicket()

	for _, subject := range []string{"", "   ", "\t\n"} {
		_, err := svc.Create(context.Background(), CreateRequest{Subject: subject}, "CS-1042")
		if err == nil {
			t.Errorf("subject %q diterima; daftar tiket tanpa subjek tidak bisa dibaca", subject)
		}
	}
}

func TestCreate_RejectsOverlongSubject(t *testing.T) {
	svc, _ := setupTicket()

	_, err := svc.Create(context.Background(), CreateRequest{
		Subject: strings.Repeat("a", maxSubjectLen+1),
	}, "CS-1042")
	if err == nil {
		t.Fatal("subject lebih panjang dari kolomnya diterima; akan gagal di database")
	}
}

func TestCreate_RejectsUnknownEnums(t *testing.T) {
	svc, _ := setupTicket()

	cases := []CreateRequest{
		{Subject: "x", Category: "NGAWUR"},
		{Subject: "x", Priority: "SANGAT_URGENT"},
		{Subject: "x", UserID: "bukan-uuid"},
	}
	for _, req := range cases {
		if _, err := svc.Create(context.Background(), req, "CS-1042"); err == nil {
			t.Errorf("%+v diterima", req)
		}
	}
}

// Nasabah tanpa rekening hanya punya session_id; nasabah lama hanya punya user_id. Tiket
// harus bisa dibuat dengan salah satu, atau tanpa keduanya.
func TestCreate_AcceptsEitherSubjectIdentifier(t *testing.T) {
	svc, _ := setupTicket()
	userID := uuid.New()

	cases := []CreateRequest{
		{Subject: "tanpa pengenal"},
		{Subject: "hanya sesi", SessionID: "onb_abc"},
		{Subject: "hanya user", UserID: userID.String()},
		{Subject: "keduanya", UserID: userID.String(), SessionID: "onb_abc"},
	}
	for _, req := range cases {
		if _, err := svc.Create(context.Background(), req, "CS-1042"); err != nil {
			t.Errorf("%q ditolak: %v", req.Subject, err)
		}
	}
}

// --- update ---

func TestUpdate_ResolvedSetsTimestamp(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "Kartu tertelan")

	status := string(StatusResolved)
	got, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &status})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.ResolvedAt == nil {
		t.Fatal("resolved_at kosong; setiap laporan waktu penyelesaian akan melewatkan tiket ini")
	}
	if got.ClosedAt != nil {
		t.Fatal("closed_at terisi padahal baru RESOLVED")
	}
}

func TestUpdate_ClosedSetsBothTimestamps(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "Kartu tertelan")

	closed := string(StatusClosed)
	got, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &closed})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.ResolvedAt == nil || got.ClosedAt == nil {
		t.Fatalf("resolved_at=%v closed_at=%v, keduanya harus terisi", got.ResolvedAt, got.ClosedAt)
	}
}

// Tiket yang sempat RESOLVED lalu ditutup diselesaikan pada waktu yang PERTAMA.
func TestUpdate_ClosingKeepsOriginalResolvedAt(t *testing.T) {
	repo := newMockRepo()
	now := time.Now()
	tick := now
	svc := NewService(ServiceConfig{Repo: repo, Clock: func() time.Time { return tick }})

	tk, err := svc.Create(context.Background(), CreateRequest{Subject: "x"}, "CS-1042")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	resolved := string(StatusResolved)
	first, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &resolved})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	firstResolvedAt := *first.ResolvedAt

	tick = now.Add(3 * time.Hour)
	closed := string(StatusClosed)
	second, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &closed})
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	if !second.ResolvedAt.Equal(firstResolvedAt) {
		t.Fatalf("resolved_at tertimpa: %v → %v; waktu penyelesaian akan dilaporkan 3 jam lebih lama",
			firstResolvedAt, *second.ResolvedAt)
	}
}

// Kembali ke OPEN harus MEMBERSIHKAN stempelnya, atau CHECK database menolak barisnya.
func TestUpdate_ReopeningFromResolvedClearsTimestamps(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "x")

	resolved := string(StatusResolved)
	if _, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &resolved}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	open := string(StatusOpen)
	got, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &open})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got.ResolvedAt != nil || got.ClosedAt != nil {
		t.Fatalf("stempel tidak dibersihkan: resolved=%v closed=%v", got.ResolvedAt, got.ClosedAt)
	}
}

// CLOSED adalah akhir. Keluhan yang muncul lagi layak jadi tiket baru.
func TestUpdate_CannotReopenClosed(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "x")

	closed := string(StatusClosed)
	if _, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &closed}); err != nil {
		t.Fatalf("close: %v", err)
	}

	open := string(StatusOpen)
	_, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &open})
	if err == nil {
		t.Fatal("tiket CLOSED bisa dibuka kembali; closed_at-nya jadi berbohong")
	}
	appErr := apperr.From(err)
	if appErr.Code != "TICKET_INVALID_TRANSITION" {
		t.Fatalf("code = %q", appErr.Code)
	}
}

// Field yang tidak dikirim harus dibiarkan. Tanpa pointer, permintaan yang hanya
// mengubah status akan menghapus penugasan.
func TestUpdate_OmittedFieldsAreLeftAlone(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "x")

	assignee := "CS-2001"
	high := string(PriorityHigh)
	if _, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{
		AssignedToAgent: &assignee,
		Priority:        &high,
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	inProgress := string(StatusInProgress)
	got, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{Status: &inProgress})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}

	if got.AssignedToAgent != "CS-2001" {
		t.Errorf("penugasan hilang saat status diubah: %q", got.AssignedToAgent)
	}
	if got.Priority != PriorityHigh {
		t.Errorf("prioritas hilang saat status diubah: %q", got.Priority)
	}
}

// String kosong BERBEDA dari field yang tidak dikirim: ia berarti lepaskan penugasan.
func TestUpdate_EmptyAssigneeUnassigns(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "x")

	assignee := "CS-2001"
	if _, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{AssignedToAgent: &assignee}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	empty := ""
	got, err := svc.Update(context.Background(), tk.TicketNumber, UpdateRequest{AssignedToAgent: &empty})
	if err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if got.AssignedToAgent != "" {
		t.Fatalf("penugasan tidak dilepas: %q", got.AssignedToAgent)
	}
}

func TestUpdate_UnknownTicketIs404(t *testing.T) {
	svc, _ := setupTicket()

	status := string(StatusResolved)
	_, err := svc.Update(context.Background(), "TKT-tidak-ada", UpdateRequest{Status: &status})
	if apperr.From(err).Status != 404 {
		t.Fatalf("err = %v, mau 404", err)
	}
}

// --- catatan ---

func TestAddNote_RecordsAuthorAndBody(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "x")

	note, err := svc.AddNote(context.Background(), tk.TicketNumber,
		AddNoteRequest{Body: "Sudah dihubungi, nasabah akan datang ke cabang."}, "CS-1042")
	if err != nil {
		t.Fatalf("add note: %v", err)
	}
	if note.Author != "CS-1042" {
		t.Errorf("author = %q", note.Author)
	}

	got, err := svc.Get(context.Background(), tk.TicketNumber)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Notes) != 1 || got.Notes[0].Body != note.Body {
		t.Fatalf("notes = %+v", got.Notes)
	}
}

func TestAddNote_RejectsBlankBody(t *testing.T) {
	svc, _ := setupTicket()
	tk := mustCreate(t, svc, "x")

	for _, body := range []string{"", "   "} {
		if _, err := svc.AddNote(context.Background(), tk.TicketNumber, AddNoteRequest{Body: body}, "CS-1042"); err == nil {
			t.Errorf("body %q diterima", body)
		}
	}
}

func TestAddNote_UnknownTicketIs404(t *testing.T) {
	svc, _ := setupTicket()

	_, err := svc.AddNote(context.Background(), "TKT-tidak-ada", AddNoteRequest{Body: "x"}, "CS-1042")
	if apperr.From(err).Status != 404 {
		t.Fatalf("err = %v, mau 404", err)
	}
}

// --- daftar ---

func TestList_PaginatesWithoutOverlap(t *testing.T) {
	repo := newMockRepo()
	base := time.Now()
	tick := base
	svc := NewService(ServiceConfig{Repo: repo, Clock: func() time.Time { return tick }})

	for i := range 5 {
		tick = base.Add(time.Duration(i) * time.Minute)
		mustCreate(t, svc, "tiket")
	}

	first, hasMore, cursor, err := svc.List(context.Background(), ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("halaman 1: %v", err)
	}
	if len(first) != 2 || !hasMore || cursor == nil {
		t.Fatalf("halaman 1: len=%d hasMore=%v cursor=%v", len(first), hasMore, cursor)
	}

	second, _, _, err := svc.List(context.Background(), ListFilter{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatalf("halaman 2: %v", err)
	}

	seen := map[uuid.UUID]bool{}
	for _, tk := range append(first, second...) {
		if seen[tk.ID] {
			t.Fatalf("%s muncul di dua halaman", tk.TicketNumber)
		}
		seen[tk.ID] = true
	}
}

func TestList_FiltersByStatusAndAssignee(t *testing.T) {
	svc, _ := setupTicket()

	a := mustCreate(t, svc, "a")
	mustCreate(t, svc, "b")

	assignee := "CS-2001"
	inProgress := string(StatusInProgress)
	if _, err := svc.Update(context.Background(), a.TicketNumber, UpdateRequest{
		Status: &inProgress, AssignedToAgent: &assignee,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _, _, err := svc.List(context.Background(), ListFilter{Status: StatusInProgress})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("filter status = %+v", got)
	}

	got, _, _, err = svc.List(context.Background(), ListFilter{AssignedTo: "CS-2001"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("filter assignee = %+v", got)
	}
}

// Batas atas ada supaya satu permintaan tidak bisa menarik seluruh tabel tiket.
func TestList_CapsLimitPassedToRepo(t *testing.T) {
	svc, repo := setupTicket()
	mustCreate(t, svc, "a")

	if _, _, _, err := svc.List(context.Background(), ListFilter{Limit: 100_000}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if repo.lastFilter.Limit != maxLimit {
		t.Fatalf("limit yang diteruskan = %d, mau dipotong jadi %d", repo.lastFilter.Limit, maxLimit)
	}
}

// Limit yang tidak dikirim harus jadi default, bukan nol — nol akan membuat repository
// mengambil LIMIT 1 dan daftar tiket tampak selalu berisi satu baris.
func TestList_AppliesDefaultLimit(t *testing.T) {
	svc, repo := setupTicket()
	mustCreate(t, svc, "a")

	if _, _, _, err := svc.List(context.Background(), ListFilter{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if repo.lastFilter.Limit != defaultLimit {
		t.Fatalf("limit = %d, mau %d", repo.lastFilter.Limit, defaultLimit)
	}
}

func TestList_PropagatesRepoFailure(t *testing.T) {
	svc, repo := setupTicket()
	repo.err = errors.New("connection refused")

	if _, _, _, err := svc.List(context.Background(), ListFilter{}); err == nil {
		t.Fatal("kegagalan database tampil sebagai daftar kosong")
	}
}

// --- transisi ---

func TestValidTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		want     bool
	}{
		{StatusOpen, StatusInProgress, true},
		{StatusOpen, StatusClosed, true},
		{StatusInProgress, StatusResolved, true},
		{StatusResolved, StatusClosed, true},
		{StatusResolved, StatusOpen, true},
		{StatusClosed, StatusClosed, true},
		{StatusClosed, StatusOpen, false},
		{StatusClosed, StatusInProgress, false},
		{StatusClosed, StatusResolved, false},
		{"NGAWUR", StatusOpen, false},
	}
	for _, c := range cases {
		if got := ValidTransition(c.from, c.to); got != c.want {
			t.Errorf("ValidTransition(%q, %q) = %v, mau %v", c.from, c.to, got, c.want)
		}
	}
}
