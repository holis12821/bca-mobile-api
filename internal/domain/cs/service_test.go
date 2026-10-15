package cs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- mock in-memory ---

type mockCustomerRepo struct {
	byAccount map[string]*Customer
	byHash    map[string]*Customer
	byID      map[uuid.UUID]*Customer
	accounts  map[uuid.UUID][]Account

	// err dipaksa untuk menguji jalur kegagalan infrastruktur.
	err error

	// hashesSeen merekam hash yang diminta, supaya test bisa memeriksa bahwa service
	// menyusun kandidat bentuk nomor — bukan hanya satu.
	hashesSeen []string
}

func newMockCustomerRepo() *mockCustomerRepo {
	return &mockCustomerRepo{
		byAccount: make(map[string]*Customer),
		byHash:    make(map[string]*Customer),
		byID:      make(map[uuid.UUID]*Customer),
		accounts:  make(map[uuid.UUID][]Account),
	}
}

func (m *mockCustomerRepo) FindByAccountNumber(_ context.Context, n string) (*Customer, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.byAccount[n], nil
}

func (m *mockCustomerRepo) FindByPhoneHashes(_ context.Context, hashes []string) ([]*Customer, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.hashesSeen = append(m.hashesSeen, hashes...)

	seen := map[uuid.UUID]bool{}
	var out []*Customer
	for _, h := range hashes {
		if c := m.byHash[h]; c != nil && !seen[c.UserID] {
			seen[c.UserID] = true
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *mockCustomerRepo) FindByID(_ context.Context, id uuid.UUID) (*Customer, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.byID[id], nil
}

func (m *mockCustomerRepo) ListAccounts(_ context.Context, id uuid.UUID) ([]Account, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.accounts[id], nil
}

type mockAccessLogRepo struct {
	logs []*AccessLog
	err  error
}

func (m *mockAccessLogRepo) Insert(_ context.Context, l *AccessLog) error {
	if m.err != nil {
		return m.err
	}
	m.logs = append(m.logs, l)
	return nil
}

// stubHasher meniru HMACHasher: deterministik, dan menormalisasi sama (trim + lowercase).
type stubHasher struct{}

func (stubHasher) Hash(in string) string {
	return "h:" + strings.ToLower(strings.TrimSpace(in))
}

func setupCS() (*Service, *mockCustomerRepo, *mockAccessLogRepo) {
	repo := newMockCustomerRepo()
	access := &mockAccessLogRepo{}
	svc := NewService(ServiceConfig{Customers: repo, Access: access, Hasher: stubHasher{}})
	return svc, repo, access
}

func sampleCustomer() *Customer {
	return &Customer{
		UserID:      uuid.New(),
		FullName:    "Siti Rahmawati",
		DisplayName: "Siti",
		NIK:         "3171064509900002",
		Phone:       "081234567890",
		Email:       "siti.rahmawati@example.com",
		Tier:        "REGULER",
		Status:      "ACTIVE",
		CreatedAt:   time.Now(),
	}
}

// --- pencarian ---

func TestSearch_ByAccountNumber(t *testing.T) {
	svc, repo, _ := setupCS()
	c := sampleCustomer()
	repo.byAccount["1234567890"] = c

	got, err := svc.Search(context.Background(), "1234567890", "CS-1042", "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].UserID != c.UserID {
		t.Fatalf("hasil = %+v", got)
	}
	// Nomor HP di hasil pencarian harus sudah tersamar.
	if got[0].PhoneMasked == c.Phone {
		t.Fatal("nomor HP tampil utuh di hasil pencarian")
	}
}

func TestSearch_ByPhoneTriesEquivalentForms(t *testing.T) {
	svc, repo, _ := setupCS()
	c := sampleCustomer()
	// Tersimpan dalam bentuk nasional, petugas mengetik E.164.
	repo.byHash[stubHasher{}.Hash("081234567890")] = c

	got, err := svc.Search(context.Background(), "+6281234567890", "CS-1042", "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("nomor tersimpan sebagai 08… tidak ditemukan lewat +62…: %+v (hash dicoba: %v)",
			got, repo.hashesSeen)
	}
}

// Pencocokan sebagian TIDAK boleh ada. Kalau suatu saat seseorang menambahkan LIKE,
// test ini yang gagal lebih dulu.
func TestSearch_RejectsNameAndPartialQueries(t *testing.T) {
	svc, repo, _ := setupCS()
	repo.byAccount["1234567890"] = sampleCustomer()

	for _, q := range []string{"siti", "Siti Rahmawati", "0812", "12345", "%"} {
		_, err := svc.Search(context.Background(), q, "CS-1042", "", "")
		if err == nil {
			t.Fatalf("q=%q diterima; pencarian sebagian mengubah endpoint ini jadi alat ekspor daftar nasabah", q)
		}
	}
}

// Tidak ditemukan = daftar kosong, BUKAN 404. 404 bisa dipakai menyapu ruang nomor
// rekening satu per satu.
func TestSearch_NoMatchIsEmptyListNotError(t *testing.T) {
	svc, _, _ := setupCS()

	got, err := svc.Search(context.Background(), "9999999999", "CS-1042", "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("hasil = %+v", got)
	}
}

func TestSearch_LogsEvenWhenNothingFound(t *testing.T) {
	svc, _, access := setupCS()

	if _, err := svc.Search(context.Background(), "9999999999", "CS-1042", "10.0.0.1", "desktop"); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(access.logs) != 1 {
		t.Fatalf("pencarian gagal tidak tercatat; pola pencarian yang gagal justru yang perlu terlihat")
	}
	l := access.logs[0]
	if l.Action != ActionCustomerSearch || l.ResultCount != 0 {
		t.Fatalf("log = %+v", l)
	}
	if l.SubjectUserID != nil {
		t.Fatal("subject_user_id terisi padahal tidak ada yang ditemukan")
	}
}

// Jejak akses TIDAK boleh memuat kata kunci pencarian — itu akan menumpuk nomor
// rekening dan nomor HP nasabah di tabel log yang jarang ditinjau.
func TestSearch_LogNeverStoresQueryValue(t *testing.T) {
	svc, repo, access := setupCS()
	repo.byAccount["1234567890"] = sampleCustomer()

	if _, err := svc.Search(context.Background(), "1234567890", "CS-1042", "", ""); err != nil {
		t.Fatalf("search: %v", err)
	}

	blob, err := json.Marshal(access.logs[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "1234567890") {
		t.Fatalf("kata kunci pencarian tersimpan di jejak akses: %s", blob)
	}
	if access.logs[0].QueryKind != QueryAccountNumber {
		t.Fatalf("query_kind = %q", access.logs[0].QueryKind)
	}
}

func TestSearch_WithoutHasherStillSearchesAccountNumber(t *testing.T) {
	repo := newMockCustomerRepo()
	repo.byAccount["1234567890"] = sampleCustomer()
	svc := NewService(ServiceConfig{Customers: repo, Access: &mockAccessLogRepo{}})

	got, err := svc.Search(context.Background(), "1234567890", "CS-1042", "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatal("pencarian nomor rekening mati hanya karena hasher tidak ada")
	}
}

func TestSearch_PropagatesRepoFailure(t *testing.T) {
	svc, repo, _ := setupCS()
	repo.err = errors.New("connection refused")

	if _, err := svc.Search(context.Background(), "1234567890", "CS-1042", "", ""); err == nil {
		t.Fatal("kegagalan database tidak boleh tampil sebagai 'tidak ditemukan'")
	}
}

// --- profil ---

func TestGetProfile_MasksAndOmitsBalance(t *testing.T) {
	svc, repo, _ := setupCS()
	c := sampleCustomer()
	repo.byID[c.UserID] = c
	repo.accounts[c.UserID] = []Account{{
		AccountNumber: "1234567890",
		AccountType:   "TAHAPAN",
		AccountLabel:  "Tahapan BCA",
		Currency:      "IDR",
		IsPrimary:     true,
		Status:        "ACTIVE",
		OpenedAt:      time.Now(),
	}}

	p, err := svc.GetProfile(context.Background(), c.UserID, "CS-1042", "", "")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}

	// Nama utuh — inti pekerjaan petugas.
	if p.FullName != c.FullName {
		t.Fatalf("full_name = %q", p.FullName)
	}

	blob, _ := json.Marshal(p)
	raw := string(blob)

	// PII kuat tidak boleh pernah utuh.
	for _, secret := range []string{c.NIK, c.Phone, c.Email, "1234567890"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("%q bocor utuh di profil: %s", secret, raw)
		}
	}
	// Saldo sengaja tidak ada. Kalau suatu saat ditambahkan, itu keputusan sadar —
	// bukan sesuatu yang menyelip lewat field baru di struct rekening.
	for _, forbidden := range []string{"balance", "available", "saldo"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("profil memuat %q: %s", forbidden, raw)
		}
	}
}

