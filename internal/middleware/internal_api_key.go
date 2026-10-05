package middleware

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

// InternalAPIKey protects internal/admin endpoints with a shared secret.
// The client must send the key via the X-Internal-API-Key header.
//
// The comparison is constant-time: this is a bearer secret, and a byte-by-byte
// string compare leaks its prefix through timing. An empty configured key
// denies every request rather than accepting an empty header — a service
// started without INTERNAL_API_KEY must not expose the CS endpoints.
func InternalAPIKey(apiKey string) func(http.Handler) http.Handler {
	expected := []byte(apiKey)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(expected) == 0 {
				slog.Error("internal api key is not configured; denying internal endpoint",
					"path", r.URL.Path,
				)
				response.Err(w, r, apperr.Error{
					Status:  http.StatusForbidden,
					Code:    "FORBIDDEN",
					Message: "Akses ditolak.",
				})
				return
			}

			provided := []byte(r.Header.Get("X-Internal-API-Key"))
			if subtle.ConstantTimeCompare(provided, expected) != 1 {
				response.Err(w, r, apperr.Error{
					Status:  http.StatusForbidden,
					Code:    "FORBIDDEN",
					Message: "Akses ditolak.",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- Identitas petugas CS ---

const (
	// headerAgentEmployeeID menyebut petugas yang mengaku; headerAgentAPIKey
	// membuktikannya.
	headerAgentEmployeeID = "X-Agent-Employee-ID"
	headerAgentAPIKey     = "X-Agent-API-Key"
)

type agentCtxKey struct{}

// agentIdentity adalah petugas CS yang sudah terautentikasi.
type agentIdentity struct {
	EmployeeID string
	Name       string
}

// AgentFromCtx mengembalikan identitas petugas yang sudah diautentikasi [AgentAuth].
//
// `ok` false berarti request ini tidak melewati AgentAuth — handler harus menolak, bukan
// melanjutkan dengan identitas kosong.
func AgentFromCtx(ctx context.Context) (employeeID, name string, ok bool) {
	v, ok := ctx.Value(agentCtxKey{}).(agentIdentity)
	if !ok {
		return "", "", false
	}
	return v.EmployeeID, v.Name, true
}

// AgentLookup memverifikasi kredensial petugas dan mengembalikan namanya.
//
// Dipenuhi *postgres.CSAgentRepo secara struktural — tipe kembaliannya string biasa,
// bukan sebuah struct bersama, supaya lapisan repository tidak perlu meng-import paket
// middleware. Pola yang sama dipakai SignalingNotifier di lapisan domain.
type AgentLookup interface {
	// AuthenticateAgent mengembalikan (nama, true, nil) kalau kredensialnya sah,
	// ("", false, nil) kalau salah atau petugasnya tidak aktif, dan ("", false, err)
	// hanya untuk kegagalan infrastruktur.
	AuthenticateAgent(ctx context.Context, employeeID, apiKey string) (name string, ok bool, err error)
}

// AgentAuth mengikat setiap permintaan video call internal ke satu petugas CS.
//
// Sebelum ini `agent_employee_id` dan `agent_name` adalah field body yang dipercaya apa
// adanya. Siapa pun yang memegang INTERNAL_API_KEY — satu secret yang sama untuk seluruh
// integrasi CS — bisa mengaku sebagai pegawai mana pun, dan string itulah yang masuk audit
// trail sebagai `actor` serta tampil ke layar nasabah lewat `agent_assigned`. Untuk
// verifikasi identitas yang hasilnya membuka pembukaan rekening, jejaknya harus bisa
// dipertanggungjawabkan ke orang.
//
// Dipasang SETELAH [InternalAPIKey], dan urutannya penting: verifikasi Argon2 itu mahal
// (64 MB × 4 thread), jadi ia tidak boleh bisa dipicu oleh lalu lintas yang belum
// membuktikan dirinya sebagai sistem CS. Dua penjaga, dua pertanyaan berbeda — sistem mana
// yang memanggil, dan petugas mana yang bertindak.
//
// lookup nil menolak semua permintaan: sebuah proses yang berjalan tanpa jalan memverifikasi
// petugas tidak boleh memaparkan endpoint yang mengatribusikan verifikasi ke seseorang.
func AgentAuth(lookup AgentLookup) func(http.Handler) http.Handler {
	forbidden := apperr.Error{
		Status:  http.StatusForbidden,
		Code:    "FORBIDDEN",
		Message: "Akses ditolak.",
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if lookup == nil {
				slog.Error("cs agent lookup is not configured; denying agent endpoint",
					"path", r.URL.Path,
				)
				response.Err(w, r, forbidden)
				return
			}

			employeeID := strings.TrimSpace(r.Header.Get(headerAgentEmployeeID))
			apiKey := r.Header.Get(headerAgentAPIKey)
			if employeeID == "" || apiKey == "" {
				response.Err(w, r, forbidden)
				return
			}

			name, ok, err := lookup.AuthenticateAgent(r.Context(), employeeID, apiKey)
			if err != nil {
				// Postgres yang tersendat bukan bukti petugasnya tidak berwenang, dan
				// bukan juga alasan meluluskannya. 503, seperti penjaga token signaling
				// saat Redis gagal: jangan pernah fail-open di jalur yang menentukan
				// siapa yang bertanggung jawab atas sebuah verifikasi.
				slog.Error("authenticate cs agent failed",
					"employee_id", employeeID, "error", err)
				response.Err(w, r, apperr.Error{
					Status:  http.StatusServiceUnavailable,
					Code:    "AGENT_AUTH_UNAVAILABLE",
					Message: "Verifikasi petugas sedang tidak tersedia.",
				})
				return
			}
			if !ok {
				slog.Warn("cs agent authentication rejected", "employee_id", employeeID)
				response.Err(w, r, forbidden)
				return
			}

			ctx := context.WithValue(r.Context(), agentCtxKey{}, agentIdentity{
				EmployeeID: employeeID,
				Name:       name,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
