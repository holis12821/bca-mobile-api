package middleware_test

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func setupJWT(t *testing.T) *crypto.JWTManager {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kp := &crypto.RSAKeyPair{PrivateKey: privateKey, PublicKey: &privateKey.PublicKey}
	return crypto.NewJWTManager(kp, 15*time.Minute, 7*24*time.Hour)
}

func TestAuth_ValidAccessToken(t *testing.T) {
	jwtMgr := setupJWT(t)
	userID := uuid.New().String()
	sessionID := uuid.New().String()
	deviceID := "test-device"

	pair, err := jwtMgr.GenerateTokenPair(userID, sessionID, deviceID)
	if err != nil {
		t.Fatal(err)
	}

	var gotUserID, gotSessionID, gotDeviceID string
	handler := middleware.Auth(jwtMgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = middleware.UserIDFromCtx(r.Context())
		gotSessionID = middleware.SessionIDFromCtx(r.Context())
		gotDeviceID = middleware.DeviceIDFromCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUserID != userID {
		t.Fatalf("expected user_id %s, got %s", userID, gotUserID)
	}
	if gotSessionID != sessionID {
		t.Fatalf("expected session_id %s, got %s", sessionID, gotSessionID)
	}
	if gotDeviceID != deviceID {
		t.Fatalf("expected device_id %s, got %s", deviceID, gotDeviceID)
	}
}

func TestAuth_RefreshTokenRejected(t *testing.T) {
	jwtMgr := setupJWT(t)
	pair, _ := jwtMgr.GenerateTokenPair(uuid.New().String(), uuid.New().String(), "device")

	handler := middleware.Auth(jwtMgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.RefreshToken)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuth_NoToken(t *testing.T) {
	jwtMgr := setupJWT(t)
	handler := middleware.Auth(jwtMgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}