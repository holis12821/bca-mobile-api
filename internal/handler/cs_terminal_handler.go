package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// CSTerminalHandler melayani pendaftaran terminal, tiga gerbang kesiapan, dan aktivasi.
type CSTerminalHandler struct {
	svc *cs.TerminalService
}

func NewCSTerminalHandler(svc *cs.TerminalService) *CSTerminalHandler {
	return &CSTerminalHandler{svc: svc}
}

// RegisterTerminal handles POST /internal/v1/terminals
//
// Di belakang AgentIdentity: pendaftar harus bisa disebut namanya di jejak audit. Tidak
// menuntut cakupan tertentu — memasang terminal adalah pekerjaan teknisi, dan cakupan
// yang ada sekarang (VIDEO_CALL, CUSTOMER_PII, CARD_ADMIN, TICKET) tidak ada yang
// menggambarkannya. Menambah cakupan baru untuk ini tanpa ada yang memberikannya hanya
// akan membuat endpoint yang tidak bisa dipakai siapa pun.
func (h *CSTerminalHandler) RegisterTerminal(w http.ResponseWriter, r *http.Request) {
	employeeID, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		h.denyUnassembled(w, r)
		return
	}

	var req cs.RegisterTerminalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	t, err := h.svc.Register(r.Context(), req, employeeID, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "register cs terminal failed", err)
		return
	}

	response.Success(w, r, http.StatusCreated, t)
}

// GetTerminal handles GET /internal/v1/terminals/{terminal_id}
//
// Menjawab layar masuk: "Terminal Secure ID #WKS-SMG-0842 / ✓ Registered Terminal".
// Di belakang InternalAPIKey saja — klien perlu tahu terminalnya terdaftar SEBELUM ada
// petugas yang masuk.
func (h *CSTerminalHandler) GetTerminal(w http.ResponseWriter, r *http.Request) {
	terminalID := chi.URLParam(r, "terminal_id")
	if terminalID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	t, err := h.svc.Get(r.Context(), terminalID)
	if err != nil {
		h.handleErr(w, r, "get cs terminal failed", err)
		return
	}

	// Terminal yang terdaftar TIDAK menyertakan siapa yang sedang memegangnya di jalur
	// pra-login ini: nama petugas giliran sebelumnya bukan hal yang perlu dibaca layar
	// masuk, dan ia tetap terbaca lewat `auth/me` oleh pemegangnya sendiri.
	response.Success(w, r, http.StatusOK, map[string]any{
		"terminal_id": t.TerminalID,
		"workstation": t.Workstation,
		"location":    t.Location,
		"status":      t.Status,
		"registered":  true,
	})
}

