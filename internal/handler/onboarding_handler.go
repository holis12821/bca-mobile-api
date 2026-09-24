package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
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
	pinKeys             *crypto.RSAKeyPair
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
		pinKeys:             pinKeys,
	}
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

	req.DeviceID = r.Header.Get("X-Device-ID")
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

	req.DeviceID = r.Header.Get("X-Device-ID")
	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.personalDataService.ResendOTP(r.Context(), req, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "resend otp failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// ProcessBiometric handles POST /v1/onboarding/biometric
func (h *OnboardingHandler) ProcessBiometric(w http.ResponseWriter, r *http.Request) {
	const maxBiometricUpload = 50 << 20 // 50 MB (face + frames)
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

	// Read face_photo
	faceFile, _, err := r.FormFile("face_photo")
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	defer faceFile.Close()
	faceData, err := io.ReadAll(faceFile)
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	// Read liveness_frames (multiple files)
	var livenessFrames [][]byte
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		for _, fh := range r.MultipartForm.File["liveness_frames"] {
			// Stop at one past the domain's ceiling: the request is already
			// doomed, and reading the rest only costs heap.
			if len(livenessFrames) >= maxLivenessFramesRead {
				break
			}
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				continue
			}
			livenessFrames = append(livenessFrames, data)
		}
	}

	// Parse liveness_meta JSON
	var meta onboarding.LivenessMeta
	if metaStr := r.FormValue("liveness_meta"); metaStr != "" {
		if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
	}

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.biometricService.ProcessBiometric(r.Context(), sessionID, faceData, livenessFrames, meta, clientIP, userAgent)
	if err != nil {
		h.handleErr(w, r, "process biometric failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

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

	clientIP := extractIP(r)
	userAgent := r.UserAgent()

	resp, err := h.videoCallService.SubmitResult(r.Context(), req, clientIP, userAgent)
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
		"key_id":         "pin-key-v1",
		"public_key_pem": string(pemBytes),
	})
}

// IssueAgentSignalingToken handles POST /v1/onboarding/video-call/agent-token
//
// Internal only. The CS backend calls it when an agent picks up a queued call;
// the returned URL carries a signed agent role, so the WebSocket side no longer
// has to believe a ?role= query parameter.
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

	resp, err := h.videoCallService.AgentSignalingURL(r.Context(), req.QueueID)
	if err != nil {
		h.handleErr(w, r, "issue agent signaling token failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
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
