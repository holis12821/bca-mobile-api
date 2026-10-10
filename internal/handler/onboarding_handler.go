package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pagination"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

const maxKTPUploadSize = 10 << 20 // 10 MB

// multipartMemory is how much of a multipart upload is buffered in RAM before
// the rest spills to a temp file (removed by net/http when the request ends).
//
// It used to be the whole upload ceiling, so a biometric request held 50 MB in
// the form AND another copy in the []byte the handler read out of it — 100 MB of
// heap per concurrent upload, which is how a handful of simultaneous uploads
// turned into an OOM kill rather than a slow response.
const multipartMemory = 1 << 20 // 1 MB

// maxLivenessFramesRead caps how many liveness frames are read into memory.
//
// The domain rejects anything above five (minLivenessFrames/maxLivenessFrames in
// biometric_service.go) — but that check used to run only after this handler had
// already read every frame the client sent, up to the full 50 MB. One frame past
// the limit is enough for the domain to still answer with its own error.
const maxLivenessFramesRead = 6

// otpCodeFormat rejects anything that is not exactly six digits before the
// request reaches the service. A malformed code is a shape error, not a wrong
// guess, and must not spend one of the nasabah's five attempts.
var otpCodeFormat = regexp.MustCompile(`^[0-9]{6}$`)

type OnboardingHandler struct {
	sessionService      *onboarding.SessionService
	ocrService          *onboarding.OCRService
	personalDataService *onboarding.PersonalDataService
	biometricService    *onboarding.BiometricService
	videoCallService    *onboarding.VideoCallService
	credentialService   *onboarding.CredentialService
	submitService       *onboarding.SubmitService
	monitoringService   *onboarding.MonitoringService
	tncService          *onboarding.TNCService
	productService      *onboarding.ProductService

	// productAdminService melayani /internal/v1/onboarding/products. Dipasang lewat
	// SetProductAdminService, bukan lewat konstruktor: tambahan argumen ke-12 pada
	// konstruktor posisional ini akan memaksa setiap pemanggil dan setiap test berubah
	// untuk sebuah jalur yang hanya dipakai admin. Pola yang sama dengan
	// card.Service.SetVerificationTokenConsumer.
	//
	// Nil berarti jalur admin menjawab 503, bukan panic.
	productAdminService *onboarding.ProductAdminService

	// livenessChallengeService menerbitkan tantangan liveness. Dipasang lewat
	// SetLivenessChallengeService dengan alasan yang sama seperti
	// productAdminService: konstruktor posisional ini sudah punya sebelas argumen,
	// dan menambah satu lagi memaksa setiap test yang membangun handler berubah.
	//
	// Nil berarti endpoint tantangan menjawab 503, bukan panic.
	livenessChallengeService *onboarding.LivenessChallengeService

	pinKeys *crypto.RSAKeyPair
}

// SetLivenessChallengeService memasang penerbit tantangan liveness.
func (h *OnboardingHandler) SetLivenessChallengeService(s *onboarding.LivenessChallengeService) {
	h.livenessChallengeService = s
}

func NewOnboardingHandler(
	svc *onboarding.SessionService,
	ocrSvc *onboarding.OCRService,
	pdSvc *onboarding.PersonalDataService,
	bioSvc *onboarding.BiometricService,
	vcSvc *onboarding.VideoCallService,
	credSvc *onboarding.CredentialService,
	submitSvc *onboarding.SubmitService,
	monSvc *onboarding.MonitoringService,
	tncSvc *onboarding.TNCService,
	productSvc *onboarding.ProductService,
	pinKeys *crypto.RSAKeyPair,
) *OnboardingHandler {
	return &OnboardingHandler{
		sessionService:      svc,
		ocrService:          ocrSvc,
		personalDataService: pdSvc,
		videoCallService:    vcSvc,
		biometricService:    bioSvc,
		credentialService:   credSvc,
		submitService:       submitSvc,
		monitoringService:   monSvc,
		tncService:          tncSvc,
		productService:      productSvc,
		pinKeys:             pinKeys,
	}
}

