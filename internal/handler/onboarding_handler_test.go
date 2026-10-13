package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
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

// --- Admin katalog produk (Fase 6) ---

// stubProductAdminRepo adalah repo tulis in-memory untuk test handler.
type stubProductAdminRepo struct {
	catalog *onboarding.AdminProductCatalog
	writes  int
	last    []onboarding.ProductWrite
	actor   string
}

func (s *stubProductAdminRepo) ListAllProducts(_ context.Context) (*onboarding.AdminProductCatalog, error) {
	return s.catalog, nil
}

func (s *stubProductAdminRepo) WriteProducts(_ context.Context, writes []onboarding.ProductWrite, actor, _ string) (*onboarding.ProductCatalogWriteResult, error) {
	s.writes++
	s.last = writes
	s.actor = actor

	updated := make([]onboarding.ProductType, 0, len(writes))
	for _, w := range writes {
		updated = append(updated, w.ProductType)
	}
	return &onboarding.ProductCatalogWriteResult{
		CatalogVersion:   "2026-10-08.2",
		Updated:          updated,
		FeaturesReplaced: []onboarding.ProductType{},
	}, nil
}

func newAdminProductHandler(repo *stubProductAdminRepo) *OnboardingHandler {
	h := NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetProductAdminService(onboarding.NewProductAdminService(repo, nil))
	return h
}

func putAdminProducts(t *testing.T, h *OnboardingHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/internal/v1/onboarding/products",
		strings.NewReader(body))
	req.Header.Set("X-Admin-Actor", "OPS-2001")
	rr := httptest.NewRecorder()
	h.AdminWriteProducts(rr, req)
	return rr
}

const validAdminProductBody = `{"products":[{
	"product_type":"TAHAPAN_BCA","name":"Tahapan BCA","description":"Tabungan utama.",
	"min_initial_deposit":500000,"currency":"IDR","icon_key":"WALLET","style":"PRIMARY",
	"badge_key":null,"features":null,"is_popular":false,"is_default":false,
	"is_active":true,"display_order":1,
	"availability_status":"AVAILABLE","availability_reason_key":null
}]}`

func TestAdminWriteProducts_Success(t *testing.T) {
	repo := &stubProductAdminRepo{}
	rr := putAdminProducts(t, newAdminProductHandler(repo), validAdminProductBody)

	if rr.Code != http.StatusOK {
		t.Fatalf("mau 200, dapat %d: %s", rr.Code, rr.Body.String())
	}
	if repo.writes != 1 {
		t.Errorf("mau 1 penulisan, dapat %d", repo.writes)
	}
	if repo.actor != "OPS-2001" {
		t.Errorf("mau aktor OPS-2001 dari X-Admin-Actor, dapat %q", repo.actor)
	}
	if !strings.Contains(rr.Body.String(), "2026-10-08.2") {
		t.Error("response harus memuat catalog_version yang baru")
	}
}

// Field yang tidak dikenal DITOLAK, tidak diabaikan.
//
// Ini alasan utama decodeAdminBody ada: "is_ative" yang terabaikan akan menulis
// is_active = false ke katalog yang tayang ke nasabah, menonaktifkan produk tanpa ada
// yang meminta — dan tidak ada yang akan tahu sampai nasabah mengeluh pilihannya hilang.
func TestAdminWriteProducts_RejectsUnknownField(t *testing.T) {
	repo := &stubProductAdminRepo{}
	body := strings.Replace(validAdminProductBody, `"is_active":true`, `"is_ative":true`, 1)

	rr := putAdminProducts(t, newAdminProductHandler(repo), body)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("mau 400 untuk field yang salah tulis, dapat %d: %s", rr.Code, rr.Body.String())
	}
	if repo.writes != 0 {
		t.Error("badan yang ditolak tidak boleh menulis apa pun")
	}
}

