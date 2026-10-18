package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/xraph/relay"

// Tracer is kept so code that built one still compiles. Relay no longer
// reads it: every span goes to the global tracer provider, which forge.New
// installs, whether or not you pass a Tracer.
//
// Deprecated: Relay traces without a Tracer. Drop relay.WithTracer.
type Tracer struct {
	tracer trace.Tracer
}

// NewTracer creates a Tracer.
//
// Deprecated: Relay traces without a Tracer. Drop relay.WithTracer.
func NewTracer() *Tracer {
	return &Tracer{
		tracer: otel.Tracer(tracerName),
	}
}

// StartDeliverySpan starts a span for a delivery attempt.
//
// Deprecated: the delivery engine starts its own "deliver webhook" span.
func (t *Tracer) StartDeliverySpan(ctx context.Context, deliveryID, eventID, endpointID string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, "deliver webhook", //nolint:spancheck // EndDeliverySpan ends it
		trace.WithAttributes(
			attribute.String("relay.delivery.id", deliveryID),
			attribute.String("relay.event.id", eventID),
			attribute.String("relay.endpoint.id", endpointID),
		),
	)
}

// EndDeliverySpan ends a delivery span. A non-empty err marks it failed,
// without the error's text, which can quote the endpoint's URL.
//
// Deprecated: the delivery engine starts and ends its own spans.
func (t *Tracer) EndDeliverySpan(span trace.Span, statusCode, _ int, err string) {
	if statusCode > 0 {
		span.SetAttributes(semconv.HTTPResponseStatusCode(statusCode))
	}
	if err != "" {
		span.SetStatus(codes.Error, "delivery failed")
	}
	span.End()
}
