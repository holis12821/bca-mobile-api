package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type TransferHandler struct {
	txnSvc *transaction.Service
}

func NewTransferHandler(txnSvc *transaction.Service) *TransferHandler {
	return &TransferHandler{txnSvc: txnSvc}
}

// Inquiry handles POST /v1/transfer/inquiry
func (h *TransferHandler) Inquiry(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	var req transaction.InquiryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.DestinationAccount == "" || req.TransferType == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.txnSvc.CreateInquiry(r.Context(), userID, req)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("inquiry failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// Execute handles POST /v1/transfer/execute
func (h *TransferHandler) Execute(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	// Idempotency key from header (§6)
	idemKey := r.Header.Get("X-Idempotency-Key")
	if idemKey == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	var req transaction.ExecuteTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	// Cross-check: if body also has idempotency_key and it differs from header → 400
	if req.IdempotencyKey != "" && req.IdempotencyKey != idemKey {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if req.InquiryID == "" || req.SourceAccountID == "" || req.VerificationToken == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, replayed, err := h.txnSvc.ExecuteTransfer(r.Context(), userID, req, idemKey)
	if err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("transfer execute failed",
				"request_id", chimiddleware.GetReqID(r.Context()),
				"error", err,
			)
		}
		response.Err(w, r, appErr)
		return
	}

	if replayed {
		w.Header().Set("X-Idempotent-Replayed", "true")
	}
	response.Success(w, r, http.StatusCreated, resp)
}