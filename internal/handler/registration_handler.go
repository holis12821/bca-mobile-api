package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type RegistrationHandler struct {
	svc        *registration.Service
	jwtManager *crypto.JWTManager
	uploadDir  string
}

func NewRegistrationHandler(svc *registration.Service, jwtManager *crypto.JWTManager, uploadDir string) *RegistrationHandler {
	return &RegistrationHandler{svc: svc, jwtManager: jwtManager, uploadDir: uploadDir}
}

// Initiate handles POST /v1/registration/initiate
func (h *RegistrationHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	var req registration.InitiateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.FullName == "" || req.NIK == "" || req.PhoneNumber == "" || req.Email == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.Initiate(r.Context(), req)
	if err != nil {
		h.handleError(w, r, err, "registration initiate")
		return
	}

	response.Success(w, r, http.StatusCreated, resp)
}

// VerifyOTP handles POST /v1/registration/verify-otp
func (h *RegistrationHandler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req registration.VerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.RegistrationID == "" || req.OTPCode == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.VerifyOTP(r.Context(), req)
	if err != nil {
		h.handleError(w, r, err, "registration verify-otp")
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// UploadDocument handles POST /v1/registration/upload-document
func (h *RegistrationHandler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	regID, err := h.extractRegistrationID(r)
	if err != nil {
		response.Err(w, r, apperr.RegistrationTokenInvalid)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, registration.MaxDocSize)
	// maxMemory, not the upload ceiling: see multipartMemory. MaxBytesReader
	// above is what actually caps the upload.
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	docType := r.FormValue("document_type")
	if !registration.ValidDocumentTypes[docType] {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	defer file.Close()

	// Validate content type from file contents (magic bytes), not header
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	contentType := http.DetectContentType(buf[:n])
	if contentType != "image/jpeg" && contentType != "image/png" {
		response.Err(w, r, apperr.RegistrationInvalidDoc)
		return
	}

	// Reset file reader.
	//
	// Seek yang gagal berarti isi file tidak bisa dibaca ulang dari awal, jadi
	// yang tersimpan nanti akan terpotong. Ditolak, bukan diabaikan: dokumen
	// KTP setengah tersimpan lebih buruk daripada unggahan yang gagal terang.
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
	}

	// Save file
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		if contentType == "image/jpeg" {
			ext = ".jpg"
		} else {
			ext = ".png"
		}
	}
	filename := uuid.New().String() + ext
	destPath := filepath.Join(h.uploadDir, regID, filename)

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		slog.Error("create upload dir failed", "error", err)
		response.Err(w, r, apperr.InternalError)
		return
	}

	dst, err := os.Create(destPath)
	if err != nil {
		slog.Error("create file failed", "error", err)
		response.Err(w, r, apperr.InternalError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		slog.Error("write file failed", "error", err)
		response.Err(w, r, apperr.InternalError)
		return
	}

	if err := h.svc.UploadDocument(r.Context(), regID, docType, destPath); err != nil {
		h.handleError(w, r, err, "registration upload-document")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{
		"message":       "Dokumen berhasil diupload.",
		"document_type": docType,
	})
}

// Complete handles POST /v1/registration/complete
func (h *RegistrationHandler) Complete(w http.ResponseWriter, r *http.Request) {
	regID, err := h.extractRegistrationID(r)
	if err != nil {
		response.Err(w, r, apperr.RegistrationTokenInvalid)
		return
	}

	var req registration.CompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.PINEncrypted == "" || req.DeviceID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.Complete(r.Context(), regID, req)
	if err != nil {
		h.handleError(w, r, err, "registration complete")
		return
	}

	response.Success(w, r, http.StatusCreated, resp)
}

// extractRegistrationID verifies the registration token from Authorization header.
func (h *RegistrationHandler) extractRegistrationID(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if len(auth) <= 7 || !strings.EqualFold(auth[:7], "bearer ") {
		return "", apperr.RegistrationTokenInvalid
	}
	tokenStr := auth[7:]

	claims, err := h.jwtManager.VerifyToken(tokenStr, crypto.TokenTypeRegistration)
	if err != nil {
		return "", apperr.RegistrationTokenInvalid
	}

	return claims.Subject, nil
}

func (h *RegistrationHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
