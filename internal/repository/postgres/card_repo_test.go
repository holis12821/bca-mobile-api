package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/repository/postgres"
)

// setupCardDB menjalankan Postgres sungguhan dan memasang SELURUH migrasi.
//
// Migrasi dijalankan apa adanya, bukan skema yang ditulis ulang di dalam test:
// yang perlu dibuktikan di sini justru bahwa kode Go cocok dengan skema yang
// benar-benar dipasang ke produksi — pemindaian kolom, tipe enum
// onboarding_product_type, dan INET yang boleh NULL.
func setupCardDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("bcamobile"),
		tcpostgres.WithUsername("bcamobile"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	applyMigrations(ctx, t, pool)
	return pool
}

// applyMigrations memutar semua *.up.sql secara berurutan.
func applyMigrations(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	dir := filepath.Join("..", "..", "..", "migrations")
	entries, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("tidak ada migrasi ditemukan di %s", dir)
	}
	sort.Strings(entries)

	for _, path := range entries {
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(path), err)
		}
	}
}

// seedCards memasang katalog uji. Angkanya sengaja berbeda per kartu supaya
// kolom yang tertukar saat pemindaian langsung terlihat.
func seedCards(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	cards := []struct {
		Type, Name, Tier, Style string
		Admin, Issue, Replace   int64
		Cash, BCA, Inter, Debit int64
	}{
		{"PASPOR_BLUE", "Blue Mastercard", "DEBIT", "BLUE", 11, 12, 13, 21, 22, 23, 24},
		{"PASPOR_GOLD", "Gold Mastercard", "DEBIT", "GOLD", 31, 32, 33, 41, 42, 43, 44},
		{"PASPOR_PLATINUM", "Platinum Mastercard", "PLATINUM_DEBIT", "PLATINUM", 51, 52, 53, 61, 62, 63, 64},
	}
	for _, c := range cards {
		_, err := pool.Exec(ctx, `
			INSERT INTO card_products (card_type,name,tier_key,style,
				fee_monthly_admin,fee_card_issuance,fee_card_replacement,
				limit_cash_withdrawal,limit_transfer_bca,
				limit_transfer_interbank,limit_debit_purchase,
				delivery_days_min,delivery_days_max)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,3,7)`,
			c.Type, c.Name, c.Tier, c.Style,
			c.Admin, c.Issue, c.Replace, c.Cash, c.BCA, c.Inter, c.Debit)
		if err != nil {
			t.Fatalf("seed card %s: %v", c.Type, err)
		}
	}

	opts := []struct {
		Card    string
		Order   int
		Default bool
		Popular bool
		Badge   string
	}{
		{"PASPOR_BLUE", 1, true, true, "RECOMMENDED_BEGINNER"},
		{"PASPOR_GOLD", 2, false, false, "FLEXIBLE_TRANSACTION"},
		{"PASPOR_PLATINUM", 3, false, false, "MAX_LIMIT"},
	}
	for _, o := range opts {
		_, err := pool.Exec(ctx, `
			INSERT INTO product_card_options
				(product_type,card_type,display_order,is_default,is_popular,badge_key)
			VALUES ('TAHAPAN_BCA',$1,$2,$3,$4,$5)`,
			o.Card, o.Order, o.Default, o.Popular, o.Badge)
		if err != nil {
			t.Fatalf("seed option %s: %v", o.Card, err)
		}
	}
}

