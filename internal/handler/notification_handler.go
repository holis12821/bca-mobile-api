package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type NotificationHandler struct {
	svc *account.Service
}

func NewNotificationHandler(svc *account.Service) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// ListNotifications handles GET /v1/notifications
func (h *NotificationHandler) ListNotifications(w http.ResponseWriter, r *http.Request) {
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

	var cursor *uuid.UUID
	if c := r.URL.Query().Get("cursor"); c != "" {
		parsed, err := uuid.Parse(c)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		cursor = &parsed
	}

	resp, hasMore, nextCursor, err := h.svc.ListNotifications(r.Context(), userID, cursor, limit)
	if err != nil {
		h.handleError(w, r, err, "list notifications")
		return
	}

	response.SuccessWithPagination(w, r, http.StatusOK, resp, response.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Limit:   limit,
	})
}

// MarkNotificationRead handles PUT /v1/notifications/{id}/read
func (h *NotificationHandler) MarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	notifID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.MarkNotificationRead(r.Context(), userID, notifID); err != nil {
		h.handleError(w, r, err, "mark notification read")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{"message": "Notifikasi ditandai sudah dibaca."})
}

// MarkAllNotificationsRead handles PUT /v1/notifications/read-all
func (h *NotificationHandler) MarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	if err := h.svc.MarkAllNotificationsRead(r.Context(), userID); err != nil {
		h.handleError(w, r, err, "mark all notifications read")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{"message": "Semua notifikasi ditandai sudah dibaca."})
}

func (h *NotificationHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}