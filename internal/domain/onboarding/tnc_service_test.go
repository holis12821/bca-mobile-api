package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- mock in-memory, mengikuti pola test domain lain di paket ini ---

type mockTNCRepo struct {
	docs map[string]*TNCDocument // kunci: versi

	// active adalah versi yang is_active. "" berarti tidak ada versi aktif.
	active string

	activeCalls  int
	versionCalls int
	activeErr    error
}

func (m *mockTNCRepo) ActiveTNC(_ context.Context) (*TNCDocument, error) {
	m.activeCalls++
	if m.activeErr != nil {
		return nil, m.activeErr
	}
	if m.active == "" {
		return nil, apperr.TNCUnavailable
	}
	return m.docs[m.active], nil
}

func (m *mockTNCRepo) TNCByVersion(_ context.Context, version string) (*TNCDocument, error) {
	m.versionCalls++
	doc, ok := m.docs[version]
	if !ok {
		return nil, apperr.TNCVersionUnknown
	}
	return doc, nil
}

type mockTNCCache struct {
	entries map[string]*TNCDocument // kunci: versi ("" = entri versi aktif)
	getErr  error
	setErr  error
	sets    int
}

func (m *mockTNCCache) GetTNC(_ context.Context, version string) (*TNCDocument, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.entries[version], nil
}

func (m *mockTNCCache) SetTNC(_ context.Context, version string, doc *TNCDocument) error {
	m.sets++
	if m.setErr != nil {
		return m.setErr
	}
	if m.entries == nil {
		m.entries = map[string]*TNCDocument{}
	}
	m.entries[version] = doc
	return nil
}

func tncDoc(version string, active bool) *TNCDocument {
	return &TNCDocument{
		Version:       version,
		Heading:       "Syarat & Ketentuan Pembukaan Rekening",
		IsActive:      active,
		EffectiveFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Sections: []TNCSection{
			{IconKey: "ACCOUNT_BOX", Title: "1. Ketentuan Umum", Body: "..."},
		},
	}
}

// repoWithVersions membangun repo yang mengenal beberapa versi, dengan `active`
// sebagai satu-satunya yang berlaku.
func repoWithVersions(active string, others ...string) *mockTNCRepo {
	docs := map[string]*TNCDocument{active: tncDoc(active, true)}
	for _, v := range others {
		docs[v] = tncDoc(v, false)
	}
	return &mockTNCRepo{docs: docs, active: active}
}

// --- Active / ByVersion ---

func TestTNCServiceActiveReadsRepoThenCaches(t *testing.T) {
	repo := repoWithVersions("2026-09-01")
	cache := &mockTNCCache{}
	svc := NewTNCService(TNCServiceConfig{Repo: repo, Cache: cache})

	doc, err := svc.Active(context.Background())
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if doc.Version != "2026-09-01" {
		t.Fatalf("version = %q, mau 2026-09-01", doc.Version)
	}
	if cache.sets != 1 {
		t.Fatalf("cache sets = %d, mau 1", cache.sets)
	}

	// Pembacaan kedua harus dijawab cache, bukan database.
	if _, err := svc.Active(context.Background()); err != nil {
		t.Fatalf("Active kedua: %v", err)
	}
	if repo.activeCalls != 1 {
		t.Fatalf("repo dibaca %d kali, mau 1 — cache tidak dipakai", repo.activeCalls)
	}
}

// Entri cache versi aktif dan entri versi tertentu TIDAK boleh saling tertukar.
// Keduanya bisa berisi dokumen dengan versi sama, tapi kunci "" adalah janji
// "apa pun yang aktif sekarang" — kalau ByVersion menulis ke sana, pergantian
// versi tidak akan pernah terlihat.
func TestTNCServiceByVersionDoesNotPoisonActiveEntry(t *testing.T) {
	repo := repoWithVersions("2026-09-01", "2025-01-01")
	cache := &mockTNCCache{}
	svc := NewTNCService(TNCServiceConfig{Repo: repo, Cache: cache})

	if _, err := svc.ByVersion(context.Background(), "2025-01-01"); err != nil {
		t.Fatalf("ByVersion: %v", err)
	}

	if _, ok := cache.entries[""]; ok {
		t.Fatal("ByVersion menulis ke entri versi aktif")
	}
	if got := cache.entries["2025-01-01"]; got == nil {
		t.Fatal("ByVersion tidak menyimpan versinya sendiri")
	}
}

