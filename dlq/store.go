package dlq

import (
	"context"
	"time"

	"github.com/xraph/relay/id"
)

// Store defines the persistence contract for the dead letter queue.
type Store interface {
	// Push moves a permanently failed delivery into the DLQ.
	Push(ctx context.Context, entry *Entry) error

	// ListDLQ returns DLQ entries, optionally filtered.
	ListDLQ(ctx context.Context, opts ListOpts) ([]*Entry, error)

	// GetDLQ returns a DLQ entry by ID.
	GetDLQ(ctx context.Context, dlqID id.ID) (*Entry, error)

	// MarkReplayed records that an entry has been replayed. The row is kept:
	// the DLQ is a log, and a marked row is what makes a second replay
	// refusable. Returns relay.ErrDLQNotFound when the entry does not exist.
	//
	// Replay itself is not a store operation. It lives in Service, the only
	// place that can see the configured retry budget.
	MarkReplayed(ctx context.Context, dlqID id.ID, at time.Time) error

	// ReplayBulk replays all DLQ entries in a time window.
	ReplayBulk(ctx context.Context, from, to time.Time) (int64, error)

	// Purge deletes DLQ entries older than a threshold.
	Purge(ctx context.Context, before time.Time) (int64, error)

	// CountDLQ returns the total number of DLQ entries.
	CountDLQ(ctx context.Context) (int64, error)
}
