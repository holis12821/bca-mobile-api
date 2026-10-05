package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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

	// The Notifikasi tabs map to types. Both ?type=PROMO and
	// ?type=PROMO,INFO are accepted; the tab bar sends one, "Semua" sends none.
	types, terr := parseNotificationTypes(r.URL.Query().Get("type"))
	if terr != nil {
		response.Err(w, r, apperr.From(terr))
		return
	}

	resp, hasMore, nextCursor, err := h.svc.ListNotifications(r.Context(), userID, types, cursor, limit)
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

// parseNotificationTypes turns the `type` query parameter into a validated,
// de-duplicated list.
//
// An unknown value is a VALIDATION_ERROR, not a silently ignored filter: the
// same mistake on `period` used to answer 200 with the account's whole history,
// which looked like working software until someone counted the rows
// (docs/10-HANDOVER-BLOCKER-BACKEND.md butir 6).
func parseNotificationTypes(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	allowed := make(map[string]bool, len(account.NotificationTypes))
	for _, t := range account.NotificationTypes {
		allowed[t] = true
	}

	seen := make(map[string]bool)
	var types []string
	for _, part := range strings.Split(raw, ",") {
		t := strings.ToUpper(strings.TrimSpace(part))
		if t == "" {
			continue
		}
		if !allowed[t] {
			return nil, apperr.Error{
				Status:  apperr.ValidationError.Status,
				Code:    apperr.ValidationError.Code,
				Message: apperr.ValidationError.Message,
				Details: map[string]any{
					"invalid_field":  "type",
					"allowed_values": account.NotificationTypes,
				},
			}
		}
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	return types, nil
}
