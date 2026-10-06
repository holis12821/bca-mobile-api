package cs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
