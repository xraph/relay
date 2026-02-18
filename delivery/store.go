package delivery

import (
	"context"

	"github.com/xraph/relay/id"
)

// Store defines the persistence contract for webhook deliveries.
type Store interface {
	// Enqueue creates a pending delivery.
	Enqueue(ctx context.Context, d *Delivery) error

	// EnqueueBatch creates multiple deliveries atomically (fan-out).
	EnqueueBatch(ctx context.Context, ds []*Delivery) error

	// Dequeue fetches pending deliveries ready for attempt (concurrent-safe).
	// Implementations must ensure no double-delivery (e.g. SKIP LOCKED).
	Dequeue(ctx context.Context, limit int) ([]*Delivery, error)

	// UpdateDelivery modifies a delivery (status, attempt count, next_attempt_at, etc.).
	UpdateDelivery(ctx context.Context, d *Delivery) error

	// GetDelivery returns a delivery by ID.
	GetDelivery(ctx context.Context, delID id.ID) (*Delivery, error)

	// ListByEndpoint returns delivery history for an endpoint.
	ListByEndpoint(ctx context.Context, epID id.ID, opts ListOpts) ([]*Delivery, error)

	// ListByEvent returns all deliveries for a specific event.
	ListByEvent(ctx context.Context, evtID id.ID) ([]*Delivery, error)

	// CountPending returns the number of deliveries awaiting attempt.
	CountPending(ctx context.Context) (int64, error)
}
