package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/card"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// --- Stub ---

type stubAccountCardReader struct {
	resp *card.CardListResponse
	err  error

	// lastUserID records what the handler passed down. The whole point of the
	// endpoint is that this comes from the access token and from nowhere else.
	lastUserID uuid.UUID
	calls      int

	lastCardID      uuid.UUID
	lastIP          string
	lastSettings    card.UpdateSettingsRequest
	lastBlock       card.BlockRequest
	lastReplacement card.ReplacementRequest

	// replayed drives the X-Idempotent-Replayed header on the replacement path.
	replayed bool
}

func (s *stubAccountCardReader) ListCards(_ context.Context, userID uuid.UUID) (*card.CardListResponse, error) {
	s.calls++
	s.lastUserID = userID
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func (s *stubAccountCardReader) UpdateSettings(_ context.Context, userID, cardID uuid.UUID, req card.UpdateSettingsRequest, ip string) (*card.CardDetailResponse, error) {
	s.calls++
	s.lastUserID = userID
	s.lastCardID = cardID
	s.lastSettings = req
	s.lastIP = ip
	if s.err != nil {
		return nil, s.err
	}
	return &card.CardDetailResponse{Card: sampleCardResponse()}, nil
}

func (s *stubAccountCardReader) Block(_ context.Context, userID, cardID uuid.UUID, req card.BlockRequest, ip string) (*card.CardDetailResponse, error) {
	s.calls++
	s.lastUserID = userID
	s.lastCardID = cardID
	s.lastBlock = req
	s.lastIP = ip
	if s.err != nil {
		return nil, s.err
	}
	blocked := sampleCardResponse()
	blocked.Status = card.StatusBlocked
	reason := req.Reason
	blocked.BlockedReason = &reason
	return &card.CardDetailResponse{Card: blocked}, nil
}

func (s *stubAccountCardReader) RequestReplacement(_ context.Context, userID, cardID uuid.UUID, req card.ReplacementRequest, ip string) (*card.ReplacementResponse, bool, error) {
	s.calls++
	s.lastUserID = userID
	s.lastCardID = cardID
	s.lastReplacement = req
	s.lastIP = ip
	if s.err != nil {
		return nil, false, s.err
	}
	return &card.ReplacementResponse{
		RequestID:            "9c1e0d22-bbbb-4ccc-8ddd-eeeeffff0000",
		CardID:               cardID.String(),
		Status:               card.ReplacementStatusNew,
		Reason:               req.Reason,
		DeliveryMethod:       req.DeliveryMethod,
		Fee:                  25000,
		EstimatedArrivalFrom: "2026-10-01",
		EstimatedArrivalTo:   "2026-10-07",
		MaskedNumber:         "•••• •••• •••• 7890",
	}, s.replayed, nil
}

// --- Harness ---
//
// The context keys middleware.Auth writes are unexported, so a test cannot fake
// an authenticated request by stuffing a value into the context. It has to go
// through the real middleware with a real signed token — which is the more
// honest test anyway: it proves the route is actually behind Auth.
//
// The session validator is nil, which middleware.Auth documents as the
// test-only path that skips the session lookup.
func newCardTestRouter(t *testing.T, svc handler.AccountCardReader) (*chi.Mux, *crypto.JWTManager) {
	t.Helper()

	jwtMgr := newTestJWTManager(t)
	h := handler.NewAccountCardHandler(svc)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(jwtMgr, nil))
		r.Get("/account/cards", h.List)
		r.Put("/account/cards/{card_id}/settings", h.UpdateSettings)
		r.Post("/account/cards/{card_id}/block", h.Block)
		r.Post("/account/cards/{card_id}/replacement", h.RequestReplacement)
	})
	return r, jwtMgr
}

func bearerFor(t *testing.T, jwtMgr *crypto.JWTManager, userID uuid.UUID) string {
	t.Helper()
	pair, err := jwtMgr.GenerateTokenPair(userID.String(), uuid.NewString(), "device-test-001")
	if err != nil {
		t.Fatalf("generate token pair: %v", err)
	}
	return "Bearer " + pair.AccessToken
}