// Badan yang melanggar invarian katalog dijawab 422 dengan kode katalog PRODUK —
// bukan kode katalog kartu, supaya klien admin yang mengelola keduanya tahu mana yang
// menolaknya.
func TestAdminWriteProducts_InvalidValueIs422(t *testing.T) {
	repo := &stubProductAdminRepo{}
	body := strings.Replace(validAdminProductBody,
		`"availability_status":"AVAILABLE"`, `"availability_status":"DISABLED"`, 1)

	rr := putAdminProducts(t, newAdminProductHandler(repo), body)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mau 422 untuk DISABLED tanpa alasan, dapat %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ONBOARDING_PRODUCT_INVALID_VALUE") {
		t.Errorf("mau kode ONBOARDING_PRODUCT_INVALID_VALUE, dapat %s", rr.Body.String())
	}
	// details.field menyebut field-nya supaya admin bisa membetulkannya pada percobaan
	// pertama tanpa membuka kode.
	if !strings.Contains(rr.Body.String(), "availability_reason_key") {
		t.Errorf("details harus menyebut field yang salah, dapat %s", rr.Body.String())
	}
	if repo.writes != 0 {
		t.Error("badan yang ditolak tidak boleh menulis apa pun")
	}
}

// Service yang tidak dirakit menjawab 503, bukan panic — pola yang sama dengan GetTNC
// dan GetProducts.
func TestAdminProducts_NoServiceIs503(t *testing.T) {
	h := NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	rr := httptest.NewRecorder()
	h.AdminListProducts(rr, httptest.NewRequest(http.MethodGet, "/internal/v1/onboarding/products", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("GET: mau 503, dapat %d", rr.Code)
	}

	rr = putAdminProducts(t, h, validAdminProductBody)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT: mau 503, dapat %d", rr.Code)
	}
}

// Jalur admin TIDAK menuntut X-Device-Id dan TIDAK mengirim ETag/Cache-Control.
//
// Keduanya disengaja: layar admin yang menerima 304 akan menyunting salinan yang mungkin
// dibuat sebelum penulisan terakhir, lalu mengirimkannya kembali — menulis ulang
// perubahan orang lain tanpa ada yang tahu.
func TestAdminListProducts_NoDeviceHeaderNoCaching(t *testing.T) {
	repo := &stubProductAdminRepo{catalog: &onboarding.AdminProductCatalog{
		CatalogVersion: "2026-10-08.1",
		Products: []onboarding.AdminProductRow{{
			ProductType:        onboarding.ProductTahapanBCA,
			Name:               "Tahapan BCA",
			IsActive:           false, // produk dihentikan: WAJIB ikut terlihat admin
			AvailabilityStatus: onboarding.ProductAvailable,
		}},
	}}

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/onboarding/products", nil)
	rr := httptest.NewRecorder()
	newAdminProductHandler(repo).AdminListProducts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("mau 200 tanpa X-Device-Id, dapat %d: %s", rr.Code, rr.Body.String())
	}
	if etag := rr.Header().Get("ETag"); etag != "" {
		t.Errorf("jalur admin tidak boleh mengirim ETag, dapat %q", etag)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "" {
		t.Errorf("jalur admin tidak boleh mengirim Cache-Control, dapat %q", cc)
	}
	if !strings.Contains(rr.Body.String(), `"is_active":false`) {
		t.Error("produk is_active = false harus ikut terlihat di jalur admin")
	}
}

// --- Liveness: lapisan validasi handler ---
//
// Dua gerbang pertama flow liveness ada di handler, bukan di domain, dan
// keduanya menjawab sebelum service apa pun disentuh. Service domain-nya punya
// test sendiri yang lengkap (biometric_service_test.go, liveness_test.go);
// yang belum teruji justru dua gerbang terluar ini — dan gerbang terluarlah
// yang paling sering dikira "sudah pasti benar".

// newLivenessHandler memberi handler dengan challenge service NON-nil tetapi
// berisi dependensi nil. Itu disengaja: seluruh kasus di bawah dijawab oleh
// validasi handler sebelum Issue dipanggil, jadi kalau suatu saat ada request
// yang lolos sampai service, test panik — bukan lulus diam-diam.
func newLivenessHandler() *OnboardingHandler {
	h := newValidationOnlyHandler()
	h.SetLivenessChallengeService(
		onboarding.NewLivenessChallengeService(onboarding.LivenessChallengeServiceConfig{}),
	)
	return h
}

