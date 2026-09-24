package redis_test

import (
	"context"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

func sampleCatalog(version string) *onboarding.CardCatalog {
	badge := "RECOMMENDED_BEGINNER"
	min, max := 3, 7
	return &onboarding.CardCatalog{
		CatalogVersion:  version,
		ProductType:     onboarding.ProductTahapanBCA,
		DefaultCardType: "PASPOR_BLUE",
		Currency:        "IDR",
		Cards: []onboarding.CardOption{{
			CardType:     "PASPOR_BLUE",
			Name:         "Blue Mastercard",
			Network:      "MASTERCARD",
			TierKey:      "DEBIT",
			Style:        onboarding.CardStyleBlue,
			BadgeKey:     &badge,
			IsPopular:    true,
			DisplayOrder: 1,
			Fees:         onboarding.CardFees{MonthlyAdmin: 11, CardIssuance: 12, CardReplacement: 13},
			Limits: onboarding.CardLimits{
				CashWithdrawal: 21, TransferBCA: 22,
				TransferInterbank: 23, DebitPurchase: 24,
			},
			Availability: onboarding.CardAvailability{Status: onboarding.CardAvailable},
			Delivery: onboarding.CardDelivery{
				PhysicalCardAvailable: true,
				EstimatedDaysMin:      &min,
				EstimatedDaysMax:      &max,
				BranchPickupAvailable: true,
			},
			Eligibility: onboarding.CardEligibility{MinAge: 17, MinInitialDeposit: 500000},
		}},
	}
}

func TestCardCatalogCache_RoundTrip(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewCardCatalogCache(client)
	ctx := context.Background()

	want := sampleCatalog("2026-09-23.1")
	if err := cache.SetCatalog(ctx, onboarding.ProductTahapanBCA, "", "2026-09-23.1", want); err != nil {
		t.Fatalf("SetCatalog: %v", err)
	}

	got, err := cache.GetCatalog(ctx, onboarding.ProductTahapanBCA, "", "2026-09-23.1")
	if err != nil {
		t.Fatalf("GetCatalog: %v", err)
	}
	if got == nil {
		t.Fatal("katalog tidak ditemukan setelah disimpan")
	}

	// Yang diperiksa bukan sekadar "ada", tapi bahwa seluruh isi bertahan
	// melewati JSON — termasuk pointer yang boleh nil.
	if got.DefaultCardType != want.DefaultCardType || got.Currency != want.Currency {
		t.Fatalf("header katalog berubah: %+v", got)
	}
	if len(got.Cards) != 1 {
		t.Fatalf("expected 1 kartu, got %d", len(got.Cards))
	}
	c := got.Cards[0]
	if c.Fees != want.Cards[0].Fees || c.Limits != want.Cards[0].Limits {
		t.Fatalf("fees/limits berubah: %+v %+v", c.Fees, c.Limits)
	}
	if c.BadgeKey == nil || *c.BadgeKey != "RECOMMENDED_BEGINNER" {
		t.Fatalf("badge_key hilang: %v", c.BadgeKey)
	}
	if c.Availability.ReasonKey != nil {
		t.Fatalf("reason_key nil harus tetap nil, got %v", *c.Availability.ReasonKey)
	}
	if c.Delivery.EstimatedDaysMin == nil || *c.Delivery.EstimatedDaysMin != 3 {
		t.Fatalf("delivery min hilang: %v", c.Delivery.EstimatedDaysMin)
	}
}

// Kunci memuat versi, jadi katalog lama tidak pernah terbaca setelah versi
// naik — itulah yang membuat invalidasi cukup dengan menaikkan versi.
func TestCardCatalogCache_VersionScopesKey(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewCardCatalogCache(client)
	ctx := context.Background()

	if err := cache.SetCatalog(ctx, onboarding.ProductTahapanBCA, "", "2026-09-23.1",
		sampleCatalog("2026-09-23.1")); err != nil {
		t.Fatal(err)
	}

	got, err := cache.GetCatalog(ctx, onboarding.ProductTahapanBCA, "", "2026-09-23.2")
	if err != nil {
		t.Fatalf("GetCatalog versi baru: %v", err)
	}
	if got != nil {
		t.Fatal("versi baru tidak boleh membaca entri versi lama")
	}
}

// Wilayah ikut ke dalam kunci: katalog DKI tidak boleh terbaca sebagai nasional.
func TestCardCatalogCache_RegionScopesKey(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewCardCatalogCache(client)
	ctx := context.Background()

	if err := cache.SetCatalog(ctx, onboarding.ProductTahapanBCA, "DKI", "v1",
		sampleCatalog("v1")); err != nil {
		t.Fatal(err)
	}

	national, err := cache.GetCatalog(ctx, onboarding.ProductTahapanBCA, "", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if national != nil {
		t.Fatal("katalog berwilayah tidak boleh terbaca sebagai nasional")
	}

	dki, err := cache.GetCatalog(ctx, onboarding.ProductTahapanBCA, "DKI", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if dki == nil {
		t.Fatal("katalog DKI hilang")
	}
}

func TestCardCatalogCache_MissAndVersion(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewCardCatalogCache(client)
	ctx := context.Background()

	// Cache miss adalah nil, nil — bukan error. Pemanggil membedakan
	// "tidak ada" dari "Redis bermasalah" lewat error, bukan lewat nil.
	got, err := cache.GetCatalog(ctx, onboarding.ProductTahapanBCA, "", "tidak-ada")
	if err != nil {
		t.Fatalf("cache miss tidak boleh error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}

	v, err := cache.GetVersion(ctx)
	if err != nil {
		t.Fatalf("GetVersion kosong tidak boleh error: %v", err)
	}
	if v != "" {
		t.Fatalf("expected versi kosong, got %q", v)
	}

	if err := cache.SetVersion(ctx, "2026-09-23.5"); err != nil {
		t.Fatalf("SetVersion: %v", err)
	}
	v, err = cache.GetVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != "2026-09-23.5" {
		t.Fatalf("versi: got %q", v)
	}
}

// Entri rusak diperlakukan sebagai cache miss: satu nilai busuk di Redis tidak
// boleh membuat layar pilih kartu ikut mati.
func TestCardCatalogCache_CorruptEntryIsMiss(t *testing.T) {
	client := setupTestRedis(t)
	cache := redisrepo.NewCardCatalogCache(client)
	ctx := context.Background()

	if err := client.Set(ctx, "onb:cards:TAHAPAN_BCA:nat:v1", "{bukan json", 0).Err(); err != nil {
		t.Fatal(err)
	}

	got, err := cache.GetCatalog(ctx, onboarding.ProductTahapanBCA, "", "v1")
	if err != nil {
		t.Fatalf("entri rusak harus jadi cache miss, bukan error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
