package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// The validation layer answers before any service is touched, so a handler
// built with nil services is exactly the right instrument for testing it: if a
// request ever got past validation, the test would panic instead of passing.
func newValidationOnlyHandler() *OnboardingHandler {
	return NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func postJSON(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func errorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not the standard envelope: %v (%s)", err, rr.Body.String())
	}
	return envelope.Error.Code
}

// encryption_key_id is NOT NULL in onboarding_credentials. Without this check
// the request reached the insert and came back as a 500 — a client mistake
// reported as a server fault. VALIDATION_ERROR is a 400 in this API.
func TestSetCredentials_RequiresEncryptionKeyID(t *testing.T) {
	h := newValidationOnlyHandler()

	rr := postJSON(t, h.SetCredentials, `{
		"session_id": "onb_123",
		"access_code_encrypted": "abc",
		"pin_encrypted": "def"
	}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR, got %s", code)
	}
}

func TestSetCredentials_RejectsIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	cases := map[string]string{
		"no session":     `{"access_code_encrypted":"a","pin_encrypted":"b","encryption_key_id":"k"}`,
		"no access code": `{"session_id":"onb_1","pin_encrypted":"b","encryption_key_id":"k"}`,
		"no pin":         `{"session_id":"onb_1","access_code_encrypted":"a","encryption_key_id":"k"}`,
		"malformed json": `{"session_id":`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.SetCredentials, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestCreateSession_RejectsIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	cases := map[string]string{
		"no product":  `{"device_id":"dev-1","accepted_tnc_version":"2026-09-01"}`,
		"no device":   `{"product_type":"TAHAPAN_BCA","accepted_tnc_version":"2026-09-01"}`,
		"no tnc":      `{"product_type":"TAHAPAN_BCA","device_id":"dev-1"}`,
		"empty body":  `{}`,
		"not an obj":  `[]`,
		"broken json": `{"device_id"`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.CreateSession, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestVerifyAndResendOTP_RejectIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	if rr := postJSON(t, h.VerifyOTP, `{"session_id":"onb_1"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("verify-otp without a code: expected 400, got %d", rr.Code)
	}
	if rr := postJSON(t, h.VerifyOTP, `{"otp_code":"123456"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("verify-otp without a session: expected 400, got %d", rr.Code)
	}
	if rr := postJSON(t, h.ResendOTP, `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("resend-otp without a session: expected 400, got %d", rr.Code)
	}
}

// A code that cannot possibly be an OTP is a malformed request, not a wrong
// guess: it must be turned away before it can spend one of the five attempts.
func TestVerifyOTP_RejectsMalformedCodes(t *testing.T) {
	h := newValidationOnlyHandler()

	malformed := map[string]string{
		"too short":     `{"session_id":"onb_1","otp_code":"12345"}`,
		"too long":      `{"session_id":"onb_1","otp_code":"1234567"}`,
		"letters":       `{"session_id":"onb_1","otp_code":"12a456"}`,
		"spaces":        `{"session_id":"onb_1","otp_code":"123 56"}`,
		"unicode digit": `{"session_id":"onb_1","otp_code":"１２３４５６"}`,
	}

	for name, body := range malformed {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.VerifyOTP, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestSubmitVideoCallResult_RejectsIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	cases := map[string]string{
		"no queue":   `{"session_id":"onb_1","result":"APPROVED"}`,
		"no result":  `{"session_id":"onb_1","queue_id":"q_1"}`,
		"no session": `{"queue_id":"q_1","result":"APPROVED"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.SubmitVideoCallResult, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestIssueAgentSignalingToken_RequiresQueueID(t *testing.T) {
	h := newValidationOnlyHandler()

	if rr := postJSON(t, h.IssueAgentSignalingToken, `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// Capture-quality signals are optional. A value that is absent or unparseable
// must read as "not reported" — never as a passing score, which would hand a
// client a way to opt out of the quality gate by sending garbage.
func TestOptionalCaptureSignals(t *testing.T) {
	if got := optionalFloat(""); got != nil {
		t.Errorf("empty value should be nil, got %v", *got)
	}
	if got := optionalFloat("  "); got != nil {
		t.Errorf("blank value should be nil, got %v", *got)
	}
	if got := optionalFloat("not-a-number"); got != nil {
		t.Errorf("unparseable value should be nil, got %v", *got)
	}
	if got := optionalFloat(" 91.5 "); got == nil || *got != 91.5 {
		t.Errorf("expected 91.5, got %v", got)
	}

	if got := optionalInt(""); got != nil {
		t.Errorf("empty value should be nil, got %v", *got)
	}
	if got := optionalInt("4.5"); got != nil {
		t.Errorf("a float is not a corner count, got %v", *got)
	}
	if got := optionalInt("4"); got == nil || *got != 4 {
		t.Errorf("expected 4, got %v", got)
	}
}

// --- GET /v1/onboarding/products (katalog jenis rekening) ---

// stubProductRepo melayani ProductCatalogRepository dari paket handler.
//
// Mock-nya di sini, bukan memakai yang ada di paket onboarding: mock itu tidak diekspor,
// dan mengekspornya hanya demi satu test akan menambah permukaan API paket domain.
type stubProductRepo struct {
	catalog *onboarding.SavingsProductCatalog
	err     error
}

func (s stubProductRepo) ActiveCatalog(_ context.Context) (*onboarding.SavingsProductCatalog, error) {
	return s.catalog, s.err
}

func productCatalogFixture() *onboarding.SavingsProductCatalog {
	badge := onboarding.ProductBadgeMostPopular
	return &onboarding.SavingsProductCatalog{
		CatalogVersion: "2026-10-07.1",
		Page: onboarding.ProductPage{
			Heading:      "Pilih Jenis Rekening",
			Subtitle:     "Pilih jenis rekening yang sesuai dengan kebutuhan dan gaya hidup Anda.",
			DepositLabel: "Setoran Awal Minimum",
			CTALabel:     "Lanjut",
			Notice: onboarding.ProductNotice{
				IconKey: "INFO",
				Title:   "Persiapan Dokumen",
				Body:    "Siapkan e-KTP fisik Anda.",
			},
			Consent: onboarding.ProductConsent{
				Prefix: "Dengan melanjutkan, Anda menyetujui ",
				Link:   "Syarat & Ketentuan",
				Suffix: " pembukaan rekening BCA.",
			},
		},
		Products: []onboarding.ProductOption{{
			ProductType:        onboarding.ProductTahapanBCA,
			Name:               "Tahapan BCA",
			Description:        "Tabungan utama untuk kemudahan transaksi harian.",
			MinInitialDeposit:  500000,
			Currency:           "IDR",
			IconKey:            onboarding.ProductIconWallet,
			Style:              onboarding.ProductStylePrimary,
			Features:           []string{"Debit Mastercard", "m-BCA & KlikBCA"},
			IsPopular:          true,
			BadgeKey:           &badge,
			IsDefault:          true,
			DisplayOrder:       1,
			AvailabilityStatus: onboarding.ProductAvailable,
		}},
	}
}

func newProductHandler(catalog *onboarding.SavingsProductCatalog) *OnboardingHandler {
	svc := onboarding.NewProductService(onboarding.ProductServiceConfig{
		Repo:    stubProductRepo{catalog: catalog},
		Enabled: true,
	})
	return NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, svc, nil)
}

func getProducts(t *testing.T, h *OnboardingHandler, deviceID, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/products", nil)
	if deviceID != "" {
		req.Header.Set("X-Device-Id", deviceID)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rr := httptest.NewRecorder()
	h.GetProducts(rr, req)
	return rr
}

func TestGetProducts_Success(t *testing.T) {
	rr := getProducts(t, newProductHandler(productCatalogFixture()), "dev-1", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, mau 200. body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("ETag"); got != `"products-2026-10-07.1"` {
		t.Errorf("ETag = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", got)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			CatalogVersion string `json:"catalog_version"`
			Page           struct {
				Heading string `json:"heading"`
				Consent struct {
					Prefix string `json:"prefix"`
					Link   string `json:"link"`
					Suffix string `json:"suffix"`
				} `json:"consent"`
			} `json:"page"`
			Products []struct {
				ProductType           string   `json:"product_type"`
				MinInitialDeposit     int64    `json:"min_initial_deposit"`
				IconKey               string   `json:"icon_key"`
				Style                 string   `json:"style"`
				Features              []string `json:"features"`
				IsPopular             bool     `json:"is_popular"`
				BadgeKey              *string  `json:"badge_key"`
				DisplayOrder          int      `json:"display_order"`
				AvailabilityStatus    string   `json:"availability_status"`
				AvailabilityReasonKey *string  `json:"availability_reason_key"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Status != "success" {
		t.Errorf("status = %q", body.Status)
	}
	if body.Data.CatalogVersion != "2026-10-07.1" {
		t.Errorf("catalog_version = %q", body.Data.CatalogVersion)
	}
	if len(body.Data.Products) != 1 {
		t.Fatalf("mau 1 produk, dapat %d", len(body.Data.Products))
	}

	p := body.Data.Products[0]
	// product_type adalah satu-satunya identitas produk — client tidak boleh perlu
	// menyimpulkannya dari posisi array.
	if p.ProductType != "TAHAPAN_BCA" {
		t.Errorf("product_type = %q", p.ProductType)
	}
	// Nominal integer rupiah penuh, bukan string terformat.
	if p.MinInitialDeposit != 500000 {
		t.Errorf("min_initial_deposit = %d, mau 500000", p.MinInitialDeposit)
	}
	if p.IconKey != "WALLET" || p.Style != "PRIMARY" {
		t.Errorf("icon_key/style = %q/%q", p.IconKey, p.Style)
	}
	if !p.IsPopular || p.BadgeKey == nil || *p.BadgeKey != "MOST_POPULAR" {
		t.Error("badge produk populer tidak benar")
	}
	// availability_reason_key harus null, bukan string kosong: client membedakan
	// "tidak ada alasan" dari "alasan tanpa teks".
	if p.AvailabilityReasonKey != nil {
		t.Errorf("availability_reason_key = %v, mau null", *p.AvailabilityReasonKey)
	}
	if p.AvailabilityStatus != "AVAILABLE" {
		t.Errorf("availability_status = %q", p.AvailabilityStatus)
	}

	// consent tetap tiga potong — client mencetak bagian tengah tebal + berwarna.
	c := body.Data.Page.Consent
	if c.Prefix == "" || c.Link == "" || c.Suffix == "" {
		t.Errorf("consent harus tiga potong: %+v", c)
	}

	// Payload tidak boleh memuat nilai visual.
	raw := rr.Body.String()
	for _, forbidden := range []string{"0xFF", "#FF", "drawable", "ic_", "AppColor"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("payload memuat nilai visual %q", forbidden)
		}
	}
}

// X-Device-Id DIWAJIBKAN di sini, berbeda dari GetTNC: rate limitnya per device, dan
// tanpa header itu seluruh nasabah di belakang satu NAT berbagi satu jatah.
func TestGetProducts_MissingDeviceHeader(t *testing.T) {
	rr := getProducts(t, newProductHandler(productCatalogFixture()), "", "")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, mau 400. body: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				MissingHeader string `json:"missing_header"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q", body.Error.Code)
	}
	if body.Error.Details.MissingHeader != "X-Device-Id" {
		t.Errorf("details.missing_header = %q", body.Error.Details.MissingHeader)
	}
}

// 304 tidak boleh membawa SATU BYTE pun body — termasuk envelope standar.
func TestGetProducts_NotModified(t *testing.T) {
	h := newProductHandler(productCatalogFixture())
	rr := getProducts(t, h, "dev-1", `"products-2026-10-07.1"`)

	if rr.Code != http.StatusNotModified {
		t.Fatalf("status = %d, mau 304", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("304 membawa %d byte body: %q", rr.Body.Len(), rr.Body.String())
	}
}

// ETag versi LAMA harus dijawab 200 dengan isi baru: client yang menahan versi basi
// hanya akan menampilkan setoran awal yang keliru.
func TestGetProducts_StaleETagGets200(t *testing.T) {
	h := newProductHandler(productCatalogFixture())
	rr := getProducts(t, h, "dev-1", `"products-2026-01-01.1"`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, mau 200", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Error("200 harus membawa body")
	}
}

// W/ (weak validator) tetap cocok — matchesETag sudah menanganinya.
func TestGetProducts_WeakETagMatches(t *testing.T) {
	h := newProductHandler(productCatalogFixture())
	rr := getProducts(t, h, "dev-1", `W/"products-2026-10-07.1"`)

	if rr.Code != http.StatusNotModified {
		t.Fatalf("status = %d, mau 304", rr.Code)
	}
}

// Katalog kosong → 503, bukan daftar kosong. Daftar kosong akan membuat client
// menampilkan layar tanpa pilihan dan nasabah berhenti di layar pertama tanpa tahu
// kenapa; 503 adalah sinyal untuk jatuh ke fallback strings.xml.
func TestGetProducts_EmptyCatalogIsUnavailable(t *testing.T) {
	empty := productCatalogFixture()
	empty.Products = nil
	rr := getProducts(t, newProductHandler(empty), "dev-1", "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, mau 503. body: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Error.Code != "ONBOARDING_CATALOG_UNAVAILABLE" {
		t.Errorf("code = %q", body.Error.Code)
	}
}

// Service yang tidak dirakit adalah katalog yang tidak tersedia, bukan panic.
func TestGetProducts_NilServiceIsUnavailable(t *testing.T) {
	h := NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rr := getProducts(t, h, "dev-1", "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, mau 503", rr.Code)
	}
}

// Feature flag mati → 503, dan client jatuh ke strings.xml.
func TestGetProducts_FlagOffIsUnavailable(t *testing.T) {
	svc := onboarding.NewProductService(onboarding.ProductServiceConfig{
		Repo:    stubProductRepo{catalog: productCatalogFixture()},
		Enabled: false,
	})
	h := NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, svc, nil)
	rr := getProducts(t, h, "dev-1", "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, mau 503", rr.Code)
	}
}