func TestCardRepo_ListCards(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)

	repo := postgres.NewCardRepo(pool)

	cards, err := repo.ListCards(ctx, onboarding.ProductTahapanBCA, "")
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if len(cards) != 3 {
		t.Fatalf("expected 3 kartu, got %d", len(cards))
	}

	// Urutan mengikuti display_order.
	want := []string{"PASPOR_BLUE", "PASPOR_GOLD", "PASPOR_PLATINUM"}
	for i, w := range want {
		if cards[i].CardType != w {
			t.Fatalf("urutan[%d]: got %s, want %s", i, cards[i].CardType, w)
		}
	}

	// Setiap kolom dipindai ke field yang benar. Angka seed sengaja unik,
	// jadi kolom yang tertukar akan terlihat di sini dan bukan di produksi.
	blue := cards[0]
	if blue.Name != "Blue Mastercard" || blue.Network != "MASTERCARD" ||
		blue.TierKey != "DEBIT" || blue.Style != onboarding.CardStyleBlue {
		t.Fatalf("identitas kartu salah pindai: %+v", blue)
	}
	if blue.Fees.MonthlyAdmin != 11 || blue.Fees.CardIssuance != 12 || blue.Fees.CardReplacement != 13 {
		t.Fatalf("fees salah pindai: %+v", blue.Fees)
	}
	if blue.Limits.CashWithdrawal != 21 || blue.Limits.TransferBCA != 22 ||
		blue.Limits.TransferInterbank != 23 || blue.Limits.DebitPurchase != 24 {
		t.Fatalf("limits salah pindai: %+v", blue.Limits)
	}
	if !blue.IsDefault || !blue.IsPopular || blue.DisplayOrder != 1 {
		t.Fatalf("flag opsi salah pindai: %+v", blue)
	}
	if blue.BadgeKey == nil || *blue.BadgeKey != "RECOMMENDED_BEGINNER" {
		t.Fatalf("badge_key salah pindai: %v", blue.BadgeKey)
	}
	if blue.Availability.Status != onboarding.CardAvailable || blue.Availability.ReasonKey != nil {
		t.Fatalf("availability salah pindai: %+v", blue.Availability)
	}
	if blue.Currency != "IDR" {
		t.Fatalf("currency: got %q", blue.Currency)
	}
	if blue.Delivery.EstimatedDaysMin == nil || *blue.Delivery.EstimatedDaysMin != 3 {
		t.Fatalf("delivery min salah pindai: %v", blue.Delivery.EstimatedDaysMin)
	}
	if blue.Eligibility.MinAge != 17 {
		t.Fatalf("min_age: got %d", blue.Eligibility.MinAge)
	}
}

