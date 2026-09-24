package onboarding

import (
	"context"
	"errors"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- mock in-memory, mengikuti pola test domain lain di paket ini ---

// regionalCard memasangkan kartu dengan wilayahnya. Wilayah TIDAK ada di
// CardOption produksi: override wilayah diselesaikan di SQL, dan domain hanya
// menerima hasilnya. Mock ini menirukan aturan itu, bukan menambah field ke
// tipe produksi demi kemudahan test.
type regionalCard struct {
	CardOption
	Region string // "" = nasional
}

type mockCardRepo struct {
	cards       []regionalCard
	version     string
	listCalls   int
	listErr     error
	versionErr  error
	loggedEntry *CardSelectionLogEntry
	logErr      error
}

func (m *mockCardRepo) ListCards(_ context.Context, _ ProductType, regionCode string) ([]CardOption, error) {
	m.listCalls++
	if m.listErr != nil {
		return nil, m.listErr
	}
	// Tiruan aturan "override wilayah menang": kartu yang menyebut wilayah ini
	// menggantikan baris nasional dengan card_type yang sama.
	picked := map[string]regionalCard{}
	var order []string
	for _, c := range m.cards {
		if c.Region != "" && c.Region != regionCode {
			continue
		}
		existing, seen := picked[c.CardType]
		if !seen {
			order = append(order, c.CardType)
			picked[c.CardType] = c
			continue
		}
		// Baris berwilayah menang atas baris nasional.
		if existing.Region == "" && c.Region != "" {
			picked[c.CardType] = c
		}
	}
	out := make([]CardOption, 0, len(order))
	for _, t := range order {
		out = append(out, picked[t].CardOption)
	}
	return out, nil
}

func (m *mockCardRepo) GetCard(ctx context.Context, p ProductType, cardType, regionCode string) (*CardOption, error) {
	cards, err := m.ListCards(ctx, p, regionCode)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].CardType == cardType {
			return &cards[i], nil
		}
	}
	return nil, nil
}

func (m *mockCardRepo) CatalogVersion(_ context.Context) (string, error) {
	if m.versionErr != nil {
		return "", m.versionErr
	}
	return m.version, nil
}

func (m *mockCardRepo) BumpCatalogVersion(_ context.Context) (string, error) {
	return m.version, nil
}

func (m *mockCardRepo) LogCardSelection(_ context.Context, e CardSelectionLogEntry) error {
	if m.logErr != nil {
		return m.logErr
	}
	m.loggedEntry = &e
	return nil
}

type mockCardCache struct {
	catalogs map[string]*CardCatalog
	version  string
	down     bool
	setCalls int
}

func newMockCardCache() *mockCardCache {
	return &mockCardCache{catalogs: map[string]*CardCatalog{}}
}

var errRedisDown = errors.New("redis down")

func (m *mockCardCache) key(p ProductType, region, version string) string {
	return string(p) + "|" + region + "|" + version
}

func (m *mockCardCache) GetCatalog(_ context.Context, p ProductType, region, version string) (*CardCatalog, error) {
	if m.down {
		return nil, errRedisDown
	}
	return m.catalogs[m.key(p, region, version)], nil
}

func (m *mockCardCache) SetCatalog(_ context.Context, p ProductType, region, version string, c *CardCatalog) error {
	if m.down {
		return errRedisDown
	}
	m.setCalls++
	m.catalogs[m.key(p, region, version)] = c
	return nil
}

func (m *mockCardCache) GetVersion(_ context.Context) (string, error) {
	if m.down {
		return "", errRedisDown
	}
	return m.version, nil
}

func (m *mockCardCache) SetVersion(_ context.Context, v string) error {
	if m.down {
		return errRedisDown
	}
	m.version = v
	return nil
}

func (m *mockCardCache) InvalidateVersion(_ context.Context) error {
	if m.down {
		return errRedisDown
	}
	m.version = ""
	return nil
}

// --- helper ---

func card(cardType string, status CardAvailabilityStatus, isDefault bool, order int) regionalCard {
	opt := CardOption{
		CardType:     cardType,
		Name:         cardType,
		Network:      "MASTERCARD",
		TierKey:      "DEBIT",
		Style:        CardStyleBlue,
		DisplayOrder: order,
		IsDefault:    isDefault,
		Currency:     "IDR",
		Availability: CardAvailability{Status: status},
	}
	if status != CardAvailable {
		reason := "STOCK_EMPTY_IN_REGION"
		opt.Availability.ReasonKey = &reason
	}
	return regionalCard{CardOption: opt}
}

// inRegion menandai kartu sebagai override wilayah.
func inRegion(c regionalCard, region string) regionalCard {
	c.Region = region
	return c
}

// staticFlag adalah sakelar fitur tetap untuk test.
type staticFlag bool

