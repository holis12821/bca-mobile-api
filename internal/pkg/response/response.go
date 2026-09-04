package response

import (
	"encoding/json"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

type Envelope struct {
	Status     string      `json:"status"`
	Data       any         `json:"data,omitempty"`
	Error      *ErrorBody  `json:"error,omitempty"`
	Pagination *Pagination `json:"pagination,omitempty"`
	Meta       Meta        `json:"meta"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details"`
}

type Pagination struct {
	Cursor  string `json:"cursor"`
	HasMore bool   `json:"has_more"`
	Limit   int    `json:"limit"`
}

type Meta struct {
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
}

func Success(w http.ResponseWriter, r *http.Request, status int, data any) {
	write(w, r, status, Envelope{
		Status: "success",
		Data:   data,
		Meta:   meta(r),
	})
}

func SuccessWithPagination(w http.ResponseWriter, r *http.Request, status int, data any, p Pagination) {
	write(w, r, status, Envelope{
		Status:     "success",
		Data:       data,
		Pagination: &p,
		Meta:       meta(r),
	})
}

func Err(w http.ResponseWriter, r *http.Request, appErr apperr.Error) {
	write(w, r, appErr.Status, Envelope{
		Status: "error",
		Error: &ErrorBody{
			Code:    appErr.Code,
			Message: appErr.Message,
			Details: appErr.Details,
		},
		Meta: meta(r),
	})
}

func ErrWithDetails(w http.ResponseWriter, r *http.Request, appErr apperr.Error, details any) {
	e := appErr
	e.Details = details
	Err(w, r, e)
}

func meta(r *http.Request) Meta {
	return Meta{
		RequestID: chimiddleware.GetReqID(r.Context()),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func write(w http.ResponseWriter, _ *http.Request, status int, body Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}