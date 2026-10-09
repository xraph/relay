package observability

import (
	"context"
	"errors"
	"testing"
	"time"

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
