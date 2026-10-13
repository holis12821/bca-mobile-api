package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// CSEscalationHandler melayani antrean kerja Tier 2 atas perkara NEED_REVIEW.
//
// Terpasang di `/internal/v1/escalations`, di belakang `InternalAPIKey` +
// `AgentAuth(ScopeEscalationReview)`. Cakupannya sengaja BUKAN `VIDEO_CALL`: petugas
// yang mengaku tidak sanggup memutuskan sebuah verifikasi tidak semestinya bisa menutup
// perkaranya — dan di jalur ini sebuah `APPROVED` memindahkan nasabah ke CREDENTIALS,
// yaitu keputusan yang sama besar dengan keputusan panggilan itu sendiri.
type CSEscalationHandler struct {
	svc *onboarding.EscalationService
}

func NewCSEscalationHandler(svc *onboarding.EscalationService) *CSEscalationHandler {
	return &CSEscalationHandler{svc: svc}
}

// listEscalationsResponse adalah jawaban `GET /internal/v1/escalations`.
type listEscalationsResponse struct {
	// Escalations selalu array, tidak pernah null: antrean kerja yang kosong adalah
	// keadaan normal setiap pagi, dan `null` di sana membuat layar melakukan `.length`
	// pada nilai yang bukan array.
	Escalations []onboarding.VideoCallEscalation `json:"escalations"`

	Count int `json:"count"`
}

// ListEscalations handles GET /internal/v1/escalations
//
// Penyaring: `queue`, `status`, `claimed_by`, `session_id`, `limit`.
//
// Tanpa `status`, yang dijawab hanya perkara TERBUKA — PENDING dan IN_REVIEW. Bawaannya
// antrean kerja, bukan arsip: layar yang dibuka setiap pagi menanyakan "apa yang harus
// saya kerjakan", dan arsip yang tercampur ke dalamnya akan membenamkan perkara baru di
// bawah perkara tahun lalu.
//
// Tidak ada kursor di sini, berbeda dari `GET /internal/v1/onboarding/sessions`. Alasannya
// bukan kelalaian: antrean terbuka Tier 2 berukuran puluhan, bukan ribuan — kalau ia
// sampai butuh halaman kedua, yang perlu diperbaiki adalah jumlah peninjaunya, bukan
// paginasinya. `limit` dibatasi 200 di service supaya satu permintaan tidak bisa menarik
// seluruh arsip.
func (h *CSEscalationHandler) ListEscalations(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		response.Err(w, r, apperr.ProviderNotConfigured)
		return
	}

	q := r.URL.Query()
	filter := onboarding.ListEscalationsFilter{
		Queue:     q.Get("queue"),
		Status:    q.Get("status"),
		ClaimedBy: q.Get("claimed_by"),
		SessionID: q.Get("session_id"),
	}

	// Nilai `queue` dan `status` yang tidak dikenal ditolak di service, bukan di sini:
	// penolakannya menyebut daftar yang diizinkan, dan daftar itu milik domain.
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			response.Err(w, r, apperr.ValidationError)
			return
		}
		filter.Limit = parsed
	}

	rows, err := h.svc.List(r.Context(), filter)
	if err != nil {
		h.handleErr(w, r, "list escalations failed", err)
		return
	}
	if rows == nil {
		rows = []onboarding.VideoCallEscalation{}
	}

	response.Success(w, r, http.StatusOK, listEscalationsResponse{
		Escalations: rows,
		Count:       len(rows),
	})
}

// UpdateEscalation handles PATCH /internal/v1/escalations/{escalation_id}
//
// Dua tindakan di satu endpoint, dibedakan oleh `action`: `CLAIM` memegang perkaranya,
// `RESOLVE` menutupnya. Kata kerja, bukan status tujuan — `{"status":"IN_REVIEW"}` akan
// memaksa aplikasi desktop tahu mesin statusnya untuk bisa memakai endpoint-nya, dan
// membuat setiap status yang nanti ditambahkan terlihat seperti tindakan yang sah diminta
// klien.
//
// Yang membedakannya dari `POST /v1/onboarding/video-call/result`: di sini tidak ada
// panggilan, tidak ada socket, dan tidak ada `call_ended`. Nasabahnya tidak sedang
// menunggu di layar — ia sudah diberi tahu perkaranya ditinjau, dan yang dilihatnya
// berubah adalah `current_step` saat ia membuka aplikasinya lagi.
func (h *CSEscalationHandler) UpdateEscalation(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		response.Err(w, r, apperr.ProviderNotConfigured)
		return
	}

	agent, ok := authenticatedAgent(w, r)
	if !ok {
		return
	}

	escalationID := chi.URLParam(r, "escalation_id")
	if escalationID == "" {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	var req onboarding.UpdateEscalationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Err(w, r, apperr.ValidationError)
		return
	}

	resp, err := h.svc.Update(r.Context(), escalationID, req, agent,
		extractIP(r), r.UserAgent())
	if err != nil {
		h.handleErr(w, r, "update escalation failed", err)
		return
	}

	response.Success(w, r, http.StatusOK, resp)
}

func (h *CSEscalationHandler) handleErr(w http.ResponseWriter, r *http.Request, msg string, err error) {
	appErr := apperr.From(err)
	if appErr.Code == apperr.InternalError.Code {
		slog.Error(msg,
			"request_id", chimiddleware.GetReqID(r.Context()),
			"error", err,
		)
	}
	response.Err(w, r, appErr)
}
