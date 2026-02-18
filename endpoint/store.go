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
	ListEndpoints(ctx context.Context, tenantID string, opts ListOpts) ([]*Endpoint, error)

	// Resolve finds all active endpoints matching an event type for a tenant.
	// This is the hot path — called on every relay.Send().
	Resolve(ctx context.Context, tenantID string, eventType string) ([]*Endpoint, error)

	// SetEnabled enables or disables an endpoint without deleting it.
	SetEnabled(ctx context.Context, epID id.ID, enabled bool) error
}
