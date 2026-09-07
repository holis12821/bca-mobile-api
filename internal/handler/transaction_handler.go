package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type TransactionHandler struct {
	svc *transaction.Service
}

func NewTransactionHandler(svc *transaction.Service) *TransactionHandler {
	return &TransactionHandler{svc: svc}
}

// ListMutations handles GET /v1/transactions/mutations
func (h *TransactionHandler) ListMutations(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	accountID := r.URL.Query().Get("account_id")
	if accountID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	acctID, err := uuid.Parse(accountID)
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	cursor := r.URL.Query().Get("cursor")

	// Parse period (WIB dates)
	var period *transaction.DateRange
	if p := r.URL.Query().Get("period"); p != "" {
		period = parsePeriod(p)
	} else {
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if from != "" && to != "" {
			fromDate, err1 := time.Parse("2006-01-02", from)
			toDate, err2 := time.Parse("2006-01-02", to)
			if err1 == nil && err2 == nil {
				period = &transaction.DateRange{From: fromDate, To: toDate}
			}
		}
	}

	items, hasMore, nextCursor, err := h.svc.ListMutations(r.Context(), userID, acctID, cursor, limit, period)
	if err != nil {
		h.handleError(w, r, err, "list mutations")
		return
	}

	response.SuccessWithPagination(w, r, http.StatusOK, map[string]any{
		"mutations": items,
	}, response.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Limit:   limit,
	})
}

// ListHistory handles GET /v1/transactions/history
func (h *TransactionHandler) ListHistory(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	cursor := r.URL.Query().Get("cursor")

	var txnType *string
	if t := r.URL.Query().Get("type"); t != "" {
		txnType = &t
	}

	items, hasMore, nextCursor, err := h.svc.ListHistory(r.Context(), userID, txnType, cursor, limit)
	if err != nil {
		h.handleError(w, r, err, "list history")
		return
	}

	response.SuccessWithPagination(w, r, http.StatusOK, map[string]any{
		"transactions": items,
	}, response.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Limit:   limit,
	})
}

// ListRecentTransfers handles GET /v1/transfer/recent
func (h *TransactionHandler) ListRecentTransfers(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	items, err := h.svc.ListRecentTransfers(r.Context(), userID)
	if err != nil {
		h.handleError(w, r, err, "list recent transfers")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"recent_transfers": items,
	})
}

func (h *TransactionHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}

// parsePeriod converts named periods (LAST_7_DAYS, etc.) to DateRange using WIB.
func parsePeriod(name string) *transaction.DateRange {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	switch name {
	case "LAST_7_DAYS":
		return &transaction.DateRange{From: today.AddDate(0, 0, -6), To: today}
	case "LAST_30_DAYS":
		return &transaction.DateRange{From: today.AddDate(0, 0, -29), To: today}
	case "LAST_90_DAYS":
		return &transaction.DateRange{From: today.AddDate(0, 0, -89), To: today}
	case "THIS_MONTH":
		firstDay := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return &transaction.DateRange{From: firstDay, To: today}
	default:
		return nil
	}
}