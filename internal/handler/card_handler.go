package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// catalogMaxAge adalah umur cache di sisi client, dalam detik.
// Sepadan dengan TTL cache server (15 menit); ETag tetap membuat klien yang
// lebih rajin bisa memvalidasi lebih sering tanpa mengunduh ulang isinya.
const catalogMaxAge = 900

// regionCodeFormat adalah bentuk yang boleh dipakai kode wilayah: huruf besar
// dan angka, maksimal 10 karakter (nilai nyatanya pendek, mis. `DKI`).
var regionCodeFormat = regexp.MustCompile(`^[A-Z0-9]{1,10}$`)

// normalizeRegionCode membakukan `region_code` dari query dan melaporkan apakah
// nilainya layak dipakai. Kosong berarti katalog nasional — itu sah.
//
// Sebelumnya nilai ini berjalan apa adanya dari query string ke DUA tempat:
// kunci cache Redis (`onb:cards:<produk>:<wilayah>:<versi>`) dan filter SQL.
// Akibatnya setiap nilai karangan mencetak entri cache baru berisi katalog utuh
// selama 15 menit, dan — karena nilainya selalu baru — setiap permintaan
// dijamin cache miss dengan satu query database di belakangnya. Endpoint
// katalog tidak memerlukan token, jadi tidak ada yang menahannya. Titik dua di
// dalam nilainya juga bisa menyelipkan pemisah tambahan ke dalam kunci Redis.
func normalizeRegionCode(raw string) (string, bool) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return "", true
	}
	if !regionCodeFormat.MatchString(code) {
		return "", false
	}
	return code, true
}

// CardCatalogReader adalah bagian dari onboarding.CardService yang dipakai
// handler ini. Bergantung pada perilaku, bukan tipe konkret — sama seperti
// DeviceHandler dengan PushTokenRegistrar — supaya handler bisa diuji tanpa
// menyeret Postgres dan Redis ke dalam test.
type CardCatalogReader interface {
	GetCatalog(ctx context.Context, productType onboarding.ProductType, regionCode string) (*onboarding.CardCatalog, error)
	SetCard(ctx context.Context, req onboarding.SetCardRequest, ipAddress, userAgent string) (*onboarding.SetCardResponse, error)
}

// CardHandler melayani katalog kartu Paspor dan pemilihan kartu pada sesi.
type CardHandler struct {
	cards CardCatalogReader
	// maintenance melaporkan produk yang sedang ditutup. Dipisah dari service
	// karena ini keputusan operasional, bukan isi katalog.
	maintenance func(productType string) bool
}

func NewCardHandler(cards CardCatalogReader, maintenance func(string) bool) *CardHandler {
	if maintenance == nil {
		maintenance = func(string) bool { return false }
	}
	return &CardHandler{cards: cards, maintenance: maintenance}
}