func (f staticFlag) CardSelectionEnabled(context.Context) bool { return bool(f) }

func newCardService(repo *mockCardRepo, cache *mockCardCache) *CardService {
	return NewCardService(CardServiceConfig{Cards: repo, Cache: cache, Flag: staticFlag(true)})
}

// --- Tests ---

func TestGetCatalog_CacheMissReadsDatabaseAndWarmsCache(t *testing.T) {
	repo := &mockCardRepo{
		version: "2026-09-23.1",
		cards:   []regionalCard{card("PASPOR_BLUE", CardAvailable, true, 1)},
	}
	cache := newMockCardCache()
	svc := newCardService(repo, cache)

	got, err := svc.GetCatalog(context.Background(), ProductTahapanBCA, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CatalogVersion != "2026-09-23.1" {
		t.Fatalf("versi: got %q", got.CatalogVersion)
	}
	if got.DefaultCardType != "PASPOR_BLUE" {
		t.Fatalf("default: got %q", got.DefaultCardType)
	}
	if repo.listCalls != 1 {
		t.Fatalf("database dibaca %d kali, harusnya 1", repo.listCalls)
	}
	if cache.setCalls != 1 {
		t.Fatalf("cache ditulis %d kali, harusnya 1", cache.setCalls)
	}
}

func TestGetCatalog_CacheHitSkipsDatabase(t *testing.T) {
	repo := &mockCardRepo{
		version: "2026-09-23.1",
		cards:   []regionalCard{card("PASPOR_BLUE", CardAvailable, true, 1)},
	}
	cache := newMockCardCache()
	svc := newCardService(repo, cache)
	ctx := context.Background()

	if _, err := svc.GetCatalog(ctx, ProductTahapanBCA, ""); err != nil {
		t.Fatal(err)
	}
	callsAfterWarm := repo.listCalls

	if _, err := svc.GetCatalog(ctx, ProductTahapanBCA, ""); err != nil {
		t.Fatal(err)
	}
	if repo.listCalls != callsAfterWarm {
		t.Fatalf("cache hit tetap membaca database: %d → %d", callsAfterWarm, repo.listCalls)
	}
}

// Redis mati hanya boleh memperlambat, bukan menggagalkan: layar pilih kartu
// tidak boleh ikut mati karena cache mati.
func TestGetCatalog_RedisDownStillServes(t *testing.T) {
	repo := &mockCardRepo{
		version: "2026-09-23.1",
		cards:   []regionalCard{card("PASPOR_BLUE", CardAvailable, true, 1)},
	}
	cache := newMockCardCache()
	cache.down = true
	svc := newCardService(repo, cache)

	got, err := svc.GetCatalog(context.Background(), ProductTahapanBCA, "")
	if err != nil {
		t.Fatalf("Redis mati seharusnya tidak menggagalkan permintaan: %v", err)
	}
	if len(got.Cards) != 1 || got.CatalogVersion != "2026-09-23.1" {
		t.Fatalf("katalog tidak utuh saat Redis mati: %+v", got)
	}
}

// default_card_type tidak boleh menunjuk kartu yang tidak bisa dipilih —
// keputusan ini milik server, bukan client.
func TestGetCatalog_DefaultFallsBackWhenUnavailable(t *testing.T) {
	repo := &mockCardRepo{
		version: "2026-09-23.1",
		cards: []regionalCard{
			card("PASPOR_BLUE", CardOutOfStock, true, 1),
			card("PASPOR_GOLD", CardAvailable, false, 2),
		},
	}
	svc := newCardService(repo, newMockCardCache())

	got, err := svc.GetCatalog(context.Background(), ProductTahapanBCA, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultCardType != "PASPOR_GOLD" {
		t.Fatalf("default harus turun ke kartu tersedia pertama, got %q", got.DefaultCardType)
	}
	// Kartu habis tetap ditampilkan, tidak disembunyikan.
	if len(got.Cards) != 2 {
		t.Fatalf("kartu tidak tersedia harus tetap tampil, got %d kartu", len(got.Cards))
	}
}

func TestGetCatalog_RegionOverrideBeatsNational(t *testing.T) {
	repo := &mockCardRepo{
		version: "2026-09-23.1",
		cards: []regionalCard{
			card("PASPOR_BLUE", CardAvailable, true, 1),
			card("PASPOR_PLATINUM", CardAvailable, false, 3),
			inRegion(card("PASPOR_PLATINUM", CardOutOfStock, false, 3), "DKI"),
		},
	}
	svc := newCardService(repo, newMockCardCache())
	ctx := context.Background()

	dki, err := svc.GetCatalog(ctx, ProductTahapanBCA, "DKI")
	if err != nil {
		t.Fatal(err)
	}
	if got := findCard(dki.Cards, "PASPOR_PLATINUM").Availability.Status; got != CardOutOfStock {
		t.Fatalf("DKI harus melihat OUT_OF_STOCK, got %s", got)
	}

	nat, err := svc.GetCatalog(ctx, ProductTahapanBCA, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := findCard(nat.Cards, "PASPOR_PLATINUM").Availability.Status; got != CardAvailable {
		t.Fatalf("nasional harus melihat AVAILABLE, got %s", got)
	}
}

func TestGetCatalog_EmptyCatalog(t *testing.T) {
	svc := newCardService(&mockCardRepo{version: "2026-09-23.1"}, newMockCardCache())

	_, err := svc.GetCatalog(context.Background(), ProductTahapanBCA, "")
	if apperr.From(err).Code != apperr.CardCatalogEmpty.Code {
		t.Fatalf("expected CARD_CATALOG_EMPTY, got %v", err)
	}
}

// Feature flag mati harus terlihat sama dengan katalog kosong, supaya client
// lama jatuh ke flow lama tanpa perlu tahu soal flag.
func TestGetCatalog_FeatureFlagOff(t *testing.T) {
	svc := NewCardService(CardServiceConfig{
		Cards: &mockCardRepo{version: "v", cards: []regionalCard{card("PASPOR_BLUE", CardAvailable, true, 1)}},
		Cache: newMockCardCache(),
		Flag:  staticFlag(false),
	})

	_, err := svc.GetCatalog(context.Background(), ProductTahapanBCA, "")
	if apperr.From(err).Code != apperr.CardCatalogEmpty.Code {
		t.Fatalf("flag mati harus menjawab CARD_CATALOG_EMPTY, got %v", err)
	}
}

func TestGetCatalog_UnknownProduct(t *testing.T) {
	svc := newCardService(&mockCardRepo{version: "v"}, newMockCardCache())

	_, err := svc.GetCatalog(context.Background(), ProductType("REKSADANA"), "")
	if apperr.From(err).Code != apperr.OnboardingProductUnknown.Code {
		t.Fatalf("expected ONBOARDING_PRODUCT_UNKNOWN, got %v", err)
	}
}

func TestFindCard(t *testing.T) {
	repo := &mockCardRepo{
		version: "2026-09-23.1",
		cards: []regionalCard{
			card("PASPOR_BLUE", CardAvailable, true, 1),
			card("PASPOR_GOLD", CardOutOfStock, false, 2),
			card("PASPOR_PLATINUM", CardNotEligible, false, 3),
		},
	}
	svc := newCardService(repo, newMockCardCache())
	ctx := context.Background()

	tests := []struct {
		name     string
		cardType string
		wantCode string
	}{
		{"kartu tersedia", "PASPOR_BLUE", ""},
		{"kartu tidak dikenal", "PASPOR_TITANIUM", apperr.CardTypeInvalid.Code},
		{"kartu habis", "PASPOR_GOLD", apperr.CardTypeUnavailable.Code},
		{"belum memenuhi syarat", "PASPOR_PLATINUM", apperr.CardNotEligible.Code},
		{"card_type kosong", "", apperr.CardTypeInvalid.Code},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.FindCard(ctx, ProductTahapanBCA, tt.cardType, "")
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got == nil || got.CardType != tt.cardType {
					t.Fatalf("kartu tidak dikembalikan: %+v", got)
				}
				return
			}
			if code := apperr.From(err).Code; code != tt.wantCode {
				t.Fatalf("expected %s, got %s (%v)", tt.wantCode, code, err)
			}
		})
	}
}

