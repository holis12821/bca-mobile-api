package content

import "context"

// Repository reads the two content tables (migration 000023). Implemented by
// repository/postgres.ContentRepo — this domain knows no SQL.
type Repository interface {
	// HelpCenter returns the active FAQ, already grouped and ordered.
	//
	// An empty database yields an empty category list and a nil error, not a
	// not-found: the screen renders "belum ada artikel" from an empty list, and
	// a 404 would make it show an error instead.
	HelpCenter(ctx context.Context) (*HelpCenterResponse, error)

	// ContactCS returns the single contact row.
	ContactCS(ctx context.Context) (*ContactCS, error)
}

// Cache is the optional Redis layer. Both pages are identical for every
// customer and change a few times a year, which is the rare case where a long
// TTL is right.
//
// Every method is allowed to fail: a cache error must degrade to a database
// read, never to an error page. The service logs and carries on.
type Cache interface {
	GetHelpCenter(ctx context.Context) (*HelpCenterResponse, error)
	SetHelpCenter(ctx context.Context, resp *HelpCenterResponse) error

	GetContactCS(ctx context.Context) (*ContactCS, error)
	SetContactCS(ctx context.Context, contact *ContactCS) error
}
