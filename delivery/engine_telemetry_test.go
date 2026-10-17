package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	log "github.com/xraph/go-utils/log"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/internal/errs"
	"github.com/xraph/relay/internal/metricstest"
	"github.com/xraph/relay/internal/tracing/tracingtest"
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

	// The span context each Dequeue and GetEndpoint ran under.
	dequeueSpans  []trace.SpanContext
	endpointSpans []trace.SpanContext
}

func (s *fakeStore) Dequeue(ctx context.Context, _ int) ([]*Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dequeueSpans = append(s.dequeueSpans, trace.SpanContextFromContext(ctx))
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

func (s *fakeStore) GetEndpoint(ctx context.Context, _ id.ID) (*endpoint.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpointSpans = append(s.endpointSpans, trace.SpanContextFromContext(ctx))
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
	return newLoggedFixture(t, url, dlq, nil)
}

// newLoggedFixture is newTelemetryFixture with the engine logging to logger.
func newLoggedFixture(t *testing.T, url string, dlq DLQPusher, logger log.Logger) *telemetryFixture {
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
	}, logger)
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

// secretURL is a webhook URL shaped like the ones tenants register: the
// secret in the path, a token and an email in the query.
func secretURL(base string) string {
	return base + "/hooks/T000/B000/XXXXSECRET?token=sk_live_SECRET&email=alice%40example.com"
}

func TestDeliverySpanIsARootWithATracedRequestUnderIt(t *testing.T) {
	rec := tracingtest.Record(t)
	type seen struct{ path, query, traceparent, auth string }
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.URL.Path, r.URL.RawQuery, r.Header.Get("traceparent"), r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	fx := newTelemetryFixture(t, secretURL(srv.URL), fakeDLQ{})
	fx.store.ep.Headers = map[string]string{"Authorization": "Bearer topsecret"}
	d := fx.delivery(3)

	// A caller's span in ctx must not become the delivery's parent.
	callerCtx, caller := otel.Tracer("test").Start(context.Background(), "caller")
	fx.engine.process(callerCtx, d)
	caller.End()
	req := <-got

	deliver := tracingtest.Named(t, rec, "deliver webhook")
	client := tracingtest.Named(t, rec, "HTTP POST")

	if deliver.Parent().IsValid() {
		t.Error("deliver webhook has a parent, want a root span")
	}
	if deliver.SpanContext().TraceID() == caller.SpanContext().TraceID() {
		t.Error("deliver webhook joined the caller's trace")
	}
	if deliver.SpanKind() != trace.SpanKindInternal || client.SpanKind() != trace.SpanKindClient {
		t.Errorf("kinds = %v, %v, want internal, client", deliver.SpanKind(), client.SpanKind())
	}
	if client.Parent().SpanID() != deliver.SpanContext().SpanID() {
		t.Error("the request span is not a child of deliver webhook")
	}
	if deliver.Status().Code != codes.Unset {
		t.Errorf("delivered span status = %v", deliver.Status())
	}

	a := tracingtest.Attrs(deliver)
	for key, want := range map[attribute.Key]any{
		"relay.delivery.id":         d.ID.String(),
		"relay.event.id":            d.EventID.String(),
		"relay.endpoint.id":         d.EndpointID.String(),
		"relay.event.type":          "invoice.paid",
		"relay.delivery.attempt":    int64(1),
		"relay.delivery.outcome":    "delivered",
		"http.response.status_code": int64(200),
	} {
		if v, ok := a[key]; !ok || v.AsInterface() != want {
			t.Errorf("deliver webhook %s = %v, want %v", key, v.AsInterface(), want)
		}
	}

	ca := tracingtest.Attrs(client)
	if ca["url.full"].AsString() != "http://REDACTED" || ca["server.address"].AsString() != "REDACTED" {
		t.Errorf("client span url.full=%q server.address=%q, want both redacted",
			ca["url.full"].AsString(), ca["server.address"].AsString())
	}
	if ca["http.request.method"].AsString() != http.MethodPost {
		t.Errorf("client span method = %q", ca["http.request.method"].AsString())
	}

	// The receiver still gets the real URL, its headers and the trace.
	if req.path != "/hooks/T000/B000/XXXXSECRET" || req.query == "" || req.auth != "Bearer topsecret" {
		t.Errorf("receiver saw path %q query %q auth %q", req.path, req.query, req.auth)
	}
	if !strings.Contains(req.traceparent, deliver.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q does not carry the delivery's trace", req.traceparent)
	}

	host := strings.TrimPrefix(srv.URL, "http://")
	tracingtest.AssertNoLeak(t, rec.Ended(), host, "127.0.0.1", "SECRET", "hooks", "alice", "topsecret")
}

