package observability

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/go-utils/metrics"

	"github.com/xraph/relay/internal/metricstest"
)

func newTestFactory() metrics.MetricFactory {
	return metrics.NewMetricsCollector("relay-test")
}

func TestNewMetricsCreatesEverySeriesUpFront(t *testing.T) {
	f := metricstest.New()
	NewMetrics(f)

	want := map[string][]string{
		MetricEventsSent:          {""},
		MetricPendingDeliveries:   {""},
		MetricDeliveriesInFlight:  {""},
		MetricDeliveriesAttempted: {"outcome=delivered", "outcome=retry", "outcome=dlq", "outcome=endpoint_disabled"},
		MetricDeliveriesSkipped:   {"reason=rate_limited", "reason=orphaned", "reason=store_error"},
		MetricDLQEntriesPushed:    {"result=ok", "result=error"},
		MetricDeliveryDuration: {
			"error_type=none,status_class=2xx", "error_type=none,status_class=3xx",
			"error_type=none,status_class=4xx", "error_type=none,status_class=5xx",
			"error_type=none,status_class=other",
			"error_type=timeout,status_class=none", "error_type=canceled,status_class=none",
			"error_type=dns,status_class=none", "error_type=tls,status_class=none",
			"error_type=connection,status_class=none", "error_type=other,status_class=none",
		},
	}
	for name, sets := range want {
		var got []string
		for _, labels := range f.Series(name) {
			got = append(got, joinLabels(labels))
		}
		slices.Sort(got)
		slices.Sort(sets)
		if !slices.Equal(got, sets) {
			t.Errorf("%s series:\n got %v\nwant %v", name, got, sets)
		}
	}
}

func joinLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

func TestRecordersCountOnlyKnownValues(t *testing.T) {
	f := metricstest.New()
	m := NewMetrics(f)

	m.RecordAttempt("delivered")
	m.RecordAttempt("delivered")
	m.RecordAttempt("dlq")
	m.RecordAttempt("https://hooks.example.com/secret") // not a label value: dropped
	m.RecordSkip("rate_limited")
	m.RecordSkip("tenant-42") // dropped
	m.RecordDLQPush(nil)
	m.RecordDLQPush(errors.New("store down"))
	m.ObserveDelivery("2xx", "none", 0.5)
	m.ObserveDelivery("2xx", "none", 1.2)
	m.ObserveDelivery("none", "timeout", 30)
	m.ObserveDelivery("2xx", "timeout", 1) // impossible pair: dropped

	counts := []struct {
		name   string
		labels []string
		want   float64
	}{
		{MetricDeliveriesAttempted, []string{"outcome", "delivered"}, 2},
		{MetricDeliveriesAttempted, []string{"outcome", "dlq"}, 1},
		{MetricDeliveriesAttempted, []string{"outcome", "retry"}, 0},
		{MetricDeliveriesSkipped, []string{"reason", "rate_limited"}, 1},
		{MetricDLQEntriesPushed, []string{"result", "ok"}, 1},
		{MetricDLQEntriesPushed, []string{"result", "error"}, 1},
	}
	for _, c := range counts {
		got, ok := f.CounterValue(c.name, c.labels...)
		if !ok || got != c.want {
			t.Errorf("%s%v = %v (exists %v), want %v", c.name, c.labels, got, ok, c.want)
		}
	}
	if len(f.Series(MetricDeliveriesAttempted)) != 4 || len(f.Series(MetricDeliveriesSkipped)) != 3 {
		t.Error("an unknown label value created a series")
	}

	if n, _ := f.HistogramCount(MetricDeliveryDuration, "status_class", "2xx", "error_type", "none"); n != 2 {
		t.Errorf("2xx observations = %d, want 2", n)
	}
	if n, _ := f.HistogramCount(MetricDeliveryDuration, "status_class", "none", "error_type", "timeout"); n != 1 {
		t.Errorf("timeout observations = %d, want 1", n)
	}
	if _, ok := f.HistogramCount(MetricDeliveryDuration, "status_class", "2xx", "error_type", "timeout"); ok {
		t.Error("an impossible label pair created a series")
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

	m.DeliveriesInFlight.Set(4)
	m.PendingDeliveries.Set(100)

	if got := m.DeliveriesInFlight.Value(); got != 4 {
		t.Fatalf("relay_deliveries_in_flight: expected 4, got %f", got)
	}
	if got := m.PendingDeliveries.Value(); got != 100 {
		t.Fatalf("relay_pending_deliveries: expected 100, got %f", got)
	}
}

type pendingFunc func(context.Context) (int64, error)

func (f pendingFunc) CountPending(ctx context.Context) (int64, error) { return f(ctx) }

func TestPendingSamplingIsSerializedAndBounded(t *testing.T) {
	m := NewMetrics(newTestFactory())
	m.PendingDeliveries.Set(7)
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- m.SyncPending(context.Background(), pendingFunc(func(ctx context.Context) (int64, error) {
			close(entered)
			select {
			case <-release:
				return 3, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.SyncPending(ctx, pendingFunc(func(context.Context) (int64, error) { t.Error("sample bypassed gate"); return 0, nil })); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission wasn't bounded: %v", err)
	}
	if got := m.PendingDeliveries.Value(); got != 7 {
		t.Fatalf("failed sample changed observation: %v", got)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := m.SyncPending(context.Background(), pendingFunc(func(context.Context) (int64, error) { return 1, nil })); err != nil {
		t.Fatal(err)
	}
	if got := m.PendingDeliveries.Value(); got != 1 {
		t.Fatalf("older sample overwrote latest: %v", got)
	}
	failure := errors.New("store unavailable")
	if err := m.SyncPending(context.Background(), pendingFunc(func(context.Context) (int64, error) { return 0, failure })); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if got := m.PendingDeliveries.Value(); got != 1 {
		t.Fatalf("failed read zeroed gauge: %v", got)
	}
}