// Kartu non-aktif hilang dari katalog; baris berwilayah menang atas nasional.
func TestCardRepo_InactiveAndRegionOverride(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO product_card_options
			(product_type,card_type,display_order,availability_status,
			 availability_reason_key,region_code)
		VALUES ('TAHAPAN_BCA','PASPOR_PLATINUM',3,'OUT_OF_STOCK',
		        'STOCK_EMPTY_IN_REGION','DKI')`); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	dki, err := repo.ListCards(ctx, onboarding.ProductTahapanBCA, "DKI")
	if err != nil {
		t.Fatalf("ListCards DKI: %v", err)
	}
	if len(dki) != 3 {
		t.Fatalf("override tidak boleh menggandakan baris: got %d kartu", len(dki))
	}
	plat := findByType(dki, "PASPOR_PLATINUM")
	if plat.Availability.Status != onboarding.CardOutOfStock {
		t.Fatalf("DKI harus OUT_OF_STOCK, got %s", plat.Availability.Status)
	}
	if plat.Availability.ReasonKey == nil || *plat.Availability.ReasonKey != "STOCK_EMPTY_IN_REGION" {
		t.Fatalf("reason_key tidak terbawa: %v", plat.Availability.ReasonKey)
	}

	nat, err := repo.ListCards(ctx, onboarding.ProductTahapanBCA, "")
	if err != nil {
		t.Fatalf("ListCards nasional: %v", err)
	}
	if findByType(nat, "PASPOR_PLATINUM").Availability.Status != onboarding.CardAvailable {
		t.Fatal("nasional tidak boleh terpengaruh override wilayah")
	}

	// Kartu dinonaktifkan → hilang dari katalog.
	if _, err := pool.Exec(ctx,
		`UPDATE card_products SET is_active = FALSE WHERE card_type = 'PASPOR_GOLD'`); err != nil {
		t.Fatalf("nonaktifkan kartu: %v", err)
	}
	after, err := repo.ListCards(ctx, onboarding.ProductTahapanBCA, "")
	if err != nil {
		t.Fatalf("ListCards setelah nonaktif: %v", err)
	}
	if len(after) != 2 || findByType(after, "PASPOR_GOLD").CardType != "" {
		t.Fatalf("kartu non-aktif masih tampil: %d kartu", len(after))
	}
}

func TestCardRepo_GetCard(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	got, err := repo.GetCard(ctx, onboarding.ProductTahapanBCA, "PASPOR_GOLD", "")
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if got == nil || got.CardType != "PASPOR_GOLD" || got.Fees.MonthlyAdmin != 31 {
		t.Fatalf("kartu salah: %+v", got)
	}

	// Tidak dikenal → nil, nil (bukan error), supaya service bisa membedakan
	// "tidak ada" dari "database bermasalah".
	missing, err := repo.GetCard(ctx, onboarding.ProductTahapanBCA, "PASPOR_TITANIUM", "")
	if err != nil {
		t.Fatalf("kartu tidak dikenal harus nil,nil — got err: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil, got %+v", missing)
	}

	// Kartu milik produk lain tidak boleh bocor lewat produk ini.
	other, err := repo.GetCard(ctx, onboarding.ProductTabunganku, "PASPOR_BLUE", "")
	if err != nil {
		t.Fatalf("GetCard produk lain: %v", err)
	}
	if other != nil {
		t.Fatal("kartu produk lain tidak boleh ditemukan")
	}
}

func TestCardRepo_CatalogVersion(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	repo := postgres.NewCardRepo(pool)

	first, err := repo.CatalogVersion(ctx)
	if err != nil {
		t.Fatalf("CatalogVersion: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	if first != today+".1" {
		t.Fatalf("versi awal: got %q, want %q", first, today+".1")
	}

	bumped, err := repo.BumpCatalogVersion(ctx)
	if err != nil {
		t.Fatalf("BumpCatalogVersion: %v", err)
	}
	if bumped != today+".2" {
		t.Fatalf("versi setelah bump: got %q, want %q", bumped, today+".2")
	}

	// Bump harus terlihat oleh pembaca berikutnya, bukan hanya dikembalikan.
	readBack, err := repo.CatalogVersion(ctx)
	if err != nil {
		t.Fatalf("CatalogVersion setelah bump: %v", err)
	}
	if readBack != bumped {
		t.Fatalf("versi tidak persisten: %q vs %q", readBack, bumped)
	}
}

func TestCardRepo_LogCardSelection(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	from := "PASPOR_BLUE"
	entries := []onboarding.CardSelectionLogEntry{
		// Pemilihan pertama: belum ada kartu sebelumnya, IP diketahui.
		{SessionID: "onb_test01", ToCardType: "PASPOR_BLUE", CatalogVersion: "2026-09-23.1",
			MonthlyAdminFeeShown: 11, Actor: "CUSTOMER", IPAddress: "203.0.113.7"},
		// Perubahan kartu, tanpa IP — INET harus menerima NULL, bukan "".
		{SessionID: "onb_test01", FromCardType: &from, ToCardType: "PASPOR_GOLD",
			CatalogVersion: "2026-09-23.1", MonthlyAdminFeeShown: 31, Actor: "CUSTOMER"},
	}
	for i, e := range entries {
		if err := repo.LogCardSelection(ctx, e); err != nil {
			t.Fatalf("LogCardSelection[%d]: %v", i, err)
		}
	}

	// Pertanyaan audit §17: kartu apa yang dipilih, dan biaya berapa yang
	// ditampilkan saat itu — harus terjawab satu SELECT.
	var toCard string
	var feeShown int64
	var hasIP bool
	err := pool.QueryRow(ctx, `
		SELECT to_card_type, monthly_admin_fee_shown, ip_address IS NOT NULL
		FROM onboarding_card_selection_log
		WHERE session_id = $1 ORDER BY id DESC LIMIT 1`, "onb_test01").
		Scan(&toCard, &feeShown, &hasIP)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if toCard != "PASPOR_GOLD" || feeShown != 31 {
		t.Fatalf("audit terakhir salah: %s / %d", toCard, feeShown)
	}
	if hasIP {
		t.Fatal("IP kosong harus tersimpan NULL, bukan string kosong")
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM onboarding_card_selection_log WHERE session_id = $1`,
		"onb_test01").Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 baris audit, got %d", count)
	}
}