// Versi kosong pada ByVersion adalah permintaan versi aktif, bukan galat:
// `?version=` tanpa nilai adalah bentuk yang wajar dari client.
func TestTNCServiceByVersionEmptyMeansActive(t *testing.T) {
	repo := repoWithVersions("2026-09-01")
	svc := NewTNCService(TNCServiceConfig{Repo: repo})

	doc, err := svc.ByVersion(context.Background(), "   ")
	if err != nil {
		t.Fatalf("ByVersion kosong: %v", err)
	}
	if doc.Version != "2026-09-01" {
		t.Fatalf("version = %q, mau versi aktif", doc.Version)
	}
	if repo.versionCalls != 0 {
		t.Fatal("versi kosong dicari sebagai versi tertentu")
	}
}

func TestTNCServiceByVersionUnknown(t *testing.T) {
	svc := NewTNCService(TNCServiceConfig{Repo: repoWithVersions("2026-09-01")})

	_, err := svc.ByVersion(context.Background(), "tidak-ada")
	if apperr.From(err).Code != apperr.TNCVersionUnknown.Code {
		t.Fatalf("code = %q, mau TNC_VERSION_UNKNOWN", apperr.From(err).Code)
	}
}

// Tidak ada versi aktif adalah 503, bukan 404: itu salah konfigurasi server
// (migrasi 000025 belum jalan), dan 4xx akan terbaca di aplikasi sebagai
// kesalahan nasabah.
func TestTNCServiceActiveMissingIsUnavailable(t *testing.T) {
	svc := NewTNCService(TNCServiceConfig{Repo: &mockTNCRepo{docs: map[string]*TNCDocument{}}})

	_, err := svc.Active(context.Background())
	if apperr.From(err).Code != apperr.TNCUnavailable.Code {
		t.Fatalf("code = %q, mau TNC_UNAVAILABLE", apperr.From(err).Code)
	}
}

// Redis mati tidak boleh menutup pintu masuk buka rekening.
func TestTNCServiceSurvivesCacheFailure(t *testing.T) {
	repo := repoWithVersions("2026-09-01")
	cache := &mockTNCCache{
		getErr: errors.New("redis down"),
		setErr: errors.New("redis down"),
	}
	svc := NewTNCService(TNCServiceConfig{Repo: repo, Cache: cache})

	doc, err := svc.Active(context.Background())
	if err != nil {
		t.Fatalf("Active dengan cache mati: %v", err)
	}
	if doc.Version != "2026-09-01" {
		t.Fatalf("version = %q", doc.Version)
	}
}

// --- ValidateVersion ---

func TestTNCServiceValidateVersionAcceptsActive(t *testing.T) {
	svc := NewTNCService(TNCServiceConfig{Repo: repoWithVersions("2026-09-01")})

	doc, err := svc.ValidateVersion(context.Background(), "2026-09-01")
	if err != nil {
		t.Fatalf("ValidateVersion: %v", err)
	}
	if doc.Version != "2026-09-01" {
		t.Fatalf("version = %q", doc.Version)
	}
}

// Versi lama DITOLAK, bukan diterima dengan penanda outdated seperti
// card_catalog_version: menerimanya berarti bank menyimpan persetujuan atas
// pasal yang tidak pernah dibaca nasabah.
func TestTNCServiceValidateVersionRejectsSupersededWithCurrent(t *testing.T) {
	svc := NewTNCService(TNCServiceConfig{Repo: repoWithVersions("2026-09-01", "2025-01-01")})

	_, err := svc.ValidateVersion(context.Background(), "2025-01-01")
	appErr := apperr.From(err)
	if appErr.Code != apperr.TNCVersionOutdated.Code {
		t.Fatalf("code = %q, mau TNC_VERSION_OUTDATED", appErr.Code)
	}

	// `current_version` adalah satu-satunya cara aplikasi tahu harus memuat
	// ulang teks apa. Tanpa itu nasabah terjebak pada layar yang selalu gagal.
	details, ok := appErr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T, mau map", appErr.Details)
	}
	if details["current_version"] != "2026-09-01" {
		t.Fatalf("current_version = %v, mau 2026-09-01", details["current_version"])
	}
	if details["sent_version"] != "2025-01-01" {
		t.Fatalf("sent_version = %v, mau 2025-01-01", details["sent_version"])
	}
}

