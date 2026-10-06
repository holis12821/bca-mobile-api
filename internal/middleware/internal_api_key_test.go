package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalAPIKey_ValidKey(t *testing.T) {
	handler := InternalAPIKey("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Internal-API-Key", "test-secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestInternalAPIKey_MissingKey(t *testing.T) {
	handler := InternalAPIKey("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestInternalAPIKey_WrongKey(t *testing.T) {
	handler := InternalAPIKey("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Internal-API-Key", "wrong-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// --- AgentAuth: cakupan kewenangan ---

// stubAgentLookup memenuhi AgentLookup tanpa Postgres maupun Argon2.
type stubAgentLookup struct {
	name   string
	scopes []string
	ok     bool
	err    error
}

func (s stubAgentLookup) AuthenticateAgent(_ context.Context, _, _ string) (string, []string, bool, error) {
	return s.name, s.scopes, s.ok, s.err
}

// agentReq menyusun permintaan yang sudah membawa kedua header petugas.
func agentReq() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Agent-Employee-ID", "CS-1042")
	req.Header.Set("X-Agent-API-Key", "secret")
	return req
}

func serveAgent(t *testing.T, lookup AgentLookup, scope string, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	handler := AgentAuth(lookup, scope)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestAgentAuth_GrantsWhenScopeHeld(t *testing.T) {
	lookup := stubAgentLookup{name: "Sarah Adisti", scopes: []string{ScopeVideoCall}, ok: true}

	if rr := serveAgent(t, lookup, ScopeVideoCall, agentReq()); rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// Inti dari pemisahan kewenangan: kredensialnya SAH, dan tetap ditolak.
//
// Sebelum scope ada, petugas video call yang memegang INTERNAL_API_KEY bisa mengubah
// katalog kartu untuk seluruh nasabah. Test ini yang menahan jalur itu tetap tertutup.
func TestAgentAuth_RejectsValidAgentWithoutScope(t *testing.T) {
	lookup := stubAgentLookup{name: "Sarah Adisti", scopes: []string{ScopeVideoCall}, ok: true}

	rr := serveAgent(t, lookup, ScopeCardAdmin, agentReq())
	if rr.Code != http.StatusForbidden {
		t.Errorf("petugas video call menembus jalur CARD_ADMIN: got %d, want 403", rr.Code)
	}
}

func TestAgentAuth_GrantsWhenAgentHoldsBothScopes(t *testing.T) {
	lookup := stubAgentLookup{
		name:   "Operator",
		scopes: []string{ScopeVideoCall, ScopeCardAdmin},
		ok:     true,
	}

	for _, scope := range []string{ScopeVideoCall, ScopeCardAdmin} {
		if rr := serveAgent(t, lookup, scope, agentReq()); rr.Code != http.StatusOK {
			t.Errorf("scope %s: expected 200, got %d", scope, rr.Code)
		}
	}
}

// Rute yang terpasang tanpa menyatakan kewenangannya adalah kesalahan perakitan, dan
// kesalahan perakitan di jalur operator tidak boleh gagal terbuka.
func TestAgentAuth_DeniesWhenScopeNotDeclared(t *testing.T) {
	lookup := stubAgentLookup{name: "Sarah Adisti", scopes: []string{ScopeVideoCall}, ok: true}

	if rr := serveAgent(t, lookup, "", agentReq()); rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestAgentAuth_DeniesWithoutLookup(t *testing.T) {
	if rr := serveAgent(t, nil, ScopeVideoCall, agentReq()); rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestAgentAuth_DeniesMissingHeaders(t *testing.T) {
	lookup := stubAgentLookup{name: "Sarah Adisti", scopes: []string{ScopeVideoCall}, ok: true}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if rr := serveAgent(t, lookup, ScopeVideoCall, req); rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// Postgres tersendat bukan bukti petugasnya tidak berwenang, dan bukan juga alasan
// meluluskannya. 503, bukan 403 — dan terutama bukan 200.
func TestAgentAuth_InfraFailureIsUnavailableNotDenied(t *testing.T) {
	lookup := stubAgentLookup{err: errors.New("connection refused")}

	rr := serveAgent(t, lookup, ScopeVideoCall, agentReq())
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

// Petugas yang lolos membawa cakupannya ke handler, bukan hanya namanya.
func TestAgentAuth_PutsIdentityInContext(t *testing.T) {
	lookup := stubAgentLookup{name: "Sarah Adisti", scopes: []string{ScopeVideoCall}, ok: true}

	var gotID, gotName string
	var gotOK bool
	handler := AgentAuth(lookup, ScopeVideoCall)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, gotName, gotOK = AgentFromCtx(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), agentReq())

	if !gotOK || gotID != "CS-1042" || gotName != "Sarah Adisti" {
		t.Fatalf("identitas tidak sampai ke handler: id=%q name=%q ok=%v", gotID, gotName, gotOK)
	}
}
