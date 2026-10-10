package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
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

// LookupEmployee handles GET /internal/v1/hris/employees/{employee_id}
//
// Di belakang kunci API petugas: yang mencari NPP orang lain di direktori pegawai harus
// bisa disebut namanya. Tidak menuntut cakupan tertentu — mendaftarkan petugas adalah
// pekerjaan supervisor/teknisi, dan tidak satu pun dari empat cakupan yang ada
// menggambarkannya.
//
// Tiga jawaban yang dibedakan: 200 dengan `active: true`, 200 dengan `active: false`,
// dan 404 EMPLOYEE_NOT_FOUND. Yang kedua BUKAN error — layar pendaftaran perlu
// menampilkan "pegawai ini sudah tidak aktif", bukan "NPP tidak ditemukan".
//
// HRIS yang tidak bisa dihubungi menjawab 503 HRIS_UNAVAILABLE, bukan 404: 404 akan
// terbaca sebagai "NPP tidak terdaftar", dan petugas yang mendapatkannya saat HRIS mati
// akan menyimpulkan hal yang salah tentang rekannya.
func (h *CSAuthHandler) LookupEmployee(w http.ResponseWriter, r *http.Request) {
	emp, err := h.svc.LookupEmployee(r.Context(), chi.URLParam(r, "employee_id"))
	if err != nil {
		h.handleErr(w, r, "hris employee lookup failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, emp)
}

// RegisterAgent handles POST /internal/v1/agents
//
// Di belakang kunci API petugas DAN otorisasi supervisor di body: pemberian kewenangan
// adalah tindakan dual-control, dan yang perlu ditandatangani adalah pemberian itu —
// bukan kesiapan loket si pendaftar.
//
// Jawabannya memuat `api_key` SEKALI. Server menyimpan hash-nya; tidak ada endpoint yang
// bisa mengembalikannya lagi.
func (h *CSAuthHandler) RegisterAgent(w http.ResponseWriter, r *http.Request) {
	registrar, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || registrar == "" {
		slog.Error("agent registration reached without an authenticated registrar",
			"request_id", chimiddleware.GetReqID(r.Context()),
		)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return
	}

	var req cs.RegisterAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.RegisterAgent(r.Context(), req, registrar, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "register cs agent failed", err)
		return
	}

	response.Success(w, r, http.StatusCreated, resp)
}

// UpdateAgent handles PATCH /internal/v1/agents/{employee_id}
//
// Mengubah cakupan atau mencabut hak petugas yang sudah terdaftar. Sebelum ini keduanya
// hanya bisa lewat SQL langsung — jalur yang tidak menghasilkan jejak audit, tidak
// menuntut otorisasi supervisor, dan tidak bisa diserahkan ke siapa pun di luar pemegang
// kredensial database.
//
// Penjaganya AgentIdentity, sama dengan `POST /agents`: tidak satu pun dari enam cakupan
// menggambarkan "boleh memberi kewenangan", dan yang menandatangani pemberiannya adalah
// supervisor lewat dual-control di body. Yang menahan penyalahgunaannya adalah larangan
// mengubah diri sendiri, bukan sebuah cakupan.
//
// NPP di PATH, bukan di body: ia menyebut SASARAN perubahan, dan sasaran yang datang dari
// body membuat satu URL bisa mengubah petugas mana pun — termasuk saat permintaannya
// tercatat di log proxy sebagai `PATCH /agents/CS-1042` yang menyentuh orang lain.
func (h *CSAuthHandler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	updater, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || updater == "" {
		slog.Error("agent update reached without an authenticated updater",
			"request_id", chimiddleware.GetReqID(r.Context()),
		)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return
	}

	var req cs.UpdateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.UpdateAgent(r.Context(), chi.URLParam(r, "employee_id"),
		req, updater, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "update cs agent failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
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