func findByType(cards []onboarding.CardOption, cardType string) onboarding.CardOption {
	for _, c := range cards {
		if c.CardType == cardType {
			return c
		}
	}
	return onboarding.CardOption{}
}

// --- Regresi: IP cacat tidak boleh membuang jejak audit ---

// Dulu kolom ip_address diisi NULLIF($7,”)::inet, yang hanya menangani string
// kosong: alamat cacat menggagalkan SELURUH INSERT dan — karena pemanggilnya
// best-effort — jejak audit hilang diam-diam.
func TestCardRepo_LogCardSelection_ToleratesBadIP(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	cases := []struct {
		name       string
		ip         string
		wantStored bool
	}{
		{"IPv4 wajar", "203.0.113.7", true},
		{"IPv6 wajar", "2001:db8::1", true},
		{"IPv4 dengan port", "203.0.113.7:54321", true},
		{"kosong", "", false},
		{"cacat", "bukan-ip", false},
		{"header XFF berisi daftar", "203.0.113.7, 10.0.0.1", false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := "onb_ip" + string(rune('a'+i))
			err := repo.LogCardSelection(ctx, onboarding.CardSelectionLogEntry{
				SessionID:            sessionID,
				ToCardType:           "PASPOR_BLUE",
				CatalogVersion:       "2026-09-23.1",
				MonthlyAdminFeeShown: 11,
				Actor:                "CUSTOMER",
				IPAddress:            tc.ip,
			})
			// Yang penting: jejaknya SELALU tertulis, apa pun isi IP-nya.
			if err != nil {
				t.Fatalf("jejak audit hilang karena IP %q: %v", tc.ip, err)
			}

			var hasIP bool
			if err := pool.QueryRow(ctx,
				`SELECT ip_address IS NOT NULL FROM onboarding_card_selection_log
				 WHERE session_id = $1`, sessionID).Scan(&hasIP); err != nil {
				t.Fatalf("jejak tidak ditemukan: %v", err)
			}
			if hasIP != tc.wantStored {
				t.Fatalf("IP %q: tersimpan=%v, harapan=%v", tc.ip, hasIP, tc.wantStored)
			}
		})
	}
}

// --- Admin katalog kartu (§Prompt 7) ---

func adminCardWrite() onboarding.CardProductWrite {
	minDays, maxDays := 4, 9
	return onboarding.CardProductWrite{
		Name:    "Gold Mastercard Baru",
		TierKey: "DEBIT",
		Style:   onboarding.CardStyleGold,
		Fees: onboarding.CardFees{
			MonthlyAdmin: 16000, CardIssuance: 25000, CardReplacement: 15000,
		},
		Limits: onboarding.CardLimits{
			CashWithdrawal: 10_000_000, TransferBCA: 75_000_000,
			TransferInterbank: 25_000_000, DebitPurchase: 75_000_000,
		},
		Delivery: onboarding.CardDelivery{
			PhysicalCardAvailable: true,
			EstimatedDaysMin:      &minDays,
			EstimatedDaysMax:      &maxDays,
			BranchPickupAvailable: true,
		},
		Eligibility: onboarding.CardEligibility{MinAge: 21, MinInitialDeposit: 1_000_000},
		IsActive:    true,
	}
}

