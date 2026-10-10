package onboarding

import (
	"context"
	"errors"
	"strconv"
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

	getErr        error
	setErr        error
	invalidateErr error

	gets        int
	sets        int
	invalidates int
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

func (m *mockProductCache) InvalidateCatalog(_ context.Context) error {
	m.invalidates++
	if m.invalidateErr != nil {
		return m.invalidateErr
	}
	m.stored = nil
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

// --- Admin katalog produk (Fase 6) ---

// mockProductAdminRepo mencatat bentuk penulisan yang diterimanya, termasuk BERAPA KALI
// versi dinaikkan — itu invarian yang paling mudah rusak dan paling sulit terlihat.
type mockProductAdminRepo struct {
	catalog *AdminProductCatalog
	listErr error

	writeErr error

	// writeCalls dan versionBumps dihitung terpisah: satu pemanggilan WriteProducts
	// yang menulis dua produk harus menghasilkan SATU kenaikan versi, dan dua angka
	// yang sama tidak bisa membuktikan itu.
	writeCalls   int
	versionBumps int

	lastWrites []ProductWrite
	lastActor  string
	lastIP     string
}

func (m *mockProductAdminRepo) ListAllProducts(_ context.Context) (*AdminProductCatalog, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.catalog, nil
}

func (m *mockProductAdminRepo) WriteProducts(_ context.Context, writes []ProductWrite, actor, ip string) (*ProductCatalogWriteResult, error) {
	m.writeCalls++
	m.lastWrites = writes
	m.lastActor = actor
	m.lastIP = ip

	if m.writeErr != nil {
		return nil, m.writeErr
	}

	// Satu kenaikan per pemanggilan, meniru UPDATE tunggal di akhir transaksi.
	m.versionBumps++

	result := &ProductCatalogWriteResult{
		CatalogVersion:   "2026-10-08." + strconv.Itoa(m.versionBumps),
		Updated:          make([]ProductType, 0, len(writes)),
		FeaturesReplaced: make([]ProductType, 0, len(writes)),
	}
	for _, w := range writes {
		result.Updated = append(result.Updated, w.ProductType)
		if w.Features != nil {
			result.FeaturesReplaced = append(result.FeaturesReplaced, w.ProductType)
		}
	}
	return result, nil
}

// validProductWrite menyusun badan penulisan yang sah untuk satu produk.
func validProductWrite(pt ProductType, order int) ProductWrite {
	return ProductWrite{
		ProductType:        pt,
		Name:               "Nama Produk",
		Description:        "Deskripsi produk.",
		MinInitialDeposit:  500000,
		Currency:           "IDR",
		IconKey:            ProductIconWallet,
		Style:              ProductStylePrimary,
		DisplayOrder:       order,
		IsActive:           true,
		AvailabilityStatus: ProductAvailable,
	}
}

// DoD Fase 6: penulisan dua produk dalam satu transaksi menaikkan versi SATU KALI.
//
// Dua versi untuk satu keputusan berarti ETag berganti dua kali padahal tidak ada
// katalog perantara yang pernah tayang, dan client yang menyimpan versi di antaranya
// menyimpan versi yang tidak pernah dilayani.
func TestWriteProducts_TwoProductsBumpVersionOnce(t *testing.T) {
	repo := &mockProductAdminRepo{}
	cache := &mockProductCache{stored: sampleProductCatalog()}
	svc := NewProductAdminService(repo, cache)

	result, err := svc.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{
			validProductWrite(ProductTahapanBCA, 1),
			validProductWrite(ProductTabunganku, 2),
		},
	}, "ops-1", "10.0.0.1")
	if err != nil {
		t.Fatalf("penulisan dua produk harus berhasil: %v", err)
	}

	if repo.writeCalls != 1 {
		t.Errorf("mau 1 pemanggilan repo, dapat %d", repo.writeCalls)
	}
	if repo.versionBumps != 1 {
		t.Errorf("mau versi naik 1 kali, dapat %d", repo.versionBumps)
	}
	if len(result.Updated) != 2 {
		t.Errorf("mau 2 produk tertulis, dapat %d", len(result.Updated))
	}
	if result.CatalogVersion != "2026-10-08.1" {
		t.Errorf("mau versi 2026-10-08.1, dapat %q", result.CatalogVersion)
	}
}

