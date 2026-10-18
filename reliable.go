package relay

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/internal/acceptanceobs"
	"github.com/xraph/relay/internal/tracing"
)

// SendReliable atomically accepts an event and its complete scoped fanout. The
// host supplies authorized source identity and scope. A receipt proves local
// acceptance, not successful HTTP delivery. Retrying returns the original
// receipt even after catalog changes, endpoint changes or event retention.
func (r *Relay) SendReliable(ctx context.Context, req acceptance.Request) (_ *acceptance.Receipt, err error) {
	ctx, span := tracing.Start(ctx, "accept event", trace.WithSpanKind(trace.SpanKindInternal))
	defer func() {
		if err != nil {
			failSpan(span, err)
		}
		span.End()
	}()

	s, ok := r.store.(acceptance.Store)
	if !ok {
		return nil, acceptance.ErrUnsupported
	}
	req, err = req.Normalize()
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.String("relay.event.type", req.Type))
	defer r.engine.SyncPending(ctx)
	ctx, observation := acceptanceobs.WithCommitObservation(ctx)
	receipt, err := s.AcceptEvent(ctx, req, r.config.MaxRetries)
	if err != nil {
		return nil, err
	}
	// Created is false for a retry that found its original receipt.
	span.SetAttributes(
		attribute.String("relay.event.id", receipt.EventID.String()),
		attribute.Bool("relay.acceptance.created", observation.Created()),
		attribute.Int("relay.fanout.count", len(receipt.Recipients)),
	)
	if r.metrics != nil && observation.Created() {
		r.metrics.EventsSentTotal.Inc()
	}
	r.engine.Wake()
	return receipt, nil
}
