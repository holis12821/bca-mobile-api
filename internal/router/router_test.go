package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/config"
)

// newTestRouter builds the real route tree. The repositories are wired to nil
// handles on purpose: every request in this file is answered by middleware
// before any storage is touched, so a route that started reaching the database
// would fail loudly instead of quietly passing.
func newTestRouter(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	h, err := New(Deps{Config: cfg})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return h
}

func productionLikeConfig(internalKey string) *config.Config {
	cfg := &config.Config{InternalAPIKey: internalKey}
	cfg.App.Env = "development"
	return cfg
}

// Internal endpoints are the CS backend's, not the public internet's.
func TestInternalEndpoints_RequireAPIKey(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig("s3cret-from-the-vault"))

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/onboarding/video-call/result"},
		{http.MethodPost, "/v1/onboarding/video-call/agent-token"},
		// Daftar antrean adalah pintu masuk sisi CS dan memuat session_id setiap
		// nasabah yang menunggu. Ia ikut di belakang penjaga ini, bukan publik.
		{http.MethodGet, "/v1/onboarding/video-call/queued"},
		{http.MethodGet, "/v1/onboarding/sessions/onb_1/audit"},
		{http.MethodGet, "/v1/onboarding/monitoring"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Run("no key", func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Errorf("expected 403 without a key, got %d", rr.Code)
				}
			})

			t.Run("wrong key", func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				req.Header.Set("X-Internal-API-Key", "s3cret-from-the-vaul") // one byte short
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Errorf("expected 403 with a wrong key, got %d", rr.Code)
				}
			})
		})
	}
}

// A service started without INTERNAL_API_KEY must not fall back to a shared
// default — the endpoints simply stay closed.
func TestInternalEndpoints_ClosedWhenKeyUnset(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig(""))

	for _, key := range []string{"", "dev-internal-key"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/monitoring", nil)
		if key != "" {
			req.Header.Set("X-Internal-API-Key", key)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("key %q: expected 403, got %d", key, rr.Code)
		}
	}
}

// The dev-only helpers must not exist outside development: the route is absent,
// so the answer is the ordinary 404 envelope.
func TestDevEndpoints_OnlyInDevelopment(t *testing.T) {
	prod := &config.Config{InternalAPIKey: "k"}
	prod.App.Env = "production"

	r := newTestRouter(t, prod)

	for _, path := range []string{"/v1/dev/pin-public-key", "/v1/dev/encrypt-pin"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected the route to be absent, got %d", path, rr.Code)
		}
	}
}

func TestUnknownRoute_UsesTheStandardEnvelope(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig("k"))

	req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code"`) {
		t.Errorf("404 should use the error envelope, got %s", rr.Body.String())
	}
}

// Protected endpoints stay protected: no token, no access.
func TestProtectedEndpoints_RequireAccessToken(t *testing.T) {
	r := newTestRouter(t, productionLikeConfig("k"))

	req := httptest.NewRequest(http.MethodGet, "/v1/account/balance", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without a token, got %d", rr.Code)
	}
}

// ICE servers come from the environment, never from code: TURN credentials
// belong to whoever runs the TURN server, and they rotate.
func TestICEServers_FromConfig(t *testing.T) {
	cfg := productionLikeConfig("k")
	cfg.WebRTC.STUNURLs = []string{"stun:stun.example.id:3478", "  "}
	cfg.WebRTC.TURNURLs = []string{"turn:turn.example.id:3478?transport=udp"}
	cfg.WebRTC.TURNUsername = "bcadev"
	cfg.WebRTC.TURNCredential = "s3cret"

	servers := iceServers(cfg)
	if len(servers) != 2 {
		t.Fatalf("expected a STUN and a TURN entry, got %d: %+v", len(servers), servers)
	}
	// The blank URL is dropped rather than handed to the client as an empty
	// entry — a PeerConnection treats that as a configuration error.
	if len(servers[0].URLs) != 1 || servers[0].Username != "" {
		t.Fatalf("STUN entry must carry one URL and no credentials, got %+v", servers[0])
	}
	if servers[1].Username != "bcadev" || servers[1].Credential != "s3cret" {
		t.Fatalf("TURN entry must carry its credentials, got %+v", servers[1])
	}
}

