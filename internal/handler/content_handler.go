package handler

import (
	"context"
	"log/slog"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/domain/content"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// ContentReader is the part of content.Service this handler uses.
type ContentReader interface {
	HelpCenter(ctx context.Context) (*content.HelpCenterResponse, error)
	ContactCS(ctx context.Context) (*content.ContactCS, error)
}

// ContentHandler serves the two static pages under /v1/content.
//
// Both are unauthenticated on purpose: neither carries customer data, and a
// customer locked out of the app is exactly the one who needs the CS number.
// They sit in their own handler rather than in AccountHandler so that nothing
// here can reach for a user id that will not be there.
type ContentHandler struct {
	svc ContentReader
}

func NewContentHandler(svc ContentReader) *ContentHandler {
	return &ContentHandler{svc: svc}
}

// HelpCenter handles GET /v1/content/help-center.
func (h *ContentHandler) HelpCenter(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.HelpCenter(r.Context())
	if err != nil {
		h.handleError(w, r, err, "read help center")
		return
	}

	// Public, identical for everyone, and changed a few times a year. Five
	// minutes of client caching saves the round trip on every visit to the
	// screen without making an edit wait a day to appear.
	w.Header().Set("Cache-Control", "public, max-age=300")
	response.Success(w, r, http.StatusOK, resp)
}

// ContactCS handles GET /v1/content/contact-cs.
func (h *ContentHandler) ContactCS(w http.ResponseWriter, r *http.Request) {
	contact, err := h.svc.ContactCS(r.Context())
	if err != nil {
		h.handleError(w, r, err, "read contact cs")
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=300")
	response.Success(w, r, http.StatusOK, contact)
}

func (h *ContentHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
