package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/card"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// AccountCardReader is the part of card.Service this handler uses. Depending on
// behaviour rather than the concrete type — the same choice CardHandler makes
// with CardCatalogReader — keeps the handler test free of Postgres.
type AccountCardReader interface {
	ListCards(ctx context.Context, userID uuid.UUID) (*card.CardListResponse, error)
	UpdateSettings(ctx context.Context, userID, cardID uuid.UUID, req card.UpdateSettingsRequest, ip string) (*card.CardDetailResponse, error)
	Block(ctx context.Context, userID, cardID uuid.UUID, req card.BlockRequest, ip string) (*card.CardDetailResponse, error)
	RequestReplacement(ctx context.Context, userID, cardID uuid.UUID, req card.ReplacementRequest, ip string) (*card.ReplacementResponse, bool, error)
}

// AccountCardHandler serves the cards a customer owns, under /v1/account/cards.
type AccountCardHandler struct {
	svc AccountCardReader
}

func NewAccountCardHandler(svc AccountCardReader) *AccountCardHandler {
	return &AccountCardHandler{svc: svc}
}

// List handles GET /v1/account/cards (requires auth middleware).
//
// The user id comes from the access token, never from the request: there is no
// path or query parameter that could point at somebody else's cards.
func (h *AccountCardHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	resp, err := h.svc.ListCards(r.Context(), userID)
	if err != nil {
		h.handleError(w, r, err, "list account cards")
		return
	}

	// 200 with an empty array for a customer without cards — not 404. The
	// screen distinguishes "no cards yet" from "something broke", and only the
	// former has an empty state.
	response.Success(w, r, http.StatusOK, resp)
}

func (h *AccountCardHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}

// cardRequestIDs pulls the authenticated user and the {card_id} path parameter,
// writing the error response itself. Returns ok=false when the caller must stop.
//
// The user id comes from the token and the card id from the URL — never the
// other way around. Every write endpoint below starts here so that none of them
// can forget one half of that pairing.
func (h *AccountCardHandler) cardRequestIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return uuid.Nil, uuid.Nil, false
	}

	cardID, err := uuid.Parse(chi.URLParam(r, "card_id"))
	if err != nil {
		// A card id that is not even a UUID is answered exactly like one that
		// does not exist. Replying VALIDATION_ERROR here would let a caller
		// tell "malformed" from "not yours" by the shape of the error.
		response.Err(w, r, apperr.CardNotFound)
		return uuid.Nil, uuid.Nil, false
	}
	return userID, cardID, true
}

// UpdateSettings handles PUT /v1/account/cards/{card_id}/settings.
func (h *AccountCardHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	userID, cardID, ok := h.cardRequestIDs(w, r)
	if !ok {
		return
	}

	var req card.UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.UpdateSettings(r.Context(), userID, cardID, req, middleware.ClientIP(r))
	if err != nil {
		h.handleError(w, r, err, "update card settings")
		return
	}
	response.Success(w, r, http.StatusOK, resp)
}

// Block handles POST /v1/account/cards/{card_id}/block.
func (h *AccountCardHandler) Block(w http.ResponseWriter, r *http.Request) {
	userID, cardID, ok := h.cardRequestIDs(w, r)
	if !ok {
		return
	}

	var req card.BlockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.Block(r.Context(), userID, cardID, req, middleware.ClientIP(r))
	if err != nil {
		h.handleError(w, r, err, "block card")
		return
	}
	response.Success(w, r, http.StatusOK, resp)
}

// RequestReplacement handles POST /v1/account/cards/{card_id}/replacement.
func (h *AccountCardHandler) RequestReplacement(w http.ResponseWriter, r *http.Request) {
	userID, cardID, ok := h.cardRequestIDs(w, r)
	if !ok {
		return
	}

	var req card.ReplacementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	// The key is read from the header, the same place /transfer/execute,
	// /ewallet/topup and /qris/pay read theirs. A body field would let a retry
	// that was rewritten by an interceptor lose it.
	req.IdempotencyKey = r.Header.Get("X-Idempotency-Key")

	resp, replayed, err := h.svc.RequestReplacement(r.Context(), userID, cardID, req, middleware.ClientIP(r))
	if err != nil {
		h.handleError(w, r, err, "request card replacement")
		return
	}

	if replayed {
		// Same header the transfer path sets, so a client can tell a fresh
		// request from a replay without comparing bodies.
		w.Header().Set("X-Idempotent-Replayed", "true")
		response.Success(w, r, http.StatusOK, resp)
		return
	}
	response.Success(w, r, http.StatusCreated, resp)
}
