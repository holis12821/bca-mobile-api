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

// Cakupan kewenangan petugas. Nilainya cocok dengan CHECK di migrasi
// 000027_cs_agent_scopes — menambah satu di sini tanpa menambahnya di sana akan
// menghasilkan scope yang tidak pernah bisa diberikan ke siapa pun.
const (
	// ScopeVideoCall: mengambil panggilan dan memutuskan hasil verifikasi e-KYC.
	ScopeVideoCall = "VIDEO_CALL"

	// ScopeCardAdmin: mengubah katalog kartu Paspor, yang terlihat seluruh nasabah.
	ScopeCardAdmin = "CARD_ADMIN"

	// ScopeCustomerPII: membuka data pribadi nasabah — NIK, alamat, kontak.
	//
	// Dipisah dari ScopeVideoCall meski aplikasi desktop yang sama memakai keduanya:
	// melayani panggilan menampilkan nasabah yang SEDANG bicara, sementara ini
	// menjangkau siapa pun yang pernah mendaftar.
	ScopeCustomerPII = "CUSTOMER_PII"

	// ScopeTicket: membaca dan menulis tiket layanan.
	ScopeTicket = "TICKET"

	// ScopeAuditRead: membaca jejak audit petugas dan terminal.
	//
	// Dipisah dari identitas petugas biasa karena `GET /cs/audit-events` menjawab
	// TENTANG REKAN SEKERJA: jam login, loket, dan setiap otorisasi supervisor yang
	// pernah gagal atas nama seseorang. Sebelum cakupan ini ada, setiap petugas
	// terautentikasi bisa membacanya — jejak yang terbuka bagi semua yang diawasinya
	// bukan pembatas kewenangan.
	ScopeAuditRead = "AUDIT_READ"

	// ScopeEscalationReview: menutup perkara NEED_REVIEW (Tier 2).
	//
	// Sengaja BUKAN ScopeVideoCall. Petugas yang mengaku tidak sanggup memutuskan sebuah
	// verifikasi tidak semestinya jadi orang yang menutup perkaranya; larangan
	// menutup-sendiri ditegakkan di service, tapi cakupan terpisah inilah yang membuat
	// petugas panggilan biasa tidak bisa menyentuh jalurnya sama sekali.
	ScopeEscalationReview = "ESCALATION_REVIEW"
)

type agentCtxKey struct{}

// agentIdentity adalah petugas CS yang sudah terautentikasi.
type agentIdentity struct {
	EmployeeID string
	Name       string
	Scopes     []string
}