func TestGetProfile_WritesAccessLog(t *testing.T) {
	svc, repo, access := setupCS()
	c := sampleCustomer()
	repo.byID[c.UserID] = c

	if _, err := svc.GetProfile(context.Background(), c.UserID, "CS-1042", "10.0.0.2", "desktop"); err != nil {
		t.Fatalf("get profile: %v", err)
	}

	if len(access.logs) != 1 {
		t.Fatalf("%d baris jejak, mau 1", len(access.logs))
	}
	l := access.logs[0]
	if l.Action != ActionCustomerViewed {
		t.Fatalf("action = %q", l.Action)
	}
	if l.SubjectUserID == nil || *l.SubjectUserID != c.UserID {
		t.Fatalf("subject_user_id = %v, mau %v", l.SubjectUserID, c.UserID)
	}
	if l.AgentEmployeeID != "CS-1042" || l.IPAddress != "10.0.0.2" {
		t.Fatalf("log = %+v", l)
	}
}

// Nasabah yang tidak ada bukan data siapa pun yang terbuka.
func TestGetProfile_UnknownCustomerWritesNoLog(t *testing.T) {
	svc, _, access := setupCS()

	if _, err := svc.GetProfile(context.Background(), uuid.New(), "CS-1042", "", ""); err == nil {
		t.Fatal("nasabah tidak dikenal seharusnya error")
	}
	if len(access.logs) != 0 {
		t.Fatal("jejak tertulis untuk nasabah yang tidak ada")
	}
}

// Gagal mencatat tidak boleh menggagalkan permintaan: datanya sudah terbaca petugas,
// dan menolak responsnya setelah itu tidak menarik kembali apa pun.
func TestGetProfile_SurvivesAccessLogFailure(t *testing.T) {
	repo := newMockCustomerRepo()
	c := sampleCustomer()
	repo.byID[c.UserID] = c
	svc := NewService(ServiceConfig{
		Customers: repo,
		Access:    &mockAccessLogRepo{err: errors.New("disk full")},
		Hasher:    stubHasher{},
	})

	if _, err := svc.GetProfile(context.Background(), c.UserID, "CS-1042", "", ""); err != nil {
		t.Fatalf("permintaan gagal hanya karena pencatatan gagal: %v", err)
	}
}

// --- deteksi jenis kata kunci ---