// sampleCardResponse memakai card_id TETAP, bukan uuid.NewString(). UUID acak
// bisa memuat deret angka panjang (mis. "…-0041156881af"), dan uji PAN di bawah
// akan gagal secara acak karenanya — bukan karena ada PAN yang bocor.
func sampleCardResponse() card.CardResponse {
	return card.CardResponse{
		CardID:         "3f2a7c10-aaaa-4bbb-8ccc-ddddeeeeffff",
		MaskedNumber:   "•••• •••• •••• 7890",
		CardholderName: "BUDI SANTOSO",
		CardType:       "PASPOR_GOLD",
		ProductName:    "Gold Mastercard",
		Network:        "MASTERCARD",
		TierKey:        "DEBIT",
		Style:          "GOLD",
		ValidThru:      "08/29",
		Status:         card.StatusActive,
		IsPrimary:      true,
		Settings: card.CardSettings{
			DebitOnlineEnabled:   true,
			InternationalEnabled: false,
		},
	}
}

// --- Tests ---

func TestAccountCards_ListHappyPath(t *testing.T) {
	userID := uuid.New()
	stub := &stubAccountCardReader{
		resp: &card.CardListResponse{Cards: []card.CardResponse{sampleCardResponse()}},
	}
	r, jwtMgr := newCardTestRouter(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/account/cards", nil)
	req.Header.Set("Authorization", bearerFor(t, jwtMgr, userID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Status string `json:"status"`
		Data   struct {
			Cards []card.CardResponse `json:"cards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v — %s", err, rec.Body.String())
	}
	if env.Status != "success" {
		t.Errorf("expected status success, got %q", env.Status)
	}
	if len(env.Data.Cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(env.Data.Cards))
	}
	if env.Data.Cards[0].ValidThru != "08/29" {
		t.Errorf("valid_thru should arrive pre-formatted MM/YY, got %q", env.Data.Cards[0].ValidThru)
	}
}

// The user id must come from the token, because nothing in the request can
// carry one. This is what stops /account/cards from being able to read somebody
// else's cards.
func TestAccountCards_UserIDComesFromToken(t *testing.T) {
	userID := uuid.New()
	stub := &stubAccountCardReader{resp: &card.CardListResponse{Cards: []card.CardResponse{}}}
	r, jwtMgr := newCardTestRouter(t, stub)

	// A query parameter naming a different user must change nothing.
	req := httptest.NewRequest(http.MethodGet, "/account/cards?user_id="+uuid.NewString(), nil)
	req.Header.Set("Authorization", bearerFor(t, jwtMgr, userID))
	r.ServeHTTP(httptest.NewRecorder(), req)

	if stub.calls != 1 {
		t.Fatalf("expected the service to be called once, got %d", stub.calls)
	}
	if stub.lastUserID != userID {
		t.Fatalf("expected the token's user id %s, got %s", userID, stub.lastUserID)
	}
}

// A customer with no cards gets 200 and an empty array, never 404: the screen
// has an empty state and would otherwise render an error.
func TestAccountCards_EmptyListIs200WithEmptyArray(t *testing.T) {
	stub := &stubAccountCardReader{resp: &card.CardListResponse{Cards: []card.CardResponse{}}}
	r, jwtMgr := newCardTestRouter(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/account/cards", nil)
	req.Header.Set("Authorization", bearerFor(t, jwtMgr, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a customer without cards, got %d", rec.Code)
	}
	if !regexp.MustCompile(`"cards"\s*:\s*\[\s*\]`).MatchString(rec.Body.String()) {
		t.Fatalf("expected an empty cards array, got %s", rec.Body.String())
	}
}

func TestAccountCards_RequiresToken(t *testing.T) {
	stub := &stubAccountCardReader{resp: &card.CardListResponse{Cards: []card.CardResponse{}}}
	r, _ := newCardTestRouter(t, stub)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/account/cards", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d: %s", rec.Code, rec.Body.String())
	}
	if stub.calls != 0 {
		t.Fatalf("service must not be reached without a token, called %d times", stub.calls)
	}
}

// Two rules from the skill, checked against the actual bytes on the wire:
// no full PAN may leave this endpoint, and no visual value (hex colour or image
// URL) may be sent — the client maps `style` to its own design token.
//
// The PAN check reads the decoded masked_number rather than scanning the whole
// body. Scanning the body was the first shape of this test and it failed
// roughly one run in three: a random UUID in card_id, or an id anywhere else in
// the envelope, can easily contain seven consecutive digits. A test that fails
// on a coin flip teaches people to re-run it, which is exactly how a real leak
// would slip past.
func TestAccountCards_ResponseCarriesNoPANAndNoVisuals(t *testing.T) {
	stub := &stubAccountCardReader{
		resp: &card.CardListResponse{Cards: []card.CardResponse{sampleCardResponse()}},
	}
	r, jwtMgr := newCardTestRouter(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/account/cards", nil)
	req.Header.Set("Authorization", bearerFor(t, jwtMgr, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var env struct {
		Data struct {
			Cards []card.CardResponse `json:"cards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v — %s", err, rec.Body.String())
	}
	if len(env.Data.Cards) == 0 {
		t.Fatal("test premise broken: expected at least one card to inspect")
	}

	longDigitRun := regexp.MustCompile(`[0-9]{7,}`)
	hasMask := regexp.MustCompile(`[\x{2022}*xX]`)
	for _, c := range env.Data.Cards {
		if longDigitRun.MatchString(c.MaskedNumber) {
			t.Errorf("masked_number %q carries a long digit run — a PAN would look like this", c.MaskedNumber)
		}
		if !hasMask.MatchString(c.MaskedNumber) {
			t.Errorf("masked_number %q has no mask character at all", c.MaskedNumber)
		}
	}

	// Visual values are deterministic, so the whole envelope can be scanned.
	body := rec.Body.String()
	for _, forbidden := range []string{"#", "http://", "https://", "color", "image"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response carries a visual value %q, which belongs to the client: %s", forbidden, body)
		}
	}
}

// --- Jalur tulis: settings, block, replacement ---

// doCardWrite sends an authenticated request and returns the recorder.
func doCardWrite(t *testing.T, r *chi.Mux, jwtMgr *crypto.JWTManager, userID uuid.UUID,
	method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerFor(t, jwtMgr, userID))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAccountCards_UpdateSettingsPassesCardIDFromURL(t *testing.T) {
	userID := uuid.New()
	cardID := uuid.New()
	stub := &stubAccountCardReader{}
	r, jwtMgr := newCardTestRouter(t, stub)

	rec := doCardWrite(t, r, jwtMgr, userID, http.MethodPut,
		"/account/cards/"+cardID.String()+"/settings",
		`{"international_enabled":true}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if stub.lastUserID != userID {
		t.Errorf("user id must come from the token, got %s", stub.lastUserID)
	}
	if stub.lastCardID != cardID {
		t.Errorf("card id must come from the URL, got %s", stub.lastCardID)
	}
	// The absent field must stay absent — a nil pointer is what tells the
	// service "do not touch debit online".
	if stub.lastSettings.DebitOnlineEnabled != nil {
		t.Errorf("omitted field should decode to nil, got %v", *stub.lastSettings.DebitOnlineEnabled)
	}
	if stub.lastSettings.InternationalEnabled == nil || !*stub.lastSettings.InternationalEnabled {
		t.Errorf("international_enabled should decode to true")
	}
}

// A card id that is not a UUID is answered exactly like one that does not
// exist. Anything else would let a caller tell "malformed" from "not yours".
func TestAccountCards_MalformedCardIDIsNotFound(t *testing.T) {
	stub := &stubAccountCardReader{}
	r, jwtMgr := newCardTestRouter(t, stub)

	rec := doCardWrite(t, r, jwtMgr, uuid.New(), http.MethodPut,
		"/account/cards/not-a-uuid/settings", `{"debit_online_enabled":false}`, nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if stub.calls != 0 {
		t.Errorf("the service must not be reached for a malformed card id, got %d calls", stub.calls)
	}
	if !strings.Contains(rec.Body.String(), "CARD_NOT_FOUND") {
		t.Errorf("expected CARD_NOT_FOUND, got %s", rec.Body.String())
	}
}

func TestAccountCards_BlockReturnsBlockedCard(t *testing.T) {
	stub := &stubAccountCardReader{}
	r, jwtMgr := newCardTestRouter(t, stub)

	rec := doCardWrite(t, r, jwtMgr, uuid.New(), http.MethodPost,
		"/account/cards/"+uuid.NewString()+"/block",
		`{"reason":"LOST","verification_token":"vt_abc"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if stub.lastBlock.Reason != "LOST" || stub.lastBlock.VerificationToken != "vt_abc" {
		t.Errorf("block request did not reach the service intact: %+v", stub.lastBlock)
	}

	var env struct {
		Data struct {
			Card card.CardResponse `json:"card"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v — %s", err, rec.Body.String())
	}
	if env.Data.Card.Status != card.StatusBlocked {
		t.Errorf("expected BLOCKED, got %q", env.Data.Card.Status)
	}
	if env.Data.Card.BlockedReason == nil || *env.Data.Card.BlockedReason != "LOST" {
		t.Errorf("a blocked card must say why it is blocked")
	}
}

// The idempotency key is read from the header, the same place /transfer/execute
// reads its own. A body field would be lost by anything that rewrites bodies.
func TestAccountCards_ReplacementReadsIdempotencyKeyFromHeader(t *testing.T) {
	stub := &stubAccountCardReader{}
	r, jwtMgr := newCardTestRouter(t, stub)

	rec := doCardWrite(t, r, jwtMgr, uuid.New(), http.MethodPost,
		"/account/cards/"+uuid.NewString()+"/replacement",
		`{"reason":"DAMAGED","delivery_method":"COURIER","verification_token":"vt_x"}`,
		map[string]string{"X-Idempotency-Key": "idem-123"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if stub.lastReplacement.IdempotencyKey != "idem-123" {
		t.Errorf("expected the header's key, got %q", stub.lastReplacement.IdempotencyKey)
	}
	if rec.Header().Get("X-Idempotent-Replayed") != "" {
		t.Errorf("a fresh request must not be marked as a replay")
	}
}

func TestAccountCards_ReplacementReplayIs200WithHeader(t *testing.T) {
	stub := &stubAccountCardReader{replayed: true}
	r, jwtMgr := newCardTestRouter(t, stub)

	rec := doCardWrite(t, r, jwtMgr, uuid.New(), http.MethodPost,
		"/account/cards/"+uuid.NewString()+"/replacement",
		`{"reason":"LOST","verification_token":"vt_x"}`,
		map[string]string{"X-Idempotency-Key": "idem-123"})

	// 200 rather than 201: nothing was created this time.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Idempotent-Replayed") != "true" {
		t.Errorf("a replay must say so in the header")
	}
}

// Every write endpoint sits behind the same auth middleware as the list.
func TestAccountCards_WritesRequireToken(t *testing.T) {
	stub := &stubAccountCardReader{}
	r, _ := newCardTestRouter(t, stub)

	cases := []struct {
		method, path, body string
	}{
		{http.MethodPut, "/account/cards/" + uuid.NewString() + "/settings", `{"debit_online_enabled":true}`},
		{http.MethodPost, "/account/cards/" + uuid.NewString() + "/block", `{"reason":"LOST","verification_token":"v"}`},
		{http.MethodPost, "/account/cards/" + uuid.NewString() + "/replacement", `{"reason":"LOST","verification_token":"v"}`},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without a token, got %d", c.method, c.path, rec.Code)
		}
	}
	if stub.calls != 0 {
		t.Errorf("no request without a token may reach the service, got %d calls", stub.calls)
	}
}