func TestFailedDeliverySpans(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		rec := tracingtest.Record(t)
		fx := newTelemetryFixture(t, secretURL(statusServer(t, http.StatusServiceUnavailable).URL), fakeDLQ{})
		fx.engine.process(context.Background(), fx.delivery(3))

		deliver := tracingtest.Named(t, rec, "deliver webhook")
		if deliver.Status().Code != codes.Error || deliver.Status().Description != "503" {
			t.Errorf("status = %v, want Error 503", deliver.Status())
		}
		a := tracingtest.Attrs(deliver)
		if a["relay.delivery.outcome"].AsString() != "retry" || a["error.type"].AsString() != "503" {
			t.Errorf("outcome %q error.type %q", a["relay.delivery.outcome"].AsString(), a["error.type"].AsString())
		}
		tracingtest.AssertNoLeak(t, rec.Ended(), "SECRET", "alice", "127.0.0.1")
	})

	t.Run("connection refused", func(t *testing.T) {
		rec := tracingtest.Record(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		url := secretURL(srv.URL)
		host := strings.TrimPrefix(srv.URL, "http://")
		srv.Close()

		fx := newTelemetryFixture(t, url, fakeDLQ{})
		d := fx.delivery(3)
		fx.engine.process(context.Background(), d)

		deliver := tracingtest.Named(t, rec, "deliver webhook")
		client := tracingtest.Named(t, rec, "HTTP POST")
		if deliver.Status().Code != codes.Error || deliver.Status().Description != "connection" {
			t.Errorf("deliver status = %v, want Error connection", deliver.Status())
		}
		if types := tracingtest.ExceptionTypes(deliver); len(types) != 1 || types[0] == "" {
			t.Errorf("exception types = %v, want one Go type", types)
		}
		if client.Status().Code != codes.Error || client.Status().Description != "connection" {
			t.Errorf("client status = %v, want Error connection", client.Status())
		}
		if tracingtest.Attrs(client)["error.type"].AsString() != "connection" {
			t.Errorf("client error.type = %q", tracingtest.Attrs(client)["error.type"].AsString())
		}
		tracingtest.AssertNoLeak(t, rec.Ended(), host, "127.0.0.1", "SECRET", "hooks", "alice", "dial")

		// The tenant still gets the full error on the delivery.
		fx.store.mu.Lock()
		last := fx.store.updated[len(fx.store.updated)-1]
		fx.store.mu.Unlock()
		if !strings.Contains(last.LastError, "127.0.0.1") {
			t.Errorf("delivery error %q lost its detail", last.LastError)
		}
	})

	t.Run("truncated response", func(t *testing.T) {
		rec := tracingtest.Record(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("short"))
		}))
		t.Cleanup(srv.Close)

		fx := newTelemetryFixture(t, secretURL(srv.URL), fakeDLQ{})
		fx.engine.process(context.Background(), fx.delivery(3))

		// The read error comes back whole for the delivery record, and
		// masked on the request span.
		fx.store.mu.Lock()
		last := fx.store.updated[len(fx.store.updated)-1]
		fx.store.mu.Unlock()
		if last.LastError != "read response: unexpected EOF" {
			t.Errorf("delivery error = %q", last.LastError)
		}
		client := tracingtest.Named(t, rec, "HTTP POST")
		if client.Status().Code != codes.Error || client.Status().Description != "connection" {
			t.Errorf("client status = %v, want Error connection", client.Status())
		}
		if got := tracingtest.Attrs(client)["error.type"].AsString(); got != "connection" {
			t.Errorf("client error.type = %q", got)
		}
		tracingtest.AssertNoLeak(t, rec.Ended(), strings.TrimPrefix(srv.URL, "http://"), "127.0.0.1",
			"SECRET", "hooks", "alice", "unexpected EOF")
	})

	t.Run("redirect", func(t *testing.T) {
		rec := tracingtest.Record(t)
		landed := make(chan string, 1)
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			landed <- r.URL.RequestURI()
			w.WriteHeader(http.StatusBadRequest)
		}))
		t.Cleanup(target.Close)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/moved/MOVEDSECRET?token=sk_live_MOVEDSECRET", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(origin.Close)

		fx := newTelemetryFixture(t, secretURL(origin.URL), fakeDLQ{})
		fx.engine.process(context.Background(), fx.delivery(3))

		if got := <-landed; got != "/moved/MOVEDSECRET?token=sk_live_MOVEDSECRET" {
			t.Errorf("the redirect landed on %q", got)
		}
		var clients int
		for _, sp := range rec.Ended() {
			if sp.Name() == "HTTP POST" {
				clients++
				if u := tracingtest.Attrs(sp)["url.full"].AsString(); u != "http://REDACTED" {
					t.Errorf("redirected request span url.full = %q", u)
				}
			}
		}
		if clients != 2 {
			t.Errorf("%d client spans, want one per hop", clients)
		}
		tracingtest.AssertNoLeak(t, rec.Ended(), strings.TrimPrefix(origin.URL, "http://"),
			strings.TrimPrefix(target.URL, "http://"), "127.0.0.1", "SECRET", "moved", "hooks")
	})
}