// Menulis katalog harus menaikkan versi TEPAT SATU KALI, dan perubahannya harus
// terlihat pada katalog publik — itu seluruh premis invalidasi berbasis versi.
func TestCardRepo_AdminUpdateBumpsVersionOnceAndShowsUp(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	before, err := repo.CatalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}

	w := adminCardWrite()
	result, err := repo.UpdateCardProduct(ctx, "PASPOR_GOLD", w, "admin@bca.test", "203.0.113.7")
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if result.CatalogVersion == before {
		t.Errorf("versi tidak naik: tetap %s", before)
	}
	current, err := repo.CatalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != result.CatalogVersion {
		t.Errorf("versi tersimpan %s, dibalas %s", current, result.CatalogVersion)
	}

	// Satu kali per transaksi, bukan per baris: counter naik tepat 1.
	if before, after := parseCounter(t, before), parseCounter(t, current); after != before+1 {
		t.Errorf("counter versi naik dari %d ke %d, harusnya +1", before, after)
	}

	// Perubahannya benar-benar terbaca di katalog publik.
	card, err := repo.GetCard(ctx, onboarding.ProductTahapanBCA, "PASPOR_GOLD", "")
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != w.Name || card.Fees.MonthlyAdmin != 16000 {
		t.Errorf("katalog belum berubah: name=%q fee=%d", card.Name, card.Fees.MonthlyAdmin)
	}
	if card.Eligibility.MinAge != 21 || *card.Delivery.EstimatedDaysMax != 9 {
		t.Errorf("eligibility/delivery belum berubah: %+v %+v", card.Eligibility, card.Delivery)
	}

	// Dan jejak auditnya tertulis dengan nilai lama DAN baru.
	var actor, action, auditVersion string
	var oldValue, newValue []byte
	err = pool.QueryRow(ctx, `
		SELECT actor, action, catalog_version, old_value, new_value
		FROM card_catalog_audit_log ORDER BY id DESC LIMIT 1`).
		Scan(&actor, &action, &auditVersion, &oldValue, &newValue)
	if err != nil {
		t.Fatalf("baca audit: %v", err)
	}
	if actor != "admin@bca.test" || action != "CARD_UPDATED" {
		t.Errorf("audit: actor=%q action=%q", actor, action)
	}
	if auditVersion != result.CatalogVersion {
		t.Errorf("audit versi %q, harusnya %q", auditVersion, result.CatalogVersion)
	}
	if !strings.Contains(string(oldValue), "Gold Mastercard\"") {
		t.Errorf("nilai lama tidak tersimpan: %s", oldValue)
	}
	if !strings.Contains(string(newValue), "Gold Mastercard Baru") {
		t.Errorf("nilai baru tidak tersimpan: %s", newValue)
	}
}