// Readiness handles GET /internal/v1/terminals/readiness
//
// Tanpa `{terminal_id}` di path: terminalnya ditentukan SESI, bukan parameter. Petugas
// tidak bisa menanyakan kesiapan loket orang lain, dan tidak bisa menyatakan kesiapan
// atas loket yang bukan tempat ia masuk.
func (h *CSTerminalHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	state, err := h.svc.Readiness(r.Context(), sess)
	if err != nil {
		h.handleErr(w, r, "get terminal readiness failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, state)
}

// ListSupervisors handles GET /internal/v1/supervisors?location=
func (h *CSTerminalHandler) ListSupervisors(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListSupervisors(r.Context(), r.URL.Query().Get("location"))
	if err != nil {
		h.handleErr(w, r, "list cs supervisors failed", err)
		return
	}
	if items == nil {
		items = []cs.Supervisor{}
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"supervisors": items,
		"count":       len(items),
	})
}

// AuthorizeSupervisor handles POST /internal/v1/supervisors/authorize
//
// Meloloskan gerbang SUPERVISOR_AUTH untuk sesi pemanggil, dan mengembalikan TANDA
// TERIMA — bukan token.
func (h *CSTerminalHandler) AuthorizeSupervisor(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	var req cs.SupervisorAuthorizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.AuthorizeSupervisor(r.Context(), sess, req, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "supervisor authorization failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

// Healthcheck handles POST /internal/v1/terminals/healthcheck
//
// Probe-nya di KLIEN, pencatatannya di server. Server tidak bisa mengukur kamera atau
// mikrofon di meja petugas.
func (h *CSTerminalHandler) Healthcheck(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	var req cs.HealthcheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.RecordHealthcheck(r.Context(), sess, req, extractIP(r), r.UserAgent()); err != nil {
		h.handleErr(w, r, "record device healthcheck failed", err)
		return
	}

	h.respondReadiness(w, r, sess, "record device healthcheck failed")
}

// AcknowledgePII handles POST /internal/v1/terminals/pii-ack
func (h *CSTerminalHandler) AcknowledgePII(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	var req cs.PIIAckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	if err := h.svc.AcknowledgePII(r.Context(), sess, req, extractIP(r), r.UserAgent()); err != nil {
		h.handleErr(w, r, "record pii acknowledgement failed", err)
		return
	}

	h.respondReadiness(w, r, sess, "record pii acknowledgement failed")
}

// Activate handles POST /internal/v1/terminals/activate
//
// Rule 3: menolak kalau salah satu gerbang belum lolos, dan menyebut gerbang MANA yang
// kurang — tanpa itu petugas hanya melihat "belum siap" dan harus menebak layar mana
// yang harus diulang.
func (h *CSTerminalHandler) Activate(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	t, err := h.svc.Activate(r.Context(), sess, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "activate cs terminal failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, t)
}

// Deactivate handles POST /internal/v1/terminals/deactivate
//
// Tanpa menutup sesi: petugas yang istirahat menonaktifkan loketnya tanpa mengakhiri
// gilirannya. Yang pulang memanggil `auth/logout`, yang melakukan keduanya.
func (h *CSTerminalHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	if err := h.svc.Deactivate(r.Context(), sess, extractIP(r), r.UserAgent()); err != nil {
		h.handleErr(w, r, "deactivate cs terminal failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"terminal_id": sess.TerminalID,
		"status":      string(cs.TerminalOffline),
	})
}

// LockDesk handles POST /internal/v1/terminals/lock
//
// Idempoten: mengunci loket yang sudah terkunci menjawab 200 dengan keadaan yang sama,
// bukan 409. Klien memanggilnya dari dua jalur — tombol dan pintasan papan tik — dan
// balapan di antara keduanya tidak boleh berbunyi seperti galat.
func (h *CSTerminalHandler) LockDesk(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	// Body boleh kosong: `reason` default AGENT, dan pintasan papan tik tidak selalu
	// mengirim apa pun.
	var req cs.LockDeskRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			response.Err(w, r, apperr.ValidationError)
			return
		}
	}

	state, err := h.svc.LockDesk(r.Context(), sess, req, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "lock cs terminal desk failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, state)
}

// UnlockDesk handles POST /internal/v1/terminals/unlock
func (h *CSTerminalHandler) UnlockDesk(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	var req cs.UnlockDeskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	state, err := h.svc.UnlockDesk(r.Context(), sess, req, extractIP(r), r.UserAgent())
	if err != nil {
		// PIN salah tidak dicatat sebagai galat server, dan PIN-nya sendiri tidak pernah
		// masuk log — handleErr hanya mencetak yang kodenya INTERNAL_ERROR.
		h.handleErr(w, r, "unlock cs terminal desk failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, state)
}

// DeskLockState handles GET /internal/v1/terminals/lock-state
//
// Dipanggil SEKALI saat pemulihan sesi, di tempat yang sama dengan `auth/me`: tirai
// lokal di aplikasi desktop hilang bersama proses yang mati, dan tanpa endpoint ini
// aplikasi yang dibuka kembali akan menampilkan layar nasabah. Bukan endpoint polling.
func (h *CSTerminalHandler) DeskLockState(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	state, err := h.svc.DeskLockState(r.Context(), sess)
	if err != nil {
		h.handleErr(w, r, "read cs terminal lock state failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, state)
}

// UnlockOverride handles POST /internal/v1/terminals/unlock-override
//
// Jalan keluar dari loket beku, lewat dual-control yang SUDAH ada: `authorization_ref`
// dari `POST /supervisors/authorize`, ditambah kata sandi petugas — bukan PIN-nya, yang
// justru sedang bermasalah.
func (h *CSTerminalHandler) UnlockOverride(w http.ResponseWriter, r *http.Request) {
	sess, ok := csSessionFromRequest(w, r)
	if !ok {
		return
	}

	var req cs.UnlockOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	state, err := h.svc.UnlockOverride(r.Context(), sess, req, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "override unlock cs terminal desk failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, state)
}

// respondReadiness menjawab dengan keadaan gerbang TERBARU setelah satu gerbang lolos.
//
// Mengembalikan keadaan penuh, bukan `{"ok": true}`: layar kesiapan menampilkan ketiga
// gerbang sekaligus, dan tanpa ini ia harus memanggil `readiness` lagi setiap kali —
// dua permintaan untuk satu tindakan, masing-masing melewati verifikasi sesi.
func (h *CSTerminalHandler) respondReadiness(w http.ResponseWriter, r *http.Request, sess *cs.SessionContext, msg string) {
	state, err := h.svc.Readiness(r.Context(), sess)
	if err != nil {
		h.handleErr(w, r, msg, err)
		return
	}
	response.Success(w, r, http.StatusOK, state)
}

func (h *CSTerminalHandler) denyUnassembled(w http.ResponseWriter, r *http.Request) {
	slog.Error("terminal endpoint reached without an authenticated agent", "path", r.URL.Path)
	response.Err(w, r, apperr.Error{
		Status:  http.StatusForbidden,
		Code:    "FORBIDDEN",
		Message: "Akses ditolak.",
	})
}

func (h *CSTerminalHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
