package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/internal/errs"
	"github.com/xraph/relay/internal/metricstest"
	"github.com/xraph/relay/observability"
)

// fakeStore is just enough EngineStore to drive process by hand. The memory
// store would do, but it imports this package, so a test in here can't use it.
type fakeStore struct {
	mu        sync.Mutex
	ep        *endpoint.Endpoint
	epErr     error
	evt       *event.Event
	queue     []*Delivery
	updated   []Delivery
	attempts  []*Attempt
	updateErr error
}

func (s *fakeStore) Dequeue(context.Context, int) ([]*Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queue
	s.queue = nil
	return out, nil
}

func (s *fakeStore) UpdateDelivery(_ context.Context, d *Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated = append(s.updated, *d)
	return s.updateErr
}

func (s *fakeStore) GetEndpoint(context.Context, id.ID) (*endpoint.Endpoint, error) {
	return s.ep, s.epErr
}

func (s *fakeStore) GetEvent(context.Context, id.ID) (*event.Event, error) {
	return s.evt, nil
}

func (s *fakeStore) SetEnabled(context.Context, id.ID, bool) error { return nil }

func (s *fakeStore) RecordAttempt(_ context.Context, a *Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, a)
	return nil
}

type fakeDLQ struct{ err error }

func (q fakeDLQ) PushFailed(context.Context, *Delivery, *endpoint.Endpoint, *event.Event, string, int) error {
	return q.err
}

// telemetryFixture is an engine over a fakeStore whose endpoint points at
// url, recording metrics into a metricstest.Factory.
type telemetryFixture struct {
	store   *fakeStore
	engine  *Engine
	metrics *metricstest.Factory
}

func newTelemetryFixture(t *testing.T, url string, dlq DLQPusher) *telemetryFixture {
	t.Helper()
	f := metricstest.New()
	ep := &endpoint.Endpoint{
		Entity: entity.New(), ID: id.NewEndpointID(), TenantID: "tenant-1", URL: url,
		Secret: "whsec_test_secret_1234567890abcdef1234567890abcdef", Enabled: true,
	}
	evt := &event.Event{
		Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.paid", TenantID: "tenant-1",
		Data: json.RawMessage(`{"email":"alice@example.com"}`),
	}
	store := &fakeStore{ep: ep, evt: evt}
	engine := NewEngine(store, dlq, EngineConfig{
		Concurrency:    2,
		PollInterval:   time.Hour,
		BatchSize:      10,
		RequestTimeout: 2 * time.Second,
		RetrySchedule:  []time.Duration{time.Minute},
		Metrics:        observability.NewMetrics(f),
	}, nil)
	return &telemetryFixture{store: store, engine: engine, metrics: f}
}

func (fx *telemetryFixture) delivery(maxAttempts int) *Delivery {
	return &Delivery{
		Entity: entity.New(), ID: id.NewDeliveryID(), EventID: fx.store.evt.ID, EndpointID: fx.store.ep.ID,
		EventType: fx.store.evt.Type, TenantID: "tenant-1", State: StateDelivering, MaxAttempts: maxAttempts,
	}
}

func (fx *telemetryFixture) counter(t *testing.T, name string, labels ...string) float64 {
	t.Helper()
	v, ok := fx.metrics.CounterValue(name, labels...)
	if !ok {
		t.Fatalf("no series %s%v", name, labels)
	}
	return v
}

func (fx *telemetryFixture) durations(t *testing.T, statusClass, errorType string) uint64 {
	t.Helper()
	n, ok := fx.metrics.HistogramCount(observability.MetricDeliveryDuration,
		"status_class", statusClass, "error_type", errorType)
	if !ok {
		t.Fatalf("no duration series for %s/%s", statusClass, errorType)
	}
	return n
}

func statusServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProcessRecordsAttemptMetrics(t *testing.T) {
	cases := []struct {
		name        string
		code        int
		maxAttempts int
		outcome     string
		statusClass string
	}{
		{"delivered", http.StatusOK, 3, "delivered", "2xx"},
		{"retry", http.StatusServiceUnavailable, 3, "retry", "5xx"},
		{"exhausted", http.StatusServiceUnavailable, 1, "dlq", "5xx"},
		{"rejected", http.StatusBadRequest, 3, "dlq", "4xx"},
		{"gone", http.StatusGone, 3, "endpoint_disabled", "4xx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newTelemetryFixture(t, statusServer(t, c.code).URL, fakeDLQ{})
			fx.engine.process(context.Background(), fx.delivery(c.maxAttempts))

			if got := fx.counter(t, observability.MetricDeliveriesAttempted, "outcome", c.outcome); got != 1 {
				t.Errorf("attempted{outcome=%s} = %v, want 1", c.outcome, got)
			}
			if got := fx.durations(t, c.statusClass, "none"); got != 1 {
				t.Errorf("duration{status_class=%s} count = %d, want 1", c.statusClass, got)
			}
			wantPushes := 0.0
			if c.outcome == "dlq" || c.outcome == "endpoint_disabled" {
				wantPushes = 1
			}
			if got := fx.counter(t, observability.MetricDLQEntriesPushed, "result", "ok"); got != wantPushes {
				t.Errorf("dlq pushed{ok} = %v, want %v", got, wantPushes)
			}
		})
	}
}