// A TURN server with no credentials refuses every allocation, so publishing it
// would hand the app a server that can only fail. It is dropped instead.
func TestICEServers_TURNWithoutCredentialsIsDropped(t *testing.T) {
	cfg := productionLikeConfig("k")
	cfg.WebRTC.TURNURLs = []string{"turn:turn.example.id:3478"}

	if servers := iceServers(cfg); len(servers) != 0 {
		t.Fatalf("expected TURN without credentials to be dropped, got %+v", servers)
	}

	// Nothing configured at all is a valid answer: an empty list is how the app
	// tells "not configured" from "configured and broken".
	if servers := iceServers(productionLikeConfig("k")); servers != nil {
		t.Fatalf("expected no ICE servers, got %+v", servers)
	}
}

// Kredensial FCM yang dikonfigurasi tapi tidak bisa dipakai harus MENGGAGALKAN
// boot, bukan diam-diam turun ke NoopPusher.
//
// Inilah alasan New mengembalikan error. Turun diam-diam berarti proses yang
// kelihatan sehat — baris notifikasi tetap ditulis, semua endpoint menjawab 200 —
// sementara tidak satu pun notifikasi sampai ke perangkat nasabah, dan tidak ada
// yang memberi tahu siapa pun.
func TestPushProvider_BrokenCredentialsFailBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := productionLikeConfig("internal-key-cukup-panjang-untuk-lolos")
	cfg.Push.CredentialsFile = path

	if _, err := New(Deps{Config: cfg}); err == nil {
		t.Fatal("kredensial FCM tidak sah harus menggagalkan New, bukan diterima")
	}
}

func TestPushProvider_MissingCredentialsFileFailsBoot(t *testing.T) {
	cfg := productionLikeConfig("internal-key-cukup-panjang-untuk-lolos")
	cfg.Push.CredentialsFile = filepath.Join(t.TempDir(), "tidak-ada.json")

	if _, err := New(Deps{Config: cfg}); err == nil {
		t.Fatal("berkas kredensial yang tidak ada harus menggagalkan New")
	}
}

// Tanpa kredensial, boot tetap berhasil: itu keadaan yang sah, dan notifikasi
// tetap tersimpan sebagai baris in-app.
func TestPushProvider_NoCredentialsStillBoots(t *testing.T) {
	cfg := productionLikeConfig("internal-key-cukup-panjang-untuk-lolos")

	if _, err := New(Deps{Config: cfg}); err != nil {
		t.Fatalf("tanpa kredensial FCM boot harus tetap berhasil: %v", err)
	}
}

// --- pemilihan transport SMS (T3) --------------------------------------------

// SMS_PROVIDER=twilio_verify harus mewire Verifier, dan kredensial yang salah
// harus menggagalkan boot — aturan yang sama dengan FCMPusher di atas. Proses
// yang sehat sambil tidak pernah bisa memeriksa satu kode pun bukan proses sehat.
func TestSMSVerifier_BrokenCredentialsFailBoot(t *testing.T) {
	cases := map[string]func(*config.Config){
		"service sid kosong": func(c *config.Config) {
			c.SMS.VerifyServiceSID = ""
		},
		"service sid terpotong": func(c *config.Config) {
			c.SMS.VerifyServiceSID = "VAxxx"
		},
		"account sid huruf kecil": func(c *config.Config) {
			c.SMS.AccountSID = "ac00000000000000000000000000000000"
		},
		"auth token kosong": func(c *config.Config) {
			c.SMS.AuthToken = ""
		},
		"channel tidak dikenal": func(c *config.Config) {
			c.SMS.VerifyChannels = []string{"sms", "voice"}
		},
	}

	for name, breakIt := range cases {
		breakIt := breakIt
		t.Run(name, func(t *testing.T) {
			cfg := verifyConfig()
			breakIt(cfg)
			if _, err := New(Deps{Config: cfg}); err == nil {
				t.Fatal("konfigurasi Verify yang salah harus menggagalkan boot, bukan diterima")
			}
		})
	}
}

