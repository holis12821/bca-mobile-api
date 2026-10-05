package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/holis12821/bca-mobile-api/internal/domain/content"
	"github.com/holis12821/bca-mobile-api/internal/handler"
)

type stubContentReader struct {
	help    *content.HelpCenterResponse
	contact *content.ContactCS
	err     error
}

func (s *stubContentReader) HelpCenter(context.Context) (*content.HelpCenterResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.help, nil
}

func (s *stubContentReader) ContactCS(context.Context) (*content.ContactCS, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.contact, nil
}

// newContentRouter mounts the two routes with NO auth middleware — which is the
// behaviour under test as much as the bodies are.
func newContentRouter(svc handler.ContentReader) *chi.Mux {
	h := handler.NewContentHandler(svc)
	r := chi.NewRouter()
	r.Get("/content/help-center", h.HelpCenter)
	r.Get("/content/contact-cs", h.ContactCS)
	return r
}

// Both pages must answer without an Authorization header. A customer locked out
// of the app is exactly the one who needs the CS number.
func TestContent_NoAuthorizationRequired(t *testing.T) {
	stub := &stubContentReader{
		help: &content.HelpCenterResponse{Categories: []content.HelpCategory{{
			Key:   "CARD",
			Title: "Kartu Paspor",
			Items: []content.HelpItem{{Question: "Q", Answer: "A"}},
		}}},
		contact: &content.ContactCS{Phone: "1500888", Hours: "24 jam setiap hari"},
	}
	r := newContentRouter(stub)

	for _, path := range []string{"/content/help-center", "/content/contact-cs"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200 without a token, got %d: %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Errorf("%s: static content should carry a Cache-Control header", path)
		}
	}
}

func TestContent_HelpCenterShape(t *testing.T) {
	stub := &stubContentReader{
		help: &content.HelpCenterResponse{Categories: []content.HelpCategory{{
			Key:   "SECURITY",
			Title: "Keamanan & PIN",
			Items: []content.HelpItem{{Question: "Apa beda kode akses dan PIN?", Answer: "Kode akses untuk masuk."}},
		}}},
	}
	rec := httptest.NewRecorder()
	newContentRouter(stub).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/help-center", nil))

	var env struct {
		Data content.HelpCenterResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v — %s", err, rec.Body.String())
	}
	if len(env.Data.Categories) != 1 || env.Data.Categories[0].Key != "SECURITY" {
		t.Fatalf("unexpected categories: %+v", env.Data.Categories)
	}
	if len(env.Data.Categories[0].Items) != 1 {
		t.Errorf("expected one item in the category")
	}
}

// An empty table answers 200 with an empty list, not 404: the screen renders
// "belum ada artikel" from a list and an error page from a 404.
func TestContent_EmptyHelpCenterIsEmptyArray(t *testing.T) {
	stub := &stubContentReader{help: &content.HelpCenterResponse{Categories: []content.HelpCategory{}}}
	rec := httptest.NewRecorder()
	newContentRouter(stub).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/help-center", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("invalid JSON: %s", rec.Body.String())
	}
	// "categories":[] and not "categories":null — the client iterates it.
	if got := rec.Body.String(); !strings.Contains(got, `"categories":[]`) {
		t.Errorf("expected an empty array, got %s", got)
	}
}

func TestContent_RepositoryErrorIs500(t *testing.T) {
	stub := &stubContentReader{err: errors.New("db down")}
	rec := httptest.NewRecorder()
	newContentRouter(stub).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/content/contact-cs", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}
