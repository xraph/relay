package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/xraph/relay"

// Tracer provides OpenTelemetry tracing for Relay.
type Tracer struct {
	tracer trace.Tracer
}

// NewTracer creates a new Relay tracer.
func NewTracer() *Tracer {
	return &Tracer{
		tracer: otel.Tracer(tracerName),
	}
}

// StartDeliverySpan starts a new span for a delivery attempt.
func (t *Tracer) StartDeliverySpan(ctx context.Context, deliveryID, eventID, endpointID string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, "relay.delivery",
		trace.WithAttributes(
			attribute.String("relay.delivery_id", deliveryID),
			attribute.String("relay.event_id", eventID),
			attribute.String("relay.endpoint_id", endpointID),
		),
	)
}

// EndDeliverySpan ends a delivery span with result attributes.
func (t *Tracer) EndDeliverySpan(span trace.Span, statusCode, latencyMs int, err string) {
	span.SetAttributes(
		attribute.Int("http.status_code", statusCode),
		attribute.Int("relay.latency_ms", latencyMs),
	)
	if err != "" {
		span.SetAttributes(attribute.String("relay.error", err))
	}
	span.End()
}