func TestDetectQueryKinds(t *testing.T) {
	cases := []struct {
		in   string
		want []QueryKind
	}{
		{"1234567890", []QueryKind{QueryAccountNumber}},
		{"081234567890", []QueryKind{QueryPhone}},
		{"+62 812-3456-7890", []QueryKind{QueryPhone}},
		{"6281234567890", []QueryKind{QueryPhone}},
		// 10 digit berawalan 08: tidak bisa dibedakan, jadi keduanya dicari.
		{"0812345678", []QueryKind{QueryAccountNumber, QueryPhone}},
		{"siti", nil},
		{"", nil},
		{"0987654321", []QueryKind{QueryAccountNumber}},
	}

	for _, tc := range cases {
		got := detectQueryKinds(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("detectQueryKinds(%q) = %v, mau %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("detectQueryKinds(%q) = %v, mau %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestPhoneCandidates_CoversStoredForms(t *testing.T) {
	got := phoneCandidates("+6281234567890")

	want := map[string]bool{"081234567890": false, "+6281234567890": false, "6281234567890": false}
	for _, g := range got {
		if _, ok := want[g]; ok {
			want[g] = true
		}
	}
	for form, found := range want {
		if !found {
			t.Errorf("bentuk %q tidak ikut dicari; nasabah yang tersimpan begitu tidak akan ditemukan", form)
		}
	}
}

func TestPhoneCandidates_RejectsNonMobile(t *testing.T) {
	for _, in := range []string{"02112345678", "siti", "123"} {
		if got := phoneCandidates(in); got != nil {
			t.Errorf("phoneCandidates(%q) = %v, mau nil", in, got)
		}
	}
}

// --- Dashboard petugas (SCR-010) ---

type mockDashboardRepo struct {
	stats *DashboardCallStats
	snap  *DashboardQueueSnapshot

	// gotSince dan gotUntil merekam rentang yang diminta service. Itu yang membuktikan
	// batas hari dihitung di aplikasi dan dalam WIB, bukan diserahkan ke SQL.
	gotSince time.Time
	gotUntil time.Time

	statsErr error
	snapErr  error
}

func (m *mockDashboardRepo) AgentCallStats(_ context.Context, _ string, since, until time.Time) (*DashboardCallStats, error) {
	m.gotSince, m.gotUntil = since, until
	if m.statsErr != nil {
		return nil, m.statsErr
	}
	return m.stats, nil
}

func (m *mockDashboardRepo) QueueSnapshot(_ context.Context, _ time.Time) (*DashboardQueueSnapshot, error) {
	if m.snapErr != nil {
		return nil, m.snapErr
	}
	return m.snap, nil
}

type mockSessionLookup struct {
	live *AgentSession
	err  error
}

func (m *mockSessionLookup) Create(context.Context, *AgentSession, string) error { return nil }
func (m *mockSessionLookup) FindByToken(context.Context, string) (*SessionContext, error) {
	return nil, nil
}
func (m *mockSessionLookup) FindLiveByEmployee(context.Context, string) (*AgentSession, error) {
	return m.live, m.err
}
func (m *mockSessionLookup) End(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	return true, nil
}
func (m *mockSessionLookup) Touch(context.Context, uuid.UUID, time.Time) error { return nil }

type mockTerminalLookup struct {
	t   *Terminal
	err error
}

func (m *mockTerminalLookup) Register(context.Context, *Terminal) error { return nil }
func (m *mockTerminalLookup) FindByID(context.Context, string) (*Terminal, error) {
	return m.t, m.err
}
func (m *mockTerminalLookup) Activate(context.Context, string, string, time.Time) error { return nil }
func (m *mockTerminalLookup) Deactivate(context.Context, string, time.Time) (bool, error) {
	return true, nil
}
func (m *mockTerminalLookup) SetStatus(context.Context, string, TerminalStatus, time.Time) error {
	return nil
}
func (m *mockTerminalLookup) UpsertGate(context.Context, *GateRecord) error { return nil }
func (m *mockTerminalLookup) ListGates(context.Context, uuid.UUID) ([]GateRecord, error) {
	return nil, nil
}
func (m *mockTerminalLookup) Lock(context.Context, string, string, LockReason, time.Time) (bool, error) {
	return false, nil
}
func (m *mockTerminalLookup) Unlock(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}
func (m *mockTerminalLookup) FreezeLock(context.Context, string, time.Time) error { return nil }

// clockAt membekukan waktu pada satu instan WIB.
func clockAt(wib string) func() time.Time {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.UTC
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", wib, loc)
	if err != nil {
		panic("clockAt: " + err.Error())
	}
	return func() time.Time { return t }
}

// SLA, CSAT, dan target shift WAJIB nil dan ditandai tidak terukur. Angka karangan di
// layar operasional akan dipakai menilai orang — itu alasan `measured` ada.
func TestDashboard_UnmeasuredKPIsStayNil(t *testing.T) {
	svc := NewDashboardService(DashboardServiceConfig{
		Calls: &mockDashboardRepo{
			stats: &DashboardCallStats{CallsHandled: 7, Approved: 5, Rejected: 1, NeedReview: 1,
				AvgDurationSeconds: 195},
			snap: &DashboardQueueSnapshot{Waiting: 3, LongestWaitSeconds: 420},
		},
		Clock: clockAt("2026-10-07 10:00:00"),
	})

	out, err := svc.Overview(context.Background(), "CS-1042", "Sarah Adisti", []string{"VIDEO_CALL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Today.SLAPercent != nil {
		t.Errorf("sla_percent harus nil, dapat %v", *out.Today.SLAPercent)
	}
	if out.Today.CSATPercent != nil {
		t.Errorf("csat_percent harus nil, dapat %v", *out.Today.CSATPercent)
	}
	if out.Today.ShiftTarget != nil {
		t.Errorf("shift_target harus nil, dapat %v", *out.Today.ShiftTarget)
	}

	for _, key := range []string{"sla_percent", "csat_percent", "shift_target"} {
		if out.Measured[key] {
			t.Errorf("measured[%q] harus false", key)
		}
	}
	for _, key := range []string{"calls_handled", "avg_duration_seconds", "queue_waiting"} {
		if !out.Measured[key] {
			t.Errorf("measured[%q] harus true", key)
		}
	}

	if out.Today.CallsHandled != 7 || out.Today.Approved != 5 || out.Today.NeedReview != 1 {
		t.Errorf("angka terukur tidak diteruskan apa adanya: %+v", out.Today)
	}
	if out.Queue.Waiting != 3 || out.Queue.LongestWaitSeconds != 420 {
		t.Errorf("antrean tidak diteruskan apa adanya: %+v", out.Queue)
	}
}

// Batas hari dihitung APLIKASI dalam WIB, bukan CURRENT_DATE. Jebakan ini sudah
// menggigit jalur limit harian di repo ini.
func TestDashboard_DayBoundaryIsJakartaMidnight(t *testing.T) {
	repo := &mockDashboardRepo{stats: &DashboardCallStats{}, snap: &DashboardQueueSnapshot{}}
	svc := NewDashboardService(DashboardServiceConfig{
		Calls: repo,
		// 00:30 WIB = 17:30 UTC hari SEBELUMNYA. Rentangnya harus tetap hari ini WIB.
		Clock: clockAt("2026-10-07 00:30:00"),
	})

	if _, err := svc.Overview(context.Background(), "CS-1042", "Sarah", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Jakarta")
	gotSince := repo.gotSince.In(loc)
	if gotSince.Hour() != 0 || gotSince.Minute() != 0 || gotSince.Day() != 7 {
		t.Errorf("since = %s, mau 2026-10-07 00:00 WIB", gotSince)
	}
	// Setengah terbuka: panggilan tepat tengah malam tidak terhitung di dua hari.
	gotUntil := repo.gotUntil.In(loc)
	if gotUntil.Day() != 8 || gotUntil.Hour() != 0 {
		t.Errorf("until = %s, mau 2026-10-08 00:00 WIB", gotUntil)
	}
}

// Petugas yang belum masuk loket mana pun BUKAN kegagalan: ia boleh membuka beranda
// dengan kunci API sebelum login, dan justru itu yang membuatnya perlu tahu.
func TestDashboard_NoLiveSessionIsNotAnError(t *testing.T) {
	svc := NewDashboardService(DashboardServiceConfig{
		Calls:    &mockDashboardRepo{stats: &DashboardCallStats{}, snap: &DashboardQueueSnapshot{}},
		Sessions: &mockSessionLookup{live: nil},
		Clock:    clockAt("2026-10-07 10:00:00"),
	})

	out, err := svc.Overview(context.Background(), "CS-1042", "Sarah", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Terminal != nil {
		t.Errorf("terminal harus nil tanpa sesi, dapat %+v", out.Terminal)
	}
	if out.Agent.SessionStartedAt != nil {
		t.Error("session_started_at harus nil tanpa sesi")
	}
	// Selalu array di JSON: klien yang menerima null akan memanggil scopes.includes().
	if out.Agent.Scopes == nil {
		t.Error("scopes tidak boleh nil")
	}
}

// can_take_calls adalah Rule 4 yang sudah dihitung. Hanya ONLINE yang boleh mengambil
// antrean, dan jawabannya harus sama dengan yang ditegakkan AgentSignalingURL.
func TestDashboard_CanTakeCallsOnlyWhenOnline(t *testing.T) {
	cases := []struct {
		status TerminalStatus
		want   bool
	}{
		{TerminalOnline, true},
		{TerminalReady, false},
		{TerminalRegistered, false},
		{TerminalOffline, false},

		// Kunci meja (000043): loket terkunci berhenti jadi kandidat penugasan tanpa
		// satu baris kode pun di jalur antrean. Inilah yang membedakannya dari tirai
		// lokal di aplikasi desktop, yang tidak pernah bisa menahan nasabah.
		{TerminalLocked, false},
	}

	for _, tc := range cases {
		svc := NewDashboardService(DashboardServiceConfig{
			Calls:    &mockDashboardRepo{stats: &DashboardCallStats{}, snap: &DashboardQueueSnapshot{}},
			Sessions: &mockSessionLookup{live: &AgentSession{EmployeeID: "CS-1042", TerminalID: "WKS-1", Shift: "PAGI", StartedAt: time.Now()}},
			Terminals: &mockTerminalLookup{t: &Terminal{
				TerminalID: "WKS-1", Workstation: "Loket 1", Location: "KCU", Status: tc.status,
			}},
			Clock: clockAt("2026-10-07 10:00:00"),
		})

		out, err := svc.Overview(context.Background(), "CS-1042", "Sarah", nil)
		if err != nil {
			t.Fatalf("status %s: unexpected error: %v", tc.status, err)
		}
		if out.Terminal == nil {
			t.Fatalf("status %s: terminal harus terisi", tc.status)
		}
		if out.Terminal.CanTakeCalls != tc.want {
			t.Errorf("status %s: can_take_calls = %v, mau %v", tc.status, out.Terminal.CanTakeCalls, tc.want)
		}
	}
}

// Kegagalan database DITERUSKAN, bukan ditelan jadi layar yang separuh benar: angka nol
// yang sebenarnya "tidak terbaca" akan dipercaya sebagai nol panggilan.
func TestDashboard_RepoErrorPropagates(t *testing.T) {
	svc := NewDashboardService(DashboardServiceConfig{
		Calls: &mockDashboardRepo{statsErr: errors.New("postgres tersendat")},
		Clock: clockAt("2026-10-07 10:00:00"),
	})

	if _, err := svc.Overview(context.Background(), "CS-1042", "Sarah", nil); err == nil {
		t.Fatal("kegagalan repo harus diteruskan")
	}
}

// --- Direktori HRIS ---

// Tiga keadaan yang WAJIB berbeda. Menyamakan "nonaktif" dengan "tidak ditemukan"
// membuat pegawai yang statusnya dicabut mengira ia salah ketik.
func TestMockHRIS_ThreeDistinctAnswers(t *testing.T) {
	dir := NewMockHRISDirectory()
	ctx := context.Background()

	emp, err := dir.LookupEmployee(ctx, "CS-1042")
	if err != nil {
		t.Fatalf("NPP aktif: %v", err)
	}
	if !emp.Active || emp.Name != "Sarah Adisti" {
		t.Errorf("NPP aktif: %+v", emp)
	}

	inactive, err := dir.LookupEmployee(ctx, "CS-0001")
	if err != nil {
		t.Fatalf("NPP nonaktif tidak boleh error: %v", err)
	}
	if inactive.Active {
		t.Error("CS-0001 harus nonaktif")
	}

	if _, err := dir.LookupEmployee(ctx, "TIDAK-ADA"); err == nil {
		t.Error("NPP tak dikenal harus error")
	}
}

// Di luar development, direktorinya menolak — tidak mengarang pegawai.
func TestHRISDirectoryFor_NonDevRefuses(t *testing.T) {
	if _, err := HRISDirectoryFor(false).LookupEmployee(context.Background(), "CS-1042"); err == nil {
		t.Fatal("direktori di luar development harus menolak")
	}
	if _, err := HRISDirectoryFor(true).LookupEmployee(context.Background(), "CS-1042"); err != nil {
		t.Fatalf("direktori development harus menjawab: %v", err)
	}
}

func TestValidScope(t *testing.T) {
	for _, s := range AllScopes {
		if !ValidScope(s) {
			t.Errorf("%q harus sah", s)
		}
	}
	// AUDIT_READ dan ESCALATION_REVIEW dulu ada di daftar ini sebagai contoh cakupan
	// yang BELUM ada; migrasi 000041 membuka keduanya. Yang dipakai sekarang adalah
	// nama yang memang tidak pernah ada, bukan nama yang kebetulan belum ada.
	for _, s := range []string{"", "PRODUCT_ADMIN", "video_call", "ADMIN"} {
		if ValidScope(s) {
			t.Errorf("%q tidak boleh sah", s)
		}
	}
}

// --- Pendaftaran petugas (SCR-001) ---

// stubPasswordHasher meniru Argon2id tanpa biayanya: test pendaftaran menjalankan jalur
// hash dua kali per kasus, dan Argon2id sungguhan (64 MB x 4 thread) akan mendominasi
// seluruh runtime paket ini.
type stubPasswordHasher struct{}

func (stubPasswordHasher) Hash(_ context.Context, plaintext string) (string, error) {
	return "argon2:" + plaintext, nil
}

func (stubPasswordHasher) Verify(_ context.Context, plaintext, encoded string) (bool, error) {
	return encoded == "argon2:"+plaintext, nil
}

type mockAgentRegistry struct {
	registered map[string][]string
	hashes     map[string]string

	// inactive melacak is_active = false. Peta tersendiri, bukan field di `registered`,
	// supaya penambahannya tidak mengubah bentuk yang dipakai test pendaftaran.
	inactive map[string]bool

	err error
}

func newMockAgentRegistry() *mockAgentRegistry {
	return &mockAgentRegistry{
		registered: make(map[string][]string),
		hashes:     make(map[string]string),
	}
}

func (m *mockAgentRegistry) Register(_ context.Context, employeeID, _, apiKeyHash string, scopes []string, _ time.Time) error {
	if m.err != nil {
		return m.err
	}
	if _, exists := m.registered[employeeID]; exists {
		return apperr.AgentAlreadyRegistered
	}
	m.registered[employeeID] = scopes
	m.hashes[employeeID] = apiKeyHash
	return nil
}

// Update meniru jalur Postgres: keadaan SEBELUM dan SESUDAH dari satu pembacaan, dan
// kosong berarti TIDAK DIUBAH.
//
// Petugas nonaktif dilacak tersendiri karena `registered` hanya memetakan NPP ke cakupan;
// tanpa itu test pencabutan hak tidak bisa membedakan "dinonaktifkan" dari "tidak pernah
// ada".
func (m *mockAgentRegistry) Update(_ context.Context, employeeID string, upd AgentUpdate, at time.Time) (*AgentRecord, *AgentRecord, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	scopes, exists := m.registered[employeeID]
	if !exists {
		return nil, nil, apperr.AgentNotFound
	}
	if m.inactive == nil {
		m.inactive = make(map[string]bool)
	}

	before := &AgentRecord{
		EmployeeID: employeeID,
		Name:       employeeID,
		Scopes:     append([]string(nil), scopes...),
		IsActive:   !m.inactive[employeeID],
		UpdatedAt:  at,
	}

	if upd.Scopes != nil {
		m.registered[employeeID] = append([]string(nil), upd.Scopes...)
	}
	if upd.IsActive != nil {
		m.inactive[employeeID] = !*upd.IsActive
	}

	after := &AgentRecord{
		EmployeeID: employeeID,
		Name:       employeeID,
		Scopes:     append([]string(nil), m.registered[employeeID]...),
		IsActive:   !m.inactive[employeeID],
		UpdatedAt:  at,
	}
	return before, after, nil
}

type mockSupervisorRepo struct {
	tokens map[string]string // supervisorID → token sah
	err    error
}

func (m *mockSupervisorRepo) ListActive(context.Context, string) ([]Supervisor, error) {
	return nil, nil
}

func (m *mockSupervisorRepo) Authenticate(_ context.Context, supervisorID, token string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	want, ok := m.tokens[supervisorID]
	if !ok || want != token {
		return "", false, nil
	}
	return "Supervisor " + supervisorID, true, nil
}

type mockAuditEvents struct {
	inserted []AuditEvent
	rows     []AuditEvent
	gotFiter AuditFilter
	err      error
}

func (m *mockAuditEvents) Insert(_ context.Context, ev *AuditEvent) error {
	m.inserted = append(m.inserted, *ev)
	return nil
}

func (m *mockAuditEvents) List(_ context.Context, f AuditFilter) ([]AuditEvent, error) {
	m.gotFiter = f
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func setupRegistration() (*AgentSessionService, *mockAgentRegistry, *mockSupervisorRepo, *mockAuditEvents) {
	registry := newMockAgentRegistry()
	supervisors := &mockSupervisorRepo{tokens: map[string]string{"SPV-0021": "123456"}}
	audit := &mockAuditEvents{}

	svc := NewAgentSessionService(AgentSessionServiceConfig{
		Registry:    registry,
		Supervisors: supervisors,
		HRIS:        NewMockHRISDirectory(),
		Audit:       audit,
		Hasher:      stubPasswordHasher{},
		Clock:       clockAt("2026-10-07 10:00:00"),
	})
	return svc, registry, supervisors, audit
}

func validRegistration() RegisterAgentRequest {
	return RegisterAgentRequest{
		EmployeeID:   "CS-2099",
		Scopes:       []string{"VIDEO_CALL", "TICKET"},
		SupervisorID: "SPV-0021",
		Token:        "123456",
	}
}

func TestRegisterAgent_Success(t *testing.T) {
	svc, registry, _, audit := setupRegistration()

	resp, err := svc.RegisterAgent(context.Background(), validRegistration(), "OPS-2001", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nama dan jabatan datang dari HRIS, bukan dari body: baris cs_agents tidak boleh
	// menyebut orang yang berbeda dari yang ada di direktori pegawai.
	if resp.Name != "Dimas Prakoso" {
		t.Errorf("name = %q, mau dari HRIS (Dimas Prakoso)", resp.Name)
	}
	if resp.Branch != "KCU Jakarta Thamrin" {
		t.Errorf("branch = %q, mau dari HRIS", resp.Branch)
	}

	// Kunci API diterbitkan SERVER dan hanya ada di response ini.
	if resp.APIKey == "" {
		t.Fatal("api_key harus terisi")
	}
	if stored := registry.hashes["CS-2099"]; stored == resp.APIKey {
		t.Error("kunci API tidak boleh tersimpan sebagai teks biasa")
	}
	if registry.hashes["CS-2099"] != "argon2:"+resp.APIKey {
		t.Errorf("kunci API harus tersimpan sebagai hash, dapat %q", registry.hashes["CS-2099"])
	}

	if got := registry.registered["CS-2099"]; len(got) != 2 {
		t.Errorf("scopes tersimpan = %v", got)
	}
	if resp.RegisteredBy != "OPS-2001" {
		t.Errorf("registered_by = %q, mau OPS-2001", resp.RegisteredBy)
	}

	if len(audit.inserted) != 1 || audit.inserted[0].EventType != EventAgentRegistered {
		t.Fatalf("jejak AGENT_REGISTERED tidak tertulis: %+v", audit.inserted)
	}
	if audit.inserted[0].Actor != "agent:OPS-2001" {
		t.Errorf("actor = %q, mau agent:OPS-2001", audit.inserted[0].Actor)
	}
}

// Kunci API tidak pernah sama dua kali: dua petugas dengan kunci identik berarti
// kewenangan yang tidak bisa dibedakan satu sama lain di jejak audit.
func TestRegisterAgent_APIKeyIsUnique(t *testing.T) {
	svc, _, _, _ := setupRegistration()

	first, err := svc.RegisterAgent(context.Background(), validRegistration(), "OPS-2001", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("pendaftaran pertama: %v", err)
	}

	req := validRegistration()
	req.EmployeeID = "SPV-3001"
	second, err := svc.RegisterAgent(context.Background(), req, "OPS-2001", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("pendaftaran kedua: %v", err)
	}

	if first.APIKey == second.APIKey {
		t.Fatal("dua petugas tidak boleh mendapat kunci API yang sama")
	}
}

// Pegawai yang statusnya dicabut TIDAK boleh diberi kewenangan, dan alasannya berbeda
// dari "NPP tidak ditemukan".
func TestRegisterAgent_InactiveEmployeeRefused(t *testing.T) {
	svc, registry, _, _ := setupRegistration()

	req := validRegistration()
	req.EmployeeID = "CS-0001" // ada di HRIS, Active: false

	if _, err := svc.RegisterAgent(context.Background(), req, "OPS-2001", "127.0.0.1", "test"); !errors.Is(err, apperr.EmployeeInactive) {
		t.Fatalf("mau EMPLOYEE_INACTIVE, dapat %v", err)
	}
	if len(registry.registered) != 0 {
		t.Error("tidak boleh ada baris tertulis untuk pegawai nonaktif")
	}
}

func TestRegisterAgent_UnknownEmployeeRefused(t *testing.T) {
	svc, registry, _, _ := setupRegistration()

	req := validRegistration()
	req.EmployeeID = "TIDAK-ADA"

	if _, err := svc.RegisterAgent(context.Background(), req, "OPS-2001", "127.0.0.1", "test"); !errors.Is(err, apperr.EmployeeNotFound) {
		t.Fatalf("mau EMPLOYEE_NOT_FOUND, dapat %v", err)
	}
	if len(registry.registered) != 0 {
		t.Error("tidak boleh ada baris tertulis untuk NPP tak dikenal")
	}
}

// Token supervisor salah ditolak, DAN percobaannya tercatat: pemberian kewenangan yang
// gagal adalah hal yang justru paling perlu terbaca di jejak.
func TestRegisterAgent_BadSupervisorTokenRefusedAndAudited(t *testing.T) {
	svc, registry, _, audit := setupRegistration()

	req := validRegistration()
	req.Token = "000000"

	if _, err := svc.RegisterAgent(context.Background(), req, "OPS-2001", "127.0.0.1", "test"); !errors.Is(err, apperr.SupervisorTokenInvalid) {
		t.Fatalf("mau SUPERVISOR_TOKEN_INVALID, dapat %v", err)
	}
	if len(registry.registered) != 0 {
		t.Error("tidak boleh ada baris tertulis tanpa otorisasi supervisor")
	}
	if len(audit.inserted) != 1 || audit.inserted[0].EventType != EventSupervisorAuthFailed {
		t.Fatalf("jejak SUPERVISOR_AUTH_FAILED tidak tertulis: %+v", audit.inserted)
	}
}

// Kegagalan infrastruktur saat memverifikasi supervisor BUKAN penolakan: Postgres yang
// tersendat tidak boleh terbaca sebagai token yang salah.
func TestRegisterAgent_SupervisorLookupErrorIsNotRejection(t *testing.T) {
	svc, _, supervisors, audit := setupRegistration()
	supervisors.err = errors.New("postgres tersendat")

	_, err := svc.RegisterAgent(context.Background(), validRegistration(), "OPS-2001", "127.0.0.1", "test")
	if err == nil {
		t.Fatal("kegagalan infrastruktur harus diteruskan")
	}
	if errors.Is(err, apperr.SupervisorTokenInvalid) {
		t.Fatal("kegagalan infrastruktur tidak boleh jadi SUPERVISOR_TOKEN_INVALID")
	}
	for _, ev := range audit.inserted {
		if ev.EventType == EventSupervisorAuthFailed {
			t.Error("kegagalan infrastruktur tidak boleh tercatat sebagai otorisasi gagal")
		}
	}
}

// Cakupan tak dikenal ditolak dengan menyebut cakupan MANA — sesuatu yang CHECK di skema
// tidak bisa lakukan.
func TestRegisterAgent_UnknownScopeNamesIt(t *testing.T) {
	svc, _, _, _ := setupRegistration()

	req := validRegistration()
	req.Scopes = []string{"VIDEO_CALL", "PRODUCT_ADMIN"}

	_, err := svc.RegisterAgent(context.Background(), req, "OPS-2001", "127.0.0.1", "test")
	appErr := apperr.From(err)
	if appErr.Code != "SCOPE_UNKNOWN" {
		t.Fatalf("mau SCOPE_UNKNOWN, dapat %v", err)
	}
	details, ok := appErr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details bukan map: %T", appErr.Details)
	}
	if details["scope"] != "PRODUCT_ADMIN" {
		t.Errorf("details.scope = %v, mau PRODUCT_ADMIN", details["scope"])
	}
}

// Cakupan ganda dibuang, bukan ditolak: ["TICKET","TICKET"] maksudnya jelas.
func TestRegisterAgent_DuplicateScopesDeduplicated(t *testing.T) {
	svc, registry, _, _ := setupRegistration()

	req := validRegistration()
	req.Scopes = []string{"ticket", "TICKET", " Ticket "}

	if _, err := svc.RegisterAgent(context.Background(), req, "OPS-2001", "127.0.0.1", "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := registry.registered["CS-2099"]
	if len(got) != 1 || got[0] != ScopeTicket {
		t.Errorf("scopes = %v, mau [TICKET]", got)
	}
}

func TestRegisterAgent_AlreadyRegistered(t *testing.T) {
	svc, _, _, _ := setupRegistration()

	if _, err := svc.RegisterAgent(context.Background(), validRegistration(), "OPS-2001", "127.0.0.1", "test"); err != nil {
		t.Fatalf("pendaftaran pertama: %v", err)
	}
	if _, err := svc.RegisterAgent(context.Background(), validRegistration(), "OPS-2001", "127.0.0.1", "test"); !errors.Is(err, apperr.AgentAlreadyRegistered) {
		t.Fatalf("mau AGENT_ALREADY_REGISTERED, dapat %v", err)
	}
}

// Tanpa direktori pegawai, pendaftaran MENOLAK — bukan meluluskan NPP yang tidak pernah
// diperiksa siapa pun.
func TestRegisterAgent_RefusedWithoutHRIS(t *testing.T) {
	svc := NewAgentSessionService(AgentSessionServiceConfig{
		Registry:    newMockAgentRegistry(),
		Supervisors: &mockSupervisorRepo{tokens: map[string]string{"SPV-0021": "123456"}},
		Hasher:      stubPasswordHasher{},
		Clock:       clockAt("2026-10-07 10:00:00"),
	})

	if _, err := svc.RegisterAgent(context.Background(), validRegistration(), "OPS-2001", "127.0.0.1", "test"); !errors.Is(err, apperr.HRISUnavailable) {
		t.Fatalf("mau HRIS_UNAVAILABLE, dapat %v", err)
	}
}

// --- Pencarian jejak audit ---

func TestAuditQuery_LimitClampedAndCursorFromLastRow(t *testing.T) {
	now := time.Now().UTC()
	repo := &mockAuditEvents{}
	// limit+1 baris: yang terakhir hanya penanda masih-ada-lagi dan tidak ikut dikirim.
	for i := 0; i < 4; i++ {
		repo.rows = append(repo.rows, AuditEvent{
			ID:        uuid.New(),
			EventType: EventAgentLogin,
			Actor:     "agent:CS-1042",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	svc := NewAuditQueryService(repo)
	items, hasMore, next, err := svc.List(context.Background(), AuditFilter{Limit: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("mau 3 baris terkirim, dapat %d", len(items))
	}
	if !hasMore {
		t.Error("hasMore harus true")
	}
	if next == nil {
		t.Fatal("kursor berikutnya harus terisi")
	}
	// Kursornya dari baris TERAKHIR yang dikirim, bukan dari penanda.
	if next.ID != items[2].ID {
		t.Error("kursor harus menunjuk baris terakhir yang dikirim")
	}
}

func TestAuditQuery_LimitBounds(t *testing.T) {
	repo := &mockAuditEvents{}
	svc := NewAuditQueryService(repo)

	if _, _, _, err := svc.List(context.Background(), AuditFilter{Limit: 0}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotFiter.Limit != auditDefaultLimit {
		t.Errorf("limit 0 harus jadi default %d, dapat %d", auditDefaultLimit, repo.gotFiter.Limit)
	}

	if _, _, _, err := svc.List(context.Background(), AuditFilter{Limit: 9999}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotFiter.Limit != auditMaxLimit {
		t.Errorf("limit berlebih harus dipangkas ke %d, dapat %d", auditMaxLimit, repo.gotFiter.Limit)
	}
}

// Tanpa penulis audit, pembacaannya MENOLAK — bukan menjawab daftar kosong. Daftar
// kosong terbaca sebagai "tidak ada yang terjadi".
func TestAuditQuery_RefusesWithoutRepo(t *testing.T) {
	svc := NewAuditQueryService(nil)
	if _, _, _, err := svc.List(context.Background(), AuditFilter{}); !errors.Is(err, apperr.ProviderNotConfigured) {
		t.Fatalf("mau PROVIDER_NOT_CONFIGURED, dapat %v", err)
	}
}

func TestValidAuditEventType(t *testing.T) {
	for _, ev := range []string{EventAgentLogin, EventTerminalActivated, EventPIIAcknowledged} {
		if !ValidAuditEventType(ev) {
			t.Errorf("%q harus sah", ev)
		}
	}
	for _, ev := range []string{"", "AGENT_LOGOUT_", "agent_login", "CS_SESSION_VIEWED"} {
		if ValidAuditEventType(ev) {
			t.Errorf("%q tidak boleh sah", ev)
		}
	}
}

// --- Kunci meja petugas (clean desk policy, migrasi 000043) ---

// fakeDeskTerminals BERSTATUS, tidak seperti mockTerminalLookup: seluruh gunanya adalah
// memeriksa transisi ONLINE → LOCKED → ONLINE, dan mock yang selalu menjawab hal yang
// sama tidak bisa membuktikan satu pun dari enam aturan di bawah.
type fakeDeskTerminals struct {
	t     *Terminal
	gates []GateRecord

	// Jejak panggilan yang TIDAK boleh terjadi saat mengunci.
	deactivateCalls int
	setStatusCalls  int
}

func (f *fakeDeskTerminals) Register(context.Context, *Terminal) error { return nil }

func (f *fakeDeskTerminals) FindByID(context.Context, string) (*Terminal, error) {
	// Salinan, bukan pointer: service menyimpan keadaan SEBELUM membuka untuk menghitung
	// lamanya terkunci, dan pointer bersama akan membuatnya menghitung dari nilai yang
	// sudah ikut berubah.
	cp := *f.t
	return &cp, nil
}

// Activate meniru UPDATE sungguhan: ia ikut mengosongkan tirai, supaya petugas
// berikutnya mengambil alih loket yang bersih (aturan 6).
func (f *fakeDeskTerminals) Activate(_ context.Context, _, employeeID string, at time.Time) error {
	f.t.Status = TerminalOnline
	f.t.ActiveAgentID = employeeID
	f.t.ActivatedAt = &at
	f.t.LockedAt = nil
	f.t.LockedReason = ""
	return nil
}

// Deactivate ikut mengosongkan tirai, seperti SQL-nya (aturan 5).
func (f *fakeDeskTerminals) Deactivate(_ context.Context, _ string, _ time.Time) (bool, error) {
	f.deactivateCalls++
	if f.t.Status == TerminalOffline {
		return false, nil
	}
	f.t.Status = TerminalOffline
	f.t.ActiveAgentID = ""
	f.t.ActivatedAt = nil
	f.t.LockedAt = nil
	f.t.LockedReason = ""
	return true, nil
}

func (f *fakeDeskTerminals) SetStatus(_ context.Context, _ string, status TerminalStatus, _ time.Time) error {
	f.setStatusCalls++
	f.t.Status = status
	return nil
}

func (f *fakeDeskTerminals) UpsertGate(context.Context, *GateRecord) error { return nil }

func (f *fakeDeskTerminals) ListGates(context.Context, uuid.UUID) ([]GateRecord, error) {
	return f.gates, nil
}

func (f *fakeDeskTerminals) Lock(_ context.Context, _, employeeID string, reason LockReason, at time.Time) (bool, error) {
	if f.t.Status != TerminalOnline || f.t.ActiveAgentID != employeeID {
		return false, nil
	}
	f.t.Status = TerminalLocked
	f.t.LockedAt = &at
	f.t.LockedReason = reason
	return true, nil
}

func (f *fakeDeskTerminals) Unlock(_ context.Context, _, employeeID string, _ time.Time) (bool, error) {
	if f.t.Status != TerminalLocked || f.t.ActiveAgentID != employeeID {
		return false, nil
	}
	f.t.Status = TerminalOnline
	f.t.LockedAt = nil
	f.t.LockedReason = ""
	return true, nil
}

func (f *fakeDeskTerminals) FreezeLock(context.Context, string, time.Time) error {
	f.t.LockedReason = LockReasonFailedUnlock
	return nil
}

type fakeDeskCreds struct {
	login   *AgentLoginRecord
	pinHash string
	failed  int

	setPINCalls  int
	loginFailure int
}

func (f *fakeDeskCreds) FindForLogin(context.Context, string) (*AgentLoginRecord, error) {
	return f.login, nil
}

func (f *fakeDeskCreds) SetPassword(context.Context, string, string, time.Time) error { return nil }

func (f *fakeDeskCreds) RecordLoginFailure(_ context.Context, _ string, max int, lockFor time.Duration, now time.Time) (*time.Time, error) {
	f.loginFailure++
	if f.loginFailure >= max {
		until := now.Add(lockFor)
		if f.login != nil {
			f.login.LockedUntil = &until
		}
		return &until, nil
	}
	return nil, nil
}

func (f *fakeDeskCreds) ClearLoginFailures(context.Context, string) error {
	f.loginFailure = 0
	return nil
}

func (f *fakeDeskCreds) FindDeskPIN(_ context.Context, employeeID string) (*AgentDeskPINRecord, error) {
	return &AgentDeskPINRecord{
		EmployeeID:           employeeID,
		DeskPINHash:          f.pinHash,
		FailedUnlockAttempts: f.failed,
	}, nil
}

func (f *fakeDeskCreds) SetDeskPIN(_ context.Context, _, pinHash string, _ time.Time) error {
	f.setPINCalls++
	f.pinHash = pinHash
	f.failed = 0
	return nil
}

func (f *fakeDeskCreds) RecordUnlockFailure(context.Context, string, time.Time) (int, error) {
	f.failed++
	return f.failed, nil
}

func (f *fakeDeskCreds) ClearUnlockFailures(context.Context, string) error {
	f.failed = 0
	return nil
}

// recordingSessions membuktikan aturan 3 dan 4: sesinya tidak disentuh sama sekali.
type recordingSessions struct {
	endCalls   int
	touchCalls int
}

func (r *recordingSessions) Create(context.Context, *AgentSession, string) error { return nil }
func (r *recordingSessions) FindByToken(context.Context, string) (*SessionContext, error) {
	return nil, nil
}
func (r *recordingSessions) FindLiveByEmployee(context.Context, string) (*AgentSession, error) {
	return nil, nil
}
func (r *recordingSessions) End(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	r.endCalls++
	return true, nil
}
func (r *recordingSessions) Touch(context.Context, uuid.UUID, time.Time) error {
	r.touchCalls++
	return nil
}

type deskFixture struct {
	svc       *TerminalService
	terminals *fakeDeskTerminals
	creds     *fakeDeskCreds
	sessions  *recordingSessions
	audit     *mockAuditEvents
	sess      *SessionContext
	now       time.Time
}

// newDeskFixture menyiapkan petugas yang sudah ONLINE dengan PIN meja "730514".
func newDeskFixture(t *testing.T) *deskFixture {
	t.Helper()

	clock := clockAt("2026-10-14 09:45:00")
	now := clock()
	activated := now.Add(-2 * time.Hour)

	terminals := &fakeDeskTerminals{t: &Terminal{
		TerminalID:    "CS-FL-04",
		Workstation:   "Loket 4",
		Location:      "KCU Semarang",
		Status:        TerminalOnline,
		ActiveAgentID: "CS-1042",
		ActivatedAt:   &activated,
	}}
	creds := &fakeDeskCreds{
		pinHash: "argon2:730514",
		login:   &AgentLoginRecord{EmployeeID: "CS-1042", Name: "Sarah", PasswordHash: "argon2:kata-sandi-panjang"},
	}
	sessions := &recordingSessions{}
	audit := &mockAuditEvents{}

	return &deskFixture{
		svc: NewTerminalService(TerminalServiceConfig{
			Terminals: terminals,
			Sessions:  sessions,
			Audit:     audit,
			Creds:     creds,
			Hasher:    stubPasswordHasher{},
			Clock:     clock,
		}),
		terminals: terminals,
		creds:     creds,
		sessions:  sessions,
		audit:     audit,
		sess: &SessionContext{
			SessionID:  uuid.New(),
			EmployeeID: "CS-1042",
			Name:       "Sarah",
			TerminalID: "CS-FL-04",
			ExpiresAt:  now.Add(7 * time.Hour),
		},
		now: now,
	}
}

func (f *deskFixture) lock(t *testing.T) *DeskLockState {
	t.Helper()
	state, err := f.svc.LockDesk(context.Background(), f.sess, LockDeskRequest{}, "10.0.0.1", "cs-desktop")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	return state
}

func (f *deskFixture) hasEvent(eventType string) bool {
	for _, ev := range f.audit.inserted {
		if ev.EventType == eventType {
			return true
		}
	}
	return false
}

func codeOf(err error) string { return apperr.From(err).Code }

// Mengunci memindahkan terminal ke LOCKED tanpa melepas pemegangnya.
//
// active_agent_id harus tetap terisi: melepasnya akan membuat unlock tidak tahu siapa
// yang berhak membuka, dan loket terkunci jadi loket yang tidak bisa dibuka siapa pun.
func TestLockDesk_MovesTerminalToLockedAndKeepsAgent(t *testing.T) {
	f := newDeskFixture(t)

	state := f.lock(t)

	if state.Status != TerminalLocked || !state.Locked {
		t.Fatalf("status = %s locked = %v, mau LOCKED/true", state.Status, state.Locked)
	}
	if state.LockedReason != LockReasonAgent {
		t.Errorf("reason = %q, mau AGENT (default saat body kosong)", state.LockedReason)
	}
	if f.terminals.t.ActiveAgentID != "CS-1042" {
		t.Error("active_agent_id tidak boleh dilepas saat mengunci")
	}
	if state.AttemptsRemaining != MaxUnlockAttempts {
		t.Errorf("attempts_remaining = %d, mau %d", state.AttemptsRemaining, MaxUnlockAttempts)
	}
	if !f.hasEvent(EventDeskLocked) {
		t.Error("DESK_LOCKED harus tercatat")
	}
}

// Aturan 1, 3, dan 4 sekaligus: mengunci TIDAK menyentuh sesi petugas.
//
// Panggilan video yang sedang berjalan juga tidak diputus — dibuktikan secara struktural:
// TerminalService tidak memegang satu pun repository panggilan atau hub signaling, jadi
// tidak ada jalan baginya memutus apa pun. Yang bisa diuji di sini adalah sisanya: sesi
// tidak ditutup (aturan 3) dan last_seen_at tidak disentuh (aturan 4).
func TestLockDesk_DoesNotTouchSession(t *testing.T) {
	f := newDeskFixture(t)
	before := f.sess.ExpiresAt

	f.lock(t)

	if f.sessions.endCalls != 0 {
		t.Error("mengunci layar tidak boleh menutup sesi petugas")
	}
	if f.sessions.touchCalls != 0 {
		t.Error("last_seen_at tidak boleh disentuh: loket terkunci adalah loket yang tidak ditunggui")
	}
	if !f.sess.ExpiresAt.Equal(before) {
		t.Error("tenggat sesi tidak boleh diperpanjang oleh penguncian")
	}
	if f.terminals.deactivateCalls != 0 {
		t.Error("mengunci bukan menonaktifkan: terminal tidak boleh jatuh ke OFFLINE")
	}
}

// Aturan 2: antrean berhenti mengalir. AssertOnline adalah penjaga Rule 4 yang dipanggil
// `agent-token`, dan ia menuntut ONLINE persis.
func TestLockDesk_StopsQueueAssignment(t *testing.T) {
	f := newDeskFixture(t)
	ctx := context.Background()

	if err := f.svc.AssertOnline(ctx, "CS-FL-04", "CS-1042"); err != nil {
		t.Fatalf("prasyarat: loket harus bisa menerima panggilan sebelum dikunci: %v", err)
	}

	f.lock(t)

	err := f.svc.AssertOnline(ctx, "CS-FL-04", "CS-1042")
	if codeOf(err) != apperr.TerminalNotOnline.Code {
		t.Fatalf("setelah terkunci AssertOnline = %v, mau TERMINAL_NOT_ONLINE", err)
	}
}

// Idempoten. Klien memanggil ini dari tombol DAN pintasan papan tik; balapan di antara
// keduanya bukan galat.
func TestLockDesk_IsIdempotent(t *testing.T) {
	f := newDeskFixture(t)

	first := f.lock(t)
	second := f.lock(t)

	if second.Status != TerminalLocked {
		t.Fatalf("kunci kedua = %s, mau tetap LOCKED", second.Status)
	}
	if first.LockedAt == nil || second.LockedAt == nil || !first.LockedAt.Equal(*second.LockedAt) {
		t.Error("locked_at tidak boleh berubah oleh penguncian kedua")
	}
}

// Mengunci tanpa pernah menyetel PIN berarti mengunci diri sendiri di luar.
func TestLockDesk_RefusesWithoutDeskPIN(t *testing.T) {
	f := newDeskFixture(t)
	f.creds.pinHash = ""

	_, err := f.svc.LockDesk(context.Background(), f.sess, LockDeskRequest{}, "", "")
	if codeOf(err) != apperr.AgentDeskPINNotSet.Code {
		t.Fatalf("err = %v, mau AGENT_DESK_PIN_NOT_SET", err)
	}
	if f.terminals.t.Status != TerminalOnline {
		t.Error("loket tidak boleh berubah saat penguncian ditolak")
	}
}

// Hanya ONLINE yang bisa dikunci. Loket READY belum menerima nasabah, jadi tidak ada
// yang perlu ditutupi.
func TestLockDesk_RefusesWhenNotOnline(t *testing.T) {
	f := newDeskFixture(t)
	f.terminals.t.Status = TerminalReady

	_, err := f.svc.LockDesk(context.Background(), f.sess, LockDeskRequest{}, "", "")
	if codeOf(err) != apperr.TerminalNotOnline.Code {
		t.Fatalf("err = %v, mau TERMINAL_NOT_ONLINE", err)
	}
}

// FAILED_UNLOCK tidak boleh datang dari klien: loket yang bisa menyatakan dirinya beku
// atas permintaan sendiri adalah loket yang bisa memanggil supervisor tanpa pernah salah.
func TestLockDesk_RejectsFailedUnlockReasonFromClient(t *testing.T) {
	f := newDeskFixture(t)

	_, err := f.svc.LockDesk(context.Background(), f.sess,
		LockDeskRequest{Reason: LockReasonFailedUnlock}, "", "")
	if codeOf(err) != apperr.ValidationError.Code {
		t.Fatalf("err = %v, mau VALIDATION_ERROR", err)
	}
}

// Membuka dengan PIN benar mengembalikan loket ke ONLINE — bukan READY, karena gerbang
// kesiapannya tidak pernah batal — dan melaporkan berapa lama ia ditinggalkan.
func TestUnlockDesk_RestoresOnlineAndReportsDuration(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)

	// Tiga menit berlalu di balik tirai.
	locked := f.now
	f.svc.clock = func() time.Time { return locked.Add(184 * time.Second) }

	state, err := f.svc.UnlockDesk(context.Background(), f.sess,
		UnlockDeskRequest{PIN: "730514"}, "10.0.0.1", "cs-desktop")
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	if state.Status != TerminalOnline || state.Locked {
		t.Fatalf("status = %s locked = %v, mau ONLINE/false", state.Status, state.Locked)
	}
	if state.LockedDurationSeconds == nil || *state.LockedDurationSeconds != 184 {
		t.Errorf("locked_duration_seconds = %v, mau 184", state.LockedDurationSeconds)
	}
	if f.creds.failed != 0 {
		t.Errorf("failed_unlock_attempts = %d, mau 0 setelah berhasil", f.creds.failed)
	}
	if !f.hasEvent(EventDeskUnlocked) {
		t.Error("DESK_UNLOCKED harus tercatat")
	}
}

// Setiap kegagalan dicatat, bukan hanya yang ketiga: pola percobaan yang berhenti di dua,
// berulang tiap hari, di loket yang sama, adalah justru yang dicari pemeriksa.
func TestUnlockDesk_CountsDownAndFreezesOnThirdFailure(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)
	ctx := context.Background()

	for _, want := range []int{2, 1} {
		_, uerr := f.svc.UnlockDesk(ctx, f.sess, UnlockDeskRequest{PIN: "000000"}, "", "")
		if codeOf(uerr) != apperr.AgentDeskPINInvalid.Code {
			t.Fatalf("err = %v, mau AGENT_DESK_PIN_INVALID", uerr)
		}
		details, _ := apperr.From(uerr).Details.(map[string]any)
		if details["attempts_remaining"] != want {
			t.Errorf("attempts_remaining = %v, mau %d", details["attempts_remaining"], want)
		}
		if f.terminals.t.Status != TerminalLocked {
			t.Fatal("loket harus tetap terkunci setelah PIN salah")
		}
	}

	_, err := f.svc.UnlockDesk(ctx, f.sess, UnlockDeskRequest{PIN: "000000"}, "", "")
	if codeOf(err) != apperr.TerminalDeskFrozen.Code {
		t.Fatalf("percobaan ketiga = %v, mau TERMINAL_DESK_FROZEN", err)
	}
	// Beku adalah ALASAN terkunci, bukan status keenam.
	if f.terminals.t.Status != TerminalLocked {
		t.Errorf("status = %s, mau tetap LOCKED", f.terminals.t.Status)
	}
	if f.terminals.t.LockedReason != LockReasonFailedUnlock {
		t.Errorf("locked_reason = %q, mau FAILED_UNLOCK", f.terminals.t.LockedReason)
	}

	// PIN yang BENAR pun tidak lagi membuka: hanya supervisor yang bisa.
	_, err = f.svc.UnlockDesk(ctx, f.sess, UnlockDeskRequest{PIN: "730514"}, "", "")
	if codeOf(err) != apperr.TerminalDeskFrozen.Code {
		t.Fatalf("loket beku = %v, mau TERMINAL_DESK_FROZEN", err)
	}

	var failed int
	for _, ev := range f.audit.inserted {
		if ev.EventType == EventDeskUnlockFailed {
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("DESK_UNLOCK_FAILED tercatat %d kali, mau 3 — setiap kegagalan, bukan hanya yang ketiga", failed)
	}
}

// Body kosong karena bug klien tidak boleh bisa membekukan loket dalam tiga permintaan.
func TestUnlockDesk_EmptyPINIsNotAnAttempt(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)

	_, err := f.svc.UnlockDesk(context.Background(), f.sess, UnlockDeskRequest{PIN: "  "}, "", "")
	if codeOf(err) != apperr.ValidationError.Code {
		t.Fatalf("err = %v, mau VALIDATION_ERROR", err)
	}
	if f.creds.failed != 0 {
		t.Errorf("failed_unlock_attempts = %d, mau 0", f.creds.failed)
	}
}

func TestUnlockDesk_RefusesWhenNotLocked(t *testing.T) {
	f := newDeskFixture(t)

	_, err := f.svc.UnlockDesk(context.Background(), f.sess, UnlockDeskRequest{PIN: "730514"}, "", "")
	if codeOf(err) != apperr.TerminalNotLocked.Code {
		t.Fatalf("err = %v, mau TERMINAL_NOT_LOCKED", err)
	}
}

// Dual-control membuka loket beku, dan sekalian menyetel PIN baru selagi supervisor
// masih berdiri di sampingnya.
func TestUnlockOverride_OpensFrozenDeskAndResetsPIN(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)
	f.creds.failed = MaxUnlockAttempts
	f.terminals.t.LockedReason = LockReasonFailedUnlock
	f.terminals.gates = []GateRecord{{
		SessionID:        f.sess.SessionID,
		Gate:             GateSupervisorAuth,
		PassedAt:         f.now.Add(-time.Hour),
		ExpiresAt:        f.now.Add(GateTTL),
		AuthorizationRef: "BCA-AUTH-7F3A11",
		SupervisorID:     "SPV-002",
	}}

	state, err := f.svc.UnlockOverride(context.Background(), f.sess, UnlockOverrideRequest{
		AuthorizationRef: "BCA-AUTH-7F3A11",
		Password:         "kata-sandi-panjang",
		NewPIN:           "204817",
	}, "10.0.0.1", "cs-desktop")
	if err != nil {
		t.Fatalf("override: %v", err)
	}

	if state.Status != TerminalOnline || state.Frozen {
		t.Fatalf("status = %s frozen = %v, mau ONLINE/false", state.Status, state.Frozen)
	}
	if f.creds.pinHash != "argon2:204817" {
		t.Errorf("PIN baru tidak tersimpan: %q", f.creds.pinHash)
	}
	if f.creds.failed != 0 {
		t.Errorf("failed_unlock_attempts = %d, mau 0", f.creds.failed)
	}

	// Jejaknya harus menyebut override DAN rujukan otorisasinya — tanpa keduanya,
	// pembukaan dual-control tidak bisa dibedakan dari pembukaan biasa.
	var found bool
	for _, ev := range f.audit.inserted {
		if ev.EventType == EventDeskUnlocked && ev.Details["override"] == true {
			found = true
			if ev.Details["authorization_ref"] != "BCA-AUTH-7F3A11" {
				t.Errorf("authorization_ref = %v", ev.Details["authorization_ref"])
			}
			if ev.Details["supervisor_id"] != "SPV-002" {
				t.Errorf("supervisor_id = %v", ev.Details["supervisor_id"])
			}
		}
	}
	if !found {
		t.Error("DESK_UNLOCKED dengan override=true harus tercatat")
	}
}

// Dua bukti, dan keduanya perlu. Supervisor sendirian tidak cukup.
func TestUnlockOverride_NeedsBothProofs(t *testing.T) {
	gate := func(f *deskFixture) {
		f.terminals.gates = []GateRecord{{
			SessionID: f.sess.SessionID, Gate: GateSupervisorAuth,
			PassedAt: f.now.Add(-time.Hour), ExpiresAt: f.now.Add(GateTTL),
			AuthorizationRef: "BCA-AUTH-7F3A11", SupervisorID: "SPV-002",
		}}
	}

	cases := []struct {
		name     string
		setup    func(*deskFixture)
		req      UnlockOverrideRequest
		wantCode string
	}{
		{
			name:     "rujukan otorisasi tidak dikenal",
			setup:    gate,
			req:      UnlockOverrideRequest{AuthorizationRef: "BCA-AUTH-PALSU", Password: "kata-sandi-panjang"},
			wantCode: apperr.SupervisorTokenInvalid.Code,
		},
		{
			name: "otorisasi supervisor kedaluwarsa",
			setup: func(f *deskFixture) {
				f.terminals.gates = []GateRecord{{
					SessionID: f.sess.SessionID, Gate: GateSupervisorAuth,
					PassedAt: f.now.Add(-9 * time.Hour), ExpiresAt: f.now.Add(-time.Hour),
					AuthorizationRef: "BCA-AUTH-7F3A11", SupervisorID: "SPV-002",
				}}
			},
			req:      UnlockOverrideRequest{AuthorizationRef: "BCA-AUTH-7F3A11", Password: "kata-sandi-panjang"},
			wantCode: apperr.SupervisorTokenInvalid.Code,
		},
		{
			name:     "kata sandi petugas salah",
			setup:    gate,
			req:      UnlockOverrideRequest{AuthorizationRef: "BCA-AUTH-7F3A11", Password: "salah"},
			wantCode: apperr.AgentCredentialInvalid.Code,
		},
		{
			name:     "PIN baru lemah",
			setup:    gate,
			req:      UnlockOverrideRequest{AuthorizationRef: "BCA-AUTH-7F3A11", Password: "kata-sandi-panjang", NewPIN: "123456"},
			wantCode: apperr.CredWeakPIN.Code,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeskFixture(t)
			f.lock(t)
			f.creds.failed = MaxUnlockAttempts
			tc.setup(f)

			_, err := f.svc.UnlockOverride(context.Background(), f.sess, tc.req, "", "")
			if codeOf(err) != tc.wantCode {
				t.Fatalf("err = %v, mau %s", err, tc.wantCode)
			}
			if f.terminals.t.Status != TerminalLocked {
				t.Error("loket harus tetap terkunci saat override ditolak")
			}
		})
	}
}

// Aturan 5: logout saat terkunci tetap boleh, dan mengakhiri segalanya. Petugas yang baru
// ingat PIN-nya setelah pulang tidak boleh meninggalkan loket beku untuk giliran
// berikutnya.
func TestLogout_WhileLockedClosesEverything(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)
	f.creds.failed = MaxUnlockAttempts

	sessionSvc := NewAgentSessionService(AgentSessionServiceConfig{
		Creds:     f.creds,
		Sessions:  f.sessions,
		Terminals: f.terminals,
		Audit:     f.audit,
		Hasher:    stubPasswordHasher{},
		Clock:     func() time.Time { return f.now },
	})

	err := sessionSvc.Logout(context.Background(), f.sess.SessionID, "CS-1042", "CS-FL-04", "", "")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}

	if f.terminals.t.Status != TerminalOffline {
		t.Errorf("status = %s, mau OFFLINE", f.terminals.t.Status)
	}
	if f.terminals.t.LockedAt != nil || f.terminals.t.LockedReason != "" {
		t.Error("tirai harus ikut dibersihkan saat logout")
	}
}

// Aturan 6: loket beku tidak menghalangi petugas LAIN membuka giliran di situ.
//
// Yang beku adalah tirai milik petugas sebelumnya — failed_unlock_attempts menempel di
// PETUGAS, bukan di terminal — dan ia ikut selesai saat sesinya ditutup.
func TestFrozenDesk_DoesNotBlockAnotherAgent(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)
	f.creds.failed = MaxUnlockAttempts
	f.terminals.t.LockedReason = LockReasonFailedUnlock

	// Petugas pengganti: kredensialnya sendiri, penghitungnya sendiri (nol).
	replacement := &fakeDeskCreds{pinHash: "argon2:884210"}
	svc := NewTerminalService(TerminalServiceConfig{
		Terminals: f.terminals,
		Sessions:  f.sessions,
		Audit:     f.audit,
		Creds:     replacement,
		Hasher:    stubPasswordHasher{},
		Clock:     func() time.Time { return f.now },
	})
	ctx := context.Background()

	if err := f.terminals.Activate(ctx, "CS-FL-04", "CS-2099", f.now); err != nil {
		t.Fatalf("activate: %v", err)
	}

	sess := &SessionContext{
		SessionID: uuid.New(), EmployeeID: "CS-2099", TerminalID: "CS-FL-04",
		ExpiresAt: f.now.Add(SessionTTL),
	}
	state, err := svc.DeskLockState(ctx, sess)
	if err != nil {
		t.Fatalf("lock-state: %v", err)
	}

	if state.Locked || state.Frozen {
		t.Fatalf("petugas pengganti mendapat locked=%v frozen=%v, mau loket bersih", state.Locked, state.Frozen)
	}
	if state.AttemptsRemaining != MaxUnlockAttempts {
		t.Errorf("attempts_remaining = %d, mau %d", state.AttemptsRemaining, MaxUnlockAttempts)
	}
	if err := svc.AssertOnline(ctx, "CS-FL-04", "CS-2099"); err != nil {
		t.Errorf("petugas pengganti harus bisa menerima panggilan: %v", err)
	}
}

// PIN meja tunduk pada aturan kekuatan yang SAMA dengan PIN transaksi nasabah. Kredensial
// yang membuka lebih sedikit boleh lebih murah; "lebih murah" bukan berarti 123456 boleh.
func TestSetDeskPIN_EnforcesSharedStrengthRules(t *testing.T) {
	ctx := context.Background()

	for _, pin := range []string{"123456", "111111", "12345", "abcdef", "654321"} {
		f := newDeskFixture(t)
		f.creds.pinHash = ""
		svc := NewAgentSessionService(AgentSessionServiceConfig{
			Creds: f.creds, Sessions: f.sessions, Terminals: f.terminals,
			Audit: f.audit, Hasher: stubPasswordHasher{},
			Clock: func() time.Time { return f.now },
		})

		if _, err := svc.SetDeskPIN(ctx, f.sess, SetDeskPINRequest{NewPIN: pin}, "", ""); err == nil {
			t.Errorf("PIN %q harus ditolak", pin)
		}
		if f.creds.setPINCalls != 0 {
			t.Errorf("PIN %q: tidak boleh tersimpan", pin)
		}
	}
}

// Penggantian menuntut PIN lama; penyetelan PERTAMA tidak.
func TestSetDeskPIN_RequiresCurrentPINOnlyWhenSet(t *testing.T) {
	ctx := context.Background()

	f := newDeskFixture(t)
	f.creds.pinHash = ""
	svc := NewAgentSessionService(AgentSessionServiceConfig{
		Creds: f.creds, Sessions: f.sessions, Terminals: f.terminals,
		Audit: f.audit, Hasher: stubPasswordHasher{},
		Clock: func() time.Time { return f.now },
	})

	if _, err := svc.SetDeskPIN(ctx, f.sess, SetDeskPINRequest{NewPIN: "730514"}, "", ""); err != nil {
		t.Fatalf("penyetelan pertama: %v", err)
	}
	if f.creds.pinHash != "argon2:730514" {
		t.Fatalf("PIN tidak tersimpan: %q", f.creds.pinHash)
	}

	if _, err := svc.SetDeskPIN(ctx, f.sess, SetDeskPINRequest{NewPIN: "204817"}, "", ""); codeOf(err) != apperr.AgentDeskPINInvalid.Code {
		t.Fatalf("ganti tanpa PIN lama = %v, mau AGENT_DESK_PIN_INVALID", err)
	}
	if _, err := svc.SetDeskPIN(ctx, f.sess, SetDeskPINRequest{CurrentPIN: "730514", NewPIN: "204817"}, "", ""); err != nil {
		t.Fatalf("ganti dengan PIN lama benar: %v", err)
	}
}

// lock-state adalah yang membuat tirai selamat dari proses yang mati.
func TestDeskLockState_SurvivesProcessRestart(t *testing.T) {
	f := newDeskFixture(t)
	f.lock(t)

	// Aplikasi desktop dimulai ulang: tidak ada keadaan klien yang tersisa.
	state, err := f.svc.DeskLockState(context.Background(), f.sess)
	if err != nil {
		t.Fatalf("lock-state: %v", err)
	}
	if !state.Locked || state.Status != TerminalLocked {
		t.Fatalf("locked = %v status = %s, mau true/LOCKED", state.Locked, state.Status)
	}
	if !state.DeskPINSet {
		t.Error("desk_pin_set harus true")
	}
	if state.LockedAt == nil {
		t.Error("locked_at harus terisi supaya layar bisa menampilkan sejak kapan")
	}
}
