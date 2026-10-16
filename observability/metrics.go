package observability

import (
	"context"
	"sync"
	"time"

	gu "github.com/xraph/go-utils/metrics"
)

// Metric names, as relay registers them. A Forge app scrapes each one with
// its namespace in front: relay_events_sent_total is
// forge_relay_events_sent_total on /_/metrics.
const (
	MetricEventsSent          = "relay_events_sent_total"
	MetricDeliveriesAttempted = "relay_deliveries_attempted_total"
	MetricDeliveriesSkipped   = "relay_deliveries_skipped_total"
	MetricDeliveryDuration    = "relay_delivery_duration_seconds"
	MetricDLQEntriesPushed    = "relay_dlq_entries_pushed_total"
	MetricPendingDeliveries   = "relay_pending_deliveries"
	MetricDeliveriesInFlight  = "relay_deliveries_in_flight"
)

// Label values. Each label has a fixed set: no metric relay records carries
// an ID, a tenant, an event type, a URL, a host or an error message.
var (
	// attemptOutcomes label relay_deliveries_attempted_total{outcome}. They
	// match the outcome persisted on each delivery attempt.
	attemptOutcomes = []string{"delivered", "retry", "dlq", "endpoint_disabled"}

	// skipReasons label relay_deliveries_skipped_total{reason}: why a
	// dequeued delivery went back to the queue, or failed, with no request.
	skipReasons = []string{"rate_limited", "orphaned", "store_error"}

	// statusClasses label relay_delivery_duration_seconds{status_class}. A
	// request that got no response is "none".
	statusClasses = []string{"2xx", "3xx", "4xx", "5xx", "other", "none"}

	// errorTypes label relay_delivery_duration_seconds{error_type}. A request
	// that got a response is "none".
	errorTypes = []string{"none", "timeout", "canceled", "dns", "tls", "connection", "other"}

	// results label relay_dlq_entries_pushed_total{result}.
	results = []string{"ok", "error"}

	// deliveryDurationBuckets bound one delivery request, in seconds. They run
	// past the default 30s request timeout, so timed-out requests land
	// together at the top.
	deliveryDurationBuckets = []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
)

// Metrics holds relay's metric instruments, backed by any go-utils
// MetricFactory. In a Forge app that is app.Metrics(), and the extension
// wires it for you.
//
// Every labelled series is created up front, one per label value, so each
// one exists at zero before the first delivery and every series in a family
// carries the same label keys. Recording a value outside the documented set
// does nothing.
type Metrics struct {
	pendingOnce sync.Once
	pendingGate chan struct{}

	// EventsSentTotal counts observed new reliable commits and successful legacy
	// fanouts. Receipt recovery does not reconstruct historical counter values.
	EventsSentTotal gu.Counter
	// PendingDeliveries is the last successful store CountPending observation.
	PendingDeliveries gu.Gauge
	// DeliveriesInFlight is the number of deliveries this process is sending
	// right now, out of the engine's Concurrency.
	DeliveriesInFlight gu.Gauge

	attempted map[string]gu.Counter
	skipped   map[string]gu.Counter
	duration  map[[2]string]gu.Histogram
	dlqPushed map[string]gu.Counter
}

// NewMetrics creates Relay metric instruments using the supplied factory.
// Pass fapp.Metrics() from a forge extension, or another go-utils factory for
// standalone use. The factory has to key series by name and labels, as
// Forge's does, or the labelled series collapse into one.
func NewMetrics(factory gu.MetricFactory) *Metrics {
	m := &Metrics{
		EventsSentTotal: factory.Counter(MetricEventsSent,
			gu.WithDescription("Events relay accepted and fanned out")),
		PendingDeliveries: factory.Gauge(MetricPendingDeliveries,
			gu.WithDescription("Deliveries waiting in the store, as of its last count")),
		DeliveriesInFlight: factory.Gauge(MetricDeliveriesInFlight,
			gu.WithDescription("Deliveries this process is sending right now")),
		attempted: counters(factory, MetricDeliveriesAttempted, "outcome", attemptOutcomes,
			"Delivery attempts, by what the engine decided after each one"),
		skipped: counters(factory, MetricDeliveriesSkipped, "reason", skipReasons,
			"Dequeued deliveries the engine did not attempt, by reason"),
		dlqPushed: counters(factory, MetricDLQEntriesPushed, "result", results,
			"Writes of permanently failed deliveries to the dead letter queue"),
		duration: make(map[[2]string]gu.Histogram),
	}

	// A request either got a response (a status class, no error type) or it
	// didn't (an error type, no status class), so only those pairs exist.
	for _, class := range statusClasses {
		for _, errType := range errorTypes {
			if (class == "none") == (errType == "none") {
				continue
			}
			m.duration[[2]string{class, errType}] = factory.Histogram(MetricDeliveryDuration,
				gu.WithLabels(map[string]string{"status_class": class, "error_type": errType}),
				gu.WithBuckets(deliveryDurationBuckets...),
				gu.WithUnit("s"),
				gu.WithDescription("Time from sending a delivery to its response headers, or to its failure"),
			)
		}
	}
	return m
}

func counters(factory gu.MetricFactory, name, label string, values []string, help string) map[string]gu.Counter {
	out := make(map[string]gu.Counter, len(values))
	for _, v := range values {
		out[v] = factory.Counter(name, gu.WithLabels(map[string]string{label: v}), gu.WithDescription(help))
	}
	return out
}

// RecordAttempt counts one delivery attempt. outcome is delivered, retry, dlq
// or endpoint_disabled.
func (m *Metrics) RecordAttempt(outcome string) {
	if c, ok := m.attempted[outcome]; ok {
		c.Inc()
	}
}

// RecordSkip counts a dequeued delivery the engine did not attempt. reason is
// rate_limited, orphaned or store_error.
func (m *Metrics) RecordSkip(reason string) {
	if c, ok := m.skipped[reason]; ok {
		c.Inc()
	}
}

// ObserveDelivery records how long one delivery request took. statusClass is
// 2xx, 3xx, 4xx, 5xx, other or none; errorType is timeout, canceled, dns, tls,
// connection, other or none. Exactly one of the two is none.
func (m *Metrics) ObserveDelivery(statusClass, errorType string, seconds float64) {
	if h, ok := m.duration[[2]string{statusClass, errorType}]; ok {
		h.Observe(seconds)
	}
}

// RecordDLQPush counts a write to the dead letter queue. A failed write is a
// delivery that failed for good and left no entry to replay.
func (m *Metrics) RecordDLQPush(err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.dlqPushed[result].Inc()
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
