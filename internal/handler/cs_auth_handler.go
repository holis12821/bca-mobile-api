package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// CSAuthHandler melayani identitas dan sesi petugas CS.
//
// Terpasang di belakang `middleware.InternalAPIKey`. `GET /auth/me` juga di belakang
// `AgentAuth` — ia menjawab TENTANG petugas yang memanggilnya, jadi tanpa petugas yang
// terbukti tidak ada yang bisa dijawab.
type CSAuthHandler struct {
	svc *cs.AgentSessionService
}

func NewCSAuthHandler(svc *cs.AgentSessionService) *CSAuthHandler {
	return &CSAuthHandler{svc: svc}
}

// agentMeResponse adalah jawaban `GET /internal/v1/auth/me`.
type agentMeResponse struct {
	EmployeeID string `json:"employee_id"`
	Name       string `json:"name"`

	// Scopes selalu berupa array, tidak pernah null: client yang menerima null akan
	// melakukan `scopes.includes(...)` pada nilai yang bukan array.
	Scopes []string `json:"scopes"`

	// Session nil kalau pemanggilnya memakai kunci API, bukan token sesi. Dibedakan
	// supaya aplikasi desktop tahu apakah ia perlu login — bukan menebaknya dari
	// keberadaan token di penyimpanannya sendiri.
	Session *agentMeSession `json:"session"`
}

// agentMeSession adalah giliran kerja yang sedang berjalan.
type agentMeSession struct {
	TerminalID string `json:"terminal_id"`
	Shift      string `json:"shift,omitempty"`

	StartedAt string `json:"started_at"`
	ExpiresAt string `json:"expires_at"`
}

// Me handles GET /internal/v1/auth/me
//
// Menjawab masalah yang sudah tercatat di `cs-desktop-api-integration` §1.2: aplikasi
// desktop TIDAK BISA menanyakan cakupannya, jadi ia menyembunyikan menu berdasarkan `403`
// yang pernah diterima. Artinya setiap menu harus dicoba sekali untuk diketahui, dan
// setiap percobaan memicu verifikasi Argon2id (64 MB × 4 thread).
//
// Satu permintaan ini menghapus seluruh tebak-tebakan itu.
func (h *CSAuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	employeeID, name, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		// Kesalahan perakitan rute, bukan kesalahan pemanggil: endpoint ini hanya boleh
		// terpasang di belakang AgentAuth.
		slog.Error("auth/me reached without an authenticated agent",
			"request_id", chimiddleware.GetReqID(r.Context()),
		)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return
	}

	scopes, _ := middleware.AgentScopesFromCtx(r.Context())
	if scopes == nil {
		scopes = []string{}
	}

	resp := agentMeResponse{
		EmployeeID: employeeID,
		Name:       name,
		Scopes:     scopes,
	}

	// Sesi hidup disertakan kalau ada. Dibaca dari tabel, bukan dari context: pemanggil
	// `auth/me` memakai kunci API (ia belum tentu punya token sesi), dan justru itu yang
	// membuatnya butuh tahu apakah gilirannya sudah terbuka di suatu loket.
	if h.svc != nil {
		if sess, err := h.svc.LiveSession(r.Context(), employeeID); err != nil {
			// Tidak menggagalkan: identitas dan cakupan sudah bisa dijawab, dan itu
			// bagian yang membuat endpoint ini ada.
			slog.Error("lookup live agent session failed",
				"employee_id", employeeID, "error", err)
		} else if sess != nil {
			resp.Session = &agentMeSession{
				TerminalID: sess.TerminalID,
				Shift:      sess.Shift,
				StartedAt:  sess.StartedAt.Format(time.RFC3339),
				ExpiresAt:  sess.ExpiresAt.Format(time.RFC3339),
			}
		}
	}

	response.Success(w, r, http.StatusOK, resp)
}

// Login handles POST /internal/v1/auth/login
//
// Di belakang InternalAPIKey saja — bukan AgentAuth: inilah tempat petugas MEMBUKTIKAN
// dirinya, jadi menuntut kredensial petugas lebih dulu akan membuatnya harus sudah masuk
// untuk bisa masuk.
func (h *CSAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req cs.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.Login(r.Context(), req, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "cs agent login failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// Logout handles POST /internal/v1/auth/logout
//
// Di belakang AgentSession: yang ditutup adalah sesi yang tokennya dikirim, bukan sesi
// yang disebut dalam body. Tanpa itu, token siapa pun bisa menutup giliran orang lain.
func (h *CSAuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	if err := h.svc.Logout(r.Context(), sess.SessionID, sess.EmployeeID, sess.TerminalID,
		extractIP(r), r.UserAgent()); err != nil {
		h.handleErr(w, r, "cs agent logout failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"ended":           true,
		"terminal_status": string(cs.TerminalOffline),
	})
}

// SetPassword handles POST /internal/v1/auth/password
//
// Di belakang AgentIdentity (kunci API petugas), BUKAN AgentSession: ini jalur
// penyetelan kata sandi PERTAMA, dan petugas yang belum punya kata sandi tidak bisa
// punya sesi.
func (h *CSAuthHandler) SetPassword(w http.ResponseWriter, r *http.Request) {
	employeeID, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		slog.Error("auth/password reached without an authenticated agent",
			"request_id", chimiddleware.GetReqID(r.Context()),
		)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return
	}

	var req cs.SetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.SetPassword(r.Context(), employeeID, req, extractIP(r), r.UserAgent()); err != nil {
		h.handleErr(w, r, "set cs agent password failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{"password_set": true})
}

func (h *CSAuthHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}

// csSessionFromRequest membaca identitas sesi yang dipasang middleware.AgentSession.
//
// Ketiadaannya adalah kesalahan perakitan rute. Dipisah sebagai fungsi paket supaya
// handler terminal memakai pembacaan yang sama — dua tempat yang meng-assert tipe
// sendiri akan berbeda pada perubahan pertama.
func csSessionFromRequest(w http.ResponseWriter, r *http.Request) (*cs.SessionContext, bool) {
	raw, ok := middleware.AgentSessionFromCtx(r.Context())
	if !ok {
		slog.Error("cs endpoint reached without an agent session", "path", r.URL.Path)
		response.Err(w, r, apperr.AgentSessionInvalid)
		return nil, false
	}
	sess, ok := raw.(*cs.SessionContext)
	if !ok || sess == nil {
		slog.Error("agent session context carries an unexpected type", "path", r.URL.Path)
		response.Err(w, r, apperr.AgentSessionInvalid)
		return nil, false
	}
	return sess, true
}