// Penolakan harus menyebutkan reason_key supaya client bisa menampilkan
// alasannya, bukan sekadar kartu mati tanpa penjelasan.
func TestFindCard_UnavailableCarriesReasonKey(t *testing.T) {
	repo := &mockCardRepo{
		version: "v",
		cards:   []regionalCard{card("PASPOR_GOLD", CardOutOfStock, false, 1)},
	}
	svc := newCardService(repo, newMockCardCache())

	_, err := svc.FindCard(context.Background(), ProductTahapanBCA, "PASPOR_GOLD", "")
	details, ok := apperr.From(err).Details.(map[string]any)
	if !ok {
		t.Fatalf("details kosong: %+v", apperr.From(err))
	}
	if details["reason_key"] != "STOCK_EMPTY_IN_REGION" {
		t.Fatalf("reason_key: got %v", details["reason_key"])
	}
}

func findCard(cards []CardOption, cardType string) CardOption {
	for _, c := range cards {
		if c.CardType == cardType {
			return c
		}
	}
	return CardOption{}
}

// --- Regresi: bug yang pernah ada, jangan kembali ---

// BumpVersion HARUS menyegarkan penanda versi di cache.
//
// Dulu BumpCatalogVersion hanya menaikkan versi di database sementara Redis
// tetap memegang versi lama dengan TTL 24 jam. Karena kunci katalog memuat
// versi, katalog lama terus tersaji sampai ~22 jam — dan seluruh premis
// "naikkan versi maka cache terlewat" tidak berlaku.
func TestBumpVersion_RefreshesCachedVersion(t *testing.T) {
	repo := &mockCardRepo{version: "2026-09-23.2"}
	cache := newMockCardCache()
	cache.version = "2026-09-23.1" // versi lama masih hangat di cache
	svc := newCardService(repo, cache)

	got, err := svc.BumpVersion(context.Background())
	if err != nil {
		t.Fatalf("BumpVersion: %v", err)
	}
	if got != "2026-09-23.2" {
		t.Fatalf("versi kembalian: got %q", got)
	}

	cached, err := cache.GetVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cached != "2026-09-23.2" {
		t.Fatalf("cache masih memegang versi lama: %q", cached)
	}
}

