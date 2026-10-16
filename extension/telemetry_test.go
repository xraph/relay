package extension

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/internal/tracing"
	"github.com/xraph/relay/store/memory"
)

// newTelemetryApp registers relay in a real Forge app, the way production
// does, and restores the global OTel state forge.New replaces.
func newTelemetryApp(t *testing.T) (forge.App, *Extension) {
	t.Helper()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	app := forge.New(forge.WithAppName("relay-telemetry-test"), forge.WithAppEnvironment("test"),
		forge.WithAppLogger(forge.NewNoopLogger()))
	ext := New(WithStore(memory.New()), WithDisableRoutes())
	if err := ext.Register(app); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return app, ext
}

func scrape(t *testing.T, app forge.App) []string {
	t.Helper()
	p, ok := app.Metrics().(interface{ PrometheusHandler() http.Handler })
	if !ok {
		t.Fatalf("app metrics %T have no Prometheus handler", app.Metrics())
	}
	rr := httptest.NewRecorder()
	p.PrometheusHandler().ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_/metrics", nil))
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatal(err)
	}
	var relayLines []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, "relay_") && !strings.HasPrefix(line, "#") {
			relayLines = append(relayLines, line)
		}
	}
	return relayLines
}

// Every relay series is on the scrape from the start, under forge's
// namespace, with exactly the documented labels.
func TestExtensionMetricsOnTheScrape(t *testing.T) {
	app, _ := newTelemetryApp(t)
	lines := scrape(t, app)

	want := []string{
		`forge_relay_events_sent_total 0`,
		`forge_relay_pending_deliveries 0`,
		`forge_relay_deliveries_in_flight 0`,
		`forge_relay_deliveries_attempted_total{outcome="delivered"} 0`,
		`forge_relay_deliveries_attempted_total{outcome="retry"} 0`,
		`forge_relay_deliveries_attempted_total{outcome="dlq"} 0`,
		`forge_relay_deliveries_attempted_total{outcome="endpoint_disabled"} 0`,
		`forge_relay_deliveries_skipped_total{reason="rate_limited"} 0`,
		`forge_relay_deliveries_skipped_total{reason="orphaned"} 0`,
		`forge_relay_deliveries_skipped_total{reason="store_error"} 0`,
		`forge_relay_dlq_entries_pushed_total{result="ok"} 0`,
		`forge_relay_dlq_entries_pushed_total{result="error"} 0`,
		`forge_relay_delivery_duration_seconds_count{error_type="none",status_class="2xx"} 0`,
		`forge_relay_delivery_duration_seconds_count{error_type="timeout",status_class="none"} 0`,
		`forge_relay_delivery_duration_seconds_bucket{error_type="none",status_class="5xx",le="30"} 0`,
	}
	for _, w := range want {
		if !slices.Contains(lines, w) {
			t.Errorf("scrape is missing %s", w)
		}
	}

	var counts int
	for _, line := range lines {
		if !strings.HasPrefix(line, "forge_relay_") {
			t.Errorf("relay series outside forge's namespace: %s", line)
		}
		if strings.HasPrefix(line, "forge_relay_delivery_duration_seconds_count{") {
			counts++
		}
		for _, gone := range []string{"relay_deliveries_total", "relay_delivery_latency_seconds", "relay_dlq_size"} {
			if strings.Contains(line, gone) {
				t.Errorf("retired metric still registered: %s", line)
			}
		}
	}
	if counts != 11 {
		t.Errorf("duration histogram has %d series, want 11", counts)
	}
}

// endSignal reports when a span with its name ends.
type endSignal struct {
	name string
	done chan struct{}
}

func (e *endSignal) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (e *endSignal) OnEnd(s sdktrace.ReadOnlySpan) {
	if s.Name() == e.name {
		select {
		case e.done <- struct{}{}:
		default:
		}
	}
}
func (e *endSignal) Shutdown(context.Context) error   { return nil }
func (e *endSignal) ForceFlush(context.Context) error { return nil }

// End to end in a Forge app: the send joins the caller's trace, the delivery
// starts its own (not the boot trace), the request under it carries that
// trace to the receiver, and the scrape counts the attempt.
func TestExtensionTracesADeliveryEndToEnd(t *testing.T) {
	app, ext := newTelemetryApp(t)
	rec := tracetest.NewSpanRecorder()
	delivered := &endSignal{name: "deliver webhook", done: make(chan struct{}, 1)}
	for _, p := range []sdktrace.SpanProcessor{rec, delivered} {
		if err := forge.RegisterSpanProcessor(app, p); err != nil {
			t.Fatalf("RegisterSpanProcessor: %v", err)
		}
	}

	traceparent := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	r := ext.Relay()
	ctx := context.Background()
	if _, err := r.RegisterEventType(ctx, catalog.WebhookDefinition{Name: "invoice.paid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Endpoints().Create(ctx, endpoint.Input{TenantID: "t1", URL: srv.URL + "/hook", EventTypes: []string{"*"}}); err != nil {
		t.Fatal(err)
	}

	bootCtx, boot := forge.StartSpan(ctx, "start relay")
	if err := ext.Start(bootCtx); err != nil {
		t.Fatal(err)
	}
	boot.End()
	t.Cleanup(func() { _ = ext.Stop(context.Background()) })

	reqCtx, handler := forge.StartSpan(ctx, "POST /orders")
	if err := r.Send(reqCtx, &event.Event{Type: "invoice.paid", TenantID: "t1", Data: map[string]any{"total": 42}}); err != nil {
		t.Fatal(err)
	}
	handler.End()
	sent := <-traceparent
	<-delivered.done // the delivery is recorded before its span ends
	if err := ext.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	byName := map[string]int{}
	var deliverTrace, deliverSpan, clientParent string
	for _, s := range rec.Ended() {
		byName[s.Name()]++
		switch s.Name() {
		case "send event":
			if s.InstrumentationScope().Name != tracing.ScopeName {
				t.Errorf("send event scope = %q", s.InstrumentationScope().Name)
			}
			if s.Parent().SpanID() != handler.SpanContext().SpanID() {
				t.Error("send event is not under the request span")
			}
		case "deliver webhook":
			if s.Parent().IsValid() || s.SpanContext().TraceID() == boot.SpanContext().TraceID() {
				t.Error("deliver webhook is not a root of its own trace")
			}
			deliverTrace = s.SpanContext().TraceID().String()
			deliverSpan = s.SpanContext().SpanID().String()
		case "HTTP POST":
			clientParent = s.Parent().SpanID().String()
		}
	}
	if byName["send event"] != 1 || byName["deliver webhook"] != 1 || byName["HTTP POST"] != 1 {
		t.Fatalf("spans = %v", byName)
	}
	if clientParent != deliverSpan {
		t.Error("HTTP POST is not under deliver webhook")
	}
	if !strings.Contains(sent, deliverTrace) {
		t.Errorf("receiver got traceparent %q, want trace %s", sent, deliverTrace)
	}

	lines := scrape(t, app)
	for _, w := range []string{
		`forge_relay_events_sent_total 1`,
		`forge_relay_deliveries_attempted_total{outcome="delivered"} 1`,
		`forge_relay_delivery_duration_seconds_count{error_type="none",status_class="2xx"} 1`,
		`forge_relay_deliveries_in_flight 0`,
	} {
		if !slices.Contains(lines, w) {
			t.Errorf("scrape is missing %s", w)
		}
	}
}
