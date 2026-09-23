package endpoint

import (
	"context"

	"github.com/xraph/relay/id"
)

// Store defines the persistence contract for webhook endpoints.
type Store interface {
	// CreateEndpoint persists a new endpoint.
	CreateEndpoint(ctx context.Context, ep *Endpoint) error

	// GetEndpoint returns an endpoint by ID.
	GetEndpoint(ctx context.Context, epID id.ID) (*Endpoint, error)

	// UpdateEndpoint modifies an existing endpoint.
	UpdateEndpoint(ctx context.Context, ep *Endpoint) error

	// DeleteEndpoint removes an endpoint.
	DeleteEndpoint(ctx context.Context, epID id.ID) error

	// ListEndpoints returns endpoints for a tenant, optionally filtered.
	//
	// Every implementation must honour this contract, because callers page
	// through it and treat what it returns as complete:
	//
	//   - An empty tenantID means every tenant. Any other value, whitespace
	//     included, is matched exactly.
	//   - opts.Enabled, when set, keeps only endpoints in that state.
	//   - opts.Offset and opts.Limit must both be honoured. A caller paging to
	//     the end stops at a short page; ignoring Offset makes it loop forever.
	//   - Results come in a total order that is the same on every call, so
	//     that paging by Offset neither skips nor repeats. created_at alone is
	//     not enough, because timestamps tie; break ties on id.
	//
	// Filter before paging. A page shortened by filtering reads as the last
	// page to a caller walking to the end.
	ListEndpoints(ctx context.Context, tenantID string, opts ListOpts) ([]*Endpoint, error)

	// Resolve finds all active endpoints matching an event type for a tenant.
	// This is the hot path — called on every relay.Send().
	Resolve(ctx context.Context, tenantID string, eventType string) ([]*Endpoint, error)

	// SetEnabled enables or disables an endpoint without deleting it.
	SetEnabled(ctx context.Context, epID id.ID, enabled bool) error
}
