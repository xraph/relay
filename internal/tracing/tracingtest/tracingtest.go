// Package tracingtest records relay's spans in tests and checks what they
// carry.
package tracingtest

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Record installs a span recorder as the global tracer provider, with the
// W3C trace context and baggage propagators, until the test ends. Build
// relay after calling it: the delivery transport captures the provider when
// it is built.
func Record(t testing.TB) *tracetest.SpanRecorder {
	t.Helper()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

// Named returns the one ended span called name, failing the test if there
// isn't exactly one.
func Named(t testing.TB, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == name {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one %q span, got %d", name, len(found))
	}
	return found[0]
}

// Attrs returns a span's attributes by key.
func Attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value
	}
	return out
}

// ExceptionTypes returns the exception.type of each exception event on s.
func ExceptionTypes(s sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, e := range s.Events() {
		if e.Name != "exception" {
			continue
		}
		for _, kv := range e.Attributes {
			if kv.Key == semconv.ExceptionTypeKey {
				out = append(out, kv.Value.AsString())
			}
		}
	}
	return out
}

// AssertNoLeak fails if any needle appears anywhere on any span: its name,
// attributes, events or status.
func AssertNoLeak(t testing.TB, spans []sdktrace.ReadOnlySpan, needles ...string) {
	t.Helper()
	for _, s := range spans {
		texts := []string{s.Name(), s.Status().Description}
		for _, kv := range s.Attributes() {
			texts = append(texts, kv.Value.String())
		}
		for _, e := range s.Events() {
			texts = append(texts, e.Name)
			for _, kv := range e.Attributes {
				texts = append(texts, kv.Value.String())
			}
		}
		for _, text := range texts {
			for _, needle := range needles {
				if strings.Contains(text, needle) {
					t.Errorf("span %q carries %q in %q", s.Name(), needle, text)
				}
			}
		}
	}
}
