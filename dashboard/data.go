package dashboard

import (
	"context"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
)

// fetchEventTypeCount returns the total number of registered event types.
func fetchEventTypeCount(ctx context.Context, r *relay.Relay) int {
	types, err := r.Catalog().ListTypes(ctx, catalog.ListOpts{Limit: 1000})
	if err != nil {
		return 0
	}
	return len(types)
}

// fetchPendingCount returns the number of pending deliveries.
func fetchPendingCount(ctx context.Context, r *relay.Relay) int64 {
	count, err := r.Store().CountPending(ctx)
	if err != nil {
		return 0
	}
	return count
}

// fetchDLQCount returns the total number of DLQ entries.
func fetchDLQCount(ctx context.Context, r *relay.Relay) int64 {
	count, err := r.Store().CountDLQ(ctx)
	if err != nil {
		return 0
	}
	return count
}

// fetchEventTypes returns event types with the given options.
func fetchEventTypes(ctx context.Context, r *relay.Relay, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	return r.Catalog().ListTypes(ctx, opts)
}

// fetchEndpoints returns endpoints for a tenant.
func fetchEndpoints(ctx context.Context, r *relay.Relay, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	return r.Endpoints().List(ctx, tenantID, opts)
}

// fetchAllEndpoints returns all endpoints across all tenants.
// Uses store directly with empty tenant to get all.
func fetchAllEndpoints(ctx context.Context, r *relay.Relay, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	return r.Store().ListEndpoints(ctx, "", opts)
}

// fetchEvents returns events with the given options.
func fetchEvents(ctx context.Context, r *relay.Relay, opts event.ListOpts) ([]*event.Event, error) {
	return r.Store().ListEvents(ctx, opts)
}

// fetchDeliveriesByEndpoint returns deliveries for a specific endpoint.
func fetchDeliveriesByEndpoint(ctx context.Context, r *relay.Relay, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	return r.Store().ListByEndpoint(ctx, epID, opts)
}

// fetchDeliveriesByEvent returns all deliveries for a specific event.
func fetchDeliveriesByEvent(ctx context.Context, r *relay.Relay, evtID id.ID) ([]*delivery.Delivery, error) {
	return r.Store().ListByEvent(ctx, evtID)
}

// fetchDLQEntries returns DLQ entries with the given options.
func fetchDLQEntries(ctx context.Context, r *relay.Relay, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	return r.Store().ListDLQ(ctx, opts)
}
