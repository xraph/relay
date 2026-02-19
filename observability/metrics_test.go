package observability

import (
	"testing"

	"github.com/xraph/go-utils/metrics"
)

func newTestFactory() metrics.MetricFactory {
	return metrics.NewMetricsCollector("relay-test")
}

func TestNewMetrics_Registers(t *testing.T) {
	m := NewMetrics(newTestFactory())

	if m.EventsSentTotal == nil {
		t.Fatal("EventsSentTotal should not be nil")
	}
	if m.DeliveriesTotal == nil {
		t.Fatal("DeliveriesTotal should not be nil")
	}
	if m.DeliveryLatency == nil {
		t.Fatal("DeliveryLatency should not be nil")
	}
	if m.DLQSize == nil {
		t.Fatal("DLQSize should not be nil")
	}
	if m.PendingDeliveries == nil {
		t.Fatal("PendingDeliveries should not be nil")
	}
}

func TestRecordDelivery(t *testing.T) {
	m := NewMetrics(newTestFactory())

	m.RecordDelivery("delivered", 0.5)
	m.RecordDelivery("delivered", 1.2)
	m.RecordDelivery("failed", 0.3)

	if got := m.DeliveryLatency.Count(); got != 3 {
		t.Fatalf("expected 3 latency observations, got %d", got)
	}

	wantSum := 0.5 + 1.2 + 0.3
	if got := m.DeliveryLatency.Sum(); got != wantSum {
		t.Fatalf("expected sum %.1f, got %.1f", wantSum, got)
	}
}

func TestEventsSentTotal(t *testing.T) {
	m := NewMetrics(newTestFactory())

	m.EventsSentTotal.Inc()
	m.EventsSentTotal.Inc()
	m.EventsSentTotal.Inc()

	if got := m.EventsSentTotal.Value(); got != 3 {
		t.Fatalf("expected count 3, got %f", got)
	}
}

func TestGauges(t *testing.T) {
	m := NewMetrics(newTestFactory())

	m.DLQSize.Set(42)
	m.PendingDeliveries.Set(100)

	if got := m.DLQSize.Value(); got != 42 {
		t.Fatalf("relay_dlq_size: expected 42, got %f", got)
	}
	if got := m.PendingDeliveries.Value(); got != 100 {
		t.Fatalf("relay_pending_deliveries: expected 100, got %f", got)
	}
}