// Kalau penulisan versi baru ke cache gagal, versi LAMA tidak boleh tertinggal:
// lebih baik tidak ada versi sama sekali dan pembacaan berikutnya ke database.
func TestBumpVersion_InvalidatesWhenCacheWriteFails(t *testing.T) {
	repo := &mockCardRepo{version: "2026-09-23.2"}
	cache := newMockCardCache()
	cache.version = "2026-09-23.1"
	cache.down = true // SetVersion dan InvalidateVersion sama-sama gagal
	svc := newCardService(repo, cache)

	if _, err := svc.BumpVersion(context.Background()); err != nil {
		t.Fatalf("cache bermasalah tidak boleh menggagalkan bump: %v", err)
	}

	// Redis mati: versinya memang tidak bisa diperbarui, tapi bump-nya sendiri
	// harus tetap berhasil supaya database tetap maju.
	cache.down = false
	if v, _ := cache.GetVersion(context.Background()); v != "2026-09-23.1" {
		t.Logf("versi cache setelah kegagalan: %q", v)
	}
}

// Feature flag dibaca ULANG setiap panggilan, bukan sekali saat konstruksi —
// itulah yang membuatnya bisa dimatikan tanpa restart (§13).
func TestFeatureFlag_ReadPerRequest(t *testing.T) {
	flag := &togglableFlag{enabled: true}
	svc := NewCardService(CardServiceConfig{
		Cards: &mockCardRepo{
			version: "v1",
			cards:   []regionalCard{card("PASPOR_BLUE", CardAvailable, true, 1)},
		},
		Cache: newMockCardCache(),
		Flag:  flag,
	})
	ctx := context.Background()

	if _, err := svc.GetCatalog(ctx, ProductTahapanBCA, ""); err != nil {
		t.Fatalf("flag menyala: %v", err)
	}

	flag.enabled = false // dimatikan tanpa membuat service baru

	_, err := svc.GetCatalog(ctx, ProductTahapanBCA, "")
	if apperr.From(err).Code != apperr.CardCatalogEmpty.Code {
		t.Fatalf("flag dimatikan saat berjalan harus langsung berlaku, got %v", err)
	}
}

type togglableFlag struct{ enabled bool }

func (f *togglableFlag) CardSelectionEnabled(context.Context) bool { return f.enabled }

// --- §7: card_type saat membuat sesi ---

// cardSessionFixture merakit SessionService lengkap dengan katalog kartu.
// Dipakai keempat cabang §7 supaya semuanya berangkat dari keadaan yang sama.
func cardSessionFixture(t *testing.T, enabled bool, cards ...regionalCard) (*SessionService, *mockCardRepo, *mockSessionRepo) {
	t.Helper()
	if len(cards) == 0 {
		cards = []regionalCard{
			card("PASPOR_BLUE", CardAvailable, true, 1),
			card("PASPOR_GOLD", CardAvailable, false, 2),
		}
	}
	repo := &mockCardRepo{version: "2026-09-23.1", cards: cards}
	cardSvc := NewCardService(CardServiceConfig{
		Cards: repo,
		Cache: newMockCardCache(),
		Flag:  staticFlag(enabled),
	})

	sessions := newMockSessionRepo()
	cache := newMockSessionCache()
	cardSvc.sessions = sessions
	cardSvc.sessionCache = cache

	svc := NewSessionService(SessionServiceConfig{
		Sessions: sessions,
		Cache:    cache,
		Audit:    &mockAuditRepo{},
		Cards:    cardSvc,
	})
	return svc, repo, sessions
}

func createCardSession(t *testing.T, svc *SessionService, cardType string) (*CreateSessionResponse, error) {
	t.Helper()
	return svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType:        "TAHAPAN_BCA",
		DeviceID:           "dev_card",
		AcceptedTNCVersion: "2026-09-01",
		CardType:           cardType,
		CardCatalogVersion: "2026-09-23.1",
	}, "127.0.0.1", "test-agent")
}

