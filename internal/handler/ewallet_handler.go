package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/ewallet"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type EWalletHandler struct {
	svc *ewallet.Service
}

func NewEWalletHandler(svc *ewallet.Service) *EWalletHandler {
	return &EWalletHandler{svc: svc}
}

// ListProviders handles GET /v1/ewallet/providers
func (h *EWalletHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.svc.ListProviders(r.Context())
	if err != nil {
		h.handleError(w, r, err, "list ewallet providers")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"providers": providers,
	})
}

// Inquiry handles POST /v1/ewallet/inquiry
func (h *EWalletHandler) Inquiry(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	var req ewallet.InquiryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.ProviderID == "" || req.PhoneNumber == "" || req.Amount <= 0 || req.SourceAccountID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.CreateInquiry(r.Context(), userID, req)
	if err != nil {
		h.handleError(w, r, err, "ewallet inquiry")
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// TopUp handles POST /v1/ewallet/topup
func (h *EWalletHandler) TopUp(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	idemKey := r.Header.Get("X-Idempotency-Key")
	if idemKey == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	var req ewallet.TopUpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.IdempotencyKey != "" && req.IdempotencyKey != idemKey {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.InquiryID == "" || req.VerificationToken == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, replayed, err := h.svc.ExecuteTopUp(r.Context(), userID, req, idemKey)
	if err != nil {
		h.handleError(w, r, err, "ewallet topup")
		return
	}

	if replayed {
		w.Header().Set("X-Idempotent-Replayed", "true")
	}
	response.Success(w, r, http.StatusCreated, resp)
}

func (h *EWalletHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
