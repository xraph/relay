package delivery

import "time"

// Decision is the outcome of evaluating a delivery attempt.
type Decision int

const (
	// Delivered means the delivery was successful (2xx).
	Delivered Decision = iota

	// Retry means the delivery should be retried later.
	Retry

	// DLQ means the delivery has permanently failed and should move to the dead letter queue.
	DLQ

	// DisableEndpoint means the endpoint should be disabled (e.g., 410 Gone).
	DisableEndpoint
)

// Result holds the outcome of a single delivery attempt.
type Result struct {
	StatusCode int
	Error      string
	Response   string
	LatencyMs  int
}

// Retrier decides what to do after a delivery attempt.
type Retrier struct {
	schedule []time.Duration
}

// NewRetrier creates a retrier with the given backoff schedule.
func NewRetrier(schedule []time.Duration) *Retrier {
	return &Retrier{schedule: schedule}
}

// Decide determines what to do with a delivery after an attempt.
//
// Decision matrix:
//   - 2xx → Delivered
//   - 410 → DisableEndpoint + DLQ
//   - 400–499 (except 410, 429) → DLQ immediately (client error won't self-correct)
//   - 429 → Retry (rate limited)
//   - 500–599 → Retry if attempts < max, else DLQ
//   - 0 (connection/timeout error) → Retry if attempts < max, else DLQ
func (r *Retrier) Decide(res Result, d *Delivery) Decision {
	code := res.StatusCode

	// 2xx → success
	if code >= 200 && code < 300 {
		return Delivered
	}

	// 410 Gone → disable endpoint
	if code == 410 {
		return DisableEndpoint
	}

	// 429 Too Many Requests → always retry (if within limits)
	if code == 429 {
		return r.retryOrDLQ(d)
	}

	// 400–499 (client errors) → DLQ immediately
	if code >= 400 && code < 500 {
		return DLQ
	}

	// 500–599 or 0 (network error) → retry if possible
	return r.retryOrDLQ(d)
}

// retryOrDLQ returns Retry if the delivery has attempts remaining, otherwise DLQ.
func (r *Retrier) retryOrDLQ(d *Delivery) Decision {
	if d.AttemptCount < d.MaxAttempts {
		return Retry
	}
	return DLQ
}

// ComputeNextAttempt returns the time at which the next attempt should be made.
func (r *Retrier) ComputeNextAttempt(attemptCount int) time.Time {
	idx := attemptCount - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(r.schedule) {
		idx = len(r.schedule) - 1
	}
	return time.Now().UTC().Add(r.schedule[idx])
}