// hasScope melaporkan apakah petugas memegang cakupan want.
//
// Perbandingan linear di atas irisan yang panjangnya paling banyak dua: sebuah map
// akan lebih lambat dialokasikan daripada dibaca.
func (a agentIdentity) hasScope(want string) bool {
	for _, s := range a.Scopes {
		if s == want {
			return true
		}
	}
	return false
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

// AgentScopesFromCtx mengembalikan cakupan petugas yang sudah diautentikasi [AgentAuth].
//
// Ada supaya `GET /internal/v1/auth/me` bisa MENJAWAB cakupan, bukan membuat aplikasi
// desktop menebaknya dari `403` yang pernah diterima. Tanpa endpoint itu, menyembunyikan
// menu berarti memanggil setiap endpoint sekali lalu mengingat mana yang ditolak — dan
// setiap panggilan itu memicu verifikasi Argon2id.
//
// Irisan yang dikembalikan adalah SALINAN: pemanggilnya menyerahkannya ke encoder JSON,
// dan irisan yang dibagikan dari context bisa diubah tanpa sengaja oleh siapa pun yang
// menerimanya.
func AgentScopesFromCtx(ctx context.Context) ([]string, bool) {
	v, ok := ctx.Value(agentCtxKey{}).(agentIdentity)
	if !ok {
		return nil, false
	}
	out := make([]string, len(v.Scopes))
	copy(out, v.Scopes)
	return out, true
}

// AgentLookup memverifikasi kredensial petugas dan mengembalikan namanya.
//
// Dipenuhi *postgres.CSAgentRepo secara struktural — tipe kembaliannya string biasa,
// bukan sebuah struct bersama, supaya lapisan repository tidak perlu meng-import paket
// middleware. Pola yang sama dipakai SignalingNotifier di lapisan domain.
type AgentLookup interface {
	// AuthenticateAgent mengembalikan (nama, cakupan, true, nil) kalau kredensialnya
	// sah, ("", nil, false, nil) kalau salah atau petugasnya tidak aktif, dan
	// ("", nil, false, err) hanya untuk kegagalan infrastruktur.
	AuthenticateAgent(ctx context.Context, employeeID, apiKey string) (name string, scopes []string, ok bool, err error)
}

// AgentAuth mengikat setiap permintaan operator internal ke satu petugas CS, dan
// menolak petugas yang tidak memegang cakupan requiredScope.
//
// Sebelum ini `agent_employee_id` dan `agent_name` adalah field body yang dipercaya apa
// adanya. Siapa pun yang memegang INTERNAL_API_KEY — satu secret yang sama untuk seluruh
// integrasi CS — bisa mengaku sebagai pegawai mana pun, dan string itulah yang masuk audit
// trail sebagai `actor` serta tampil ke layar nasabah lewat `agent_assigned`. Untuk
// verifikasi identitas yang hasilnya membuka pembukaan rekening, jejaknya harus bisa
// dipertanggungjawabkan ke orang.
//
// requiredScope menjawab pertanyaan ketiga: petugas ini berwenang melakukan APA. Tanpanya,
// satu kredensial membuka video call sekaligus administrasi katalog kartu — dan petugas
// yang tugasnya melayani panggilan bisa mengubah biaya kartu untuk seluruh nasabah.
// Cakupannya diminta di titik pasang rute, bukan dibaca dari handler, supaya sebuah
// endpoint operator tidak bisa terpasang tanpa menyatakan kewenangan yang dituntutnya.
//
// Dipasang SETELAH [InternalAPIKey], dan urutannya penting: verifikasi Argon2 itu mahal
// (64 MB × 4 thread), jadi ia tidak boleh bisa dipicu oleh lalu lintas yang belum
// membuktikan dirinya sebagai sistem CS. Tiga penjaga, tiga pertanyaan berbeda — sistem
// mana yang memanggil, petugas mana yang bertindak, dan boleh melakukan apa.
//
// lookup nil menolak semua permintaan: sebuah proses yang berjalan tanpa jalan memverifikasi
// petugas tidak boleh memaparkan endpoint yang mengatribusikan verifikasi ke seseorang.
// requiredScope kosong juga ditolak, karena itu berarti rute terpasang tanpa menyatakan
// kewenangannya — sebuah kesalahan perakitan yang tidak boleh gagal terbuka.
func AgentAuth(lookup AgentLookup, requiredScope string) func(http.Handler) http.Handler {
	// requiredScope kosong ditolak di sini, sebelum satu permintaan pun dilayani:
	// rute yang terpasang tanpa menyatakan kewenangannya adalah kesalahan perakitan,
	// dan jalur operator tidak boleh gagal terbuka. Yang memang butuh identitas TANPA
	// kewenangan tertentu memakai [AgentIdentity], bukan scope kosong di sini.
	return agentGuard(lookup, requiredScope, requiredScope != "")
}

// AgentIdentity mengautentikasi petugas TANPA memeriksa cakupan apa pun.
//
// Dipakai endpoint yang menjawab TENTANG pemanggilnya sendiri — sejauh ini hanya
// `GET /internal/v1/auth/me`, yang justru ada untuk memberi tahu cakupan apa yang
// dipegangnya. Memeriksa cakupan di sana akan membuat petugas harus sudah tahu
// jawabannya untuk bisa menanyakannya.
//
// Bukan `AgentAuth(lookup, "")`: scope kosong di sana sengaja berarti "rute salah
// rakit". Niat "tanpa cakupan" harus terbaca di titik pasang rute, bukan tersembunyi
// sebagai string kosong yang tidak bisa dibedakan dari kelalaian.
func AgentIdentity(lookup AgentLookup) func(http.Handler) http.Handler {
	return agentGuard(lookup, "", true)
}

// agentGuard adalah isi bersama [AgentAuth] dan [AgentIdentity].
//
// scopeDeclared membedakan "tidak menuntut cakupan, dan itu disengaja" dari "lupa
// menyebut cakupan". Keduanya menghasilkan requiredScope kosong, tapi hanya yang kedua
// adalah kesalahan.
func agentGuard(lookup AgentLookup, requiredScope string, scopeDeclared bool) func(http.Handler) http.Handler {
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
			if !scopeDeclared {
				slog.Error("agent endpoint mounted without a required scope; denying",
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

			name, scopes, ok, err := lookup.AuthenticateAgent(r.Context(), employeeID, apiKey)
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

			identity := agentIdentity{
				EmployeeID: employeeID,
				Name:       name,
				Scopes:     scopes,
			}

			// Kredensialnya sah, kewenangannya tidak. Dicatat terpisah dari penolakan
			// kredensial: yang satu kemungkinan serangan, yang satu lagi hampir selalu
			// baris cs_agents yang kurang scope — dan membedakannya di log adalah selisih
			// antara satu menit dan satu jam saat menelusurinya.
			if requiredScope != "" && !identity.hasScope(requiredScope) {
				slog.Warn("cs agent lacks required scope",
					"employee_id", employeeID,
					"required_scope", requiredScope,
					"granted_scopes", scopes,
					"path", r.URL.Path,
				)
				response.Err(w, r, forbidden)
				return
			}

			ctx := context.WithValue(r.Context(), agentCtxKey{}, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// --- Sesi petugas CS ---

const headerAgentSession = "Authorization"

type agentSessionCtxKey struct{}

// AgentSessionResolver menukar token sesi dengan identitas pembawanya.
//
// Dipenuhi *cs.AgentSessionService secara struktural. Tipe kembaliannya `any` supaya
// paket middleware tidak perlu meng-import paket domain — arah impornya `handler → domain`,
// dan middleware dipakai keduanya.
type AgentSessionResolver interface {
	// ResolveSession mengembalikan (identitas, nil) kalau tokennya sah, atau
	// (nil, err) kalau tidak. Identitasnya dibaca handler lewat [AgentSessionFromCtx].
	ResolveSession(ctx context.Context, token string) (identity any, err error)
}

// AgentSession mengautentikasi permintaan lewat token sesi, bukan kunci statis.
//
// Dipasang SETELAH [InternalAPIKey], seperti [AgentAuth]. Dipakai jalur yang memang
// menyangkut SATU GILIRAN KERJA — kesiapan terminal, aktivasi, logout — dan bukan
// kunci API, karena kunci API tidak tahu apa pun tentang giliran.
//
// Token dibaca dari `Authorization: Bearer <token>`. Skema lain diabaikan, bukan
// ditebak: token yang dikirim dengan skema salah lebih baik ditolak daripada diterima
// sebagian waktu.
func AgentSession(resolver AgentSessionResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if resolver == nil {
				slog.Error("agent session resolver is not configured; denying endpoint",
					"path", r.URL.Path,
				)
				response.Err(w, r, apperr.AgentSessionInvalid)
				return
			}

			raw := strings.TrimSpace(r.Header.Get(headerAgentSession))
			token, ok := bearerToken(raw)
			if !ok {
				response.Err(w, r, apperr.AgentSessionInvalid)
				return
			}

			identity, err := resolver.ResolveSession(r.Context(), token)
			if err != nil {
				response.Err(w, r, apperr.From(err))
				return
			}

			ctx := context.WithValue(r.Context(), agentSessionCtxKey{}, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AgentSessionFromCtx mengembalikan identitas sesi yang dipasang [AgentSession].
//
// Pemanggilnya meng-assert tipenya sendiri ke *cs.SessionContext. Tidak ada tipe
// bersama di sini supaya paket ini tetap tidak bergantung pada paket domain.
func AgentSessionFromCtx(ctx context.Context) (any, bool) {
	v := ctx.Value(agentSessionCtxKey{})
	return v, v != nil
}

// bearerToken memisahkan token dari skema `Bearer`.
//
// Skema dibandingkan case-insensitive (RFC 7235 menyebutnya tidak peka huruf), tapi
// tokennya tidak disentuh sama sekali — ia base64 URL-safe, dan mengubah besar-kecilnya
// akan menghasilkan hash yang berbeda.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
