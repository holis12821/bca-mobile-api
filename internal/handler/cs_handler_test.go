package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
)

// --- stub repository ---

type stubCSCustomers struct {
	byAccount map[string]*cs.Customer
	byID      map[uuid.UUID]*cs.Customer
	accounts  map[uuid.UUID][]cs.Account
}

func (s *stubCSCustomers) FindByAccountNumber(_ context.Context, n string) (*cs.Customer, error) {
	return s.byAccount[n], nil
}

func (s *stubCSCustomers) FindByPhoneHashes(_ context.Context, _ []string) ([]*cs.Customer, error) {
	return nil, nil
}

func (s *stubCSCustomers) FindByID(_ context.Context, id uuid.UUID) (*cs.Customer, error) {
	return s.byID[id], nil
}

func (s *stubCSCustomers) ListAccounts(_ context.Context, id uuid.UUID) ([]cs.Account, error) {
	return s.accounts[id], nil
}

type stubCSAccessLog struct{ count int }

func (s *stubCSAccessLog) Insert(_ context.Context, _ *cs.AccessLog) error {
	s.count++
	return nil
}

// newCSRouter memasang rute CS dengan penjaga identitas petugas yang BISA dimatikan.
//
// withAgent=false meniru kesalahan perakitan rute — endpoint PII terpasang tanpa
// AgentAuth. Itu keadaan yang harus dijawab 403, bukan 200 tanpa pelaku tercatat, dan
// satu-satunya cara mengujinya adalah merakitnya salah dengan sengaja.
func newCSRouter(withAgent bool) (*chi.Mux, *stubCSCustomers, *stubCSAccessLog) {
	customers := &stubCSCustomers{
		byAccount: make(map[string]*cs.Customer),
		byID:      make(map[uuid.UUID]*cs.Customer),
		accounts:  make(map[uuid.UUID][]cs.Account),
	}
	access := &stubCSAccessLog{}

	// audit dan dashboard nil: test ini hanya soal pencarian dan profil nasabah, dan
	// kedua handler yang memakainya menolak dengan PROVIDER_NOT_CONFIGURED saat nil —
	// bukan panic.
	h := handler.NewCSHandler(cs.NewService(cs.ServiceConfig{
		Customers: customers,
		Access:    access,
	}), nil, nil)

	r := chi.NewMux()
	r.Route("/customers", func(r chi.Router) {
		if withAgent {
			r.Use(middleware.AgentAuth(stubCSLookup{}, middleware.ScopeCustomerPII))
		}
		r.Get("/", h.SearchCustomers)
		r.Get("/{user_id}", h.GetCustomerProfile)
	})
	return r, customers, access
}

type stubCSLookup struct{}

func (stubCSLookup) AuthenticateAgent(_ context.Context, _, _ string) (string, []string, bool, error) {
	return "Sarah Adisti", []string{middleware.ScopeCustomerPII}, true, nil
}

func csRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Agent-Employee-ID", "CS-1042")
	req.Header.Set("X-Agent-API-Key", "secret")
	return req
}

// --- test ---

func TestCSSearch_RequiresQuery(t *testing.T) {
	r, _, _ := newCSRouter(true)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("q kosong = %d, mau 400", rr.Code)
	}
}

func TestCSSearch_ReturnsEmptyListNot404(t *testing.T) {
	r, _, _ := newCSRouter(true)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers?q=9999999999"))

	if rr.Code != http.StatusOK {
		t.Fatalf("tidak ditemukan = %d, mau 200 dengan daftar kosong", rr.Code)
	}

	var body struct {
		Data struct {
			Customers []cs.CustomerMatch `json:"customers"`
			Count     int                `json:"count"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Count != 0 || len(body.Data.Customers) != 0 {
		t.Fatalf("body = %+v", body.Data)
	}
}

func TestCSSearch_RejectsPartialQuery(t *testing.T) {
	r, _, _ := newCSRouter(true)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers?q=siti"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("pencarian nama = %d, mau 400", rr.Code)
	}
}

func TestCSSearch_FindsByAccountNumber(t *testing.T) {
	r, customers, access := newCSRouter(true)
	id := uuid.New()
	customers.byAccount["1234567890"] = &cs.Customer{
		UserID: id, FullName: "Siti Rahmawati",
		Phone: "081234567890", Tier: "REGULER", Status: "ACTIVE",
	}

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers?q=1234567890"))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rr.Code, rr.Body.String())
	}
	if access.count != 1 {
		t.Fatalf("%d baris jejak akses, mau 1", access.count)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Siti Rahmawati") {
		t.Fatalf("nama tidak ada di hasil: %s", body)
	}
}

func TestCSProfile_RejectsMalformedUUID(t *testing.T) {
	r, _, _ := newCSRouter(true)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers/bukan-uuid"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("uuid ngawur = %d, mau 400", rr.Code)
	}
}

func TestCSProfile_UnknownCustomerIs404(t *testing.T) {
	r, _, access := newCSRouter(true)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers/"+uuid.NewString()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, mau 404", rr.Code)
	}
	if access.count != 0 {
		t.Fatal("jejak tertulis untuk nasabah yang tidak ada")
	}
}

func TestCSProfile_ReturnsMaskedProfile(t *testing.T) {
	r, customers, access := newCSRouter(true)
	id := uuid.New()
	customers.byID[id] = &cs.Customer{
		UserID: id, FullName: "Siti Rahmawati", DisplayName: "Siti",
		NIK: "3171064509900002", Phone: "081234567890",
		Email: "siti.rahmawati@example.com",
		Tier:  "REGULER", Status: "ACTIVE", CreatedAt: time.Now(),
	}
	customers.accounts[id] = []cs.Account{{
		AccountNumber: "1234567890", AccountType: "TAHAPAN",
		AccountLabel: "Tahapan BCA", Currency: "IDR",
		IsPrimary: true, Status: "ACTIVE", OpenedAt: time.Now(),
	}}

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, csRequest("/customers/"+id.String()))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rr.Code, rr.Body.String())
	}
	if access.count != 1 {
		t.Fatalf("%d baris jejak akses, mau 1", access.count)
	}

	body := rr.Body.String()
	for _, secret := range []string{"3171064509900002", "081234567890", "siti.rahmawati@example.com"} {
		if strings.Contains(body, secret) {
			t.Fatalf("%q bocor utuh lewat HTTP: %s", secret, body)
		}
	}
}

// Rute PII yang terpasang tanpa AgentAuth harus menolak, bukan melayani tanpa pelaku.
func TestCSEndpoints_DenyWhenMountedWithoutAgentAuth(t *testing.T) {
	r, customers, access := newCSRouter(false)
	id := uuid.New()
	customers.byAccount["1234567890"] = &cs.Customer{UserID: id, FullName: "Siti"}
	customers.byID[id] = &cs.Customer{UserID: id, FullName: "Siti"}

	for _, path := range []string{"/customers?q=1234567890", "/customers/" + id.String()} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, csRequest(path))

		if rr.Code != http.StatusForbidden {
			t.Errorf("%s = %d, mau 403", path, rr.Code)
		}
	}
	if access.count != 0 {
		t.Fatal("data dilayani tanpa pelaku yang tercatat")
	}
}
