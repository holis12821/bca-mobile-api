package handler_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type stubCatalogReader struct {
	catalog *onboarding.CardCatalog
	err     error
	calls   int

	// Bagian PUT kartu: permintaan terakhir disimpan supaya test bisa
	// memastikan handler mengisi session_id dari path, bukan dari body.
	setCard    *onboarding.SetCardResponse
	setCardErr error
	lastSet    onboarding.SetCardRequest

	// lastRegion menyimpan region_code yang diterima service, supaya test bisa
	// membuktikan handler sudah membakukannya sebelum nilainya dipakai sebagai
	// kunci cache Redis.
	lastRegion string
}

func (s *stubCatalogReader) GetCatalog(_ context.Context, _ onboarding.ProductType, regionCode string) (*onboarding.CardCatalog, error) {
	s.calls++
	s.lastRegion = regionCode
	if s.err != nil {
		return nil, s.err
	}
	return s.catalog, nil
}

func (s *stubCatalogReader) SetCard(_ context.Context, req onboarding.SetCardRequest, _, _ string) (*onboarding.SetCardResponse, error) {
	s.lastSet = req
	if s.setCardErr != nil {
		return nil, s.setCardErr
	}
	return s.setCard, nil
}

// specCatalog menirukan contoh §4 docs/08-PILIH-KARTU-API-SPEC.md.
//
// Angkanya diambil dari contoh itu HANYA untuk membuktikan bentuk payload.
// Angka resmi belum ada (§17) dan tidak boleh disemai ke database mana pun.
func specCatalog() *onboarding.CardCatalog {
	badgeBlue := "RECOMMENDED_BEGINNER"
	min, max := 3, 7
	return &onboarding.CardCatalog{
		CatalogVersion:  "2026-09-22.1",
		ProductType:     onboarding.ProductTahapanBCA,
		DefaultCardType: "PASPOR_BLUE",
		Currency:        "IDR",
		Cards: []onboarding.CardOption{{
			CardType:     "PASPOR_BLUE",
			Name:         "Blue Mastercard",
			Network:      "MASTERCARD",
			TierKey:      "DEBIT",
			Style:        onboarding.CardStyleBlue,
			BadgeKey:     &badgeBlue,
			IsPopular:    true,
			DisplayOrder: 1,
			Fees: onboarding.CardFees{
				MonthlyAdmin: 14000, CardIssuance: 0, CardReplacement: 15000,
			},
			Limits: onboarding.CardLimits{
				CashWithdrawal: 10000000, TransferBCA: 50000000,
				TransferInterbank: 15000000, DebitPurchase: 50000000,
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

func cardRouter(h *handler.CardHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/v1/onboarding/products/{product_type}/cards", h.GetCatalog)
	return r
}

func catalogRequest(productType string) *http.Request {
	req := httptest.NewRequest(http.MethodGet,
		"/v1/onboarding/products/"+productType+"/cards", nil)
	req.Header.Set("X-Device-Id", "dev-test-001")
	return req
}

func doCatalog(t *testing.T, h *handler.CardHandler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	cardRouter(h).ServeHTTP(rec, req)
	return rec
}

// Payload harus persis mengikuti bentuk §4: nama field, tipe, dan susunan objek.
func TestCardCatalog_PayloadMatchesSpecShape(t *testing.T) {
	h := handler.NewCardHandler(&stubCatalogReader{catalog: specCatalog()}, nil)
	rec := doCatalog(t, h, catalogRequest("TAHAPAN_BCA"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("envelope tidak valid: %v", err)
	}
	if envelope.Status != "success" {
		t.Fatalf("status: got %q", envelope.Status)
	}

	var data map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("data tidak valid: %v", err)
	}

	for _, key := range []string{"catalog_version", "product_type", "default_card_type", "currency", "cards"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("field %q hilang dari data: %v", key, keysOf(data))
		}
	}

	cards, ok := data["cards"].([]any)
	if !ok || len(cards) != 1 {
		t.Fatalf("cards bukan array berisi 1: %T", data["cards"])
	}
	card, _ := cards[0].(map[string]any)

	wantCardKeys := []string{
		"card_type", "name", "network", "tier_key", "style", "badge_key",
		"is_popular", "display_order", "fees", "limits", "availability",
		"delivery", "eligibility",
	}
	for _, key := range wantCardKeys {
		if _, ok := card[key]; !ok {
			t.Fatalf("field kartu %q hilang: %v", key, keysOf(card))
		}
	}

	// Field internal tidak boleh bocor ke payload.
	for _, forbidden := range []string{"is_default", "Currency", "region_code"} {
		if _, ok := card[forbidden]; ok {
			t.Fatalf("field internal %q ikut terkirim", forbidden)
		}
	}

	// Nominal harus angka, bukan string terformat (aturan wajib #2).
	fees, _ := card["fees"].(map[string]any)
	if _, ok := fees["monthly_admin"].(float64); !ok {
		t.Fatalf("monthly_admin harus number, got %T", fees["monthly_admin"])
	}

	// reason_key hadir sebagai null, bukan hilang — client membedakan
	// "tidak ada alasan" dari "field tidak dikirim".
	availability, _ := card["availability"].(map[string]any)
	if _, ok := availability["reason_key"]; !ok {
		t.Fatal("availability.reason_key harus tetap ada walau null")
	}
}

// Penjaga aturan wajib #1 dan #2: tidak ada hex warna maupun string "Rp..."
// di seluruh payload. Test ini harus gagal kalau keduanya sengaja disisipkan.
func TestCardCatalog_NoVisualsOrFormattedCurrency(t *testing.T) {
	hexColor := regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	// Tanpa jangkar kutip: nominal terformat bisa tersisip di tengah nilai
	// ("Blue Mastercard Rp14.000"), bukan hanya berdiri sendiri sebagai nilai.
	// Versi pertama penjaga ini mensyaratkan kutip tepat sebelum "Rp" dan
	// karena itu meloloskan kasus tersisip — ketahuan oleh subtest di bawah.
	rupiah := regexp.MustCompile(`Rp\s?\.?[0-9]`)

	t.Run("payload bersih", func(t *testing.T) {
		h := handler.NewCardHandler(&stubCatalogReader{catalog: specCatalog()}, nil)
		body := doCatalog(t, h, catalogRequest("TAHAPAN_BCA")).Body.String()

		if hexColor.MatchString(body) {
			t.Fatalf("payload memuat hex warna: %s", body)
		}
		if rupiah.MatchString(body) {
			t.Fatalf("payload memuat nominal terformat: %s", body)
		}
	})

	// Pelanggaran disengaja harus tertangkap — kalau tidak, penjaga di atas
	// tidak membuktikan apa pun.
	t.Run("pelanggaran tertangkap", func(t *testing.T) {
		dirty := specCatalog()
		badge := "#1A73E8"
		dirty.Cards[0].BadgeKey = &badge
		dirty.Cards[0].Name = "Blue Mastercard Rp14.000"

		h := handler.NewCardHandler(&stubCatalogReader{catalog: dirty}, nil)
		body := doCatalog(t, h, catalogRequest("TAHAPAN_BCA")).Body.String()

		if !hexColor.MatchString(body) {
			t.Fatal("penjaga hex warna tidak menangkap pelanggaran")
		}
		if !rupiah.MatchString(body) {
			t.Fatal("penjaga nominal terformat tidak menangkap pelanggaran")
		}
	})
}

func TestCardCatalog_ETagAndConditionalGet(t *testing.T) {
	h := handler.NewCardHandler(&stubCatalogReader{catalog: specCatalog()}, nil)

	first := doCatalog(t, h, catalogRequest("TAHAPAN_BCA"))
	etag := first.Header().Get("ETag")
	// ETag mengidentifikasi representasi: produk, wilayah, versi.
	// "nat" mewakili permintaan tanpa region_code.
	if etag != `"TAHAPAN_BCA:nat:2026-09-22.1"` {
		t.Fatalf("ETag: got %q", etag)
	}
	if cc := first.Header().Get("Cache-Control"); cc != "public, max-age=900" {
		t.Fatalf("Cache-Control: got %q", cc)
	}

	tests := []struct {
		name         string
		ifNoneMatch  string
		wantStatus   int
		wantEmptyBod bool
	}{
		{"cocok persis", etag, http.StatusNotModified, true},
		{"weak validator", "W/" + etag, http.StatusNotModified, true},
		{"wildcard", "*", http.StatusNotModified, true},
		{"salah satu dari beberapa", `"lama", ` + etag, http.StatusNotModified, true},
		{"versi lama", `"TAHAPAN_BCA:nat:2026-09-21.1"`, http.StatusOK, false},
		{"kosong", "", http.StatusOK, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := catalogRequest("TAHAPAN_BCA")
			if tt.ifNoneMatch != "" {
				req.Header.Set("If-None-Match", tt.ifNoneMatch)
			}
			rec := doCatalog(t, h, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantEmptyBod && rec.Body.Len() != 0 {
				t.Fatalf("304 tidak boleh membawa body, got %d byte", rec.Body.Len())
			}
		})
	}
}

func TestCardCatalog_Errors(t *testing.T) {
	tests := []struct {
		name        string
		productType string
		deviceID    string
		maintenance bool
		svcErr      error
		wantStatus  int
		wantCode    string
	}{
		{
			name: "produk di luar enum", productType: "REKSADANA", deviceID: "dev-1",
			wantStatus: http.StatusNotFound, wantCode: apperr.OnboardingProductUnknown.Code,
		},
		{
			name: "produk maintenance", productType: "TAHAPAN_BCA", deviceID: "dev-1",
			maintenance: true,
			wantStatus:  http.StatusUnprocessableEntity, wantCode: apperr.OnboardingProductUnavailable.Code,
		},
		{
			name: "katalog kosong", productType: "TAHAPAN_BCA", deviceID: "dev-1",
			svcErr:     apperr.CardCatalogEmpty,
			wantStatus: http.StatusNotFound, wantCode: apperr.CardCatalogEmpty.Code,
		},
		{
			name: "tanpa X-Device-Id", productType: "TAHAPAN_BCA", deviceID: "",
			wantStatus: http.StatusBadRequest, wantCode: apperr.ValidationError.Code,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubCatalogReader{catalog: specCatalog(), err: tt.svcErr}
			h := handler.NewCardHandler(stub, func(string) bool { return tt.maintenance })

			req := httptest.NewRequest(http.MethodGet,
				"/v1/onboarding/products/"+tt.productType+"/cards", nil)
			if tt.deviceID != "" {
				req.Header.Set("X-Device-Id", tt.deviceID)
			}
			rec := doCatalog(t, h, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: got %d want %d — %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body tidak valid: %v", err)
			}
			if body.Error.Code != tt.wantCode {
				t.Fatalf("code: got %q want %q", body.Error.Code, tt.wantCode)
			}
		})
	}
}

// Produk tak dikenal dan produk maintenance harus ditolak SEBELUM menyentuh
// service — keduanya tidak perlu membaca katalog.
func TestCardCatalog_RejectsBeforeReadingCatalog(t *testing.T) {
	stub := &stubCatalogReader{catalog: specCatalog()}
	h := handler.NewCardHandler(stub, func(string) bool { return true })

	doCatalog(t, h, catalogRequest("REKSADANA"))
	doCatalog(t, h, catalogRequest("TAHAPAN_BCA")) // maintenance

	if stub.calls != 0 {
		t.Fatalf("service dipanggil %d kali, seharusnya 0", stub.calls)
	}
}

// --- Regresi: region_code yang tidak dibakukan membengkakkan cache Redis ---

// region_code dulu berjalan apa adanya dari query string ke kunci cache
// (onb:cards:{produk}:{wilayah}:{versi}) dan ke filter SQL. Setiap nilai
// karangan mencetak entri cache baru berisi katalog utuh selama 15 menit, dan
// karena nilainya selalu baru, setiap permintaan dijamin cache miss dengan satu
// query database di belakangnya — di endpoint yang tidak memerlukan token.
func TestCardCatalog_RejectsMalformedRegionCode(t *testing.T) {
	cases := []struct {
		name   string
		region string
	}{
		{"titik dua menyelipkan pemisah kunci Redis", "DKI:extra"},
		{"spasi dan tanda baca", "dki jakarta!"},
		{"terlalu panjang", strings.Repeat("A", 11)},
		{"karakter non-ASCII", "DKÌ"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubCatalogReader{catalog: specCatalog()}
			h := handler.NewCardHandler(stub, nil)

			req := httptest.NewRequest(http.MethodGet,
				"/v1/onboarding/products/TAHAPAN_BCA/cards?region_code="+url.QueryEscape(tc.region), nil)
			req.Header.Set("X-Device-Id", "dev-test-001")
			rec := doCatalog(t, h, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400 — body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "region_code") {
				t.Errorf("details tidak menyebut field yang salah: %s", rec.Body.String())
			}
			if stub.calls != 0 {
				t.Errorf("service dipanggil %d kali; nilai yang ditolak tidak boleh menyentuh cache atau database", stub.calls)
			}
		})
	}
}

// Huruf kecil dibakukan, bukan ditolak: "dki" dan "DKI" adalah wilayah yang
// sama, dan membiarkan keduanya lewat berarti dua entri cache untuk satu isi.
func TestCardCatalog_NormalizesRegionCode(t *testing.T) {
	stub := &stubCatalogReader{catalog: specCatalog()}
	h := handler.NewCardHandler(stub, nil)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/onboarding/products/TAHAPAN_BCA/cards?region_code=+dki+", nil)
	req.Header.Set("X-Device-Id", "dev-test-001")
	rec := doCatalog(t, h, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body %s", rec.Code, rec.Body.String())
	}
	if stub.lastRegion != "DKI" {
		t.Errorf("region_code yang diteruskan: got %q, want \"DKI\"", stub.lastRegion)
	}
}

// PUT kartu memakai gerbang yang sama: nilainya ikut ke FindCard dan ke kunci
// cache yang sama.
func TestSetCard_RejectsMalformedRegionCode(t *testing.T) {
	stub := &stubCatalogReader{setCard: &onboarding.SetCardResponse{}}
	h := handler.NewCardHandler(stub, nil)

	req := httptest.NewRequest(http.MethodPut,
		"/v1/onboarding/sessions/onb_abc/card?region_code=DKI%3Aextra",
		strings.NewReader(`{"card_type":"PASPOR_GOLD"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	setCardRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 — body %s", rec.Code, rec.Body.String())
	}
	if stub.lastSet.CardType != "" {
		t.Error("service dipanggil padahal region_code ditolak")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- Regresi: ETag harus mengidentifikasi representasi ---

// Dulu ETag hanya memuat catalog_version, sehingga klien yang memegang katalog
// nasional lalu meminta katalog wilayah menerima 304 dan terus menampilkan data
// nasional — kartu yang habis di satu wilayah tidak pernah terlihat habis.
func TestCardCatalog_ETagDistinguishesRepresentations(t *testing.T) {
	h := handler.NewCardHandler(&stubCatalogReader{catalog: specCatalog()}, nil)

	etagOf := func(url string) string {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("X-Device-Id", "dev-test-001")
		rec := httptest.NewRecorder()
		cardRouter(h).ServeHTTP(rec, req)
		return rec.Header().Get("ETag")
	}

	national := etagOf("/v1/onboarding/products/TAHAPAN_BCA/cards")
	regional := etagOf("/v1/onboarding/products/TAHAPAN_BCA/cards?region_code=DKI")

	if national == "" || regional == "" {
		t.Fatalf("ETag kosong: %q / %q", national, regional)
	}
	if national == regional {
		t.Fatalf("wilayah berbeda harus punya ETag berbeda, keduanya %q", national)
	}

	// ETag nasional tidak boleh membuat permintaan wilayah dijawab 304.
	req := httptest.NewRequest(http.MethodGet,
		"/v1/onboarding/products/TAHAPAN_BCA/cards?region_code=DKI", nil)
	req.Header.Set("X-Device-Id", "dev-test-001")
	req.Header.Set("If-None-Match", national)
	rec := httptest.NewRecorder()
	cardRouter(h).ServeHTTP(rec, req)

	if rec.Code == http.StatusNotModified {
		t.Fatal("ETag nasional dipakai ulang untuk wilayah lain: klien akan menampilkan katalog yang salah")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Tapi ETag yang benar tetap harus menghasilkan 304.
	req2 := httptest.NewRequest(http.MethodGet,
		"/v1/onboarding/products/TAHAPAN_BCA/cards?region_code=DKI", nil)
	req2.Header.Set("X-Device-Id", "dev-test-001")
	req2.Header.Set("If-None-Match", regional)
	rec2 := httptest.NewRecorder()
	cardRouter(h).ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("ETag yang cocok harus 304, got %d", rec2.Code)
	}
}

// default_card_type kosong saat tidak ada kartu yang bisa dipilih — kartunya
// tetap dikirim beserta alasannya (§4).
func TestCardCatalog_EmptyDefaultWhenNothingSelectable(t *testing.T) {
	catalog := specCatalog()
	reason := "STOCK_EMPTY_IN_REGION"
	catalog.Cards[0].Availability = onboarding.CardAvailability{
		Status: onboarding.CardOutOfStock, ReasonKey: &reason,
	}
	catalog.DefaultCardType = ""

	h := handler.NewCardHandler(&stubCatalogReader{catalog: catalog}, nil)
	rec := doCatalog(t, h, catalogRequest("TAHAPAN_BCA"))

	var body struct {
		Data struct {
			DefaultCardType *string `json:"default_card_type"`
			Cards           []struct {
				Availability struct {
					ReasonKey *string `json:"reason_key"`
				} `json:"availability"`
			} `json:"cards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body tidak valid: %v", err)
	}

	// String kosong, bukan null: klien dengan field non-nullable tidak boleh
	// gagal mem-parsing.
	if body.Data.DefaultCardType == nil {
		t.Fatal("default_card_type harus string kosong, bukan null")
	}
	if *body.Data.DefaultCardType != "" {
		t.Fatalf("expected string kosong, got %q", *body.Data.DefaultCardType)
	}
	if len(body.Data.Cards) != 1 {
		t.Fatalf("kartu harus tetap dikirim, got %d", len(body.Data.Cards))
	}
	if body.Data.Cards[0].Availability.ReasonKey == nil {
		t.Fatal("kartu tak tersedia harus menyertakan reason_key")
	}
}

// --- PUT /v1/onboarding/sessions/{session_id}/card (§8) ---

func setCardRouter(h *handler.CardHandler) http.Handler {
	r := chi.NewRouter()
	r.Put("/v1/onboarding/sessions/{session_id}/card", h.SetCard)
	return r
}

func doSetCard(h *handler.CardHandler, sessionID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut,
		"/v1/onboarding/sessions/"+sessionID+"/card", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	setCardRouter(h).ServeHTTP(rec, req)
	return rec
}

func TestSetCard_UsesSessionIDFromPath(t *testing.T) {
	stub := &stubCatalogReader{setCard: &onboarding.SetCardResponse{
		Card:        onboarding.SessionCard{CardType: "PASPOR_GOLD", Name: "Gold Mastercard"},
		CurrentStep: onboarding.StepOCR,
	}}
	h := handler.NewCardHandler(stub, nil)

	// Body menyebut sesi lain. Handler harus mengabaikannya: kalau body yang
	// menentukan, satu permintaan bisa mengubah kartu sesi yang berbeda dari
	// sesi yang dibatasi laju oleh path.
	rec := doSetCard(h, "onb_benar",
		`{"card_type":"PASPOR_GOLD","session_id":"onb_orang_lain"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body %s", rec.Code, rec.Body.String())
	}
	if stub.lastSet.SessionID != "onb_benar" {
		t.Errorf("session_id: got %q, want onb_benar", stub.lastSet.SessionID)
	}
	if stub.lastSet.CardType != "PASPOR_GOLD" {
		t.Errorf("card_type: got %q", stub.lastSet.CardType)
	}
}

func TestSetCard_EmptyCardTypeRejected(t *testing.T) {
	stub := &stubCatalogReader{}
	h := handler.NewCardHandler(stub, nil)

	rec := doSetCard(h, "onb_abc", `{"card_type":"   "}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CARD_TYPE_INVALID") {
		t.Errorf("kode error: %s", rec.Body.String())
	}
	if stub.lastSet.CardType != "" {
		t.Error("service dipanggil padahal card_type kosong")
	}
}

func TestSetCard_PropagatesDomainError(t *testing.T) {
	stub := &stubCatalogReader{setCardErr: apperr.CardLocked}
	h := handler.NewCardHandler(stub, nil)

	rec := doSetCard(h, "onb_abc", `{"card_type":"PASPOR_GOLD"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CARD_LOCKED") {
		t.Errorf("kode error: %s", rec.Body.String())
	}
}

// --- Admin katalog kartu: /internal/v1/cards* (§Prompt 7) ---

type stubCardAdmin struct {
	rows   []onboarding.AdminCardRow
	result *onboarding.CardCatalogWriteResult
	err    error

	lastCardType    string
	lastProductType onboarding.ProductType
	lastActor       string
	lastCardWrite   onboarding.CardProductWrite
	lastPlacement   onboarding.ProductCardWrite
	calls           int
}

func (s *stubCardAdmin) ListCards(context.Context) ([]onboarding.AdminCardRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

func (s *stubCardAdmin) UpdateCard(_ context.Context, cardType string, w onboarding.CardProductWrite, actor, _ string) (*onboarding.CardCatalogWriteResult, error) {
	s.calls++
	s.lastCardType, s.lastActor, s.lastCardWrite = cardType, actor, w
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func (s *stubCardAdmin) UpdateProductCard(_ context.Context, productType onboarding.ProductType, cardType string, w onboarding.ProductCardWrite, actor, _ string) (*onboarding.CardCatalogWriteResult, error) {
	s.calls++
	s.lastProductType, s.lastCardType, s.lastActor, s.lastPlacement = productType, cardType, actor, w
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func adminRouter(h *handler.CardAdminHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/internal/v1/cards", h.ListCards)
	r.Put("/internal/v1/cards/{card_type}", h.UpdateCard)
	r.Put("/internal/v1/products/{product_type}/cards/{card_type}", h.UpdateProductCard)
	return r
}

func doAdmin(h *handler.CardAdminHandler, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Actor", "admin@bca.test")
	rec := httptest.NewRecorder()
	adminRouter(h).ServeHTTP(rec, req)
	return rec
}

const validAdminCardBody = `{
  "name": "Gold Mastercard",
  "tier_key": "DEBIT",
  "style": "GOLD",
  "fees": {"monthly_admin": 16000, "card_issuance": 0, "card_replacement": 15000},
  "limits": {"cash_withdrawal": 10000000, "transfer_bca": 75000000,
             "transfer_interbank": 25000000, "debit_purchase": 75000000},
  "delivery": {"physical_card_available": true, "estimated_days_min": 3,
               "estimated_days_max": 7, "branch_pickup_available": true},
  "eligibility": {"min_age": 17, "min_initial_deposit": 500000},
  "is_active": true
}`

func TestCardAdmin_ListReturnsArrayNotNull(t *testing.T) {
	h := handler.NewCardAdminHandler(&stubCardAdmin{})

	rec := doAdmin(h, http.MethodGet, "/internal/v1/cards", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d — %s", rec.Code, rec.Body.String())
	}
	// Daftar kosong harus berupa [], bukan null: klien yang melakukan .length
	// pada null akan meledak tanpa sebab yang jelas.
	if !strings.Contains(rec.Body.String(), `"cards":[]`) {
		t.Errorf("daftar kosong bukan array: %s", rec.Body.String())
	}
}

func TestCardAdmin_UpdateCardPassesPathAndActor(t *testing.T) {
	stub := &stubCardAdmin{result: &onboarding.CardCatalogWriteResult{
		CatalogVersion: "2026-09-24.2", CardType: "PASPOR_GOLD",
	}}
	h := handler.NewCardAdminHandler(stub)

	rec := doAdmin(h, http.MethodPut, "/internal/v1/cards/PASPOR_GOLD", validAdminCardBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d — %s", rec.Code, rec.Body.String())
	}
	if stub.lastCardType != "PASPOR_GOLD" {
		t.Errorf("card_type: got %q", stub.lastCardType)
	}
	if stub.lastActor != "admin@bca.test" {
		t.Errorf("actor: got %q", stub.lastActor)
	}
	if stub.lastCardWrite.Fees.MonthlyAdmin != 16000 || !stub.lastCardWrite.IsActive {
		t.Errorf("badan tidak terbaca utuh: %+v", stub.lastCardWrite)
	}
	if !strings.Contains(rec.Body.String(), "2026-09-24.2") {
		t.Errorf("versi katalog tidak dibalas: %s", rec.Body.String())
	}
}

// Tanpa header aktor, jejak audit tetap tertulis dengan penanda kunci API —
// bukan string kosong yang tidak bisa ditanyakan ke siapa pun.
func TestCardAdmin_ActorFallsBackToAPIKeyLabel(t *testing.T) {
	stub := &stubCardAdmin{result: &onboarding.CardCatalogWriteResult{}}
	h := handler.NewCardAdminHandler(stub)

	req := httptest.NewRequest(http.MethodPut, "/internal/v1/cards/PASPOR_GOLD",
		strings.NewReader(validAdminCardBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	adminRouter(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d — %s", rec.Code, rec.Body.String())
	}
	if stub.lastActor != "internal-api-key" {
		t.Errorf("actor: got %q, want internal-api-key", stub.lastActor)
	}
}

// Field yang salah tulis ditolak, tidak diabaikan: "is_ative" yang terabaikan
// akan menulis is_active=false dan menonaktifkan kartu tanpa ada yang meminta.
func TestCardAdmin_UnknownFieldRejected(t *testing.T) {
	stub := &stubCardAdmin{result: &onboarding.CardCatalogWriteResult{}}
	h := handler.NewCardAdminHandler(stub)

	body := `{"name":"Gold","tier_key":"DEBIT","style":"GOLD","is_ative":true}`
	rec := doAdmin(h, http.MethodPut, "/internal/v1/cards/PASPOR_GOLD", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 — %s", rec.Code, rec.Body.String())
	}
	if stub.calls != 0 {
		t.Error("service dipanggil padahal badan ditolak")
	}
}

func TestCardAdmin_UnknownProductTypeRejected(t *testing.T) {
	stub := &stubCardAdmin{result: &onboarding.CardCatalogWriteResult{}}
	h := handler.NewCardAdminHandler(stub)

	rec := doAdmin(h, http.MethodPut,
		"/internal/v1/products/TAHAPAN_PALSU/cards/PASPOR_GOLD",
		`{"display_order":1,"is_default":false,"is_popular":false,"badge_key":null,
		  "availability_status":"AVAILABLE","availability_reason_key":null,"region_code":null}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404 — %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ONBOARDING_PRODUCT_UNKNOWN") {
		t.Errorf("kode error: %s", rec.Body.String())
	}
	if stub.calls != 0 {
		t.Error("service dipanggil untuk produk tak dikenal")
	}
}

func TestCardAdmin_UpdatePlacementPassesPath(t *testing.T) {
	stub := &stubCardAdmin{result: &onboarding.CardCatalogWriteResult{CatalogVersion: "2026-09-24.3"}}
	h := handler.NewCardAdminHandler(stub)

	rec := doAdmin(h, http.MethodPut,
		"/internal/v1/products/TAHAPAN_BCA/cards/PASPOR_PLATINUM",
		`{"display_order":3,"is_default":false,"is_popular":true,"badge_key":"MAX_LIMIT",
		  "availability_status":"OUT_OF_STOCK",
		  "availability_reason_key":"STOCK_EMPTY_IN_REGION","region_code":"JKT"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d — %s", rec.Code, rec.Body.String())
	}
	if stub.lastProductType != onboarding.ProductTahapanBCA || stub.lastCardType != "PASPOR_PLATINUM" {
		t.Errorf("path: product=%q card=%q", stub.lastProductType, stub.lastCardType)
	}
	if stub.lastPlacement.RegionCode == nil || *stub.lastPlacement.RegionCode != "JKT" {
		t.Errorf("region_code: %v", stub.lastPlacement.RegionCode)
	}
	if stub.lastPlacement.AvailabilityStatus != onboarding.CardOutOfStock {
		t.Errorf("status: %s", stub.lastPlacement.AvailabilityStatus)
	}
}

// Penolakan dari domain diteruskan apa adanya, beserta details-nya — itulah yang
// membuat admin tahu nilai apa yang sah.
func TestCardAdmin_PropagatesValidationError(t *testing.T) {
	stub := &stubCardAdmin{err: apperr.Error{
		Status:  422,
		Code:    apperr.CardCatalogInvalidValue.Code,
		Message: "style tidak dikenal",
		Details: map[string]any{"field": "style", "allowed_values": []string{"BLUE", "GOLD", "PLATINUM"}},
	}}
	h := handler.NewCardAdminHandler(stub)

	rec := doAdmin(h, http.MethodPut, "/internal/v1/cards/PASPOR_GOLD", validAdminCardBody)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422 — %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "CARD_CATALOG_INVALID_VALUE") || !strings.Contains(body, "allowed_values") {
		t.Errorf("details tidak diteruskan: %s", body)
	}
}
