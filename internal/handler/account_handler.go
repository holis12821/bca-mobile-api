package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type AccountHandler struct {
	svc *account.Service
}

func NewAccountHandler(svc *account.Service) *AccountHandler {
	return &AccountHandler{svc: svc}
}

// Profile handles GET /v1/account/profile
func (h *AccountHandler) Profile(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	profile, err := h.svc.GetProfile(r.Context(), userID)
	if err != nil {
		h.handleError(w, r, err, "get profile")
		return
	}

	data := map[string]any{
		"id":           profile.ID.String(),
		"full_name":    profile.FullName,
		"display_name": profile.DisplayName,
		"phone":        maskPhone(profile.Phone),
		"email":        maskEmail(profile.Email),
		"accounts":     toAccountBalanceList(profile.Accounts),
	}
	if profile.LastLoginAt != nil {
		data["last_login_at"] = profile.LastLoginAt.UTC().Format(time.RFC3339)
	}

	response.Success(w, r, http.StatusOK, data)
}

// Balance handles GET /v1/account/balance
func (h *AccountHandler) Balance(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	accounts, err := h.svc.GetBalances(r.Context(), userID)
	if err != nil {
		h.handleError(w, r, err, "get balances")
		return
	}

	balances := make([]account.AccountBalance, len(accounts))
	for i, a := range accounts {
		balances[i] = account.AccountBalance{
			AccountID:        a.ID.String(),
			AccountNumber:    a.AccountNumber,
			AccountType:      a.AccountType,
			AccountLabel:     a.AccountLabel,
			Currency:         a.Currency,
			Balance:          a.Balance.String(),
			AvailableBalance: a.AvailableBalance.String(),
			HoldAmount:       a.HoldAmount.String(),
			IsPrimary:        a.IsPrimary,
		}
	}

	response.Success(w, r, http.StatusOK, account.BalanceResponse{Accounts: balances})
}

// Dashboard handles GET /v1/account/dashboard
func (h *AccountHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	dash, err := h.svc.GetDashboard(r.Context(), userID)
	if err != nil {
		h.handleError(w, r, err, "get dashboard")
		return
	}

	response.Success(w, r, http.StatusOK, dash)
}

// UpdateTransactionLimit handles PUT /v1/account/transaction-limit
func (h *AccountHandler) UpdateTransactionLimit(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	var req account.UpdateLimitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}
	if len(req.Limits) == 0 {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.UpdateTransactionLimit(r.Context(), userID, req); err != nil {
		h.handleError(w, r, err, "update transaction limit")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{"message": "Limit transaksi berhasil diperbarui."})
}

// UpdateProfile handles PUT /v1/account/profile
func (h *AccountHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	var req account.UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.UpdateProfile(r.Context(), userID, req); err != nil {
		h.handleError(w, r, err, "update profile")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{"message": "Profil berhasil diperbarui"})
}

// UpdateSettings handles PUT /v1/account/settings
func (h *AccountHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.UserIDFromCtx(r.Context()))
	if err != nil {
		response.Err(w, r, apperr.TokenInvalid)
		return
	}

	var req account.UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.UpdateSettings(r.Context(), userID, req); err != nil {
		h.handleError(w, r, err, "update settings")
		return
	}

	response.Success(w, r, http.StatusOK, map[string]string{"message": "Pengaturan berhasil diperbarui"})
}

func (h *AccountHandler) handleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(operation+" failed",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}

func toAccountBalanceList(accounts []account.Account) []account.AccountBalance {
	result := make([]account.AccountBalance, len(accounts))
	for i, a := range accounts {
		result[i] = account.AccountBalance{
			AccountID:        a.ID.String(),
			AccountNumber:    a.AccountNumber,
			AccountType:      a.AccountType,
			AccountLabel:     a.AccountLabel,
			Currency:         a.Currency,
			Balance:          a.Balance.String(),
			AvailableBalance: a.AvailableBalance.String(),
			HoldAmount:       a.HoldAmount.String(),
			IsPrimary:        a.IsPrimary,
		}
	}
	return result
}

// maskPhone masks a phone number: 0812****5678
func maskPhone(phone string) string {
	if len(phone) <= 4 {
		return phone
	}
	visible := 4
	if len(phone) > 8 {
		visible = 4
	}
	prefix := phone[:4]
	suffix := phone[len(phone)-visible:]
	masked := strings.Repeat("*", len(phone)-4-visible)
	return prefix + masked + suffix
}

// maskEmail masks an email: n***s@gmail.com
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 2 {
		return local + "***" + domain
	}
	return string(local[0]) + strings.Repeat("*", len(local)-2) + string(local[len(local)-1]) + domain
}