// Cache katalog aktif WAJIB dihapus setelah penulisan. Tanpa itu, kunci tetap
// onboarding:products:v1:catalog menyajikan katalog lama sampai TTL 24 jam habis —
// nasabah melihat setoran awal yang sudah diubah product owner.
func TestWriteProducts_InvalidatesCatalogCache(t *testing.T) {
	repo := &mockProductAdminRepo{}
	cache := &mockProductCache{stored: sampleProductCatalog()}
	svc := NewProductAdminService(repo, cache)

	if _, err := svc.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{validProductWrite(ProductTahapanBCA, 1)},
	}, "ops-1", "10.0.0.1"); err != nil {
		t.Fatalf("penulisan harus berhasil: %v", err)
	}

	if cache.invalidates != 1 {
		t.Errorf("mau cache dihapus 1 kali, dapat %d", cache.invalidates)
	}
	if cache.stored != nil {
		t.Error("entri katalog lama masih ada di cache setelah penulisan")
	}
}

// Cache yang gagal dihapus TIDAK menggagalkan penulisan: transaksinya sudah commit, dan
// error di titik ini akan membuat pemanggil mengulang permintaan yang sudah berhasil —
// menaikkan versi sekali lagi untuk perubahan yang sama.
func TestWriteProducts_CacheFailureDoesNotFailWrite(t *testing.T) {
	repo := &mockProductAdminRepo{}
	cache := &mockProductCache{invalidateErr: errors.New("redis tersendat")}
	svc := NewProductAdminService(repo, cache)

	result, err := svc.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{validProductWrite(ProductTahapanBCA, 1)},
	}, "ops-1", "10.0.0.1")
	if err != nil {
		t.Fatalf("galat cache tidak boleh menggagalkan penulisan: %v", err)
	}
	if result.CatalogVersion == "" {
		t.Error("versi katalog harus tetap dilaporkan")
	}
}

// Cache nil adalah deployment yang sah — tanpa Redis, pembacaan berikutnya memang sudah
// langsung ke database.
func TestWriteProducts_NilCacheIsFine(t *testing.T) {
	svc := NewProductAdminService(&mockProductAdminRepo{}, nil)

	if _, err := svc.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{validProductWrite(ProductTahapanBCA, 1)},
	}, "ops-1", ""); err != nil {
		t.Fatalf("cache nil harus tetap bisa menulis: %v", err)
	}
}

// Badan yang ditolak validasi TIDAK boleh menyentuh repository sama sekali: penulisan
// yang gagal di tengah transaksi lebih mahal daripada penolakan sebelum transaksi lahir.
func TestWriteProducts_ValidationRejectsBeforeRepo(t *testing.T) {
	disabled := validProductWrite(ProductTahapanBCA, 1)
	disabled.AvailabilityStatus = ProductDisabled // tanpa reason_key

	popularA := validProductWrite(ProductTahapanBCA, 1)
	popularA.IsPopular = true
	popularB := validProductWrite(ProductTabunganku, 2)
	popularB.IsPopular = true

	defaultDisabled := validProductWrite(ProductTahapanBCA, 1)
	defaultDisabled.IsDefault = true
	defaultDisabled.AvailabilityStatus = ProductComingSoon
	reason := ProductReasonComingSoon
	defaultDisabled.AvailabilityReasonKey = &reason

	inactivePopular := validProductWrite(ProductTahapanBCA, 1)
	inactivePopular.IsActive = false
	inactivePopular.IsPopular = true

	badIcon := validProductWrite(ProductTahapanBCA, 1)
	badIcon.IconKey = "GRADIENT_BLUE"

	zeroOrder := validProductWrite(ProductTahapanBCA, 0)

	negativeDeposit := validProductWrite(ProductTahapanBCA, 1)
	negativeDeposit.MinInitialDeposit = -1

	emptyFeature := validProductWrite(ProductTahapanBCA, 1)
	features := []string{"Gratis tarik tunai", "   "}
	emptyFeature.Features = &features

	cases := map[string][]ProductWrite{
		"daftar kosong":                {},
		"DISABLED tanpa alasan":        {disabled},
		"dua is_popular":               {popularA, popularB},
		"default tapi tidak AVAILABLE": {defaultDisabled},
		"tidak aktif tapi is_popular":  {inactivePopular},
		"icon_key di luar enum":        {badIcon},
		"display_order nol":            {zeroOrder},
		"setoran awal negatif":         {negativeDeposit},
		"fitur kosong":                 {emptyFeature},
		"produk disebut dua kali":      {validProductWrite(ProductTahapanBCA, 1), validProductWrite(ProductTahapanBCA, 2)},
		"product_type di luar enum":    {validProductWrite(ProductType("DEPOSITO"), 1)},
	}

	for name, writes := range cases {
		repo := &mockProductAdminRepo{}
		svc := NewProductAdminService(repo, nil)

		_, err := svc.WriteProducts(context.Background(),
			ProductCatalogWriteRequest{Products: writes}, "ops-1", "")
		if err == nil {
			t.Errorf("%s: harus ditolak", name)
			continue
		}
		if repo.writeCalls != 0 {
			t.Errorf("%s: repository tidak boleh dipanggil, dipanggil %d kali", name, repo.writeCalls)
		}
	}
}

