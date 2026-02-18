package delivery

import (
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// State represents the current state of a delivery.
type State string

const (
	// StatePending indicates the delivery is awaiting attempt.
	StatePending State = "pending"

	// StateDelivered indicates the delivery was successfully sent.
	StateDelivered State = "delivered"

	// StateFailed indicates the delivery permanently failed and was moved to the DLQ.
	StateFailed State = "failed"
)

// Delivery represents a single webhook delivery attempt to an endpoint.
type Delivery struct {
	entity.Entity

	// ID is the unique TypeID for this delivery.
	ID id.ID `json:"id"`

	// EventID references the event being delivered.
	EventID id.ID `json:"event_id"`

	// EndpointID references the target endpoint.
	EndpointID id.ID `json:"endpoint_id"`

	// State is the current delivery state.
	State State `json:"state"`

	// AttemptCount is the number of delivery attempts made so far.
	AttemptCount int `json:"attempt_count"`

	// MaxAttempts is the maximum number of attempts before moving to DLQ.
	MaxAttempts int `json:"max_attempts"`

	// NextAttemptAt is when the next delivery attempt should occur.
	NextAttemptAt time.Time `json:"next_attempt_at"`

	// LastError is the error message from the most recent failed attempt.
	LastError string `json:"last_error,omitempty"`

	// LastStatusCode is the HTTP status code from the most recent attempt.
	LastStatusCode int `json:"last_status_code,omitempty"`

	// LastResponse is the response body from the most recent attempt (capped at 1KB).
	LastResponse string `json:"last_response,omitempty"`

	// LastLatencyMs is the latency in milliseconds of the most recent attempt.
	LastLatencyMs int `json:"last_latency_ms,omitempty"`

	// CompletedAt is when the delivery was completed (delivered or failed).
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ListOpts configures filtering and pagination for delivery listing.
type ListOpts struct {
	Offset int
	Limit  int
	State  *State
}