// Tanpa penerbit tantangan tidak ada nonce, dan tanpa nonce tidak ada yang bisa
// diverifikasi nanti. Fail closed 503 — bukan panic, dan bukan 200 tanpa nonce.
func TestRequestLivenessChallenge_NilServiceIsUnavailable(t *testing.T) {
	rr := postJSON(t, newValidationOnlyHandler().RequestLivenessChallenge, `{
		"session_id": "onb_1",
		"device_key_id": "key-1",
		"device_public_key": "BASE64"
	}`)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("mau 503, dapat %d (%s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "LIVENESS_PROVIDER_UNAVAILABLE" {
		t.Errorf("mau LIVENESS_PROVIDER_UNAVAILABLE, dapat %s", code)
	}
}

// device_key_id dan device_public_key wajib: tantangan diikat ke kunci yang
// didaftarkan SAAT PENERBITAN, dan tanda tangan nanti diverifikasi terhadap
// kunci itu — bukan terhadap kunci di dalam submission. Tanpa kunci di sini,
// tidak ada apa pun untuk mengikat.
func TestRequestLivenessChallenge_RejectsIncompleteBodies(t *testing.T) {
	h := newLivenessHandler()

	cases := map[string]string{
		"tanpa session":    `{"device_key_id":"key-1","device_public_key":"BASE64"}`,
		"tanpa key id":     `{"session_id":"onb_1","device_public_key":"BASE64"}`,
		"tanpa public key": `{"session_id":"onb_1","device_key_id":"key-1"}`,
		"body kosong":      `{}`,
		"json rusak":       `{"session_id":`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rr := postJSON(t, h.RequestLivenessChallenge, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("mau 400, dapat %d (%s)", rr.Code, rr.Body.String())
			}
			if code := errorCode(t, rr); code != "VALIDATION_ERROR" {
				t.Errorf("mau VALIDATION_ERROR, dapat %s", code)
			}
		})
	}
}

// Algoritma yang tidak didukung ditolak di penerbitan, bukan nanti di
// verifikasi. Menerbitkan nonce yang akan ditandatangani dalam format yang
// tidak bisa diverifikasi server berarti nasabah membakar satu percobaan untuk
// kegagalan yang sudah pasti — dan kegagalan itu ikut menghitung ke cooldown.
func TestRequestLivenessChallenge_RejectsUnsupportedAlgorithm(t *testing.T) {
	rr := postJSON(t, newLivenessHandler().RequestLivenessChallenge, `{
		"session_id": "onb_1",
		"device_key_id": "key-1",
		"device_public_key": "BASE64",
		"signature_algorithm": "RS256"
	}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mau 422, dapat %d (%s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "LIVENESS_DEVICE_KEY_INVALID" {
		t.Errorf("mau LIVENESS_DEVICE_KEY_INVALID, dapat %s", code)
	}
}

// POST /biometric adalah multipart. Body JSON — kesalahan client yang paling
// mudah terjadi — harus jadi 400, bukan 500: ParseMultipartForm gagal duluan.
func TestProcessBiometric_RejectsNonMultipartBody(t *testing.T) {
	rr := postJSON(t, newValidationOnlyHandler().ProcessBiometric, `{"session_id":"onb_1"}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mau 400, dapat %d (%s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "VALIDATION_ERROR" {
		t.Errorf("mau VALIDATION_ERROR, dapat %s", code)
	}
}

// session_id kosong ditolak SEBELUM deviceOwnsSession. Urutan ini yang membuat
// handler ber-service nil aman di test ini, dan di produksi ia menghemat satu
// query untuk request yang sudah pasti tidak sah.
func TestProcessBiometric_RequiresSessionIDBeforeTouchingServices(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("challenge_id", "ch_1"); err != nil {
		t.Fatalf("tulis field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	newValidationOnlyHandler().ProcessBiometric(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mau 400, dapat %d (%s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "VALIDATION_ERROR" {
		t.Errorf("mau VALIDATION_ERROR, dapat %s", code)
	}
}
