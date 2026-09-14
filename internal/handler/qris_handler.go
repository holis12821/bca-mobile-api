package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/qris"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type QRISHandler struct {
	svc *qris.Service
}

func NewQRISHandler(svc *qris.Service) *QRISHandler {
	return &QRISHandler{svc: svc}
}

// Decode handles POST /v1/qris/decode
func (h *QRISHandler) Decode(w http.ResponseWriter, r *http.Request) {
	var req qris.DecodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.QRData == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	decoded, err := h.svc.Decode(r.Context(), req)
	if err != nil {
		h.handleError(w, r, err, "qris decode")
		return
	}

	response.Success(w, r, http.StatusOK, decoded)
}

// Pay handles POST /v1/qris/pay
func (h *QRISHandler) Pay(w http.ResponseWriter, r *http.Request) {
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

	var req qris.PayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.IdempotencyKey != "" && req.IdempotencyKey != idemKey {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.QRISID == "" || req.SourceAccountID == "" || req.VerificationToken == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, replayed, err := h.svc.Pay(r.Context(), userID, req, idemKey)
	if err != nil {
		h.handleError(w, r, err, "qris pay")
		return
	}

	if replayed {
		w.Header().Set("X-Idempotent-Replayed", "true")
	}
	response.Success(w, r, http.StatusCreated, resp)
}

func (h *QRISHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}