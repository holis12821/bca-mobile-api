package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// ProductService melayani layar Pilih Jenis Rekening — layar PERTAMA buka rekening.
//
// Service sendiri, bukan bagian SessionService, dengan alasan yang sama seperti
// TNCService: isinya dibaca SEBELUM sesi ada. Layar ini muncul tanpa session_id dan
// tanpa Authorization, jadi menaruh pembacaannya di SessionService berarti sebuah
// service tentang sesi harus melayani permintaan yang tidak punya sesi.
type ProductService struct {
	repo  ProductCatalogRepository
	cache ProductCatalogCache

	// enabled mematikan katalog tanpa rollback deployment. Lihat
	// config.Client.ProductCatalogEnabled.
	enabled bool

	// underMaintenance menyebut produk yang sedang tidak melayani pembukaan rekening.
	// Predikat, bukan daftar, supaya service ini tidak perlu tahu bentuk konfigurasinya
	// — pola yang sama dengan NewCardHandler.
	underMaintenance func(productType string) bool
}

type ProductServiceConfig struct {
	Repo ProductCatalogRepository

	// Cache boleh nil. Katalognya kecil dan jarang berubah; melayaninya langsung dari
	// Postgres adalah deployment yang benar, hanya lebih lambat.
	Cache ProductCatalogCache

	// Enabled false membuat seluruh pembacaan menjawab
	// apperr.OnboardingCatalogUnavailable TANPA menyentuh database.
	Enabled bool

	// UnderMaintenance boleh nil; nil berarti tidak ada produk yang ditutup.
	UnderMaintenance func(productType string) bool
}

func NewProductService(cfg ProductServiceConfig) *ProductService {
	return &ProductService{
		repo:             cfg.Repo,
		cache:            cfg.Cache,
		enabled:          cfg.Enabled,
		underMaintenance: cfg.UnderMaintenance,
	}
}

// Catalog melayani GET /v1/onboarding/products.
//
// Urutan langkahnya mengikat:
//
//  1. Flag mati → menolak tanpa menyentuh database. Database yang tetap dibaca saat
//     fiturnya mati hanya membayar biaya untuk jawaban yang sudah ditentukan.
//  2. Cache, lalu database saat miss. Galat cache TURUN ke database, bukan ke halaman
//     error — Redis yang mati tidak boleh menutup pintu masuk buka rekening.
//  3. Katalog tanpa satu pun produk → 503. Daftar kosong akan membuat client
//     menampilkan layar tanpa pilihan, dan nasabah berhenti di layar pertama tanpa
//     tahu kenapa.
//  4. Maintenance diterapkan SESUDAH cache. Statusnya datang dari environment, bukan
//     dari database, jadi menyimpannya ke cache akan membuat perubahan env tidak
//     terlihat sampai TTL habis.
func (s *ProductService) Catalog(ctx context.Context) (*SavingsProductCatalog, error) {
	if !s.enabled {
		return nil, apperr.OnboardingCatalogUnavailable
	}

	catalog, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if catalog == nil || len(catalog.Products) == 0 {
		return nil, apperr.OnboardingCatalogUnavailable
	}

	// Salinan, bukan pointer ke objek cache: penerapan maintenance di bawah menyunting
	// per produk, dan menyunting objek yang dipegang cache in-process akan membuat
	// permintaan berikutnya melihat hasil suntingan permintaan sebelumnya.
	out := *catalog
	out.Products = make([]ProductOption, len(catalog.Products))
	copy(out.Products, catalog.Products)

	s.applyMaintenance(out.Products)

	// Urut menurut display_order. Produk tutup TIDAK dipindah ke bawah: urutannya
	// keputusan product owner, dan memindahkannya sendiri berarti layar menyusun ulang
	// dirinya tanpa ada yang memintanya.
	//
	// SliceStable, bukan Slice: dua produk dengan display_order yang sama harus tetap
	// pada urutan yang diberikan database, bukan urutan yang ditentukan pemecah seri
	// yang bisa berubah antar pemanggilan.
	sort.SliceStable(out.Products, func(i, j int) bool {
		return out.Products[i].DisplayOrder < out.Products[j].DisplayOrder
	})

	return &out, nil
}

