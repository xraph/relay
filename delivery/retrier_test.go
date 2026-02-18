package delivery_test

import (
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
)

func TestRetrierDecide(t *testing.T) {
	schedule := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}
	retrier := delivery.NewRetrier(schedule)

	tests := []struct {
		name     string
		result   delivery.Result
		delivery *delivery.Delivery
		want     delivery.Decision
	}{
		{
			name:     "200 OK → Delivered",
			result:   delivery.Result{StatusCode: 200},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Delivered,
		},
		{
			name:     "201 Created → Delivered",
			result:   delivery.Result{StatusCode: 201},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Delivered,
		},
		{
			name:     "204 No Content → Delivered",
			result:   delivery.Result{StatusCode: 204},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Delivered,
		},
		{
			name:     "299 → Delivered",
			result:   delivery.Result{StatusCode: 299},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Delivered,
		},
		{
			name:     "410 Gone → DisableEndpoint",
			result:   delivery.Result{StatusCode: 410},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.DisableEndpoint,
		},
		{
			name:     "429 Too Many Requests → Retry (within limits)",
			result:   delivery.Result{StatusCode: 429},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Retry,
		},
		{
			name:     "429 Too Many Requests → DLQ (exhausted)",
			result:   delivery.Result{StatusCode: 429},
			delivery: &delivery.Delivery{AttemptCount: 5, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "400 Bad Request → DLQ immediately",
			result:   delivery.Result{StatusCode: 400},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "401 Unauthorized → DLQ immediately",
			result:   delivery.Result{StatusCode: 401},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "403 Forbidden → DLQ immediately",
			result:   delivery.Result{StatusCode: 403},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "404 Not Found → DLQ immediately",
			result:   delivery.Result{StatusCode: 404},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "422 Unprocessable → DLQ immediately",
			result:   delivery.Result{StatusCode: 422},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "500 Internal Server Error → Retry (within limits)",
			result:   delivery.Result{StatusCode: 500},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Retry,
		},
		{
			name:     "502 Bad Gateway → Retry (within limits)",
			result:   delivery.Result{StatusCode: 502},
			delivery: &delivery.Delivery{AttemptCount: 2, MaxAttempts: 5},
			want:     delivery.Retry,
		},
		{
			name:     "503 Service Unavailable → Retry (within limits)",
			result:   delivery.Result{StatusCode: 503},
			delivery: &delivery.Delivery{AttemptCount: 3, MaxAttempts: 5},
			want:     delivery.Retry,
		},
		{
			name:     "500 → DLQ (attempts exhausted)",
			result:   delivery.Result{StatusCode: 500},
			delivery: &delivery.Delivery{AttemptCount: 5, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
		{
			name:     "0 (connection error) → Retry (within limits)",
			result:   delivery.Result{StatusCode: 0, Error: "connection refused"},
			delivery: &delivery.Delivery{AttemptCount: 1, MaxAttempts: 5},
			want:     delivery.Retry,
		},
		{
			name:     "0 (timeout) → DLQ (attempts exhausted)",
			result:   delivery.Result{StatusCode: 0, Error: "context deadline exceeded"},
			delivery: &delivery.Delivery{AttemptCount: 5, MaxAttempts: 5},
			want:     delivery.DLQ,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retrier.Decide(tt.result, tt.delivery)
			if got != tt.want {
				t.Errorf("Decide() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRetrierComputeNextAttempt(t *testing.T) {
	schedule := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}
	retrier := delivery.NewRetrier(schedule)

	tests := []struct {
		name         string
		attemptCount int
		wantDelay    time.Duration
	}{
		{"attempt 1 → 5s", 1, 5 * time.Second},
		{"attempt 2 → 30s", 2, 30 * time.Second},
		{"attempt 3 → 2m", 3, 2 * time.Minute},
		{"attempt 4 → 2m (capped at last)", 4, 2 * time.Minute},
		{"attempt 10 → 2m (capped at last)", 10, 2 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().UTC()
			next := retrier.ComputeNextAttempt(tt.attemptCount)
			after := time.Now().UTC()

			expectedMin := before.Add(tt.wantDelay)
			expectedMax := after.Add(tt.wantDelay)

			if next.Before(expectedMin.Add(-time.Millisecond)) || next.After(expectedMax.Add(time.Millisecond)) {
				t.Errorf("ComputeNextAttempt(%d) = %v, expected between %v and %v",
					tt.attemptCount, next, expectedMin, expectedMax)
			}
		})
	}
}

func TestRetrierBoundaryAttemptCount(t *testing.T) {
	schedule := []time.Duration{5 * time.Second}
	retrier := delivery.NewRetrier(schedule)

	// Attempt 0 should use index 0.
	_ = retrier.ComputeNextAttempt(0)

	// Exactly at max attempts → DLQ.
	d := &delivery.Delivery{
		ID:           id.NewDeliveryID(),
		AttemptCount: 3,
		MaxAttempts:  3,
	}
	got := retrier.Decide(delivery.Result{StatusCode: 500}, d)
	if got != delivery.DLQ {
		t.Errorf("expected DLQ at max attempts, got %d", got)
	}

	// One below max → Retry.
	d.AttemptCount = 2
	got = retrier.Decide(delivery.Result{StatusCode: 500}, d)
	if got != delivery.Retry {
		t.Errorf("expected Retry below max, got %d", got)
	}
}
