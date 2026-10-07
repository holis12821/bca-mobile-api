package onboarding

import (
	"context"
	"errors"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- mock in-memory ---

type mockProductRepo struct {
	catalog *SavingsProductCatalog
	err     error
	calls   int
}

func (m *mockProductRepo) ActiveCatalog(_ context.Context) (*SavingsProductCatalog, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.catalog, nil
}

type mockProductCache struct {
	stored *SavingsProductCatalog

	getErr error
	setErr error

	gets int
	sets int
}

func (m *mockProductCache) GetCatalog(_ context.Context) (*SavingsProductCatalog, error) {
	m.gets++
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.stored, nil
}

func (m *mockProductCache) SetCatalog(_ context.Context, c *SavingsProductCatalog) error {
	m.sets++
	if m.setErr != nil {
		return m.setErr
	}
	m.stored = c
	return nil
}

// sampleProductCatalog meniru isi migrasi 000039, termasuk urutannya.
func sampleProductCatalog() *SavingsProductCatalog {
	badge := ProductBadgeMostPopular
	return &SavingsProductCatalog{
		CatalogVersion: "2026-10-07.1",
		Page: ProductPage{
			Heading:      "Pilih Jenis Rekening",
			Subtitle:     "Pilih jenis rekening yang sesuai dengan kebutuhan dan gaya hidup Anda.",
			DepositLabel: "Setoran Awal Minimum",
			CTALabel:     "Lanjut",
			Notice: ProductNotice{
				IconKey: "INFO",
				Title:   "Persiapan Dokumen",
				Body:    "Siapkan e-KTP fisik Anda.",
			},
			Consent: ProductConsent{
				Prefix: "Dengan melanjutkan, Anda menyetujui ",
				Link:   "Syarat & Ketentuan",
				Suffix: " pembukaan rekening BCA.",
			},
		},
		Products: []ProductOption{
			{
				ProductType: ProductTahapanBCA, Name: "Tahapan BCA",
				MinInitialDeposit: 500000, Currency: "IDR",
				IconKey: ProductIconWallet, Style: ProductStylePrimary,
				Features:  []string{"Debit Mastercard", "m-BCA & KlikBCA", "Bebas tarik tunai di ribuan ATM"},
				IsPopular: true, BadgeKey: &badge, IsDefault: true,
				DisplayOrder: 1, AvailabilityStatus: ProductAvailable,
			},
			{
				ProductType: ProductTahapanXpre, Name: "Tahapan Xpresi",
				MinInitialDeposit: 50000, Currency: "IDR",
				IconKey: ProductIconCard, Style: ProductStyleSecondary,
				Features:     []string{"Desain Kartu Custom", "m-Banking 24/7"},
				DisplayOrder: 2, AvailabilityStatus: ProductAvailable,
			},
			{
				ProductType: ProductTabunganku, Name: "TabunganKu",
				MinInitialDeposit: 20000, Currency: "IDR",
				IconKey: ProductIconSavings, Style: ProductStyleNeutral,
				Features:     []string{"Tanpa biaya administrasi bulanan"},
				DisplayOrder: 3, AvailabilityStatus: ProductAvailable,
			},
		},
	}
}

func setupProductService(repo *mockProductRepo, cache *mockProductCache, maintenance ...string) *ProductService {
	var pred func(string) bool
	if len(maintenance) > 0 {
		pred = func(pt string) bool {
			for _, m := range maintenance {
				if m == pt {
					return true
				}
			}
			return false
		}
	}

	var c ProductCatalogCache
	if cache != nil {
		c = cache
	}

	return NewProductService(ProductServiceConfig{
		Repo:             repo,
		Cache:            c,
		Enabled:          true,
		UnderMaintenance: pred,
	})
}

// --- katalog normal ---

func TestProductCatalog_Success(t *testing.T) {
	repo := &mockProductRepo{catalog: sampleProductCatalog()}
	svc := setupProductService(repo, nil)

	got, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CatalogVersion != "2026-10-07.1" {
		t.Errorf("catalog_version = %q", got.CatalogVersion)
	}
	if len(got.Products) != 3 {
		t.Fatalf("mau 3 produk, dapat %d", len(got.Products))
	}

	// product_type adalah satu-satunya identitas produk; urutannya dari display_order.
	want := []ProductType{ProductTahapanBCA, ProductTahapanXpre, ProductTabunganku}
	for i, pt := range want {
		if got.Products[i].ProductType != pt {
			t.Errorf("produk[%d] = %s, mau %s", i, got.Products[i].ProductType, pt)
		}
	}

	// Nominal integer rupiah penuh, bukan string terformat.
	if got.Products[0].MinInitialDeposit != 500000 {
		t.Errorf("min_initial_deposit = %d, mau 500000", got.Products[0].MinInitialDeposit)
	}
	if !got.Products[0].IsPopular || got.Products[0].BadgeKey == nil {
		t.Error("TAHAPAN_BCA harus is_popular dengan badge_key")
	}
	if got.Products[1].BadgeKey != nil {
		t.Error("produk non-populer tidak boleh punya badge_key")
	}

	// Copy halaman ikut dilayani supaya bisa diubah tanpa rilis APK.
	if got.Page.Heading == "" || got.Page.CTALabel == "" {
		t.Error("page harus terisi")
	}
	// consent tetap tiga potong — client mencetak bagian tengah tebal.
	if got.Page.Consent.Prefix == "" || got.Page.Consent.Link == "" || got.Page.Consent.Suffix == "" {
		t.Errorf("consent harus tiga potong, dapat %+v", got.Page.Consent)
	}
}

// display_order yang terbalik di database harus tetap menghasilkan urutan yang benar —
// inilah yang membuat client berhenti bergantung posisi array.
func TestProductCatalog_SortedByDisplayOrder(t *testing.T) {
	catalog := sampleProductCatalog()
	catalog.Products[0].DisplayOrder = 3
	catalog.Products[2].DisplayOrder = 1

	svc := setupProductService(&mockProductRepo{catalog: catalog}, nil)
	got, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Products[0].ProductType != ProductTabunganku {
		t.Errorf("produk pertama = %s, mau TABUNGANKU", got.Products[0].ProductType)
	}
	if got.Products[2].ProductType != ProductTahapanBCA {
		t.Errorf("produk terakhir = %s, mau TAHAPAN_BCA", got.Products[2].ProductType)
	}
}

// --- katalog kosong ---

func TestProductCatalog_EmptyIsUnavailable(t *testing.T) {
	empty := sampleProductCatalog()
	empty.Products = nil

	for name, repo := range map[string]*mockProductRepo{
		"nil katalog": {catalog: nil},
		"nol produk":  {catalog: empty},
	} {
		svc := setupProductService(repo, nil)
		_, err := svc.Catalog(context.Background())
		if !errors.Is(err, apperr.OnboardingCatalogUnavailable) {
			t.Errorf("%s: mau ONBOARDING_CATALOG_UNAVAILABLE, dapat %v", name, err)
		}
	}
}

// --- cache ---

func TestProductCatalog_CacheMissFillsCache(t *testing.T) {
	repo := &mockProductRepo{catalog: sampleProductCatalog()}
	cache := &mockProductCache{}
	svc := setupProductService(repo, cache)

	if _, err := svc.Catalog(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cache.sets != 1 {
		t.Errorf("cache harus diisi sekali, dapat %d", cache.sets)
	}
	if repo.calls != 1 {
		t.Errorf("database harus dibaca sekali, dapat %d", repo.calls)
	}

	// Pembacaan kedua dilayani cache, bukan database.
	if _, err := svc.Catalog(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.calls != 1 {
		t.Errorf("database tidak boleh dibaca lagi, dapat %d pembacaan", repo.calls)
	}
}

// Galat cache TURUN ke database, bukan ke halaman error: Redis yang mati tidak boleh
// menutup pintu masuk buka rekening.
func TestProductCatalog_CacheErrorFallsThroughToDatabase(t *testing.T) {
	repo := &mockProductRepo{catalog: sampleProductCatalog()}
	cache := &mockProductCache{getErr: errors.New("redis down"), setErr: errors.New("redis down")}
	svc := setupProductService(repo, cache)

	got, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("galat cache tidak boleh menggagalkan permintaan: %v", err)
	}
	if len(got.Products) != 3 {
		t.Errorf("mau 3 produk dari database, dapat %d", len(got.Products))
	}
}

// Katalog kosong TIDAK di-cache: menyimpannya berarti menahan 503 selama TTL penuh
// setelah migrasi yang mengisinya akhirnya dijalankan.
func TestProductCatalog_EmptyIsNotCached(t *testing.T) {
	empty := sampleProductCatalog()
	empty.Products = nil
	cache := &mockProductCache{}
	svc := setupProductService(&mockProductRepo{catalog: empty}, cache)

	_, _ = svc.Catalog(context.Background())
	if cache.sets != 0 {
		t.Errorf("katalog kosong tidak boleh di-cache, dapat %d penulisan", cache.sets)
	}
}

// Service tanpa cache adalah deployment yang benar, hanya lebih lambat.
func TestProductCatalog_WorksWithoutCache(t *testing.T) {
	svc := setupProductService(&mockProductRepo{catalog: sampleProductCatalog()}, nil)
	if _, err := svc.Catalog(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProductCatalog_RepoErrorPropagates(t *testing.T) {
	svc := setupProductService(&mockProductRepo{err: errors.New("postgres tersendat")}, nil)
	if _, err := svc.Catalog(context.Background()); err == nil {
		t.Fatal("kegagalan database harus diteruskan")
	}
}

// --- maintenance ---

// INI test paling penting di berkas ini: maintenance TIDAK menghilangkan produk dari
// daftar. Nasabah perlu tahu produk itu ada dan sedang tutup, bukan bingung karena
// pilihannya lenyap.
func TestProductCatalog_MaintenanceKeepsProductInList(t *testing.T) {
	svc := setupProductService(&mockProductRepo{catalog: sampleProductCatalog()}, nil, "TABUNGANKU")

	got, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("maintenance tidak boleh membuat seluruh endpoint gagal: %v", err)
	}
	if len(got.Products) != 3 {
		t.Fatalf("produk tutup harus TETAP di daftar; mau 3, dapat %d", len(got.Products))
	}

	var tabunganku *ProductOption
	for i := range got.Products {
		if got.Products[i].ProductType == ProductTabunganku {
			tabunganku = &got.Products[i]
		}
	}
	if tabunganku == nil {
		t.Fatal("TABUNGANKU hilang dari daftar")
	}
	if tabunganku.AvailabilityStatus != ProductDisabled {
		t.Errorf("availability_status = %q, mau DISABLED", tabunganku.AvailabilityStatus)
	}
	if tabunganku.AvailabilityReasonKey == nil || *tabunganku.AvailabilityReasonKey != ProductReasonMaintenance {
		t.Errorf("availability_reason_key = %v, mau MAINTENANCE", tabunganku.AvailabilityReasonKey)
	}
	if tabunganku.Selectable() {
		t.Error("produk tutup tidak boleh selectable")
	}

	// Urutannya tidak berubah: produk tutup tidak dipindah ke bawah.
	if got.Products[2].ProductType != ProductTabunganku {
		t.Errorf("produk tutup tidak boleh dipindah; posisi 3 = %s", got.Products[2].ProductType)
	}

	// Dua produk lain tetap bisa dipilih.
	for _, p := range got.Products[:2] {
		if !p.Selectable() {
			t.Errorf("%s harus tetap bisa dipilih", p.ProductType)
		}
	}
}

// Status dari database tidak ditimpa kalau sudah bukan AVAILABLE: COMING_SOON punya
// alasan yang lebih tepat daripada MAINTENANCE.
func TestProductCatalog_MaintenanceDoesNotOverrideExistingStatus(t *testing.T) {
	catalog := sampleProductCatalog()
	reason := ProductReasonComingSoon
	catalog.Products[2].AvailabilityStatus = ProductComingSoon
	catalog.Products[2].AvailabilityReasonKey = &reason

	svc := setupProductService(&mockProductRepo{catalog: catalog}, nil, "TABUNGANKU")
	got, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Products[2].AvailabilityStatus != ProductComingSoon {
		t.Errorf("status = %q, mau COMING_SOON tetap", got.Products[2].AvailabilityStatus)
	}
	if *got.Products[2].AvailabilityReasonKey != ProductReasonComingSoon {
		t.Errorf("reason = %q, mau COMING_SOON tetap", *got.Products[2].AvailabilityReasonKey)
	}
}

// Maintenance diterapkan SESUDAH cache: statusnya datang dari environment, jadi objek
// yang dipegang cache tidak boleh ikut tersunting — permintaan berikutnya dengan env
// berbeda harus melihat hasil yang berbeda.
func TestProductCatalog_MaintenanceDoesNotMutateCachedCatalog(t *testing.T) {
	cached := sampleProductCatalog()
	cache := &mockProductCache{stored: cached}
	svc := setupProductService(&mockProductRepo{}, cache, "TABUNGANKU")

	if _, err := svc.Catalog(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cached.Products[2].AvailabilityStatus != ProductAvailable {
		t.Errorf("objek cache ikut tersunting: status = %q", cached.Products[2].AvailabilityStatus)
	}
	if cached.Products[2].AvailabilityReasonKey != nil {
		t.Error("objek cache ikut tersunting: reason_key terisi")
	}
}

// --- feature flag ---

// Flag mati menolak TANPA menyentuh database: database yang tetap dibaca saat fiturnya
// mati hanya membayar biaya untuk jawaban yang sudah ditentukan.
func TestProductCatalog_FlagOffRefusesWithoutTouchingDatabase(t *testing.T) {
	repo := &mockProductRepo{catalog: sampleProductCatalog()}
	svc := NewProductService(ProductServiceConfig{Repo: repo, Enabled: false})

	_, err := svc.Catalog(context.Background())
	if !errors.Is(err, apperr.OnboardingCatalogUnavailable) {
		t.Fatalf("mau ONBOARDING_CATALOG_UNAVAILABLE, dapat %v", err)
	}
	if repo.calls != 0 {
		t.Errorf("database tidak boleh dibaca saat flag mati, dapat %d pembacaan", repo.calls)
	}
}

// --- jejak setoran awal untuk sesi ---

func TestDepositShownFor(t *testing.T) {
	svc := setupProductService(&mockProductRepo{catalog: sampleProductCatalog()}, nil)

	deposit, version, ok := svc.DepositShownFor(context.Background(), ProductTahapanXpre)
	if !ok {
		t.Fatal("produk yang ada di katalog harus ditemukan")
	}
	if deposit != 50000 {
		t.Errorf("deposit = %d, mau 50000", deposit)
	}
	if version != "2026-10-07.1" {
		t.Errorf("version = %q", version)
	}
}

// Katalog yang mati TIDAK boleh mematikan pembuatan sesi: pemanggilnya melanjutkan
// dengan kedua kolom NULL.
func TestDepositShownFor_FailuresAreNotFatal(t *testing.T) {
	cases := map[string]*ProductService{
		"flag mati":      NewProductService(ProductServiceConfig{Repo: &mockProductRepo{catalog: sampleProductCatalog()}, Enabled: false}),
		"database galat": setupProductService(&mockProductRepo{err: errors.New("postgres tersendat")}, nil),
		"katalog nil":    setupProductService(&mockProductRepo{catalog: nil}, nil),
	}

	for name, svc := range cases {
		deposit, version, ok := svc.DepositShownFor(context.Background(), ProductTahapanBCA)
		if ok {
			t.Errorf("%s: harus melaporkan tidak ketemu", name)
		}
		if deposit != 0 || version != "" {
			t.Errorf("%s: mau nilai kosong, dapat (%d, %q)", name, deposit, version)
		}
	}
}

// Produk yang tidak ada di katalog melaporkan tidak ketemu, bukan angka nol yang
// terbaca sebagai "setoran awalnya Rp 0".
func TestDepositShownFor_UnknownProduct(t *testing.T) {
	catalog := sampleProductCatalog()
	catalog.Products = catalog.Products[:1] // hanya TAHAPAN_BCA
	svc := setupProductService(&mockProductRepo{catalog: catalog}, nil)

	if _, _, ok := svc.DepositShownFor(context.Background(), ProductTabunganku); ok {
		t.Fatal("produk di luar katalog harus melaporkan tidak ketemu")
	}
}
