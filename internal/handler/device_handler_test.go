package handler_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// --- Stub ---

// stubPushRegistrar records what the handler passed down. The arguments are the
// point of this endpoint: user and device must both come from the access token,
// never from the request body.
type stubPushRegistrar struct {
	err error

	calls        int
	lastUserID   uuid.UUID
	lastDeviceID string
	lastToken    string
}

func (s *stubPushRegistrar) RegisterPushToken(_ context.Context, userID uuid.UUID, deviceID, pushToken string) error {
	s.calls++
	s.lastUserID = userID
	s.lastDeviceID = deviceID
	s.lastToken = pushToken
	return s.err
}

// --- Harness ---

// newTestJWTManager builds a throwaway RSA key pair and JWT manager. Shared with
// the other handler tests in this package so the setup lives in one place.
func newTestJWTManager(t *testing.T) *crypto.JWTManager {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	kp, err := crypto.ParseRSAKeyPair(privPEM, pubPEM)
	if err != nil {
		t.Fatalf("parse key pair: %v", err)
	}
	return crypto.NewJWTManager(kp, 15*time.Minute, 168*time.Hour)
}

// newDeviceTestRouter puts the route behind the real Auth middleware, for the
// same reason the card tests do: the context keys are unexported, so the only
// honest way to make an authenticated request is to sign a token — which also
// proves the route is actually protected.
func newDeviceTestRouter(t *testing.T, devices handler.PushTokenRegistrar) (*chi.Mux, *crypto.JWTManager) {
	t.Helper()

	jwtMgr := newTestJWTManager(t)
	h := handler.NewDeviceHandler(devices)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(jwtMgr, nil))
		r.Post("/account/device/push-token", h.RegisterPushToken)
	})
	return r, jwtMgr
}

// bearerForDevice signs a token whose did claim names a specific device, which is
// what the 403 case needs.
func bearerForDevice(t *testing.T, jwtMgr *crypto.JWTManager, userID uuid.UUID, deviceID string) string {
	t.Helper()
	pair, err := jwtMgr.GenerateTokenPair(userID.String(), uuid.NewString(), deviceID)
	if err != nil {
		t.Fatalf("generate token pair: %v", err)
	}
	return "Bearer " + pair.AccessToken
}

func postPushToken(t *testing.T, r *chi.Mux, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/account/device/push-token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func errorCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error envelope (%s): %v", rec.Body.String(), err)
	}
	return payload.Error.Code
}

// --- Tests ---

func TestRegisterPushToken_StoresTokenAgainstTheTokenClaims(t *testing.T) {
	devices := &stubPushRegistrar{}
	r, jwtMgr := newDeviceTestRouter(t, devices)

	userID := uuid.New()
	bearer := bearerForDevice(t, jwtMgr, userID, "device-nurholis-001")

	// The body carries a device_id on purpose. It must be ignored: a client that
	// could name the device would be able to attach its push token to someone
	// else's handset, and every notification for that customer would be
	// delivered to the attacker's phone.
	rec := postPushToken(t, r, bearer,
		`{"push_token":"fcm-token-abc","device_id":"device-penyerang-999"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if devices.calls != 1 {
		t.Fatalf("repository dipanggil %d kali, harusnya 1", devices.calls)
	}
	if devices.lastUserID != userID {
		t.Errorf("user_id: got %s, want %s", devices.lastUserID, userID)
	}
	if devices.lastDeviceID != "device-nurholis-001" {
		t.Errorf("device_id diambil dari body, bukan dari klaim did: %q", devices.lastDeviceID)
	}
	if devices.lastToken != "fcm-token-abc" {
		t.Errorf("push_token: %q", devices.lastToken)
	}
}

// FCM merotasi token, jadi aplikasi memanggil endpoint ini berulang kali. Ia
// harus idempoten — tidak ada penolakan duplikat, tidak ada idempotency key.
func TestRegisterPushToken_IsIdempotent(t *testing.T) {
	devices := &stubPushRegistrar{}
	r, jwtMgr := newDeviceTestRouter(t, devices)
	bearer := bearerForDevice(t, jwtMgr, uuid.New(), "device-nurholis-001")

	for i := range 2 {
		rec := postPushToken(t, r, bearer, `{"push_token":"fcm-token-abc"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("panggilan %d: status %d, want 200: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if devices.calls != 2 {
		t.Errorf("kedua panggilan harus diteruskan: %d", devices.calls)
	}
}

func TestRegisterPushToken_RejectsBadBodies(t *testing.T) {
	cases := map[string]string{
		"token kosong":      `{"push_token":""}`,
		"field tidak ada":   `{}`,
		"bukan json":        `bukan json`,
		"token kepanjangan": `{"push_token":"` + strings.Repeat("x", 513) + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			devices := &stubPushRegistrar{}
			r, jwtMgr := newDeviceTestRouter(t, devices)
			bearer := bearerForDevice(t, jwtMgr, uuid.New(), "device-nurholis-001")

			rec := postPushToken(t, r, bearer, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if code := errorCodeOf(t, rec); code != apperr.ValidationError.Code {
				t.Errorf("error code: %s, want %s", code, apperr.ValidationError.Code)
			}
			if devices.calls != 0 {
				t.Error("body tidak sah tidak boleh sampai ke repository")
			}
		})
	}

	// Tepat 512 karakter masih diterima — batasnya inklusif.
	t.Run("tepat 512 diterima", func(t *testing.T) {
		devices := &stubPushRegistrar{}
		r, jwtMgr := newDeviceTestRouter(t, devices)
		bearer := bearerForDevice(t, jwtMgr, uuid.New(), "device-nurholis-001")

		rec := postPushToken(t, r, bearer, `{"push_token":"`+strings.Repeat("x", 512)+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

// Perangkat yang sudah dicabut: RegisterPushToken tidak menemukan baris aktif dan
// menjawab DeviceNotRecognized. Handler harus meneruskannya sebagai 403, bukan
// 200 tanpa efek — 200 membuat aplikasi yakin token tersimpan padahal tidak, dan
// nasabah berhenti menerima notifikasi tanpa satu pun tanda.
func TestRegisterPushToken_RevokedDeviceIsRejected(t *testing.T) {
	devices := &stubPushRegistrar{err: apperr.DeviceNotRecognized}
	r, jwtMgr := newDeviceTestRouter(t, devices)
	bearer := bearerForDevice(t, jwtMgr, uuid.New(), "device-yang-sudah-dicabut")

	rec := postPushToken(t, r, bearer, `{"push_token":"fcm-token-abc"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, rec); code != apperr.DeviceNotRecognized.Code {
		t.Errorf("error code: %s, want %s", code, apperr.DeviceNotRecognized.Code)
	}
}

func TestRegisterPushToken_RequiresAuth(t *testing.T) {
	devices := &stubPushRegistrar{}
	r, _ := newDeviceTestRouter(t, devices)

	rec := postPushToken(t, r, "", `{"push_token":"fcm-token-abc"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if devices.calls != 0 {
		t.Error("permintaan tanpa token tidak boleh menyentuh repository")
	}
}
