package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pdf"
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

	// Parse period (WIB dates).
	//
	// The custom range is accepted under both spellings: the spec documents
	// start_date/end_date, the handler was written against from/to. A client
	// that picked the documented pair used to get its dates silently dropped
	// and the account's whole history back.
	period, perr := resolvePeriod(r.URL.Query().Get("period"),
		firstNonEmpty(r.URL.Query().Get("from"), r.URL.Query().Get("start_date")),
		firstNonEmpty(r.URL.Query().Get("to"), r.URL.Query().Get("end_date")))
	if perr != nil {
		response.Err(w, r, apperr.From(perr))
		return
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

	// Riwayat takes the same period vocabulary as Mutasi. Two screens that both
	// show "7 hari terakhir" but disagree on how to ask for it is how a client
	// ends up sending from/to here and getting the whole history back.
	period, perr := resolvePeriod(r.URL.Query().Get("period"),
		firstNonEmpty(r.URL.Query().Get("from"), r.URL.Query().Get("start_date")),
		firstNonEmpty(r.URL.Query().Get("to"), r.URL.Query().Get("end_date")))
	if perr != nil {
		response.Err(w, r, apperr.From(perr))
		return
	}

	items, hasMore, nextCursor, err := h.svc.ListHistory(r.Context(), userID, txnType, period, cursor, limit)
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

// GetReceipt handles GET /v1/transactions/{transaction_id}/receipt
func (h *TransactionHandler) GetReceipt(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	txnID, err := uuid.Parse(chi.URLParam(r, "transaction_id"))
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	receipt, err := h.svc.GetReceipt(r.Context(), userID, txnID)
	if err != nil {
		h.handleError(w, r, err, "get receipt")
		return
	}

	response.Success(w, r, http.StatusOK, receipt)
}

// GetReceiptPDF handles GET /v1/transactions/{transaction_id}/receipt/pdf
//
// Same ownership rules as the JSON receipt — it goes through the same service
// call, so a transaction that is not the caller's is a 404 here too.
func (h *TransactionHandler) GetReceiptPDF(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	txnID, err := uuid.Parse(chi.URLParam(r, "transaction_id"))
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	receipt, err := h.svc.GetReceipt(r.Context(), userID, txnID)
	if err != nil {
		h.handleError(w, r, err, "get receipt pdf")
		return
	}

	body := renderReceiptPDF(receipt)

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=\"bukti-transaksi-%s.pdf\"", receipt.ReferenceNumber))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		slog.Error("write receipt pdf failed",
			"request_id", chimiddleware.GetReqID(r.Context()), "error", err)
	}
}