// GetTNC menangani GET /v1/onboarding/tnc.
//
// Tanpa Authorization dan tanpa session_id: layar S&K adalah langkah PERTAMA
// buka rekening, dan sesi baru lahir setelah nasabah menekan setuju. Itu juga
// sebabnya ia tidak ikut grup ber-rate-limit per session_id.
//
// `?version=` opsional, untuk menampilkan kembali teks versi lama yang pernah
// disetujui. Tanpa parameter: versi yang sedang berlaku.
// GetProducts handles GET /v1/onboarding/products
//
// Layar PERTAMA buka rekening — tampil sebelum S&K, sebelum kartu, sebelum sesi lahir.
// Publik: tanpa Authorization dan tanpa session_id.
//
// Sebelum endpoint ini, isi layar hidup sebagai 16 entri strings.xml di dalam APK.
// Akibatnya setoran awal yang DILIHAT nasabah tidak punya arsip, produk tidak bisa
// ditutup tanpa rilis aplikasi, dan client memilih produk berdasarkan POSISI array —
// yang membuat nasabah membuka rekening yang bukan pilihannya begitu server mengurutkan
// atau menyembunyikan satu produk.
func (h *OnboardingHandler) GetProducts(w http.ResponseWriter, r *http.Request) {
	if h.productService == nil {
		// Pola GetTNC: service yang tidak dirakit adalah katalog yang tidak tersedia,
		// bukan panic. Client jatuh ke fallback strings.xml.
		response.Err(w, r, apperr.OnboardingCatalogUnavailable)
		return
	}

	// X-Device-Id DIWAJIBKAN di sini, berbeda dari GetTNC yang membolehkannya kosong.
	// Bedanya bukan selera: rate limit endpoint ini per device, dan tanpa header itu
	// seluruh nasabah di belakang satu NAT operator berbagi satu jatah. Teks S&K sama
	// untuk semua perangkat sehingga jatuh ke IP tidak merugikan siapa pun; katalog
	// dibaca sekali per pendaftaran oleh setiap perangkat.
	deviceID := strings.TrimSpace(r.Header.Get("X-Device-Id"))
	if deviceID == "" {
		missing := apperr.ValidationError
		missing.Details = map[string]any{"missing_header": "X-Device-Id"}
		response.Err(w, r, missing)
		return
	}

	catalog, err := h.productService.Catalog(r.Context())
	if err != nil {
		h.handleErr(w, r, "get onboarding product catalog failed", err)
		return
	}

	// ETag dari versi yang BENAR-BENAR dilayani, bukan dari apa pun yang datang di
	// request — pelajaran yang sama dari catalogETag dan GetTNC. ETag yang dibangun dari
	// nilai request akan membuat client terus menerima 304 berisi setoran awal lama.
	etag := `"products-` + catalog.CatalogVersion + `"`
	w.Header().Set("ETag", etag)

	// Lima menit, bukan 24 jam seperti TTL cache server: client yang menahan katalog
	// lebih lama hanya menampilkan setoran awal yang keliru, dan setoran awal adalah
	// komitmen 30 hari kalender yang disebut pasal 4 S&K.
	w.Header().Set("Cache-Control", "public, max-age=300")

	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		// 304 tidak boleh membawa body — termasuk envelope standar.
		w.WriteHeader(http.StatusNotModified)
		return
	}

	response.Success(w, r, http.StatusOK, catalog)
}

// SetProductAdminService memasang jalur tulis katalog produk. Lihat field-nya.
func (h *OnboardingHandler) SetProductAdminService(s *onboarding.ProductAdminService) {
	h.productAdminService = s
}

// AdminListProducts menangani GET /internal/v1/onboarding/products.
//
// Berbeda dari GetProducts yang melayani nasabah, dan bedanya bukan hanya penjaga:
//
//   - Tidak ada ETag dan tidak ada Cache-Control. Layar admin yang menerima 304 akan
//     menyunting salinan yang mungkin dibuat sebelum penulisan terakhir, lalu
//     mengirimkannya kembali — menulis ulang perubahan orang lain tanpa ada yang tahu.
//   - Tidak menuntut X-Device-Id. Jatah rate limit di /internal/v1 bukan per perangkat
//     nasabah, dan tidak ada aplikasi nasabah yang memanggil jalur ini.
//   - Produk is_active = FALSE IKUT dikembalikan. Admin yang menyalakannya kembali
//     harus bisa melihat barisnya.
func (h *OnboardingHandler) AdminListProducts(w http.ResponseWriter, r *http.Request) {
	if h.productAdminService == nil {
		response.Err(w, r, apperr.OnboardingCatalogUnavailable)
		return
	}

	catalog, err := h.productAdminService.ListProducts(r.Context())
	if err != nil {
		h.handleErr(w, r, "list admin product catalog failed", err)
		return
	}
	response.Success(w, r, http.StatusOK, catalog)
}

