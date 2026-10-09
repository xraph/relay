package observability

import (
	"context"
	"sync"
	"time"

	gu "github.com/xraph/go-utils/metrics"
)

// Metrics holds metric instruments for Relay, backed by any go-utils MetricFactory
// (e.g. the forge-managed metrics system via fapp.Metrics()).
type Metrics struct {
	pendingOnce sync.Once
	pendingGate chan struct{}
	// EventsSentTotal counts observed new reliable commits and successful legacy
	// fanouts. Receipt recovery does not reconstruct historical counter values.
	EventsSentTotal gu.Counter
	DeliveriesTotal gu.Counter
	DeliveryLatency gu.Histogram
	DLQSize         gu.Gauge
	// PendingDeliveries is the last successful store CountPending observation.
	PendingDeliveries gu.Gauge
}

// NewMetrics creates Relay metric instruments using the supplied factory.
// Pass fapp.Metrics() from a forge extension, or metrics.NewMetricsCollector()
// for standalone usage.
func NewMetrics(factory gu.MetricFactory) *Metrics {
	return &Metrics{
		EventsSentTotal:   factory.Counter("relay_events_sent_total"),
		DeliveriesTotal:   factory.Counter("relay_deliveries_total"),
		DeliveryLatency:   factory.Histogram("relay_delivery_latency_seconds"),
		DLQSize:           factory.Gauge("relay_dlq_size"),
		PendingDeliveries: factory.Gauge("relay_pending_deliveries"),
	}
}

// RecordDelivery records a delivery attempt with the given status and latency.
func (m *Metrics) RecordDelivery(status string, latencySeconds float64) {
	m.DeliveriesTotal.WithLabels(map[string]string{"status": status}).Inc()
	m.DeliveryLatency.Observe(latencySeconds)
}

// PendingCounter supplies an authoritative snapshot of pending delivery rows.
type PendingCounter interface {
	CountPending(context.Context) (int64, error)
}

// SyncPending serializes query and publication so concurrent observations using
// this metrics object cannot publish in reverse order. A failed read preserves
// the last successful observation. In-flight rows follow the backend's pending
// state semantics; this gauge is not a count of all unfinished HTTP requests.
func (m *Metrics) SyncPending(ctx context.Context, store PendingCounter) error {
	// The deadline covers admission as well as the database query. A committed
	// acceptance never waits indefinitely behind another telemetry sample.
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	m.pendingOnce.Do(func() { m.pendingGate = make(chan struct{}, 1) })
	select {
	case m.pendingGate <- struct{}{}:
		defer func() { <-m.pendingGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	count, err := store.CountPending(ctx)
	if err != nil {
		return err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	m.PendingDeliveries.Set(float64(count))
	return nil
}
