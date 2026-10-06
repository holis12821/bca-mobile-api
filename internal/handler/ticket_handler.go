package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/ticket"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pagination"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// TicketHandler melayani tiket layanan untuk petugas CS.
//
// Seluruh endpoint di sini WAJIB di belakang middleware.InternalAPIKey DAN
// middleware.AgentAuth ber-scope TICKET: setiap tulis mencatat petugasnya sebagai
// pembuat atau penulis catatan, dan tanpa identitas itu tiketnya tidak bisa
// dipertanggungjawabkan ke siapa pun.
type TicketHandler struct {
	svc *ticket.Service
}

func NewTicketHandler(svc *ticket.Service) *TicketHandler {
	return &TicketHandler{svc: svc}
}

// CreateTicket handles POST /internal/v1/tickets
func (h *TicketHandler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	var req ticket.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := h.agent(w, r)
	if !ok {
		return
	}

	t, err := h.svc.Create(r.Context(), req, agent)
	if err != nil {
		h.handleErr(w, r, "create ticket failed", err)
		return
	}

	response.Success(w, r, http.StatusCreated, t)
}

// ListTickets handles GET /internal/v1/tickets
func (h *TicketHandler) ListTickets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var filter ticket.ListFilter

	if raw := q.Get("status"); raw != "" {
		st := ticket.Status(strings.ToUpper(raw))
		if !ticket.ValidStatus(st) {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.Status = st
	}

	if raw := q.Get("category"); raw != "" {
		c := ticket.Category(strings.ToUpper(raw))
		if !ticket.ValidCategory(c) {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.Category = c
	}

	// assigned_to=me adalah tampilan pertama di layar petugas, dan menuliskannya
	// sebagai employee_id sendiri menuntut client tahu identitasnya — padahal server
	// sudah tahu dari kredensialnya.
	if raw := q.Get("assigned_to"); raw != "" {
		if raw == "me" {
			agent, ok := h.agent(w, r)
			if !ok {
				return
			}
			filter.AssignedTo = agent
		} else {
			filter.AssignedTo = raw
		}
	}

	if raw := q.Get("user_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.UserID = &id
	}

	limit := 20
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 100 {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		limit = parsed
	}
	filter.Limit = limit

	if raw := q.Get("cursor"); raw != "" {
		decoded, err := pagination.DecodeHistoryCursor(raw)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		createdAt, err := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		id, err := uuid.Parse(decoded.ID)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.Cursor = &ticket.Cursor{CreatedAt: createdAt, ID: id}
	}

	items, hasMore, next, err := h.svc.List(r.Context(), filter)
	if err != nil {
		h.handleErr(w, r, "list tickets failed", err)
		return
	}

	nextCursor := ""
	if next != nil {
		nextCursor = pagination.HistoryCursor{
			CreatedAt: next.CreatedAt.Format(time.RFC3339Nano),
			ID:        next.ID.String(),
		}.Encode()
	}

	response.SuccessWithPagination(w, r, http.StatusOK, map[string]any{
		"tickets": items,
	}, response.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Limit:   limit,
	})
}

// GetTicket handles GET /internal/v1/tickets/{ticket_number}
func (h *TicketHandler) GetTicket(w http.ResponseWriter, r *http.Request) {
	number, ok := h.ticketNumber(w, r)
	if !ok {
		return
	}

	t, err := h.svc.Get(r.Context(), number)
	if err != nil {
		h.handleErr(w, r, "get ticket failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, t)
}

// UpdateTicket handles PATCH /internal/v1/tickets/{ticket_number}
func (h *TicketHandler) UpdateTicket(w http.ResponseWriter, r *http.Request) {
	number, ok := h.ticketNumber(w, r)
	if !ok {
		return
	}

	var req ticket.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	// Petugas tetap diverifikasi meski identitasnya tidak masuk ke baris apa pun:
	// endpoint ini mengubah tiket orang lain, dan AgentAuth yang gagal terpasang harus
	// terlihat di sini juga, bukan hanya di endpoint yang menulis nama.
	if _, ok := h.agent(w, r); !ok {
		return
	}

	t, err := h.svc.Update(r.Context(), number, req)
	if err != nil {
		h.handleErr(w, r, "update ticket failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, t)
}

// AddNote handles POST /internal/v1/tickets/{ticket_number}/notes
func (h *TicketHandler) AddNote(w http.ResponseWriter, r *http.Request) {
	number, ok := h.ticketNumber(w, r)
	if !ok {
		return
	}

	var req ticket.AddNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := h.agent(w, r)
	if !ok {
		return
	}

	note, err := h.svc.AddNote(r.Context(), number, req, agent)
	if err != nil {
		h.handleErr(w, r, "add ticket note failed", err)
		return
	}

	response.Success(w, r, http.StatusCreated, note)
}

// ticketNumber membaca nomor tiket dari path.
//
// Nomor, bukan UUID: TKT-20261005-000123 adalah yang tampil di layar petugas dan yang
// disebutkan nasabah lewat telepon. UUID internalnya tidak pernah keluar dari server,
// jadi client yang baru saja membuat tiket tidak punya cara menyebutnya — pola yang sama
// dengan session_id onboarding dan queue_id video call.
func (h *TicketHandler) ticketNumber(w http.ResponseWriter, r *http.Request) (string, bool) {
	number := strings.TrimSpace(chi.URLParam(r, "ticket_number"))
	if number == "" || len(number) > 32 {
		response.Err(w, r, apperr.ValidationError)
		return "", false
	}
	return number, true
}

// agent membaca identitas petugas yang dipasang middleware.AgentAuth.
//
// Ketiadaannya adalah kesalahan perakitan rute, bukan kesalahan pemanggil — dan rute
// yang salah rakit tidak boleh berakhir dengan tiket yang pembuatnya kosong.
func (h *TicketHandler) agent(w http.ResponseWriter, r *http.Request) (string, bool) {
	employeeID, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		slog.Error("ticket endpoint reached without an authenticated agent", "path", r.URL.Path)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return "", false
	}
	return employeeID, true
}

func (h *TicketHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