// SetCard menangani PUT /v1/onboarding/sessions/{session_id}/card (§8).
//
// Dipakai saat nasabah menekan Back dari S&K lalu ganti kartu, melanjutkan draf
// yang berhenti di CARD_SELECTION, atau mengubah kartu dari layar Ringkasan.
func (h *CardHandler) SetCard(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	var req onboarding.SetCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	// session_id diambil dari path, bukan dari body: body yang menentukan sesi
	// akan membuat satu permintaan bisa mengubah kartu sesi yang berbeda dari
	// yang dibatasi laju oleh path.
	req.SessionID = sessionID
	req.CardType = strings.TrimSpace(req.CardType)

	regionCode, ok := normalizeRegionCode(r.URL.Query().Get("region_code"))
	if !ok {
		response.ErrWithDetails(w, r, apperr.ValidationError, map[string]any{
			"invalid_field": "region_code",
		})
		return
	}
	req.RegionCode = regionCode

	if req.CardType == "" {
		response.Err(w, r, apperr.CardTypeInvalid)
		return
	}

	resp, err := h.cards.SetCard(r.Context(), req, extractIP(r), r.UserAgent())
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("set session card failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"session_id", sessionID,
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// GetCatalog menangani GET /v1/onboarding/products/{product_type}/cards
//
// Tanpa Authorization dan tanpa session_id: layar pilih kartu muncul sebelum
// sesi onboarding dibuat. X-Device-Id wajib — itulah satu-satunya identitas
// yang ada untuk membatasi laju di titik ini.
func (h *CardHandler) GetCatalog(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("X-Device-Id")) == "" {
		response.ErrWithDetails(w, r, apperr.ValidationError, map[string]any{
			"missing_header": "X-Device-Id",
		})
		return
	}

	productType := chi.URLParam(r, "product_type")

	// Urutan pemeriksaan mengikuti §4: enum dulu (404), baru maintenance (422).
	// Produk yang tidak ada bukan produk yang sedang tutup.
	if !onboarding.ProductType(productType).Valid() {
		response.Err(w, r, apperr.OnboardingProductUnknown)
		return
	}
	if h.maintenance(productType) {
		response.Err(w, r, apperr.OnboardingProductUnavailable)
		return
	}

	regionCode, ok := normalizeRegionCode(r.URL.Query().Get("region_code"))
	if !ok {
		response.ErrWithDetails(w, r, apperr.ValidationError, map[string]any{
			"invalid_field": "region_code",
		})
		return
	}

	catalog, err := h.cards.GetCatalog(r.Context(), onboarding.ProductType(productType), regionCode)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("get card catalog failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"product_type", productType,
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	// ETag harus mengidentifikasi REPRESENTASI, bukan sekadar versi katalog.
	//
	// Isi respons berbeda per produk DAN per wilayah. Ketika ETag hanya memuat
	// versi, klien yang sudah memegang katalog nasional lalu meminta katalog
	// wilayah menerima 304 dan terus menampilkan data nasional — kartu yang
	// habis di satu wilayah tidak pernah terlihat habis. Ketiga komponen kunci
	// cache ikut ke sini supaya keduanya tidak bisa melenceng.
	etag := catalogETag(catalog.ProductType, regionCode, catalog.CatalogVersion)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(catalogMaxAge))

	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		// 304 tidak boleh membawa body — termasuk envelope standar.
		w.WriteHeader(http.StatusNotModified)
		return
	}

	response.Success(w, r, http.StatusOK, catalog)
}

// catalogETag membentuk penanda unik per representasi.
// Sejajar dengan kunci cache onb:cards:{produk}:{wilayah}:{versi}.
func catalogETag(productType onboarding.ProductType, regionCode, version string) string {
	region := regionCode
	if region == "" {
		region = "nat"
	}
	return `"` + string(productType) + ":" + region + ":" + version + `"`
}

// matchesETag membandingkan header If-None-Match dengan ETag saat ini.
//
// Header itu boleh memuat beberapa nilai dipisah koma, boleh "*", dan boleh
// diawali penanda weak (W/). Perbandingan string mentah akan meleset pada
// ketiganya dan membuat klien mengunduh ulang katalog yang sebenarnya sudah
// mutakhir.
func matchesETag(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == etag {
			return true
		}
	}
	return false
}

// --- Admin katalog kartu (§3, Prompt 7) ---

// CardAdminWriter adalah bagian onboarding.CardAdminService yang dipakai
// handler ini. Antarmuka, bukan tipe konkret, dengan alasan yang sama seperti
// CardCatalogReader: handler harus bisa diuji tanpa Postgres.
type CardAdminWriter interface {
	ListCards(ctx context.Context) ([]onboarding.AdminCardRow, error)
	UpdateCard(ctx context.Context, cardType string, w onboarding.CardProductWrite,
		actor, ip string) (*onboarding.CardCatalogWriteResult, error)
	UpdateProductCard(ctx context.Context, productType onboarding.ProductType, cardType string,
		w onboarding.ProductCardWrite, actor, ip string) (*onboarding.CardCatalogWriteResult, error)
}