// renderReceiptPDF lays the receipt out as a one-page document.
func renderReceiptPDF(receipt *transaction.ReceiptResponse) []byte {
	doc := pdf.New("Bukti Transaksi")

	doc.Heading("BCA mobile", 18)
	doc.Heading("Bukti Transaksi", 13)
	doc.Rule()

	doc.Field("Status", receipt.Status)
	doc.Field("Nomor Referensi", receipt.ReferenceNumber)
	doc.Field("Tanggal", receipt.Date+" "+receipt.Time)
	doc.Rule()

	doc.Field("Rekening Sumber", receipt.SourceAccount+"  "+receipt.SourceName)
	if receipt.DestinationAccount != "" {
		dest := receipt.DestinationAccount
		if receipt.DestinationName != "" {
			dest += "  " + receipt.DestinationName
		}
		doc.Field("Rekening Tujuan", dest)
	}
	if receipt.DestinationBank != "" {
		doc.Field("Bank Tujuan", receipt.DestinationBank)
	}
	if receipt.ProviderName != "" {
		doc.Field("Provider", receipt.ProviderName)
	}
	doc.Rule()

	doc.Field("Nominal", receipt.Currency+" "+receipt.Amount)
	doc.Field("Biaya Admin", receipt.Currency+" "+receipt.AdminFee)
	doc.Field("Total", receipt.Currency+" "+receipt.Total)
	if receipt.Notes != "" {
		doc.Field("Berita", receipt.Notes)
	}

	doc.Rule()
	doc.Text("Dokumen ini dihasilkan otomatis dan sah tanpa tanda tangan.")

	return doc.Render()
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

// firstNonEmpty returns the first value that is not blank after trimming.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// PeriodValues is the closed list of `period` values GET /transactions/mutations
// accepts. The Mutasi screen offers four ranges; the spec used to name only two,
// which is what butir 6 of docs/10-HANDOVER-BLOCKER-BACKEND.md was blocked on.
// Anything outside this list is a VALIDATION_ERROR — see resolvePeriod.
var PeriodValues = []string{
	"LAST_7_DAYS", "LAST_30_DAYS", "LAST_90_DAYS",
	"THIS_MONTH", "LAST_MONTH", "CUSTOM",
}

// resolvePeriod turns `period` (plus from/to for CUSTOM) into a WIB date range.
//
// An unknown value is REJECTED rather than ignored. It used to fall through to a
// nil range, which quietly means "no date filter": a typo in `period` answered
// 200 with the account's entire history, and the screen showed it as though it
// were the last seven days.
func resolvePeriod(name, from, to string) (*transaction.DateRange, error) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	// WIB dates are computed here, never with CURRENT_DATE: the day boundary
	// that matters is midnight in Jakarta, not in the database's timezone.
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "":
		// No period named. from+to alone still filter — the app sends them that
		// way for a custom range it built before the enum existed.
		return customRange(from, to, true)
	case "LAST_7_DAYS":
		return &transaction.DateRange{From: today.AddDate(0, 0, -6), To: today}, nil
	case "LAST_30_DAYS":
		return &transaction.DateRange{From: today.AddDate(0, 0, -29), To: today}, nil
	case "LAST_90_DAYS":
		return &transaction.DateRange{From: today.AddDate(0, 0, -89), To: today}, nil
	case "THIS_MONTH":
		firstDay := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return &transaction.DateRange{From: firstDay, To: today}, nil
	case "LAST_MONTH":
		firstThis := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		firstLast := firstThis.AddDate(0, -1, 0)
		// Inclusive end: the last day of the previous month, not the 1st of this
		// one — the range is compared as dates, so the boundary day would
		// otherwise leak into "bulan lalu".
		lastLast := firstThis.AddDate(0, 0, -1)
		return &transaction.DateRange{From: firstLast, To: lastLast}, nil
	case "CUSTOM":
		return customRange(from, to, false)
	default:
		return nil, apperr.Error{
			Status:  apperr.ValidationError.Status,
			Code:    apperr.ValidationError.Code,
			Message: apperr.ValidationError.Message,
			Details: map[string]any{
				"invalid_field":  "period",
				"allowed_values": PeriodValues,
			},
		}
	}
}

// customRange parses from/to as YYYY-MM-DD in WIB. optional reports whether an
// absent pair is acceptable — it is when no period was named at all, and it is
// not when the caller explicitly asked for CUSTOM.
func customRange(from, to string, optional bool) (*transaction.DateRange, error) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)

	if from == "" || to == "" {
		if optional {
			return nil, nil
		}
		return nil, apperr.Error{
			Status:  apperr.ValidationError.Status,
			Code:    apperr.ValidationError.Code,
			Message: apperr.ValidationError.Message,
			Details: map[string]any{
				"invalid_field": "from,to",
				"reason":        "period=CUSTOM membutuhkan from dan to (YYYY-MM-DD).",
			},
		}
	}

	fromDate, err1 := time.ParseInLocation("2006-01-02", from, loc)
	toDate, err2 := time.ParseInLocation("2006-01-02", to, loc)
	if err1 != nil || err2 != nil {
		return nil, apperr.Error{
			Status:  apperr.ValidationError.Status,
			Code:    apperr.ValidationError.Code,
			Message: apperr.ValidationError.Message,
			Details: map[string]any{
				"invalid_field": "from,to",
				"reason":        "Format tanggal harus YYYY-MM-DD.",
			},
		}
	}
	if toDate.Before(fromDate) {
		return nil, apperr.Error{
			Status:  apperr.ValidationError.Status,
			Code:    apperr.ValidationError.Code,
			Message: apperr.ValidationError.Message,
			Details: map[string]any{
				"invalid_field": "from,to",
				"reason":        "Tanggal akhir tidak boleh lebih awal dari tanggal awal.",
			},
		}
	}

	return &transaction.DateRange{From: fromDate, To: toDate}, nil
}
