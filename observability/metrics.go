package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds Prometheus metrics for Relay.
type Metrics struct {
	EventsSentTotal   prometheus.Counter
	DeliveriesTotal   *prometheus.CounterVec
	DeliveryLatency   prometheus.Histogram
	DLQSize           prometheus.Gauge
	PendingDeliveries prometheus.Gauge
}

// NewMetrics creates and registers Relay Prometheus metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)

	return &Metrics{
		EventsSentTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "relay_events_sent_total",
			Help: "Total number of events submitted to Relay.",
		}),
		DeliveriesTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_deliveries_total",
			Help: "Total number of delivery attempts by status.",
		}, []string{"status"}),
		DeliveryLatency: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "relay_delivery_latency_seconds",
			Help:    "Delivery HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}),
		DLQSize: factory.NewGauge(prometheus.GaugeOpts{
			Name: "relay_dlq_size",
			Help: "Current number of entries in the dead letter queue.",
		}),
		PendingDeliveries: factory.NewGauge(prometheus.GaugeOpts{
			Name: "relay_pending_deliveries",
			Help: "Current number of pending deliveries.",
		}),
	}
}

// RecordDelivery records a delivery attempt with the given status.
func (m *Metrics) RecordDelivery(status string, latencySeconds float64) {
	m.DeliveriesTotal.WithLabelValues(status).Inc()
	m.DeliveryLatency.Observe(latencySeconds)
}
