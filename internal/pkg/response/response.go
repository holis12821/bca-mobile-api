package response

import (
	"encoding/json"
	"log/slog"
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

	// CatalogOutdated menandai client mengirim catalog_version katalog kartu
	// yang sudah bukan versi terkini (§7 docs/08-PILIH-KARTU-API-SPEC.md).
	//
	// Field ini ada di Meta yang dipakai SELURUH endpoint, bukan hanya kartu —
	// itu konsekuensi dari spec yang menempatkannya di meta, bukan di data.
	// `omitempty` menjaga agar respons lain tidak berubah sama sekali: kunci
	// ini hanya muncul ketika bernilai true.
	CatalogOutdated bool `json:"catalog_outdated,omitempty"`
}

func Success(w http.ResponseWriter, r *http.Request, status int, data any) {
	write(w, r, status, Envelope{
		Status: "success",
		Data:   data,
		Meta:   meta(r),
	})
}

// SuccessWithMeta menulis respons sukses dengan Meta yang sudah disesuaikan.
//
// mutate menerima Meta standar (request_id, timestamp) dan boleh menambahinya.
// Dipakai endpoint yang perlu menyelipkan penanda di meta — sejauh ini hanya
// catalog_outdated — tanpa memaksa setiap pemanggil Success menyusun Meta.
func SuccessWithMeta(w http.ResponseWriter, r *http.Request, status int, data any, mutate func(*Meta)) {
	m := meta(r)
	if mutate != nil {
		mutate(&m)
	}
	write(w, r, status, Envelope{
		Status: "success",
		Data:   data,
		Meta:   m,
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

	// Status sudah terkirim, jadi tidak ada lagi yang bisa disampaikan ke client
	// bila encoding gagal — lazimnya karena koneksi ditutup di tengah jalan.
	// Dicatat, bukan diabaikan: kegagalan yang BUKAN koneksi terputus (tipe yang
	// tidak bisa di-marshal pada payload baru) hanya akan tampak sebagai respons
	// terpotong, dan itu nyaris mustahil dilacak tanpa satu baris log.
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("tulis respons JSON gagal", "status", status, "error", err)
	}
}
