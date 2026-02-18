package dlq

import (
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Entry represents a permanently failed delivery in the dead letter queue.
type Entry struct {
	entity.Entity

	// ID is the unique TypeID for this DLQ entry.
	ID id.ID `json:"id"`

	// DeliveryID references the failed delivery.
	DeliveryID id.ID `json:"delivery_id"`

	// EventID references the original event.
	EventID id.ID `json:"event_id"`

	// EndpointID references the target endpoint.
	EndpointID id.ID `json:"endpoint_id"`

	// EventType is the event type name for filtering.
	EventType string `json:"event_type"`

	// TenantID identifies the tenant that owns the event.
	TenantID string `json:"tenant_id"`

	// URL is the endpoint URL at the time of failure.
	URL string `json:"url"`

	// Payload is the event data that failed to deliver.
	Payload any `json:"payload"`

	// Error is the error message from the final attempt.
	Error string `json:"error"`

	// AttemptCount is the total number of attempts made.
	AttemptCount int `json:"attempt_count"`

	// LastStatusCode is the HTTP status code from the final attempt.
	LastStatusCode int `json:"last_status_code,omitempty"`

	// ReplayedAt is set when the entry has been replayed.
	ReplayedAt *time.Time `json:"replayed_at,omitempty"`

	// FailedAt is when the delivery permanently failed.
	FailedAt time.Time `json:"failed_at"`
}

// ListOpts configures filtering and pagination for DLQ listing.
type ListOpts struct {
	Offset     int
	Limit      int
	TenantID   string
	EndpointID *id.ID
	From       *time.Time
	To         *time.Time
}
