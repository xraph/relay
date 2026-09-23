package delivery

import (
	"time"

	"github.com/xraph/relay/id"
)

// Outcome is what the engine decided after one attempt: the retrier's
// Decision, persisted. It is what lets a retry sequence say which attempt was
// the give-up and why, rather than only showing a run of failures.
type Outcome string

const (
	// OutcomeDelivered means the receiver answered 2xx.
	OutcomeDelivered Outcome = "delivered"

	// OutcomeRetry means another attempt was scheduled.
	OutcomeRetry Outcome = "retry"

	// OutcomeDLQ means the delivery gave up and went to the dead letter
	// queue: retries ran out, or a 4xx that will not fix itself.
	OutcomeDLQ Outcome = "dlq"

	// OutcomeEndpointDisabled means the receiver answered 410 Gone and the
	// endpoint was disabled.
	OutcomeEndpointDisabled Outcome = "endpoint_disabled"
)

// outcomeOf maps the retrier's decision onto its persisted name.
func outcomeOf(d Decision) Outcome {
	switch d {
	case Delivered:
		return OutcomeDelivered
	case Retry:
		return OutcomeRetry
	case DisableEndpoint:
		return OutcomeEndpointDisabled
	default:
		return OutcomeDLQ
	}
}

// Attempt is one HTTP attempt at a delivery.
type Attempt struct {
	ID         id.ID `json:"id"`
	DeliveryID id.ID `json:"delivery_id"`

	// AttemptNum counts from 1.
	AttemptNum int `json:"attempt_num"`

	// StatusCode is 0 when no response came back: a connection error or a
	// timeout, with the reason in Error.
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`

	// Response is the start of the response body, capped like
	// Delivery.LastResponse.
	Response  string  `json:"response,omitempty"`
	LatencyMs int     `json:"latency_ms"`
	Outcome   Outcome `json:"outcome"`

	// NextAttemptAt is set only when Outcome is OutcomeRetry.
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	AttemptedAt   time.Time  `json:"attempted_at"`
}