// CardAdminHandler melayani endpoint /internal/v1/cards*.
//
// Dilindungi middleware.InternalAPIKey di router. Model otorisasi di atas API
// key itu — siapa yang boleh menulis katalog — belum diputuskan (§17 butir 7),
// jadi aktor diambil dari header X-Admin-Actor dan hanya DICATAT, tidak
// dipercaya sebagai identitas. Saat model perannya ada, di sinilah tempatnya.
type CardAdminHandler struct {
	admin CardAdminWriter
}

func NewCardAdminHandler(admin CardAdminWriter) *CardAdminHandler {
	return &CardAdminHandler{admin: admin}
}

// ListCards menangani GET /internal/v1/cards
func (h *CardAdminHandler) ListCards(w http.ResponseWriter, r *http.Request) {
	rows, err := h.admin.ListCards(r.Context())
	if err != nil {
		h.fail(w, r, "list admin card catalog failed", err)
		return
	}
	// Daftar kosong dikirim sebagai array kosong, bukan null: klien admin yang
	// melakukan .length pada null akan meledak tanpa sebab yang jelas.
	if rows == nil {
		rows = []onboarding.AdminCardRow{}
	}
	response.Success(w, r, http.StatusOK, map[string]any{"cards": rows})
}

// UpdateCard menangani PUT /internal/v1/cards/{card_type}
func (h *CardAdminHandler) UpdateCard(w http.ResponseWriter, r *http.Request) {
	cardType := strings.TrimSpace(chi.URLParam(r, "card_type"))
	if cardType == "" {
		response.Err(w, r, apperr.CardTypeInvalid)
		return
	}

	var body onboarding.CardProductWrite
	if err := decodeAdminBody(r, &body); err != nil {
		response.Err(w, r, apperr.From(err))
		return
	}

	result, err := h.admin.UpdateCard(r.Context(), cardType, body, adminActor(r), extractIP(r))
	if err != nil {
		h.fail(w, r, "update card product failed", err)
		return
	}
	response.Success(w, r, http.StatusOK, result)
}

// UpdateProductCard menangani
// PUT /internal/v1/products/{product_type}/cards/{card_type}
func (h *CardAdminHandler) UpdateProductCard(w http.ResponseWriter, r *http.Request) {
	productType := onboarding.ProductType(strings.TrimSpace(chi.URLParam(r, "product_type")))
	cardType := strings.TrimSpace(chi.URLParam(r, "card_type"))

	if !productType.Valid() {
		response.Err(w, r, apperr.OnboardingProductUnknown)
		return
	}
	if cardType == "" {
		response.Err(w, r, apperr.CardTypeInvalid)
		return
	}

	var body onboarding.ProductCardWrite
	if err := decodeAdminBody(r, &body); err != nil {
		response.Err(w, r, apperr.From(err))
		return
	}

	result, err := h.admin.UpdateProductCard(r.Context(), productType, cardType, body,
		adminActor(r), extractIP(r))
	if err != nil {
		h.fail(w, r, "update product card failed", err)
		return
	}
	response.Success(w, r, http.StatusOK, result)
}

// decodeAdminBody membaca badan JSON dan MENOLAK field yang tidak dikenal.
//
// DisallowUnknownFields sengaja dipakai di sini dan tidak di endpoint nasabah:
// salah tulis nama field pada endpoint admin akan diam-diam menulis nilai
// default ke katalog yang tayang ke nasabah — "is_ative" yang terabaikan
// menonaktifkan kartu tanpa ada yang meminta.
func decodeAdminBody(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return apperr.Error{
			Status:  apperr.ValidationError.Status,
			Code:    apperr.ValidationError.Code,
			Message: "badan permintaan tidak bisa dibaca: " + err.Error(),
		}
	}
	return nil
}

// adminActor membaca penanda aktor dari header.
//
// Bukan identitas yang terverifikasi — X-Internal-API-Key adalah satu kunci
// bersama, dan §17 butir 7 belum menetapkan model peran di atasnya. Nilainya
// hanya masuk jejak audit supaya perubahan bisa ditanyakan ke orangnya.
func adminActor(r *http.Request) string {
	if actor := strings.TrimSpace(r.Header.Get("X-Admin-Actor")); actor != "" {
		return actor
	}
	return "internal-api-key"
}

func (h *CardAdminHandler) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
