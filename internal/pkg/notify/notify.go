// Package notify writes in-app notification rows.
//
// The notifications table, its endpoints and the unread badge all existed, but
// nothing in the codebase ever inserted a row: GET /notifications returned an
// empty list forever and unread_notification_count was permanently 0. This is
// the missing writer, shared by the flows that have something to tell the
// nasabah about.
//
// Every method is best-effort. A notification that cannot be written must
// never fail the transfer it describes, so errors are logged and swallowed.
package notify

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

// Notification types accepted by the table's CHECK constraint.
const (
	TypeTransaction = "TRANSACTION"
	TypePromo       = "PROMO"
	TypeSecurity    = "SECURITY"
	TypeSystem      = "SYSTEM"
	TypeInfo        = "INFO"
)

// Entry is one notification to write.
type Entry struct {
	UserID   uuid.UUID
	Type     string
	Title    string
	Body     string
	DeepLink string
	Metadata map[string]any
}

// Store persists notification rows. Implemented by postgres.NotificationRepo.
type Store interface {
	Insert(ctx context.Context, e Entry) error
}

// Notifier is the façade the domain services hold.
type Notifier struct {
	store Store
	// pusher delivers the same content to the device. Optional: when no
	// provider is configured the row is still written and the app sees it on
	// its next poll.
	pusher Pusher
}

// Pusher delivers a notification to a user's registered devices.
type Pusher interface {
	Push(ctx context.Context, userID uuid.UUID, title, body string, data map[string]string) error
}

func New(store Store, pusher Pusher) *Notifier {
	return &Notifier{store: store, pusher: pusher}
}

// Write records the entry and, if a pusher is configured, delivers it.
func (n *Notifier) Write(ctx context.Context, e Entry) {
	if n == nil || n.store == nil {
		return
	}
	if e.Type == "" {
		e.Type = TypeInfo
	}
	if err := n.store.Insert(ctx, e); err != nil {
		slog.Error("write notification failed",
			"user_id", e.UserID, "type", e.Type, "error", err)
		return
	}
	if n.pusher == nil {
		return
	}
	data := map[string]string{"type": e.Type}
	if e.DeepLink != "" {
		data["deep_link"] = e.DeepLink
	}
	if err := n.pusher.Push(ctx, e.UserID, e.Title, e.Body, data); err != nil {
		slog.Warn("push notification failed", "user_id", e.UserID, "error", err)
	}
}

// Transaction records a completed transaction for the nasabah.
func (n *Notifier) Transaction(ctx context.Context, userID uuid.UUID, title, body, deepLink string, metadata map[string]any) {
	n.Write(ctx, Entry{
		UserID:   userID,
		Type:     TypeTransaction,
		Title:    title,
		Body:     body,
		DeepLink: deepLink,
		Metadata: metadata,
	})
}

// Security records an event the nasabah needs to know about for safety
// reasons — a revoked session, a changed credential, a suspicious login.
func (n *Notifier) Security(ctx context.Context, userID uuid.UUID, title, body string) {
	n.Write(ctx, Entry{
		UserID: userID,
		Type:   TypeSecurity,
		Title:  title,
		Body:   body,
	})
}