// Menonaktifkan kartu yang sedang menjadi default ditolak, kecuali penggantinya
// disertakan dalam permintaan yang sama.
func TestCardRepo_AdminCannotDisableDefaultWithoutReplacement(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	versionBefore, _ := repo.CatalogVersion(ctx)

	w := adminCardWrite()
	w.Name = "Blue Mastercard"
	w.Style = onboarding.CardStyleBlue
	w.IsActive = false // PASPOR_BLUE adalah default TAHAPAN_BCA

	_, err := repo.UpdateCardProduct(ctx, "PASPOR_BLUE", w, "admin@bca.test", "")
	if err == nil {
		t.Fatal("penonaktifan default diterima tanpa pengganti")
	}
	appErr := apperr.From(err)
	if appErr.Status != 422 {
		t.Fatalf("status: got %d, want 422 (%v)", appErr.Status, err)
	}

	// Transaksi harus benar-benar dibatalkan: kartu tetap aktif dan versi tidak
	// naik. Penolakan yang menyisakan setengah perubahan lebih buruk daripada
	// tidak menolak sama sekali.
	var active bool
	if err := pool.QueryRow(ctx,
		`SELECT is_active FROM card_products WHERE card_type = 'PASPOR_BLUE'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("kartu ternonaktifkan padahal permintaannya ditolak")
	}
	versionAfter, _ := repo.CatalogVersion(ctx)
	if versionAfter != versionBefore {
		t.Errorf("versi naik meski ditolak: %s → %s", versionBefore, versionAfter)
	}

	// Dengan pengganti yang sah, penulisannya lolos dan default berpindah.
	w.NewDefaultCardType = "PASPOR_GOLD"
	if _, err := repo.UpdateCardProduct(ctx, "PASPOR_BLUE", w, "admin@bca.test", ""); err != nil {
		t.Fatalf("penonaktifan dengan pengganti ditolak: %v", err)
	}

	var defaultCard string
	if err := pool.QueryRow(ctx, `
		SELECT card_type FROM product_card_options
		WHERE product_type = 'TAHAPAN_BCA' AND is_default`).Scan(&defaultCard); err != nil {
		t.Fatalf("baca default: %v", err)
	}
	if defaultCard != "PASPOR_GOLD" {
		t.Errorf("default sekarang %q, harusnya PASPOR_GOLD", defaultCard)
	}
}

// Pengganti yang tidak ditawarkan untuk produk itu ditolak: default yang
// menunjuk kartu yang tidak ada sama buruknya dengan tidak ada default.
func TestCardRepo_AdminRejectsUnknownReplacementDefault(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	w := adminCardWrite()
	w.Name = "Blue Mastercard"
	w.Style = onboarding.CardStyleBlue
	w.IsActive = false
	w.NewDefaultCardType = "PASPOR_TIDAK_ADA"

	_, err := repo.UpdateCardProduct(ctx, "PASPOR_BLUE", w, "admin@bca.test", "")
	if apperr.From(err).Status != 422 {
		t.Fatalf("pengganti tak dikenal diterima: %v", err)
	}
}

// Mencabut is_default lewat endpoint penempatan juga menuntut pengganti.
func TestCardRepo_AdminPlacementDefaultHandover(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	// Mencabut default tanpa pengganti → ditolak.
	w := onboarding.ProductCardWrite{
		DisplayOrder:       1,
		IsDefault:          false,
		AvailabilityStatus: onboarding.CardAvailable,
	}
	_, err := repo.UpdateProductCard(ctx, onboarding.ProductTahapanBCA, "PASPOR_BLUE", w, "admin@bca.test", "")
	if apperr.From(err).Status != 422 {
		t.Fatalf("pencabutan default diterima tanpa pengganti: %v", err)
	}

	// Dengan pengganti → default berpindah, dan hanya ada SATU default.
	w.NewDefaultCardType = "PASPOR_PLATINUM"
	result, err := repo.UpdateProductCard(ctx, onboarding.ProductTahapanBCA, "PASPOR_BLUE", w, "admin@bca.test", "")
	if err != nil {
		t.Fatalf("pencabutan dengan pengganti ditolak: %v", err)
	}
	if result.ProductType == nil || *result.ProductType != onboarding.ProductTahapanBCA {
		t.Errorf("product_type pada hasil: %+v", result.ProductType)
	}

	var defaults []string
	rows, err := pool.Query(ctx, `
		SELECT card_type FROM product_card_options
		WHERE product_type = 'TAHAPAN_BCA' AND is_default`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		defaults = append(defaults, c)
	}
	if len(defaults) != 1 || defaults[0] != "PASPOR_PLATINUM" {
		t.Errorf("default sekarang %v, harusnya [PASPOR_PLATINUM]", defaults)
	}
}

// Menandai kartu habis stok membuatnya tidak bisa dipilih, tapi TETAP tampil di
// katalog beserta alasannya.
func TestCardRepo_AdminOutOfStockStillListed(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	reason := "STOCK_EMPTY_IN_REGION"
	w := onboarding.ProductCardWrite{
		DisplayOrder:          3,
		AvailabilityStatus:    onboarding.CardOutOfStock,
		AvailabilityReasonKey: &reason,
	}
	if _, err := repo.UpdateProductCard(ctx, onboarding.ProductTahapanBCA, "PASPOR_PLATINUM", w, "admin@bca.test", ""); err != nil {
		t.Fatalf("update: %v", err)
	}

	cards, err := repo.ListCards(ctx, onboarding.ProductTahapanBCA, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *onboarding.CardOption
	for i := range cards {
		if cards[i].CardType == "PASPOR_PLATINUM" {
			found = &cards[i]
		}
	}
	if found == nil {
		t.Fatal("kartu habis stok hilang dari katalog; seharusnya tetap tampil")
	}
	if found.Availability.Status != onboarding.CardOutOfStock {
		t.Errorf("status: %s", found.Availability.Status)
	}
	if found.Availability.ReasonKey == nil || *found.Availability.ReasonKey != reason {
		t.Errorf("reason_key: %v", found.Availability.ReasonKey)
	}
}

// Kartu yang dinonaktifkan hilang dari katalog nasabah, tapi tetap terlihat
// admin — kalau tidak, kartu yang ditarik tidak bisa dihidupkan kembali.
func TestCardRepo_AdminListShowsInactiveCards(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	w := adminCardWrite()
	w.Name = "Platinum Mastercard"
	w.Style = onboarding.CardStylePlatinum
	w.TierKey = "PLATINUM_DEBIT"
	w.IsActive = false
	if _, err := repo.UpdateCardProduct(ctx, "PASPOR_PLATINUM", w, "admin@bca.test", ""); err != nil {
		t.Fatalf("nonaktifkan: %v", err)
	}

	// Katalog nasabah: hilang.
	cards, err := repo.ListCards(ctx, onboarding.ProductTahapanBCA, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if c.CardType == "PASPOR_PLATINUM" {
			t.Error("kartu nonaktif masih tampil di katalog nasabah")
		}
	}

	// Katalog admin: ada, dengan is_active false dan penempatannya utuh.
	rows, err := repo.ListAllCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *onboarding.AdminCardRow
	for i := range rows {
		if rows[i].CardType == "PASPOR_PLATINUM" {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("kartu nonaktif hilang dari katalog admin")
	}
	if found.IsActive {
		t.Error("is_active masih true")
	}
	if len(found.Placements) != 1 || found.Placements[0].ProductType != onboarding.ProductTahapanBCA {
		t.Errorf("penempatan: %+v", found.Placements)
	}
}

func TestCardRepo_AdminUpdateUnknownCardRejected(t *testing.T) {
	ctx := context.Background()
	pool := setupCardDB(t)
	seedCards(ctx, t, pool)
	repo := postgres.NewCardRepo(pool)

	_, err := repo.UpdateCardProduct(ctx, "PASPOR_TIDAK_ADA", adminCardWrite(), "admin@bca.test", "")
	if apperr.From(err).Code != apperr.CardTypeInvalid.Code {
		t.Fatalf("got %v, want CARD_TYPE_INVALID", err)
	}
}

// parseCounter mengambil angka setelah titik pada versi "YYYY-MM-DD.n".
func parseCounter(t *testing.T, version string) int {
	t.Helper()
	_, counter, found := strings.Cut(version, ".")
	if !found {
		t.Fatalf("versi %q tidak berformat YYYY-MM-DD.n", version)
	}
	n, err := strconv.Atoi(counter)
	if err != nil {
		t.Fatalf("counter versi %q: %v", version, err)
	}
	return n
}
