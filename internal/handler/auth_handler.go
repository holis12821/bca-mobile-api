package handler

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type AuthHandler struct {
	authService *auth.Service
	txnService  *transaction.Service // for PIN verify → verification token issuance
}

func NewAuthHandler(authService *auth.Service) *AuthHandler {
	return &AuthHandler{authService: authService}
}

// SetTransactionService sets the transaction service for PIN verify flow.
func (h *AuthHandler) SetTransactionService(svc *transaction.Service) {
	h.txnService = svc
}

// LoginPIN handles POST /v1/auth/login/pin
func (h *AuthHandler) LoginPIN(w http.ResponseWriter, r *http.Request) {
	var req auth.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.DeviceID == "" || req.PINEncrypted == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	clientIP := extractIP(r)

	loginResp, err := h.authService.LoginByPIN(r.Context(), req, clientIP)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("login failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, loginResp)
}

// RefreshToken handles POST /v1/auth/token/refresh
func (h *AuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		// Also accept JSON body for mobile clients
		var req auth.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			response.Err(w, r, apperr.TokenInvalid)
			return
		}
		token = req.RefreshToken
	}

	clientIP := extractIP(r)

	resp, err := h.authService.RefreshToken(r.Context(), token, clientIP)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("refresh token failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// Logout handles POST /v1/auth/logout (requires auth middleware)
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(middleware.SessionIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}
	deviceID := middleware.DeviceIDFromCtx(r.Context())

	// Check if all_devices requested
	var req auth.LogoutRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.AllDevices {
		if err := h.authService.LogoutAll(r.Context(), userID, &sessionID); err != nil {
			slog.Error("logout all failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
			response.Err(w, r, apperr.From(err))
			return
		}
	} else {
		if err := h.authService.Logout(r.Context(), sessionID, userID, deviceID); err != nil {
			slog.Error("logout failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
			response.Err(w, r, apperr.From(err))
			return
		}
	}

	response.Success(w, r, http.StatusOK, map[string]string{"message": "Berhasil logout."})
}

// BiometricChallenge handles POST /v1/auth/biometric/challenge
func (h *AuthHandler) BiometricChallenge(w http.ResponseWriter, r *http.Request) {
	var req auth.BiometricChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.DeviceID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.authService.CreateChallenge(r.Context(), req)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("create biometric challenge failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// BiometricLogin handles POST /v1/auth/login/biometric
func (h *AuthHandler) BiometricLogin(w http.ResponseWriter, r *http.Request) {
	var req auth.BiometricLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.DeviceID == "" || req.KeyID == "" || req.ChallengeID == "" || req.Signature == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	clientIP := extractIP(r)

	resp, err := h.authService.LoginByBiometric(r.Context(), req, clientIP)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("biometric login failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// RegisterBiometric handles POST /v1/auth/biometric/register (requires auth middleware)
func (h *AuthHandler) RegisterBiometric(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}
	deviceID := middleware.DeviceIDFromCtx(r.Context())

	var req auth.BiometricRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.KeyID == "" || req.PublicKey == "" || req.BiometricType == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.authService.RegisterBiometricKey(r.Context(), userID, deviceID, req); err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("register biometric failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusCreated, map[string]string{"message": "Biometrik berhasil didaftarkan."})
}

// PINVerify handles POST /v1/auth/pin/verify (requires auth middleware)
// Verifies PIN and issues a purpose-bound verification token (TTL 120s, single-use).
func (h *AuthHandler) PINVerify(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}
	deviceID := middleware.DeviceIDFromCtx(r.Context())

	var req transaction.PINVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.PINEncrypted == "" || req.Purpose == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if !transaction.ValidPurposes[req.Purpose] {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	clientIP := extractIP(r)

	// Verify PIN (shares lockout counter with login)
	if err := h.authService.VerifyPIN(r.Context(), userID, deviceID, req.PINEncrypted, clientIP); err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("pin verify failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	// Issue verification token
	if h.txnService == nil {
		slog.Error("transaction service not set on auth handler")
		response.Err(w, r, apperr.InternalError)
		return
	}

	resp, err := h.txnService.CreateVerificationToken(r.Context(), userID, req.Purpose)
	if err != nil {
		slog.Error("create verification token failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
		response.Err(w, r, apperr.From(err))
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return auth[7:]
	}
	return ""
}

func extractIP(r *http.Request) string {
	// Check X-Forwarded-For first (behind reverse proxy)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (client IP)
		if ip, _, err := net.SplitHostPort(xff); err == nil {
			return ip
		}
		return xff
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}