func TestSkippedDeliverySpans(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*telemetryFixture)
		outcome string
		failed  bool
	}{
		{"orphaned", func(fx *telemetryFixture) { fx.store.epErr = errs.ErrEndpointNotFound }, "orphaned", false},
		{"store error", func(fx *telemetryFixture) { fx.store.epErr = errors.New("alice@example.com: no route") }, "store_error", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := tracingtest.Record(t)
			fx := newTelemetryFixture(t, statusServer(t, http.StatusOK).URL, fakeDLQ{})
			c.setup(fx)
			fx.engine.process(context.Background(), fx.delivery(3))

			deliver := tracingtest.Named(t, rec, "deliver webhook")
			if got := tracingtest.Attrs(deliver)["relay.delivery.outcome"].AsString(); got != c.outcome {
				t.Errorf("outcome = %q, want %q", got, c.outcome)
			}
			if failed := deliver.Status().Code == codes.Error; failed != c.failed {
				t.Errorf("status = %v, want failed=%v", deliver.Status(), c.failed)
			}
			if c.failed && deliver.Status().Description != "store error" {
				t.Errorf("status description = %q", deliver.Status().Description)
			}
			tracingtest.AssertNoLeak(t, rec.Ended(), "alice")
		})
	}
}

// Under Forge, Start's ctx carries the "start relay" boot span. Deliveries
// must not land in that trace, and its baggage must not reach receivers.
func TestEngineStartDetachesFromTheBootSpan(t *testing.T) {
	rec := tracingtest.Record(t)
	type seen struct{ traceparent, baggage string }
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.Header.Get("traceparent"), r.Header.Get("baggage")}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	fx := newTelemetryFixture(t, srv.URL, fakeDLQ{})
	fx.store.queue = []*Delivery{fx.delivery(3)}

	member, err := baggage.NewMember("user", "alice")
	if err != nil {
		t.Fatal(err)
	}
	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}
	bootCtx, boot := otel.Tracer("test").Start(baggage.ContextWithBaggage(context.Background(), bag), "start relay")
	fx.engine.Start(bootCtx)
	boot.End()
	fx.engine.Wake()
	req := <-got
	fx.engine.Stop(context.Background())

	deliver := tracingtest.Named(t, rec, "deliver webhook")
	if deliver.Parent().IsValid() || deliver.SpanContext().TraceID() == boot.SpanContext().TraceID() {
		t.Error("the delivery joined the boot trace")
	}
	if req.traceparent == "" || strings.Contains(req.traceparent, boot.SpanContext().TraceID().String()) {
		t.Errorf("traceparent = %q, want the delivery's own trace", req.traceparent)
	}
	if req.baggage != "" {
		t.Errorf("receiver got baggage %q", req.baggage)
	}

	// The poll's own query runs under no span, so grove starts none for it;
	// the delivery's queries run under the delivery's span.
	fx.store.mu.Lock()
	defer fx.store.mu.Unlock()
	for i, sc := range fx.store.dequeueSpans {
		if sc.IsValid() {
			t.Errorf("dequeue %d ran under span %s", i, sc.SpanID())
		}
	}
	if len(fx.store.endpointSpans) != 1 || fx.store.endpointSpans[0].SpanID() != deliver.SpanContext().SpanID() {
		t.Errorf("GetEndpoint ran under %v, want the deliver webhook span", fx.store.endpointSpans)
	}
}

