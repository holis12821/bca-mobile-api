package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// PushTokenRegistrar stores an FCM token against the caller's device.
type PushTokenRegistrar interface {
	RegisterPushToken(ctx context.Context, userID uuid.UUID, deviceID, pushToken string) error
}

// DeviceHandler serves the device-scoped endpoints.
type DeviceHandler struct {
	devices PushTokenRegistrar
}

func NewDeviceHandler(devices PushTokenRegistrar) *DeviceHandler {
	return &DeviceHandler{devices: devices}
}

type registerPushTokenRequest struct {
	PushToken string `json:"push_token"`
}

// RegisterPushToken handles POST /v1/account/device/push-token
//
// The device is taken from the access token's did claim, never from the body:
// a client must not be able to attach a push token to a device it is not
// currently authenticated on.
func (h *DeviceHandler) RegisterPushToken(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	deviceID := middleware.DeviceIDFromCtx(r.Context())
	if deviceID == "" {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	var req registerPushTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if req.PushToken == "" || len(req.PushToken) > 512 {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.devices.RegisterPushToken(r.Context(), userID, deviceID, req.PushToken); err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.InternalError.Code {
			slog.Error("register push token failed",
				"request_id", chimiddleware.GetReqID(r.Context()), "error", err)
		}
		response.Err(w, r, appErr)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{
		"message": "Token notifikasi berhasil didaftarkan.",
	})
}
