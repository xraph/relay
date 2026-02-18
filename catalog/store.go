package catalog

import (
	"context"

	"github.com/xraph/relay/id"
)

// Store defines the persistence contract for the event type catalog.
type Store interface {
	// RegisterType creates or updates an event type definition.
	RegisterType(ctx context.Context, et *EventType) error

	// GetType returns an event type by name (e.g. "invoice.created").
	GetType(ctx context.Context, name string) (*EventType, error)

	// GetTypeByID returns an event type by its TypeID.
	GetTypeByID(ctx context.Context, etID id.ID) (*EventType, error)

	// ListTypes returns all registered event types, optionally filtered.
	ListTypes(ctx context.Context, opts ListOpts) ([]*EventType, error)

	// DeleteType soft-deletes (deprecates) an event type.
	DeleteType(ctx context.Context, name string) error

	// MatchTypes returns event types matching a glob pattern (e.g. "invoice.*").
	MatchTypes(ctx context.Context, pattern string) ([]*EventType, error)
}
