// Package tracing holds the span helpers relay's packages share. Spans go to
// the global tracer provider, which forge.New installs, so relay needs no
// tracing option: outside a Forge app with no provider installed, every span
// is a no-op.
package tracing

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope on every span relay starts.
const ScopeName = "github.com/xraph/relay"

// Start starts a span under relay's instrumentation scope. The tracer is
// looked up on every call rather than cached, so a provider installed after
// relay was built (a second Forge app in the same process, a test recorder)
// still receives the span.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer(ScopeName).Start(ctx, name, opts...) //nolint:spancheck // the caller ends the span
}

// Fail sets an Error status with class as its description and adds an
// exception event whose only attribute is exception.type, set to typ. Both
// are short and bounded: a sentinel's name or an error's Go type, never
// err.Error(). Driver and transport messages quote values (URLs, hosts,
// payload fragments), and none of those belong on a span.
func Fail(span trace.Span, class, typ string) {
	span.SetStatus(codes.Error, class)
	Exception(span, typ)
}

// Exception adds an exception event carrying only exception.type. An empty
// typ adds nothing.
func Exception(span trace.Span, typ string) {
	if typ == "" {
		return
	}
	span.AddEvent("exception", trace.WithAttributes(semconv.ExceptionType(typ)))
}

// ErrorType returns the Go type of the innermost error in err's chain, such
// as "*net.OpError" or "*pgconn.PgError". It returns "" for a nil err.
func ErrorType(err error) string {
	if err == nil {
		return ""
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
}

// Detach returns ctx without its span or baggage, keeping its values,
// deadline and cancellation. Long-lived background work calls it on the
// context it was started with. forge v1.12.9 hands an extension's Start a
// context that carries the "start <ext>" boot span, and anything derived from
// it would hang off the boot trace: every poll's queries and every delivery.
// Baggage goes too, because the delivery transport sends it to customer
// endpoints along with traceparent.
func Detach(ctx context.Context) context.Context {
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	return baggage.ContextWithoutBaggage(ctx)
}