// The engine logs every failed delivery. A transport error's text quotes the
// endpoint's URL, so the log gets the failure class and status, and the full
// text stays on the delivery record.
func TestLogsNeverCarryTheEndpointURL(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	target := statusServer(t, http.StatusBadRequest)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/moved/MOVEDSECRET", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	cases := []struct {
		name        string
		base        string
		maxAttempts int
		dlq         DLQPusher
		updateErr   error
		failure     string
	}{
		{"delivered", statusServer(t, http.StatusOK).URL, 3, fakeDLQ{}, nil, ""},
		{"retry", statusServer(t, http.StatusServiceUnavailable).URL, 3, fakeDLQ{}, nil, "503"},
		{"rejected", statusServer(t, http.StatusBadRequest).URL, 3, fakeDLQ{}, nil, "400"},
		{"refused", closedURL, 1, fakeDLQ{}, nil, "connection"},
		{"gone", statusServer(t, http.StatusGone).URL, 3, fakeDLQ{}, nil, ""},
		{"redirected", redirect.URL, 3, fakeDLQ{}, nil, "400"},
		{"store failures", closedURL, 1, fakeDLQ{err: errors.New("disk full")}, errors.New("conn lost"), "connection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logger := log.NewTestLogger().(*log.TestLogger)
			fx := newLoggedFixture(t, secretURL(c.base), c.dlq, logger)
			fx.store.updateErr = c.updateErr
			fx.engine.process(context.Background(), fx.delivery(c.maxAttempts))

			needles := []string{"SECRET", "token", "alice", "hooks", "127.0.0.1", "dial",
				strings.TrimPrefix(c.base, "http://"), strings.TrimPrefix(target.URL, "http://")}
			var failures []string
			for _, entry := range logger.GetLogs() {
				texts := []string{entry.Message}
				for _, f := range entry.Fields {
					texts = append(texts, fmt.Sprint(f.Value()))
					if f.Key() == "failure" {
						failures = append(failures, fmt.Sprint(f.Value()))
					}
				}
				for _, text := range texts {
					for _, needle := range needles {
						if strings.Contains(text, needle) {
							t.Errorf("%s log %q carries %q in %q", entry.Level, entry.Message, needle, text)
						}
					}
				}
			}
			if c.failure != "" && !slices.Contains(failures, c.failure) {
				t.Errorf("logged failures %v, want %q among them", failures, c.failure)
			}
		})
	}
}

// With no transport of its own, a delivery goes out on whatever
// http.DefaultTransport is when it's sent, as with a plain client, so a test
// double swapped in after the engine was built still gets the request.
func TestNilTransportReadsTheDefaultPerRequest(t *testing.T) {
	sender := NewSender(time.Second)

	swapped := &forwardingTransport{}
	prev := http.DefaultTransport
	http.DefaultTransport = swapped
	t.Cleanup(func() { http.DefaultTransport = prev })

	ep := &endpoint.Endpoint{URL: "https://hooks.example.com/in", Secret: "whsec_test"}
	if result := sender.Send(context.Background(), ep, &event.Event{Type: "invoice.paid"}, &Delivery{}); result.Error != "" {
		t.Fatalf("send: %s", result.Error)
	}
	if len(swapped.requests) != 1 || swapped.requests[0].URL.String() != ep.URL {
		t.Fatalf("the swapped-in default transport sent %d requests", len(swapped.requests))
	}
}
