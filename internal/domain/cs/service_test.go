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
