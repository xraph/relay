package observability

import (
	gu "github.com/xraph/go-utils/metrics"
)

// Metrics holds metric instruments for Relay, backed by any go-utils MetricFactory
// (e.g. the forge-managed metrics system via fapp.Metrics()).
type Metrics struct {
	EventsSentTotal   gu.Counter
	DeliveriesTotal   gu.Counter
	DeliveryLatency   gu.Histogram
	DLQSize           gu.Gauge
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
