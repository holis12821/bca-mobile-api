package content

import (
	"context"
	"fmt"
	"log/slog"
)

// Service reads content through the cache.
type Service struct {
	repo  Repository
	cache Cache
}

type ServiceConfig struct {
	Repo Repository

	// Cache may be nil. Content is small and rarely read; serving it straight
	// from Postgres is a correct, if slower, deployment.
	Cache Cache
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{repo: cfg.Repo, cache: cfg.Cache}
}

// HelpCenter serves GET /v1/content/help-center.
func (s *Service) HelpCenter(ctx context.Context) (*HelpCenterResponse, error) {
	if s.cache != nil {
		cached, err := s.cache.GetHelpCenter(ctx)
		if err != nil {
			// Warn, not fail. Redis being down must not take the FAQ with it.
			slog.Warn("help center cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	resp, err := s.repo.HelpCenter(ctx)
	if err != nil {
		return nil, fmt.Errorf("read help center: %w", err)
	}

	if s.cache != nil {
		if err := s.cache.SetHelpCenter(ctx, resp); err != nil {
			slog.Warn("help center cache set failed", "error", err)
		}
	}
	return resp, nil
}

// ContactCS serves GET /v1/content/contact-cs.
func (s *Service) ContactCS(ctx context.Context) (*ContactCS, error) {
	if s.cache != nil {
		cached, err := s.cache.GetContactCS(ctx)
		if err != nil {
			slog.Warn("contact cs cache get failed", "error", err)
		}
		if cached != nil {
			return cached, nil
		}
	}

	contact, err := s.repo.ContactCS(ctx)
	if err != nil {
		return nil, fmt.Errorf("read contact cs: %w", err)
	}

	if s.cache != nil {
		if err := s.cache.SetContactCS(ctx, contact); err != nil {
			slog.Warn("contact cs cache set failed", "error", err)
		}
	}
	return contact, nil
}