// Konfigurasi Verify yang benar boot, dan SMS_BASE_URL tidak ikut mengubahnya:
// itu host Messages, dan Verify yang membacanya menjawab 404 — yang oleh
// transport dibaca sebagai "tidak ada verifikasi aktif", jadi OTP_EXPIRED untuk
// setiap nasabah dengan kredensial yang sebenarnya benar.
func TestSMSVerifier_ValidConfigBootsAndIgnoresMessagesBaseURL(t *testing.T) {
	cfg := verifyConfig()
	cfg.SMS.BaseURL = "http://messages-host.invalid"

	if _, err := New(Deps{Config: cfg}); err != nil {
		t.Fatalf("konfigurasi Verify yang sah harus boot: %v", err)
	}
}

// Nama provider yang tidak dikenal tetap menggagalkan boot, bukan jatuh ke mock.
func TestSMSProvider_UnknownNameFailsBoot(t *testing.T) {
	cfg := productionLikeConfig("internal-key-cukup-panjang-untuk-lolos")
	cfg.SMS.Provider = "twillio"

	if _, err := New(Deps{Config: cfg}); err == nil {
		t.Fatal("typo pada SMS_PROVIDER harus menggagalkan boot")
	}
}

// verifyConfig adalah konfigurasi twilio_verify yang lolos; tiap test di atas
// merusak tepat satu hal, supaya pesan kegagalannya tetap terbaca.
func verifyConfig() *config.Config {
	cfg := productionLikeConfig("internal-key-cukup-panjang-untuk-lolos")
	cfg.SMS.Provider = "twilio_verify"
	cfg.SMS.AccountSID = "AC00000000000000000000000000000000"
	cfg.SMS.AuthToken = "token-dummy-bukan-rahasia"
	cfg.SMS.VerifyServiceSID = "VA00000000000000000000000000000000"
	cfg.SMS.VerifyChannels = []string{"sms", "call"}
	return cfg
}

// Mengambil panggilan dan memutuskan hasil verifikasi adalah tindakan SESEORANG, bukan
// tindakan sebuah sistem: namanya tampil di layar nasabah dan keputusannya yang membuka
// pembukaan rekening. X-Internal-API-Key saja tidak cukup — satu secret yang sama dipakai
// seluruh integrasi CS, jadi siapa pun yang memegangnya bisa mengaku sebagai pegawai mana
// pun, dan string itulah yang masuk audit trail sebagai `actor`.
func TestAgentEndpoints_RequireAnAuthenticatedAgent(t *testing.T) {
	const key = "s3cret-from-the-vault"
	r := newTestRouter(t, productionLikeConfig(key))

	agentPaths := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/onboarding/video-call/agent-token"},
		{http.MethodPost, "/v1/onboarding/video-call/result"},
	}

	for _, tc := range agentPaths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// Kunci sistem benar, identitas petugas tidak ada.
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("X-Internal-API-Key", key)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("expected 403 without agent credentials, got %d", rr.Code)
			}

			// Identitas disebut tapi tidak dibuktikan.
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("X-Internal-API-Key", key)
			req.Header.Set("X-Agent-Employee-ID", "CS-1042")
			rr = httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("expected 403 for an unproven employee id, got %d", rr.Code)
			}
		})
	}
}

// Endpoint pengawas tidak menuntut identitas petugas: melihat antrean, jejak audit, dan
// kesehatan sistem tidak diatribusikan ke seseorang. Yang diperiksa di sini adalah bahwa
// penjaga petugas TIDAK ikut terpasang di sana — kalau ikut, panel pemantauan CS akan
// 403 tanpa alasan yang terlihat.
func TestSupervisorEndpoints_DoNotRequireAgentCredentials(t *testing.T) {
	const key = "s3cret-from-the-vault"
	r := newTestRouter(t, productionLikeConfig(key))

	req := httptest.NewRequest(http.MethodGet, "/v1/onboarding/video-call/queued", nil)
	req.Header.Set("X-Internal-API-Key", key)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// Permintaannya sampai ke handler, lalu gagal di penyimpanan yang tidak ada —
	// middleware Recovery menjawabnya 500. Justru 500 itulah buktinya: ia lolos kedua
	// penjaga. 403 berarti salah satu penjaga menolaknya, dan itu yang diuji di sini.
	if rr.Code == http.StatusForbidden {
		t.Error("endpoint pengawas tidak boleh menuntut kredensial petugas")
	}
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("tanpa database endpoint ini seharusnya sampai ke handler dan gagal 500, dapat %d", rr.Code)
	}
}
