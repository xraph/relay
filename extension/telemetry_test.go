package extension

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"go.opentelemetry.io/otel"

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

	app := forge.New(forge.WithAppName("relay-telemetry-test"), forge.WithAppLogger(forge.NewNoopLogger()))
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