// features: null berarti JANGAN SENTUH; features: [] berarti HAPUS SEMUA. Keduanya harus
// bisa dibedakan — tanpa itu, setiap penulisan harga akan menghapus teks fitur produknya.
func TestWriteProducts_FeaturesNilVersusEmpty(t *testing.T) {
	untouched := validProductWrite(ProductTahapanBCA, 1)
	// Features sengaja dibiarkan nil.

	cleared := validProductWrite(ProductTabunganku, 2)
	empty := []string{}
	cleared.Features = &empty

	repo := &mockProductAdminRepo{}
	svc := NewProductAdminService(repo, nil)

	result, err := svc.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{untouched, cleared},
	}, "ops-1", "")
	if err != nil {
		t.Fatalf("penulisan harus berhasil: %v", err)
	}

	if len(result.FeaturesReplaced) != 1 {
		t.Fatalf("mau 1 produk yang fiturnya diganti, dapat %d", len(result.FeaturesReplaced))
	}
	if result.FeaturesReplaced[0] != ProductTabunganku {
		t.Errorf("mau TABUNGANKU yang fiturnya diganti, dapat %s", result.FeaturesReplaced[0])
	}
}

// Aktor dan IP diteruskan ke repository: perubahan setoran awal harus bisa ditanyakan ke
// orangnya, dan jejaknya hari ini adalah log terstruktur.
func TestWriteProducts_PassesActorAndIP(t *testing.T) {
	repo := &mockProductAdminRepo{}
	svc := NewProductAdminService(repo, nil)

	if _, err := svc.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{validProductWrite(ProductTahapanBCA, 1)},
	}, "OPS-2001", "10.1.2.3"); err != nil {
		t.Fatalf("penulisan harus berhasil: %v", err)
	}

	if repo.lastActor != "OPS-2001" {
		t.Errorf("mau aktor OPS-2001, dapat %q", repo.lastActor)
	}
	if repo.lastIP != "10.1.2.3" {
		t.Errorf("mau IP 10.1.2.3, dapat %q", repo.lastIP)
	}
}

// Jalur admin TIDAK membaca feature flag katalog: katalog yang dimatikan karena isinya
// salah adalah justru saat isinya paling perlu diubah.
//
// Dibuktikan dengan membandingkannya langsung ke jalur nasabah pada keadaan yang sama.
func TestProductAdmin_IgnoresFeatureFlag(t *testing.T) {
	customer := NewProductService(ProductServiceConfig{
		Repo:    &mockProductRepo{catalog: sampleProductCatalog()},
		Enabled: false,
	})
	if _, err := customer.Catalog(context.Background()); err == nil {
		t.Fatal("jalur nasabah harus menolak saat flag mati")
	}

	admin := NewProductAdminService(&mockProductAdminRepo{
		catalog: &AdminProductCatalog{CatalogVersion: "2026-10-08.1"},
	}, nil)

	if _, err := admin.ListProducts(context.Background()); err != nil {
		t.Errorf("jalur admin harus tetap melayani saat flag mati: %v", err)
	}
	if _, err := admin.WriteProducts(context.Background(), ProductCatalogWriteRequest{
		Products: []ProductWrite{validProductWrite(ProductTahapanBCA, 1)},
	}, "ops-1", ""); err != nil {
		t.Errorf("jalur admin harus tetap bisa menulis saat flag mati: %v", err)
	}
}

// Katalog kosong untuk ADMIN adalah array kosong, bukan 503: layar admin yang melihat
// daftar kosong sedang melihat keadaan database yang sebenarnya.
func TestListProducts_EmptyIsNotAnError(t *testing.T) {
	for name, repo := range map[string]*mockProductAdminRepo{
		"katalog nil":   {catalog: nil},
		"produk nil":    {catalog: &AdminProductCatalog{CatalogVersion: "2026-10-08.1"}},
		"produk kosong": {catalog: &AdminProductCatalog{Products: []AdminProductRow{}}},
	} {
		svc := NewProductAdminService(repo, nil)

		catalog, err := svc.ListProducts(context.Background())
		if err != nil {
			t.Errorf("%s: tidak boleh error, dapat %v", name, err)
			continue
		}
		if catalog.Products == nil {
			t.Errorf("%s: products harus array kosong, bukan nil", name)
		}
	}
}
