package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/pagination"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// CSHandler melayani endpoint nasabah untuk petugas Customer Service.
//
// Seluruh endpoint di sini WAJIB terpasang di belakang middleware.InternalAPIKey DAN
// middleware.AgentAuth ber-scope CUSTOMER_PII. Tanpa yang kedua tidak ada identitas
// petugas, dan tanpa identitas petugas jejak aksesnya tidak menjawab apa pun.
type CSHandler struct {
	svc       *cs.Service
	audit     *cs.AuditQueryService
	dashboard *cs.DashboardService
}

func NewCSHandler(
	svc *cs.Service,
	audit *cs.AuditQueryService,
	dashboard *cs.DashboardService,
) *CSHandler {
	return &CSHandler{svc: svc, audit: audit, dashboard: dashboard}
}

// SearchCustomers handles GET /internal/v1/customers?q=
//
// Cocok PERSIS nomor rekening atau nomor HP. Tidak ada pencarian nama dan tidak ada
// pencocokan sebagian — lihat [cs.CustomerRepository] untuk alasannya.
func (h *CSHandler) SearchCustomers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := h.agent(w, r)
	if !ok {
		return
	}

	matches, err := h.svc.Search(r.Context(), query, agent, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "cs customer search failed", err)
		return
	}

	// Daftar kosong, bukan 404. 404 akan memberi tahu pemanggil bahwa kata kuncinya
	// bukan nomor terdaftar — jawaban yang bisa dipakai menyapu ruang nomor rekening.
	response.Success(w, r, http.StatusOK, map[string]any{
		"customers": matches,
		"count":     len(matches),
	})
}

// GetCustomerProfile handles GET /internal/v1/customers/{user_id}
//
// Setiap pemanggilan yang berhasil menulis CUSTOMER_VIEWED ke cs_access_logs, terikat
// ke petugas dan ke nasabahnya.
func (h *CSHandler) GetCustomerProfile(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "user_id")
	userID, err := uuid.Parse(raw)
	if err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	agent, ok := h.agent(w, r)
	if !ok {
		return
	}

	profile, err := h.svc.GetProfile(r.Context(), userID, agent, extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "cs customer profile failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, profile)
}

// agent membaca identitas petugas yang dipasang middleware.AgentAuth.
//
// Ketiadaannya adalah kesalahan perakitan rute, bukan kesalahan pemanggil — dan di jalur
// yang membuka PII, kesalahan perakitan tidak boleh berakhir dengan data terkirim tanpa
// pelaku yang tercatat.
func (h *CSHandler) agent(w http.ResponseWriter, r *http.Request) (string, bool) {
	employeeID, _, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		slog.Error("cs endpoint reached without an authenticated agent", "path", r.URL.Path)
		response.Err(w, r, apperr.Error{
			Status:  http.StatusForbidden,
			Code:    "FORBIDDEN",
			Message: "Akses ditolak.",
		})
		return "", false
	}
	return employeeID, true
}

// handleErr mengikuti pola yang sama dengan OnboardingHandler: hanya kegagalan yang
// BUKAN apperr yang dicatat sebagai error, supaya 404 dan 400 yang wajar tidak memenuhi
// log dengan hal yang tidak perlu ditindaklanjuti siapa pun.
func (h *CSHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}

// ListAuditEvents handles GET /internal/v1/cs/audit-events
//
// Penjaganya kunci sistem + cakupan `AUDIT_READ`, dan cakupan itu BARU ADA sejak migrasi
// 000041 melebarkan CHECK `cs_agents_scopes_valid`. Sebelumnya penjaganya hanya identitas
// petugas — satu-satunya pilihan selama daftar cakupan terkunci ke empat nilai — jadi
// setiap petugas terautentikasi bisa membaca jejak rekannya: jam login, loket, dan setiap
// otorisasi supervisor yang pernah gagal atas namanya.
//
// Pengetatannya MEMUTUS pemanggil lama dengan sengaja: tidak ada baris cs_agents yang
// diberi AUDIT_READ oleh migrasinya, jadi endpoint ini menjawab 403 sampai seseorang
// diberi cakupan itu lewat `PATCH /internal/v1/agents/{employee_id}`. Migrasi yang
// diam-diam memberi kewenangan pengawas kepada semua petugas yang sudah ada akan
// melakukan persis hal yang pemisahan ini ada untuk mencegahnya.
func (h *CSHandler) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		slog.Error("audit-events reached without an audit query service", "path", r.URL.Path)
		response.Err(w, r, apperr.ProviderNotConfigured)
		return
	}

	q := r.URL.Query()
	filter := cs.AuditFilter{
		Actor:      strings.TrimSpace(q.Get("actor")),
		TerminalID: strings.TrimSpace(q.Get("terminal_id")),
	}

	// Jenis peristiwa yang tidak dikenal DITOLAK, bukan dibiarkan menghasilkan daftar
	// kosong: nol baris karena salah ketik tidak bisa dibedakan dari nol baris karena
	// memang belum ada yang terjadi, dan yang kedua adalah kesimpulan pemeriksaan.
	if raw := strings.TrimSpace(q.Get("event_type")); raw != "" {
		if !cs.ValidAuditEventType(raw) {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.EventType = raw
	}

	// Rentang waktu. Keduanya opsional dan boleh sendirian.
	for _, b := range []struct {
		key string
		dst **time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		raw := strings.TrimSpace(q.Get(b.key))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		*b.dst = &parsed
	}

	// from setelah to tidak mungkin menghasilkan apa pun. Ditolak supaya pengawas tahu
	// filternya terbalik, bukan menyimpulkan tidak ada peristiwa di rentang itu.
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	limit := 50
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
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
		filter.Cursor = &cs.AuditCursor{CreatedAt: createdAt, ID: id}
	}

	items, hasMore, next, err := h.audit.List(r.Context(), filter)
	if err != nil {
		h.handleErr(w, r, "list cs audit events failed", err)
		return
	}
	if items == nil {
		items = []cs.AuditEvent{}
	}

	nextCursor := ""
	if next != nil {
		nextCursor = pagination.HistoryCursor{
			CreatedAt: next.CreatedAt.Format(time.RFC3339Nano),
			ID:        next.ID.String(),
		}.Encode()
	}

	response.SuccessWithPagination(w, r, http.StatusOK, map[string]any{
		"events": items,
	}, response.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Limit:   limit,
	})
}

// Dashboard handles GET /internal/v1/cs/dashboard
//
// Satu permintaan, bukan empat: layar beranda dimuat setiap petugas membukanya, dan
// empat permintaan untuk satu layar adalah empat kali verifikasi Argon2id (64 MB x 4
// thread).
func (h *CSHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if h.dashboard == nil {
		slog.Error("dashboard reached without a dashboard service", "path", r.URL.Path)
		response.Err(w, r, apperr.ProviderNotConfigured)
		return
	}

	employeeID, name, ok := middleware.AgentFromCtx(r.Context())
	if !ok || employeeID == "" {
		// Kesalahan perakitan rute, bukan kesalahan pemanggil.
		slog.Error("dashboard reached without an authenticated agent",
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

	data, err := h.dashboard.Overview(r.Context(), employeeID, name, scopes)
	if err != nil {
		h.handleErr(w, r, "build cs dashboard failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, data)
}