// applyMaintenance menutup produk yang disebut environment.
//
// Produk yang tutup TETAP DIKEMBALIKAN dengan availability_status DISABLED dan
// availability_reason_key MAINTENANCE — tidak disaring keluar, dan tidak membuat seluruh
// endpoint menjawab 422. Nasabah perlu tahu produk itu ada dan sedang tutup, bukan
// bingung karena pilihannya lenyap.
//
// Status dari database TIDAK ditimpa kalau ia sudah bukan AVAILABLE: produk yang
// ditandai COMING_SOON di katalog punya alasan yang lebih tepat daripada MAINTENANCE,
// dan menimpanya akan menyembunyikan alasan yang sebenarnya.
func (s *ProductService) applyMaintenance(products []ProductOption) {
	if s.underMaintenance == nil {
		return
	}

	for i := range products {
		if products[i].AvailabilityStatus != ProductAvailable {
			continue
		}
		if !s.underMaintenance(string(products[i].ProductType)) {
			continue
		}

		reason := ProductReasonMaintenance
		products[i].AvailabilityStatus = ProductDisabled
		products[i].AvailabilityReasonKey = &reason
	}
}

// load membaca katalog lewat cache.
func (s *ProductService) load(ctx context.Context) (*SavingsProductCatalog, error) {
	if s.cache != nil {
		cached, err := s.cache.GetCatalog(ctx)
		if err != nil {
			// Peringatan, bukan kegagalan — alasan yang sama dengan TNCService.load.
			slog.Warn("product catalog cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	catalog, err := s.repo.ActiveCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("read product catalog: %w", err)
	}

	// Katalog kosong tidak di-cache: menyimpannya berarti menahan jawaban 503 selama
	// 24 jam setelah migrasi yang mengisinya akhirnya dijalankan.
	if s.cache != nil && catalog != nil && len(catalog.Products) > 0 {
		if err := s.cache.SetCatalog(ctx, catalog); err != nil {
			slog.Warn("product catalog cache set failed", "error", err)
		}
	}

	return catalog, nil
}

// DepositShownFor mencari setoran awal yang DILIHAT nasabah untuk satu produk.
//
// Dipanggil CreateSession untuk mengisi min_initial_deposit_shown. Mengembalikan
// (0, "", false) kalau katalognya tidak bisa dibaca atau produknya tidak ada di sana —
// dan itu BUKAN kegagalan: sesi tetap lahir dengan kedua kolom NULL. Katalog yang mati
// tidak boleh mematikan pembuatan sesi.
//
// Produk yang tutup tetap menjawab angkanya. Yang menolak pembuatan sesi adalah
// pemeriksaan maintenance di CreateSession, bukan fungsi ini — dan kalau nasabah
// berhasil membuat sesi, angka yang dilihatnya tetap perlu tercatat.
func (s *ProductService) DepositShownFor(ctx context.Context, pt ProductType) (deposit int64, catalogVersion string, ok bool) {
	if !s.enabled {
		return 0, "", false
	}

	catalog, err := s.load(ctx)
	if err != nil {
		// Dicatat, tidak dinaikkan: pemanggilnya sedang membuat sesi, dan jejak
		// setoran awal bukan alasan yang cukup untuk menolaknya.
		slog.Warn("read product catalog for session trail failed", "error", err)
		return 0, "", false
	}
	if catalog == nil {
		return 0, "", false
	}

	for _, p := range catalog.Products {
		if p.ProductType == pt {
			return p.MinInitialDeposit, catalog.CatalogVersion, true
		}
	}
	return 0, "", false
}
