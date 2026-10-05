package content_test

import (
	"context"
	"errors"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/domain/content"
)

// --- Mock ---

type mockRepo struct {
	help    *content.HelpCenterResponse
	contact *content.ContactCS
	err     error
	calls   int
}

func (m *mockRepo) HelpCenter(context.Context) (*content.HelpCenterResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.help, nil
}

func (m *mockRepo) ContactCS(context.Context) (*content.ContactCS, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.contact, nil
}

type mockCache struct {
	help    *content.HelpCenterResponse
	contact *content.ContactCS

	getErr error
	setErr error

	sets int
}

func (m *mockCache) GetHelpCenter(context.Context) (*content.HelpCenterResponse, error) {
	return m.help, m.getErr
}

func (m *mockCache) SetHelpCenter(_ context.Context, resp *content.HelpCenterResponse) error {
	m.sets++
	m.help = resp
	return m.setErr
}

func (m *mockCache) GetContactCS(context.Context) (*content.ContactCS, error) {
	return m.contact, m.getErr
}

func (m *mockCache) SetContactCS(_ context.Context, c *content.ContactCS) error {
	m.sets++
	m.contact = c
	return m.setErr
}

func sampleHelp() *content.HelpCenterResponse {
	return &content.HelpCenterResponse{
		Categories: []content.HelpCategory{{
			Key:   "CARD",
			Title: "Kartu Paspor",
			Items: []content.HelpItem{{
				Question: "Bagaimana cara memblokir kartu?",
				Answer:   "Buka Profil Saya, pilih kartu, lalu tekan Blokir Kartu.",
			}},
		}},
	}
}

// --- Tests ---

func TestHelpCenter_CacheHitSkipsDatabase(t *testing.T) {
	repo := &mockRepo{help: sampleHelp()}
	cache := &mockCache{help: sampleHelp()}
	svc := content.NewService(content.ServiceConfig{Repo: repo, Cache: cache})

	if _, err := svc.HelpCenter(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.calls != 0 {
		t.Errorf("a cache hit must not reach the database, got %d calls", repo.calls)
	}
}

func TestHelpCenter_CacheMissReadsAndStores(t *testing.T) {
	repo := &mockRepo{help: sampleHelp()}
	cache := &mockCache{}
	svc := content.NewService(content.ServiceConfig{Repo: repo, Cache: cache})

	resp, err := svc.HelpCenter(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.calls != 1 {
		t.Errorf("expected one database read, got %d", repo.calls)
	}
	if cache.sets != 1 {
		t.Errorf("the result should be cached, got %d sets", cache.sets)
	}
	if len(resp.Categories) != 1 {
		t.Fatalf("expected one category, got %d", len(resp.Categories))
	}
}

// Redis being down must not take the help page with it: the database still has
// the answer, and this page is where a locked-out customer finds the CS number.
func TestHelpCenter_CacheFailureFallsBackToDatabase(t *testing.T) {
	repo := &mockRepo{help: sampleHelp()}
	cache := &mockCache{getErr: errors.New("redis down"), setErr: errors.New("redis down")}
	svc := content.NewService(content.ServiceConfig{Repo: repo, Cache: cache})

	resp, err := svc.HelpCenter(context.Background())
	if err != nil {
		t.Fatalf("a cache failure must not fail the request, got %v", err)
	}
	if resp == nil || len(resp.Categories) != 1 {
		t.Fatalf("expected the database answer to come through")
	}
}

func TestContactCS_WorksWithoutCacheConfigured(t *testing.T) {
	repo := &mockRepo{contact: &content.ContactCS{Phone: "1500888", Hours: "24 jam setiap hari"}}
	svc := content.NewService(content.ServiceConfig{Repo: repo})

	contact, err := svc.ContactCS(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contact.Phone != "1500888" {
		t.Errorf("expected the CS number to survive the trip, got %q", contact.Phone)
	}
}

func TestContactCS_DatabaseErrorSurfaces(t *testing.T) {
	repo := &mockRepo{err: errors.New("boom")}
	svc := content.NewService(content.ServiceConfig{Repo: repo})

	if _, err := svc.ContactCS(context.Background()); err == nil {
		t.Fatal("expected the database error to surface")
	}
}