func TestProcessTimesAFailedConnectionByErrorClass(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now

	fx := newTelemetryFixture(t, url, fakeDLQ{})
	fx.engine.process(context.Background(), fx.delivery(3))

	if got := fx.counter(t, observability.MetricDeliveriesAttempted, "outcome", "retry"); got != 1 {
		t.Errorf("attempted{retry} = %v, want 1", got)
	}
	if got := fx.durations(t, "none", "connection"); got != 1 {
		t.Errorf("duration{none,connection} count = %d, want 1", got)
	}
}

func TestProcessCountsAFailedDLQWrite(t *testing.T) {
	fx := newTelemetryFixture(t, statusServer(t, http.StatusBadRequest).URL, fakeDLQ{err: errors.New("disk full")})
	fx.engine.process(context.Background(), fx.delivery(3))

	if got := fx.counter(t, observability.MetricDLQEntriesPushed, "result", "error"); got != 1 {
		t.Errorf("dlq pushed{error} = %v, want 1", got)
	}
}

func TestProcessDoesNotTimeARequestThatNeverWentOut(t *testing.T) {
	fx := newTelemetryFixture(t, statusServer(t, http.StatusOK).URL, fakeDLQ{})
	fx.store.ep.Secret = ""
	fx.engine.process(context.Background(), fx.delivery(3))

	if got := fx.counter(t, observability.MetricDeliveriesAttempted, "outcome", "retry"); got != 1 {
		t.Errorf("attempted{retry} = %v, want 1", got)
	}
	for _, labels := range fx.metrics.Series(observability.MetricDeliveryDuration) {
		if n := fx.durations(t, labels["status_class"], labels["error_type"]); n != 0 {
			t.Errorf("an unsent request was timed under %v", labels)
		}
	}
}

func TestProcessCountsSkips(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		fx := newTelemetryFixture(t, statusServer(t, http.StatusOK).URL, fakeDLQ{})
		fx.store.ep.RateLimit = 1
		// The bucket starts with one token: the first goes, the second waits.
		fx.engine.process(context.Background(), fx.delivery(3))
		fx.engine.process(context.Background(), fx.delivery(3))
		if got := fx.counter(t, observability.MetricDeliveriesSkipped, "reason", "rate_limited"); got != 1 {
			t.Errorf("skipped{rate_limited} = %v, want 1", got)
		}
		if got := fx.counter(t, observability.MetricDeliveriesAttempted, "outcome", "delivered"); got != 1 {
			t.Errorf("attempted{delivered} = %v, want 1", got)
		}
	})
	t.Run("orphaned", func(t *testing.T) {
		fx := newTelemetryFixture(t, statusServer(t, http.StatusOK).URL, fakeDLQ{})
		fx.store.epErr = errs.ErrEndpointNotFound
		fx.engine.process(context.Background(), fx.delivery(3))
		if got := fx.counter(t, observability.MetricDeliveriesSkipped, "reason", "orphaned"); got != 1 {
			t.Errorf("skipped{orphaned} = %v, want 1", got)
		}
	})
	t.Run("store error", func(t *testing.T) {
		fx := newTelemetryFixture(t, statusServer(t, http.StatusOK).URL, fakeDLQ{})
		fx.store.epErr = errors.New("connection reset")
		fx.engine.process(context.Background(), fx.delivery(3))
		if got := fx.counter(t, observability.MetricDeliveriesSkipped, "reason", "store_error"); got != 1 {
			t.Errorf("skipped{store_error} = %v, want 1", got)
		}
	})
}

func TestInFlightGaugeFollowsTheWorkers(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	fx := newTelemetryFixture(t, srv.URL, fakeDLQ{})
	fx.store.queue = []*Delivery{fx.delivery(3), fx.delivery(3)}
	gauge := func() float64 {
		v, _ := fx.metrics.GaugeValue(observability.MetricDeliveriesInFlight)
		return v
	}

	fx.engine.Start(context.Background())
	fx.engine.Wake()
	<-entered
	<-entered
	if got := gauge(); got != 2 {
		t.Errorf("in flight with two requests open = %v, want 2", got)
	}
	close(release)
	fx.engine.Stop(context.Background())
	if got := gauge(); got != 0 {
		t.Errorf("in flight after Stop = %v, want 0", got)
	}
}
