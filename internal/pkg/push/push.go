// Package push delivers notifications to a user's registered devices.
//
// devices.push_token has existed since migration 000001 and nothing ever read
// or wrote it: the app had no way to register a token and the backend had no
// way to reach a device. This package closes the backend half. The transport
// itself (FCM) is still a placeholder — what matters is that the token is
// stored, the call site exists, and a production process is told plainly that
// nothing is being delivered rather than believing it is.
package push

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

// TokenStore reads the push tokens registered for a user.
type TokenStore interface {
	ListPushTokens(ctx context.Context, userID uuid.UUID) ([]string, error)
}

// LoggingPusher resolves the user's tokens and logs what would be sent.
// Development and staging only.
type LoggingPusher struct {
	tokens TokenStore
}

func NewLoggingPusher(tokens TokenStore) *LoggingPusher {
	return &LoggingPusher{tokens: tokens}
}

func (p *LoggingPusher) Push(ctx context.Context, userID uuid.UUID, title, body string, data map[string]string) error {
	tokens, err := p.tokens.ListPushTokens(ctx, userID)
	if err != nil {
		return err
	}
	slog.Info("push (logging pusher)",
		"user_id", userID, "devices", len(tokens), "title", title, "body", body, "data", data)
	return nil
}

// NoopPusher delivers nothing and says so once per call at debug level. This
// is what a production process gets until FCM credentials are wired in; the
// in-app notification row is still written either way, so the user sees the
// message the next time the app polls.
type NoopPusher struct{}

func NewNoopPusher() *NoopPusher { return &NoopPusher{} }

func (NoopPusher) Push(_ context.Context, userID uuid.UUID, _, _ string, _ map[string]string) error {
	slog.Debug("push provider not configured; notification stored in-app only", "user_id", userID)
	return nil
}