// AdminWriteProducts menangani PUT /internal/v1/onboarding/products.
//
// Badan permintaan memakai decodeAdminBody, yang MENOLAK field yang tidak dikenal.
// Alasannya sama dengan admin katalog kartu, dan di sini akibatnya lebih mahal: salah
// tulis nama field pada katalog yang tayang ke nasabah akan diam-diam menulis nilai
// default, dan "is_ative" yang terabaikan menonaktifkan produk tanpa ada yang meminta.
func (h *OnboardingHandler) AdminWriteProducts(w http.ResponseWriter, r *http.Request) {
	if h.productAdminService == nil {
		response.Err(w, r, apperr.OnboardingCatalogUnavailable)
		return
	}

	var body onboarding.ProductCatalogWriteRequest
	if err := decodeAdminBody(r, &body); err != nil {
		response.Err(w, r, apperr.From(err))
		return
	}

	result, err := h.productAdminService.WriteProducts(
		r.Context(), body, adminActor(r), extractIP(r))
	if err != nil {
		h.handleErr(w, r, "write admin product catalog failed", err)
		return
	}
	response.Success(w, r, http.StatusOK, result)
}

func (h *OnboardingHandler) GetTNC(w http.ResponseWriter, r *http.Request) {
	if h.tncService == nil {
		response.Err(w, r, apperr.TNCUnavailable)
		return
	}

	version := strings.TrimSpace(r.URL.Query().Get("version"))

	doc, err := h.tncService.ByVersion(r.Context(), version)
	if err != nil {
		h.handleErr(w, r, "get onboarding tnc failed", err)
		return
	}

	// ETag memuat versi yang BENAR-BENAR dilayani, bukan yang diminta. Ketika
	// `?version=` kosong, yang dilayani adalah versi aktif — dan begitu versi
	// aktif berganti, ETag-nya berganti sendiri. ETag dari nilai query akan
	// membuat client terus menerima 304 berisi teks lama persis pada hari
	// pergantian versi, lalu persetujuannya ditolak 409 tanpa ia pernah bisa
	// melihat teks baru. Pelajaran yang sama dari catalogETag.
	etag := `"tnc-` + doc.Version + `"`
	w.Header().Set("ETag", etag)

	// Lima menit, bukan sehari seperti TTL cache server. Teks hukum yang
	// diperbarui harus cepat terlihat: client yang menahannya lebih lama hanya
	// akan mengumpulkan penolakan 409 saat nasabah menekan setuju.
	w.Header().Set("Cache-Control", "public, max-age=300")

	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		// 304 tidak boleh membawa body — termasuk envelope standar.
		w.WriteHeader(http.StatusNotModified)
		return
	}

	response.Success(w, r, http.StatusOK, doc)
}

// CreateSession handles POST /v1/onboarding/sessions
func (h *OnboardingHandler) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req onboarding.CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.ProductType == "" || req.DeviceID == "" || req.AcceptedTNCVersion == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	// Wilayah dan versi aplikasi tidak datang dari body (§7). Keduanya sempat
	// tidak pernah diisi sama sekali: akibatnya kartu selalu divalidasi
	// terhadap katalog nasional — kartu yang habis di satu wilayah tetap bisa
	// dipilih di wilayah itu — dan fallback client lama tidak pernah aktif.
	regionCode, ok := normalizeRegionCode(r.URL.Query().Get("region_code"))
	if !ok {
		response.ErrWithDetails(w, r, apperr.ValidationError, map[string]any{
			"invalid_field": "region_code",
		})
		return
	}
	req.RegionCode = regionCode
	req.AppVersion = strings.TrimSpace(r.Header.Get("X-App-Version"))

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.sessionService.CreateSession(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "create onboarding session failed", err)
		return
	}

	// meta.catalog_outdated memberi tahu client bahwa biaya yang tampil di
	// layarnya berasal dari katalog yang sudah bergeser, supaya layar Ringkasan
	// menyegarkan diri sebelum nasabah menyetujui biaya yang salah.
	response.SuccessWithMeta(w, r, http.StatusCreated, resp, func(m *response.Meta) {
		m.CatalogOutdated = resp.CatalogOutdated
	})
}