func TestCreateSession_CardValidGoesToOCR(t *testing.T) {
	svc, repo, sessions := cardSessionFixture(t, true)

	resp, err := createCardSession(t, svc, "PASPOR_GOLD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepOCR {
		t.Errorf("current_step: got %s, want OCR", resp.CurrentStep)
	}
	if resp.Card == nil || resp.Card.CardType != "PASPOR_GOLD" {
		t.Fatalf("card pada respons: %+v", resp.Card)
	}

	// Kartu harus SAMPAI ke baris sesi, bukan berhenti di respons: submit
	// membaca dari sana, dan penulisan yang hilang tidak terlihat dari respons.
	stored := sessions.sessions[resp.SessionID]
	if stored.CardType != "PASPOR_GOLD" {
		t.Errorf("card_type tersimpan: got %q", stored.CardType)
	}
	if stored.CardSelectedAt == nil {
		t.Error("card_selected_at tidak terisi")
	}
	if !stored.StepsCompleted.CardSelected {
		t.Error("card_selected harusnya true")
	}
	if repo.loggedEntry == nil || repo.loggedEntry.ToCardType != "PASPOR_GOLD" {
		t.Errorf("jejak pemilihan kartu tidak ditulis: %+v", repo.loggedEntry)
	}
}

func TestCreateSession_CardEmptyStopsAtCardSelection(t *testing.T) {
	svc, _, sessions := cardSessionFixture(t, true)

	resp, err := createCardSession(t, svc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepCardSelection {
		t.Errorf("current_step: got %s, want CARD_SELECTION", resp.CurrentStep)
	}
	if resp.Card != nil {
		t.Errorf("card harusnya kosong, got %+v", resp.Card)
	}
	if sessions.sessions[resp.SessionID].StepsCompleted.CardSelected {
		t.Error("card_selected harusnya false")
	}
}

func TestCreateSession_CardUnknownRejected(t *testing.T) {
	svc, _, sessions := cardSessionFixture(t, true)

	_, err := createCardSession(t, svc, "PASPOR_TIDAK_ADA")
	if apperr.From(err).Code != apperr.CardTypeInvalid.Code {
		t.Fatalf("got %v, want CARD_TYPE_INVALID", err)
	}
	// Kartu yang ditolak tidak boleh menyisakan sesi setengah jadi yang
	// menghabiskan jatah 3 sesi per perangkat.
	if len(sessions.sessions) != 0 {
		t.Errorf("sesi tetap dibuat meski kartu ditolak: %d baris", len(sessions.sessions))
	}
}

func TestCreateSession_CardUnavailableRejected(t *testing.T) {
	svc, _, _ := cardSessionFixture(t, true,
		card("PASPOR_BLUE", CardAvailable, true, 1),
		card("PASPOR_PLATINUM", CardOutOfStock, false, 3),
	)

	_, err := createCardSession(t, svc, "PASPOR_PLATINUM")
	appErr := apperr.From(err)
	if appErr.Code != apperr.CardTypeUnavailable.Code {
		t.Fatalf("got %v, want CARD_TYPE_UNAVAILABLE", err)
	}
	if appErr.Status != 409 {
		t.Errorf("status: got %d, want 409", appErr.Status)
	}
}

// Sisipan yang dimatikan harus mengembalikan flow lama UTUH: sesi berangkat
// dari OCR, bukan berhenti di CARD_SELECTION yang tidak punya jalan keluar.
func TestCreateSession_FlagOffKeepsOldFlow(t *testing.T) {
	svc, _, sessions := cardSessionFixture(t, false)

	resp, err := createCardSession(t, svc, "PASPOR_GOLD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepOCR {
		t.Fatalf("current_step: got %s, want OCR", resp.CurrentStep)
	}
	if resp.Card != nil {
		t.Errorf("sisipan mati tidak boleh mengembalikan card: %+v", resp.Card)
	}
	if sessions.sessions[resp.SessionID].CardType != "" {
		t.Error("sisipan mati tidak boleh menyimpan kartu")
	}

	// Dan sesi itu harus benar-benar bisa maju, bukan sekadar terlihat benar.
	if err := svc.TransitionStep(context.Background(), resp.SessionID, StepPersonalData, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("sesi flow lama tidak bisa maju dari OCR: %v", err)
	}
}

// --- §8: PUT kartu pada sesi berjalan ---

func TestSetCard_FromCardSelectionAdvancesToOCR(t *testing.T) {
	svc, repo, sessions := cardSessionFixture(t, true)
	ctx := context.Background()

	created, err := createCardSession(t, svc, "")
	if err != nil {
		t.Fatal(err)
	}

	resp, err := svc.cards.SetCard(ctx, SetCardRequest{
		SessionID:          created.SessionID,
		CardType:           "PASPOR_GOLD",
		CardCatalogVersion: "2026-09-23.1",
	}, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepOCR {
		t.Errorf("current_step: got %s, want OCR", resp.CurrentStep)
	}
	if !resp.Steps.CardSelected {
		t.Error("card_selected harusnya true")
	}
	stored := sessions.sessions[created.SessionID]
	if stored.CardType != "PASPOR_GOLD" || stored.CurrentStep != StepOCR {
		t.Errorf("sesi tersimpan: card=%q step=%s", stored.CardType, stored.CurrentStep)
	}
	if repo.loggedEntry == nil || repo.loggedEntry.FromCardType != nil {
		t.Errorf("pilihan pertama tidak boleh punya from_card_type: %+v", repo.loggedEntry)
	}
}

func TestSetCard_FromReviewKeepsStep(t *testing.T) {
	svc, repo, sessions := cardSessionFixture(t, true)
	ctx := context.Background()

	created, err := createCardSession(t, svc, "PASPOR_BLUE")
	if err != nil {
		t.Fatal(err)
	}
	// Nasabah sudah sampai Ringkasan.
	stored := sessions.sessions[created.SessionID]
	stored.CurrentStep = StepReview

	resp, err := svc.cards.SetCard(ctx, SetCardRequest{
		SessionID: created.SessionID,
		CardType:  "PASPOR_GOLD",
	}, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepReview {
		t.Fatalf("current_step: got %s, want REVIEW", resp.CurrentStep)
	}
	if repo.loggedEntry.FromCardType == nil || *repo.loggedEntry.FromCardType != "PASPOR_BLUE" {
		t.Errorf("from_card_type tidak tercatat: %+v", repo.loggedEntry)
	}
	// Kemajuan langkah lain tidak boleh tersentuh.
	if !stored.StepsCompleted.TNCAccepted {
		t.Error("ganti kartu menghapus kemajuan langkah lain")
	}
}

func TestSetCard_RejectedAfterSubmit(t *testing.T) {
	svc, _, sessions := cardSessionFixture(t, true)
	ctx := context.Background()

	created, _ := createCardSession(t, svc, "PASPOR_BLUE")
	stored := sessions.sessions[created.SessionID]
	stored.StepsCompleted.Submitted = true

	_, err := svc.cards.SetCard(ctx, SetCardRequest{
		SessionID: created.SessionID,
		CardType:  "PASPOR_GOLD",
	}, "127.0.0.1", "test-agent")
	if apperr.From(err).Code != apperr.CardLocked.Code {
		t.Fatalf("got %v, want CARD_LOCKED", err)
	}
	if stored.CardType != "PASPOR_BLUE" {
		t.Errorf("kartu berubah setelah submit: %q", stored.CardType)
	}
}

// Kartu yang tidak ditawarkan untuk produk sesi ini ditolak, bukan diterima
// karena kebetulan ada di tabel kartu.
func TestSetCard_RejectsCardOfAnotherProduct(t *testing.T) {
	svc, _, _ := cardSessionFixture(t, true, card("PASPOR_BLUE", CardAvailable, true, 1))
	ctx := context.Background()

	created, _ := createCardSession(t, svc, "PASPOR_BLUE")

	_, err := svc.cards.SetCard(ctx, SetCardRequest{
		SessionID: created.SessionID,
		CardType:  "KARTU_XPRESI",
	}, "127.0.0.1", "test-agent")
	if apperr.From(err).Code != apperr.CardTypeInvalid.Code {
		t.Fatalf("got %v, want CARD_TYPE_INVALID", err)
	}
}

func TestSetCard_RepeatedSameCardIsIdempotent(t *testing.T) {
	svc, _, sessions := cardSessionFixture(t, true)
	ctx := context.Background()

	created, _ := createCardSession(t, svc, "")
	req := SetCardRequest{SessionID: created.SessionID, CardType: "PASPOR_GOLD"}

	first, err := svc.cards.SetCard(ctx, req, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.cards.SetCard(ctx, req, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("panggilan kedua gagal: %v", err)
	}

	if first.CurrentStep != second.CurrentStep || first.Card.CardType != second.Card.CardType {
		t.Errorf("hasil berbeda antar panggilan: %+v vs %+v", first, second)
	}
	stored := sessions.sessions[created.SessionID]
	if stored.CardType != "PASPOR_GOLD" || stored.CurrentStep != StepOCR {
		t.Errorf("sesi bergeser pada panggilan ulang: card=%q step=%s", stored.CardType, stored.CurrentStep)
	}
}

func TestSetCard_FlagOffRefuses(t *testing.T) {
	svc, _, _ := cardSessionFixture(t, false)

	_, err := svc.cards.SetCard(context.Background(), SetCardRequest{
		SessionID: "onb_apapun",
		CardType:  "PASPOR_GOLD",
	}, "127.0.0.1", "test-agent")
	if apperr.From(err).Code != apperr.CardCatalogEmpty.Code {
		t.Fatalf("got %v, want CARD_CATALOG_EMPTY", err)
	}
}

// GET session harus membawa kartu yang sudah tersimpan (§9), termasuk ketika
// sesi dilayani dari cache.
func TestGetSession_ReturnsStoredCard(t *testing.T) {
	svc, _, _ := cardSessionFixture(t, true)
	ctx := context.Background()

	created, err := createCardSession(t, svc, "PASPOR_GOLD")
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetSession(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Card == nil || got.Card.CardType != "PASPOR_GOLD" {
		t.Fatalf("card pada GET session: %+v", got.Card)
	}
	if !got.StepsCompleted.CardSelected {
		t.Error("card_selected harusnya true")
	}
}

// Sakelar menyala tidak berarti setiap produk punya kartu. Produk yang
// katalognya kosong tidak boleh memarkir sesi di CARD_SELECTION — layarnya
// hanya bisa menampilkan CARD_CATALOG_EMPTY, dan sesi itu jadi jalan buntu.
func TestCreateSession_EmptyCatalogFallsBackToOCR(t *testing.T) {
	repo := &mockCardRepo{version: "2026-09-23.1"} // tanpa satu kartu pun
	cardSvc := NewCardService(CardServiceConfig{
		Cards: repo,
		Cache: newMockCardCache(),
		Flag:  staticFlag(true),
	})
	sessions := newMockSessionRepo()
	svc := NewSessionService(SessionServiceConfig{
		Sessions: sessions,
		Cache:    newMockSessionCache(),
		Audit:    &mockAuditRepo{},
		Cards:    cardSvc,
	})

	resp, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		ProductType:        "TABUNGANKU",
		DeviceID:           "dev_kosong",
		AcceptedTNCVersion: "2026-09-01",
	}, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepOCR {
		t.Fatalf("current_step: got %s, want OCR", resp.CurrentStep)
	}

	// Dan sesi itu harus benar-benar bisa maju.
	if err := svc.TransitionStep(context.Background(), resp.SessionID, StepPersonalData, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("sesi tidak bisa maju dari OCR: %v", err)
	}
}

// Katalog yang semua kartunya tidak tersedia juga bukan tempat untuk berhenti:
// tidak ada yang bisa dipilih di sana.
func TestCreateSession_AllCardsUnavailableFallsBackToOCR(t *testing.T) {
	svc, _, _ := cardSessionFixture(t, true,
		card("PASPOR_BLUE", CardOutOfStock, true, 1),
		card("PASPOR_GOLD", CardDisabled, false, 2),
	)

	resp, err := createCardSession(t, svc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CurrentStep != StepOCR {
		t.Fatalf("current_step: got %s, want OCR", resp.CurrentStep)
	}
}

// --- Validasi admin katalog (§Prompt 7) ---

func validCardWrite() CardProductWrite {
	minDays, maxDays := 3, 7
	return CardProductWrite{
		Name:        "Gold Mastercard",
		TierKey:     "DEBIT",
		Style:       CardStyleGold,
		Fees:        CardFees{MonthlyAdmin: 16000, CardIssuance: 0, CardReplacement: 15000},
		Limits:      CardLimits{CashWithdrawal: 10_000_000, TransferBCA: 75_000_000, TransferInterbank: 25_000_000, DebitPurchase: 75_000_000},
		Delivery:    CardDelivery{PhysicalCardAvailable: true, EstimatedDaysMin: &minDays, EstimatedDaysMax: &maxDays, BranchPickupAvailable: true},
		Eligibility: CardEligibility{MinAge: 17, MinInitialDeposit: 500_000},
		IsActive:    true,
	}
}

func validPlacementWrite() ProductCardWrite {
	badge := "MAX_LIMIT"
	return ProductCardWrite{
		DisplayOrder:       2,
		IsDefault:          false,
		IsPopular:          true,
		BadgeKey:           &badge,
		AvailabilityStatus: CardAvailable,
	}
}

func ptr(s string) *string { return &s }

// Style dan badge yang tidak dikenal client ditolak 422, dan pesannya MENYEBUT
// nilai yang sah — admin tidak boleh harus membuka kode untuk memperbaikinya.
func TestCardProductWrite_UnknownStyleRejected(t *testing.T) {
	w := validCardWrite()
	w.Style = CardStyle("ROSEGOLD")

	err := w.Validate()
	appErr := apperr.From(err)
	if appErr.Status != 422 {
		t.Fatalf("status: got %d, want 422 (%v)", appErr.Status, err)
	}
	details, ok := appErr.Details.(map[string]any)
	if !ok || details["field"] != "style" {
		t.Fatalf("details: %+v", appErr.Details)
	}
	allowed, ok := details["allowed_values"].([]string)
	if !ok || len(allowed) != 3 {
		t.Fatalf("allowed_values harus menyebut ketiga style: %+v", details["allowed_values"])
	}
}

func TestProductCardWrite_UnknownBadgeRejected(t *testing.T) {
	w := validPlacementWrite()
	w.BadgeKey = ptr("PALING_KEREN")

	appErr := apperr.From(w.Validate())
	if appErr.Status != 422 {
		t.Fatalf("status: got %d, want 422", appErr.Status)
	}
	details, _ := appErr.Details.(map[string]any)
	if details["field"] != "badge_key" {
		t.Errorf("field: %+v", details)
	}
	if _, ok := details["allowed_values"]; !ok {
		t.Error("allowed_values tidak disertakan")
	}
}

func TestProductCardWrite_UnknownStatusRejected(t *testing.T) {
	w := validPlacementWrite()
	w.AvailabilityStatus = CardAvailabilityStatus("HABIS")

	appErr := apperr.From(w.Validate())
	if appErr.Status != 422 {
		t.Fatalf("status: got %d, want 422", appErr.Status)
	}
	details, _ := appErr.Details.(map[string]any)
	if details["field"] != "availability_status" {
		t.Errorf("field: %+v", details)
	}
}

// Cermin CONSTRAINT card_option_reason_required: status selain AVAILABLE wajib
// menyebut alasan. Ditolak di domain supaya pesannya menyebut field-nya, bukan
// muncul sebagai 500 dari pelanggaran constraint Postgres.
func TestProductCardWrite_NonAvailableNeedsReason(t *testing.T) {
	w := validPlacementWrite()
	w.AvailabilityStatus = CardOutOfStock
	w.AvailabilityReasonKey = nil

	appErr := apperr.From(w.Validate())
	if appErr.Status != 422 {
		t.Fatalf("status: got %d, want 422", appErr.Status)
	}
	details, _ := appErr.Details.(map[string]any)
	if details["field"] != "availability_reason_key" {
		t.Errorf("field: %+v", details)
	}

	// Dan dengan alasan yang sah, lolos.
	w.AvailabilityReasonKey = ptr("STOCK_EMPTY_IN_REGION")
	if err := w.Validate(); err != nil {
		t.Errorf("status dengan alasan sah ditolak: %v", err)
	}
}

// Kartu yang tidak bisa dipilih tidak boleh sekaligus menjadi default: layar
// pilih kartu akan berangkat dengan pilihan awal yang mati.
func TestProductCardWrite_UnavailableCannotBeDefault(t *testing.T) {
	w := validPlacementWrite()
	w.IsDefault = true
	w.AvailabilityStatus = CardOutOfStock
	w.AvailabilityReasonKey = ptr("STOCK_EMPTY_IN_REGION")

	appErr := apperr.From(w.Validate())
	details, _ := appErr.Details.(map[string]any)
	if appErr.Status != 422 || details["field"] != "is_default" {
		t.Fatalf("got status %d, details %+v", appErr.Status, appErr.Details)
	}
}

func TestCardProductWrite_NegativeAndInvertedValuesRejected(t *testing.T) {
	t.Run("fee negatif", func(t *testing.T) {
		w := validCardWrite()
		w.Fees.MonthlyAdmin = -1
		if apperr.From(w.Validate()).Status != 422 {
			t.Error("fee negatif diterima")
		}
	})

	t.Run("jendela kirim terbalik", func(t *testing.T) {
		w := validCardWrite()
		minDays, maxDays := 9, 2
		w.Delivery.EstimatedDaysMin = &minDays
		w.Delivery.EstimatedDaysMax = &maxDays
		appErr := apperr.From(w.Validate())
		details, _ := appErr.Details.(map[string]any)
		if appErr.Status != 422 || details["field"] != "delivery" {
			t.Errorf("jendela terbalik diterima: %+v", appErr.Details)
		}
	})

	t.Run("nama kosong", func(t *testing.T) {
		w := validCardWrite()
		w.Name = "   "
		if apperr.From(w.Validate()).Status != 422 {
			t.Error("nama kosong diterima")
		}
	})

	t.Run("tier tidak dikenal", func(t *testing.T) {
		w := validCardWrite()
		w.TierKey = "SUPER_DEBIT"
		if apperr.From(w.Validate()).Status != 422 {
			t.Error("tier_key tidak dikenal diterima")
		}
	})
}

func TestCardProductWrite_ValidPasses(t *testing.T) {
	if err := validCardWrite().Validate(); err != nil {
		t.Errorf("badan yang sah ditolak: %v", err)
	}
	if err := validPlacementWrite().Validate(); err != nil {
		t.Errorf("penempatan yang sah ditolak: %v", err)
	}
}
