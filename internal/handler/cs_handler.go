package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// CSHandler melayani endpoint nasabah untuk petugas Customer Service.
//
// Seluruh endpoint di sini WAJIB terpasang di belakang middleware.InternalAPIKey DAN
// middleware.AgentAuth ber-scope CUSTOMER_PII. Tanpa yang kedua tidak ada identitas
// petugas, dan tanpa identitas petugas jejak aksesnya tidak menjawab apa pun.
type CSHandler struct {
	svc *cs.Service
}

func NewCSHandler(svc *cs.Service) *CSHandler {
	return &CSHandler{svc: svc}
}

// SearchCustomers handles GET /internal/v1/customers?q=
//
// Cocok PERSIS nomor rekening atau nomor HP. Tidak ada pencarian nama dan tidak ada
// pencocokan sebagian — lihat [cs.CustomerRepository] untuk alasannya.
func (h *CSHandler) SearchCustomers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := h.agent(w, r)
	if !ok {
		return
	}

	matches, err := h.svc.Search(r.Context(), query, agent, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "cs customer search failed", err)
		return
	}

	// Daftar kosong, bukan 404. 404 akan memberi tahu pemanggil bahwa kata kuncinya
	// bukan nomor terdaftar — jawaban yang bisa dipakai menyapu ruang nomor rekening.
	response.Success(w, r, http.StatusOK, map[string]any{
		"customers": matches,
		"count":     len(matches),
	})
}

// GetCustomerProfile handles GET /internal/v1/customers/{user_id}
//
// Setiap pemanggilan yang berhasil menulis CUSTOMER_VIEWED ke cs_access_logs, terikat
// ke petugas dan ke nasabahnya.
func (h *CSHandler) GetCustomerProfile(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "user_id")
	userID, err := uuid.Parse(raw)
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := h.agent(w, r)
	if !ok {
		return
	}

	profile, err := h.svc.GetProfile(r.Context(), userID, agent, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "cs customer profile failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, profile)
}

// agent membaca identitas petugas yang dipasang middleware.AgentAuth.
//
// Ketiadaannya adalah kesalahan perakitan rute, bukan kesalahan pemanggil — dan di jalur
// yang membuka PII, kesalahan perakitan tidak boleh berakhir dengan data terkirim tanpa
// pelaku yang tercatat.
func (h *CSHandler) agent(w http.ResponseWriter, r *http.Request) (string, bool) {
	employeeID, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		slog.Error("cs endpoint reached without an authenticated agent", "path", r.URL.Path)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return "", false
	}
	return employeeID, true
}

// handleErr mengikuti pola yang sama dengan OnboardingHandler: hanya kegagalan yang
// BUKAN apperr yang dicatat sebagai error, supaya 404 dan 400 yang wajar tidak memenuhi
// log dengan hal yang tidak perlu ditindaklanjuti siapa pun.
func (h *CSHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