// GetSession handles GET /v1/onboarding/sessions/{session_id}
func (h *OnboardingHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	resp, err := h.sessionService.GetSession(r.Context(), sessionID)
	if err != nil {
		h.handleErr(w, r, "get onboarding session failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// CancelSession handles DELETE /v1/onboarding/sessions/{session_id}
func (h *OnboardingHandler) CancelSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	if err := h.sessionService.CancelSession(r.Context(), sessionID, clientIP, userAgent); err != nil {
		h.handleErr(w, r, "cancel onboarding session failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]bool{"deleted": true})
}

// ProcessOCR handles POST /v1/onboarding/ocr
func (h *OnboardingHandler) ProcessOCR(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxKTPUploadSize)

	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	sessionID := r.FormValue("session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	file, _, err := r.FormFile("ktp_photo")
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	defer file.Close()

	imageData, err := io.ReadAll(file)
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	captureMeta := onboarding.DeviceCaptureMeta{
		FlashUsed:    r.FormValue("flash_used") == "true",
		AutoCaptured: r.FormValue("auto_captured") == "true",
		Resolution:   r.FormValue("resolution"),
		// Quality signals measured by the capture SDK. Absent fields stay nil
		// and are treated as "not reported" rather than as a passing score.
		SharpnessScore:  optionalFloat(r.FormValue("sharpness_score")),
		GlareScore:      optionalFloat(r.FormValue("glare_score")),
		CornersDetected: optionalInt(r.FormValue("corners_detected")),

		// What the device's own recognizer read from this photo. Used only when
		// no server-side OCR engine is configured, and validated either way.
		ClientOCRText: r.FormValue("client_ocr_text"),
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.ocrService.ProcessKTP(r.Context(), sessionID, imageData, captureMeta, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "process ocr failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// GetOCRResult handles GET /v1/onboarding/ocr/{session_id}
func (h *OnboardingHandler) GetOCRResult(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	result, err := h.ocrService.GetOCRResult(r.Context(), sessionID)
	if err != nil {
		h.handleErr(w, r, "get ocr result failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, result)
}

// SavePersonalData handles POST /v1/onboarding/personal-data
func (h *OnboardingHandler) SavePersonalData(w http.ResponseWriter, r *http.Request) {
	var req onboarding.SavePersonalDataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.SessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	if !h.deviceOwnsSession(w, r, req.SessionID) {
		return
	}

	resp, err := h.personalDataService.SavePersonalData(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "save personal data failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// VerifyOTP handles POST /v1/onboarding/verify-otp
func (h *OnboardingHandler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req onboarding.VerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.SessionID == "" || !otpCodeFormat.MatchString(req.OTPCode) {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	req.DeviceID = deviceIDHeader(r)
	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.personalDataService.VerifyOTP(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "verify otp failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// ResendOTP handles POST /v1/onboarding/resend-otp
func (h *OnboardingHandler) ResendOTP(w http.ResponseWriter, r *http.Request) {
	var req onboarding.ResendOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	req.DeviceID = deviceIDHeader(r)
	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.personalDataService.ResendOTP(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "resend otp failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// RequestLivenessChallenge handles POST /v1/onboarding/liveness/challenge
//
// The challenge — which movements, in which order — is minted here and nowhere
// else. The client renders what it is given and never generates or reorders it.
func (h *OnboardingHandler) RequestLivenessChallenge(w http.ResponseWriter, r *http.Request) {
	if h.livenessChallengeService == nil {
		// Fail closed: without an issuer there is no nonce, and without a nonce
		// there is nothing to verify later.
		response.Err(w, r, apperr.LivenessProviderUnavailable)
		return
	}

	var req struct {
		SessionID          string `json:"session_id"`
		DeviceKeyID        string `json:"device_key_id"`
		DevicePublicKey    string `json:"device_public_key"`
		SignatureAlgorithm string `json:"signature_algorithm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" || req.DevicePublicKey == "" || req.DeviceKeyID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SignatureAlgorithm != "" && req.SignatureAlgorithm != livenessSignatureAlgorithm {
		// Refusing early is kinder than minting a nonce the client will sign in a
		// format this server cannot verify.
		response.Err(w, r, apperr.LivenessDeviceKeyInvalid)
		return
	}

	if !h.deviceOwnsSession(w, r, req.SessionID) {
		return
	}

	challenge, err := h.livenessChallengeService.Issue(
		r.Context(),
		req.SessionID,
		deviceIDHeader(r),
		req.DeviceKeyID,
		req.DevicePublicKey,
		extractIP(r),
		r.UserAgent(),
	)
	if err != nil {
		h.handleErr(w, r, "issue liveness challenge failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, challenge)
}

// ProcessBiometric handles POST /v1/onboarding/biometric
//
// Decodes the evidence and hands it to the service. There is no part in this
// request that states a result: the previous contract had `liveness_meta` with
// `completed_actions`, which the client filled with a constant and the server
// believed. `step_meta` here only orders the frames and timestamps them; the
// server re-detects the pose in each frame itself.
func (h *OnboardingHandler) ProcessBiometric(w http.ResponseWriter, r *http.Request) {
	const maxBiometricUpload = 50 << 20 // 50 MB (neutral frame + step frames)
	r.Body = http.MaxBytesReader(w, r.Body, maxBiometricUpload)

	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	sessionID := r.FormValue("session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	neutralFrame, err := readUploadedFile(r, "neutral_frame")
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	var stepMeta []onboarding.LivenessStepMeta
	if raw := r.FormValue("step_meta"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &stepMeta); err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
	}

	var risk onboarding.DeviceRiskSignals
	if raw := r.FormValue("risk_signals"); raw != "" {
		// Malformed risk signals do not fail the request: they are an input to
		// review, not a decision, and an absent signal is handled the same way as
		// a false one.
		if err := json.Unmarshal([]byte(raw), &risk); err != nil {
			slog.Warn("liveness risk_signals undecodable", "session_id", sessionID)
		}
	}

	frames := readLivenessFrames(r)
	if len(frames) != len(stepMeta) {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	stepFrames := make([]onboarding.LivenessStepFrame, 0, len(frames))
	for i, meta := range stepMeta {
		if !meta.Action.Valid() {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		stepFrames = append(stepFrames, onboarding.LivenessStepFrame{
			Index:      meta.Index,
			Action:     meta.Action,
			CapturedAt: time.UnixMilli(meta.CapturedAtMillis).UTC(),
			JPEG:       frames[i],
		})
	}

	submission := &onboarding.LivenessSubmission{
		SessionID:          sessionID,
		ChallengeID:        r.FormValue("challenge_id"),
		Nonce:              r.FormValue("nonce"),
		DeviceID:           deviceIDHeader(r),
		DeviceKeyID:        r.FormValue("device_key_id"),
		DevicePublicKey:    r.FormValue("device_public_key"),
		SignatureAlgorithm: r.FormValue("signature_algorithm"),
		Signature:          r.FormValue("signature"),
		NeutralFrame:       neutralFrame,
		StepFrames:         stepFrames,
		IntegrityToken:     r.FormValue("integrity_token"),
		RiskSignals:        risk,
	}

	if submission.ChallengeID == "" || submission.Nonce == "" || submission.Signature == "" {
		// All three are required. An unsigned submission is not verifiable, and
		// accepting one "for now" is how the client-trusted path came back.
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.biometricService.ProcessBiometric(
		r.Context(), submission, extractIP(r), r.UserAgent(),
	)
	if err != nil {
		h.handleErr(w, r, "process biometric failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// readUploadedFile reads one named file part fully into memory.
func readUploadedFile(r *http.Request, field string) ([]byte, error) {
	file, _, err := r.FormFile(field)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// readLivenessFrames reads the step frames in the order the client sent them.
//
// Order matters: it is what pairs each frame with its step_meta entry, and the
// pairing is what the signature covers.
func readLivenessFrames(r *http.Request) [][]byte {
	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		return nil
	}
	var frames [][]byte
	for _, fh := range r.MultipartForm.File["liveness_frames"] {
		// Stop one past the ceiling: the request is already doomed, and reading
		// the rest only costs heap.
		if len(frames) >= maxLivenessFramesRead {
			break
		}
		f, err := fh.Open()
		if err != nil {
			return nil
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil
		}
		frames = append(frames, data)
	}
	return frames
}

// livenessSignatureAlgorithm is the only signature format this server verifies.
const livenessSignatureAlgorithm = "EC-P256"

// JoinVideoCallQueue handles POST /v1/onboarding/video-call/queue
func (h *OnboardingHandler) JoinVideoCallQueue(w http.ResponseWriter, r *http.Request) {
	var req onboarding.JoinQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	if !h.deviceOwnsSession(w, r, req.SessionID) {
		return
	}

	resp, err := h.videoCallService.JoinQueue(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "join video call queue failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// SubmitVideoCallResult handles POST /v1/onboarding/video-call/result
func (h *OnboardingHandler) SubmitVideoCallResult(w http.ResponseWriter, r *http.Request) {
	var req onboarding.SubmitVideoCallResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" || req.QueueID == "" || req.Result == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := authenticatedAgent(w, r)
	if !ok {
		return
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.videoCallService.SubmitResult(r.Context(), req, agent, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "submit video call result failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// Submit handles POST /v1/onboarding/submit
func (h *OnboardingHandler) Submit(w http.ResponseWriter, r *http.Request) {
	var req onboarding.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	idempotencyKey := r.Header.Get("X-Idempotency-Key")
	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	if !h.deviceOwnsSession(w, r, req.SessionID) {
		return
	}

	resp, err := h.submitService.Submit(r.Context(), req, idempotencyKey, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "submit onboarding failed", err)
		return
	}

	response.Success(w, r, http.StatusCreated, resp)
}

// SetCredentials handles POST /v1/onboarding/credentials
func (h *OnboardingHandler) SetCredentials(w http.ResponseWriter, r *http.Request) {
	var req onboarding.SetCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" || req.AccessCodeEncrypted == "" || req.PINEncrypted == "" || req.EncryptionKeyID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	if !h.deviceOwnsSession(w, r, req.SessionID) {
		return
	}

	resp, err := h.credentialService.SetCredentials(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "set credentials failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// GetEncryptionPublicKey handles GET /v1/onboarding/credentials/public-key
func (h *OnboardingHandler) GetEncryptionPublicKey(w http.ResponseWriter, r *http.Request) {
	if h.pinKeys == nil {
		response.Success(w, r, http.StatusOK, map[string]any{
			"algorithm":      "RSA-OAEP-SHA256",
			"key_id":         "dev-mode",
			"public_key_pem": "",
			"note":           "Dev mode: send plaintext credentials (no encryption needed).",
		})
		return
	}

	pemBytes, err := h.pinKeys.PublicKeyPEM()
	if err != nil {
		h.handleErr(w, r, "export public key failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"algorithm":      "RSA-OAEP-SHA256",
		"key_id":         h.pinKeys.ActiveKeyID(),
		"public_key_pem": string(pemBytes),
	})
}

// ListQueuedVideoCalls handles GET /v1/onboarding/video-call/queued
//
// Internal only. Ini pintu masuk sisi CS: tanpa daftar ini petugas tidak punya cara
// menemukan `queue_id` yang dibutuhkan /video-call/agent-token.
//
// Path-nya `/queued`, bukan GET pada `/video-call/queue`, karena dua alasan: artinya
// berbeda (melihat antrean vs bergabung ke antrean), dan jalur nasabah pada path itu hidup
// di grup rute tanpa X-Internal-API-Key — mendaftarkan dua metode pada satu pola di dua
// grup dengan middleware berbeda mengundang ambiguitas yang tidak perlu.
func (h *OnboardingHandler) ListQueuedVideoCalls(w http.ResponseWriter, r *http.Request) {
	resp, err := h.videoCallService.ListQueued(r.Context())
	if err != nil {
		h.handleErr(w, r, "list queued video calls failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// IssueAgentSignalingToken handles POST /v1/onboarding/video-call/agent-token
//
// Internal only. The CS backend calls it when an agent picks up a queued call;
// the returned URL carries a signed agent role, so the WebSocket side no longer
// has to believe a ?role= query parameter.
//
// Identitas petugas datang dari kredensial yang diautentikasi middleware.AgentAuth,
// bukan dari body. Permintaan ini tetap satu-satunya titik saat CS memberi tahu siapa
// yang mengambil panggilan — nasabah butuh namanya **sekarang**, karena `agent_assigned`
// dikirim dari sini — tapi sekarang nama itu tidak bisa dikarang pemanggil.
func (h *OnboardingHandler) IssueAgentSignalingToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueID string `json:"queue_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.QueueID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := authenticatedAgent(w, r)
	if !ok {
		return
	}

	resp, err := h.videoCallService.AgentSignalingURL(
		r.Context(), req.QueueID, agent, extractIP(r), r.UserAgent(),
	)
	if err != nil {
		h.handleErr(w, r, "issue agent signaling token failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// authenticatedAgent membaca identitas petugas yang dipasang middleware.AgentAuth.
//
// Ketiadaannya adalah kesalahan perakitan rute, bukan kesalahan pemanggil: handler ini
// hanya boleh terpasang di belakang AgentAuth. Dijawab 403 dan dicatat sebagai error
// supaya rute yang salah rakit terlihat di log, bukan diam-diam mengatribusikan verifikasi
// ke petugas kosong.
func authenticatedAgent(w http.ResponseWriter, r *http.Request) (onboarding.AgentInfo, bool) {
	employeeID, name, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		slog.Error("agent endpoint reached without an authenticated agent", "path", r.URL.Path)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return onboarding.AgentInfo{}, false
	}
	return onboarding.AgentInfo{EmployeeID: employeeID, Name: name}, true
}

// GetAuditTrail handles GET /v1/onboarding/sessions/{session_id}/audit
func (h *OnboardingHandler) GetAuditTrail(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.monitoringService.GetAuditTrail(r.Context(), sessionID)
	if err != nil {
		h.handleErr(w, r, "get audit trail failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// GetMonitoringStatus handles GET /v1/onboarding/monitoring
func (h *OnboardingHandler) GetMonitoringStatus(w http.ResponseWriter, r *http.Request) {
	status := h.monitoringService.EvaluateAlerts(r.Context())
	response.Success(w, r, http.StatusOK, status)
}

// optionalFloat parses a form value that may be absent. An unparseable value
// is treated the same as an absent one: the quality gate then has no signal,
// rather than a wrong one.
func optionalFloat(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

func optionalInt(raw string) *int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &v
}

// handleErr converts domain errors to HTTP responses, logging internal errors.
// deviceIDHeader reads the device identity the app sends on every request.
// http.Header.Get is case-insensitive, so X-Device-ID and X-Device-Id both land
// here — clients disagree on the casing and neither spelling is wrong.
func deviceIDHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Device-ID"))
}

// deviceOwnsSession guards a session-scoped endpoint against a session_id used
// from a device that did not create it, and writes the error response itself.
// Returns false when the caller must stop.
//
// Every endpoint that takes a session_id calls this. The alternative — each
// service checking for itself — is what left the binding on the two OTP
// endpoints only, so a leaked session_id could still be driven through OCR,
// biometrics, credentials, and submit from another phone.
func (h *OnboardingHandler) deviceOwnsSession(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	if err := h.sessionService.AssertDeviceOwnsSession(r.Context(), sessionID, deviceIDHeader(r)); err != nil {
		h.handleErr(w, r, "onboarding device binding rejected", err)
		return false
	}
	return true
}

func (h *OnboardingHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}

// --- Pemantauan sesi sisi CS ---

// ListSessionsForCS handles GET /internal/v1/onboarding/sessions
//
// Internal, ber-scope VIDEO_CALL. Tidak memuat PII: daftar ini terbuka sepanjang hari
// di layar petugas, dan data pribadi hanya relevan untuk sesi yang sedang ditangani.
func (h *OnboardingHandler) ListSessionsForCS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := onboarding.ListCSSessionsFilter{
		IncludeExpired: q.Get("include_expired") == "true",
	}

	if raw := q.Get("step"); raw != "" {
		step := onboarding.Step(raw)
		// Langkah tak dikenal ditolak, bukan dibiarkan menghasilkan daftar kosong:
		// nol baris karena salah ketik tidak bisa dibedakan dari nol baris karena
		// memang tidak ada yang di langkah itu.
		if !onboarding.ValidStep(step) {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.Step = step
	}

	if raw := q.Get("stalled_for_seconds"); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil || secs < 0 {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.StalledFor = time.Duration(secs) * time.Second
	}

	limit := 20
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 100 {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		limit = parsed
	}
	filter.Limit = limit

	if raw := q.Get("cursor"); raw != "" {
		decoded, err := pagination.DecodeHistoryCursor(raw)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		createdAt, err := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		id, err := uuid.Parse(decoded.ID)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.Cursor = &onboarding.CSSessionCursor{CreatedAt: createdAt, ID: id}
	}

	items, hasMore, next, err := h.monitoringService.ListSessions(r.Context(), filter)
	if err != nil {
		h.handleErr(w, r, "list sessions for cs failed", err)
		return
	}

	nextCursor := ""
	if next != nil {
		nextCursor = pagination.HistoryCursor{
			CreatedAt: next.CreatedAt.Format(time.RFC3339Nano),
			ID:        next.ID.String(),
		}.Encode()
	}

	response.SuccessWithPagination(w, r, http.StatusOK, map[string]any{
		"sessions": items,
	}, response.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Limit:   limit,
	})
}

// GetSessionDetailForCS handles GET /internal/v1/onboarding/sessions/{session_id}
//
// Internal, ber-scope CUSTOMER_PII — bukan VIDEO_CALL. Melayani panggilan menampilkan
// nasabah yang SEDANG bicara; endpoint ini menjangkau siapa pun yang pernah mendaftar,
// dan itu kewenangan yang berbeda ukurannya.
//
// Setiap pemanggilan yang berhasil menulis CS_SESSION_VIEWED ke jejak audit sesi.
func (h *OnboardingHandler) GetSessionDetailForCS(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := authenticatedAgent(w, r)
	if !ok {
		return
	}

	detail, err := h.monitoringService.GetSessionDetail(
		r.Context(), sessionID, "agent:"+agent.EmployeeID, extractIP(r), r.UserAgent(),
	)
	if err != nil {
		h.handleErr(w, r, "get session detail for cs failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, detail)
}

// --- Penjadwalan ulang video call (sisi nasabah) ---

// ScheduleVideoCall handles POST /v1/onboarding/video-call/schedule
//
// Endpoint NASABAH, bukan CS: penjaganya deviceOwnsSession, sama dengan
// /video-call/queue. Tanpa itu, session_id siapa pun yang tertebak bisa dijadwalkan
// oleh siapa pun.
//
// Melayani tombol "Jadwalkan Panggilan Nanti" yang sampai sekarang dimatikan di aplikasi
// Android karena tidak ada endpoint yang menerimanya.
func (h *OnboardingHandler) ScheduleVideoCall(w http.ResponseWriter, r *http.Request) {
	var req onboarding.ScheduleVideoCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.SessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, req.SessionID) {
		return
	}

	resp, err := h.videoCallService.ScheduleVideoCall(r.Context(), req, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "schedule video call failed", err)
		return
	}

	response.Success(w, r, http.StatusCreated, resp)
}

// GetVideoCallSchedule handles GET /v1/onboarding/video-call/schedule?session_id=
//
// Mengembalikan 200 dengan schedule null kalau tidak ada jadwal aktif, bukan 404: "belum
// menjadwalkan" adalah keadaan normal bagi hampir semua sesi, dan 404 memaksa client
// memperlakukan keadaan normal itu sebagai kegagalan.
func (h *OnboardingHandler) GetVideoCallSchedule(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	sch, err := h.videoCallService.GetVideoCallSchedule(r.Context(), sessionID)
	if err != nil {
		h.handleErr(w, r, "get video call schedule failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"schedule":        sch,
		"operating_hours": onboarding.DefaultOperatingHours(),
	})
}

// CancelVideoCallSchedule handles DELETE /v1/onboarding/video-call/schedule?session_id=
func (h *OnboardingHandler) CancelVideoCallSchedule(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if !h.deviceOwnsSession(w, r, sessionID) {
		return
	}

	if err := h.videoCallService.CancelVideoCallSchedule(
		r.Context(), sessionID, extractIP(r), r.UserAgent(),
	); err != nil {
		h.handleErr(w, r, "cancel video call schedule failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{"cancelled": true})
}
