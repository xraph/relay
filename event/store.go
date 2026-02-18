package event

import (
	"context"

	"github.com/xraph/relay/id"
)

// Store defines the persistence contract for webhook events.
type Store interface {
	// CreateEvent persists an event. Must be durable before returning.
	CreateEvent(ctx context.Context, evt *Event) error

	// GetEvent returns an event by ID.
	GetEvent(ctx context.Context, evtID id.ID) (*Event, error)

	// ListEvents returns events, optionally filtered by type, tenant, or time range.
	ListEvents(ctx context.Context, opts ListOpts) ([]*Event, error)

	// ListEventsByTenant returns events for a specific tenant.
	ListEventsByTenant(ctx context.Context, tenantID string, opts ListOpts) ([]*Event, error)
}
