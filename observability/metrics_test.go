package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMetrics_Registers(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

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
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.RecordDelivery("delivered", 0.5)
	m.RecordDelivery("delivered", 1.2)
	m.RecordDelivery("failed", 0.3)

	// Verify the counter vec has values by gathering.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	found := false
	for _, f := range families {
		if f.GetName() == "relay_deliveries_total" {
			found = true
			metrics := f.GetMetric()
			if len(metrics) != 2 { // delivered + failed
				t.Fatalf("expected 2 label combinations, got %d", len(metrics))
			}
		}
	}
	if !found {
		t.Fatal("relay_deliveries_total metric not found")
	}
}

func TestEventsSentTotal(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.EventsSentTotal.Inc()
	m.EventsSentTotal.Inc()
	m.EventsSentTotal.Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	for _, f := range families {
		if f.GetName() == "relay_events_sent_total" {
			metrics := f.GetMetric()
			if len(metrics) != 1 {
				t.Fatalf("expected 1 metric, got %d", len(metrics))
			}
			val := metrics[0].GetCounter().GetValue()
			if val != 3 {
				t.Fatalf("expected count 3, got %f", val)
			}
			return
		}
	}
	t.Fatal("relay_events_sent_total metric not found")
}

func TestGauges(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.DLQSize.Set(42)
	m.PendingDeliveries.Set(100)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	gauges := map[string]float64{
		"relay_dlq_size":           42,
		"relay_pending_deliveries": 100,
	}

	for _, f := range families {
		expected, ok := gauges[f.GetName()]
		if !ok {
			continue
		}
		val := f.GetMetric()[0].GetGauge().GetValue()
		if val != expected {
			t.Fatalf("%s: expected %f, got %f", f.GetName(), expected, val)
		}
		delete(gauges, f.GetName())
	}

	if len(gauges) > 0 {
		t.Fatalf("metrics not found: %v", gauges)
	}
}