// Versi karangan dipisah dari versi lama: yang satu berarti client lama yang
// perlu memuat ulang, yang satu berarti bug client. Sebelum migrasi 000025
// keduanya diterima apa adanya.
func TestTNCServiceValidateVersionRejectsUnknown(t *testing.T) {
	svc := NewTNCService(TNCServiceConfig{Repo: repoWithVersions("2026-09-01")})

	_, err := svc.ValidateVersion(context.Background(), "haha")
	if apperr.From(err).Code != apperr.TNCVersionUnknown.Code {
		t.Fatalf("code = %q, mau TNC_VERSION_UNKNOWN", apperr.From(err).Code)
	}
}

func TestTNCServiceValidateVersionRejectsEmpty(t *testing.T) {
	repo := repoWithVersions("2026-09-01")
	svc := NewTNCService(TNCServiceConfig{Repo: repo})

	_, err := svc.ValidateVersion(context.Background(), "  ")
	if apperr.From(err).Code != apperr.ValidationError.Code {
		t.Fatalf("code = %q, mau VALIDATION_ERROR", apperr.From(err).Code)
	}
	// Tidak ada gunanya menyentuh database untuk versi kosong.
	if repo.activeCalls != 0 {
		t.Fatal("versi kosong tetap membaca database")
	}
}

// Database yang mati harus menjadi 500, bukan TNC_VERSION_OUTDATED. Kalau
// ValidateVersion menelan galatnya, setiap gangguan database akan terbaca di
// aplikasi sebagai "S&K Anda kedaluwarsa" dan nasabah memuat ulang teks yang
// sudah benar tanpa akhir.
func TestTNCServiceValidateVersionPropagatesRepoFailure(t *testing.T) {
	repo := repoWithVersions("2026-09-01")
	repo.activeErr = errors.New("connection refused")
	svc := NewTNCService(TNCServiceConfig{Repo: repo})

	_, err := svc.ValidateVersion(context.Background(), "2026-09-01")
	if apperr.From(err).Code != apperr.InternalError.Code {
		t.Fatalf("code = %q, mau INTERNAL_ERROR", apperr.From(err).Code)
	}
}

// --- CreateSession menolak versi yang salah ---

// Pemeriksaan S&K harus terjadi SEBELUM batas 3 sesi per perangkat: persetujuan
// yang salah versi tidak boleh menghabiskan satu jatah.
//
// Urutannya diuji dengan membuat KEDUA syarat gagal sekaligus — perangkat sudah
// di batas DAN versinya lama. Kode error yang kembali menunjukkan pemeriksaan
// mana yang berjalan lebih dulu, tanpa perlu menambah penghitung ke mock
// bersama.
func TestCreateSessionChecksTNCBeforeDeviceSessionLimit(t *testing.T) {
	sessions := newMockSessionRepo()
	sessions.counts["dev-1"] = maxSessionsPerDevice

	svc := NewSessionService(SessionServiceConfig{
		Sessions: sessions,
		TNC:      NewTNCService(TNCServiceConfig{Repo: repoWithVersions("2026-09-01", "2025-01-01")}),
	})

	_, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType:        string(ProductTahapanBCA),
		DeviceID:           "dev-1",
		AcceptedTNCVersion: "2025-01-01",
	}, "127.0.0.1", "test")

	if code := apperr.From(err).Code; code != apperr.TNCVersionOutdated.Code {
		t.Fatalf("code = %q, mau TNC_VERSION_OUTDATED — jatah sesi terpakai oleh persetujuan yang ditolak", code)
	}
	if len(sessions.sessions) != 0 {
		t.Fatal("sesi dibuat meski versi S&K ditolak")
	}
}

// Versi aktif tetap menghasilkan sesi: pemeriksaan baru ini tidak boleh
// mematikan jalur normal.
func TestCreateSessionAcceptsActiveTNCVersion(t *testing.T) {
	sessions := newMockSessionRepo()
	svc := NewSessionService(SessionServiceConfig{
		Sessions: sessions,
		TNC:      NewTNCService(TNCServiceConfig{Repo: repoWithVersions("2026-09-01")}),
	})

	resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType:        string(ProductTahapanBCA),
		DeviceID:           "dev-1",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	stored := sessions.sessions[resp.SessionID]
	if stored == nil {
		t.Fatal("sesi tidak tersimpan")
	}
	if stored.TNCVersion != "2026-09-01" {
		t.Fatalf("tnc_version tersimpan = %q, mau 2026-09-01", stored.TNCVersion)
	}
}
