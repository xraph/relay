package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	log "github.com/xraph/go-utils/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/internal/tracing"
	"github.com/xraph/relay/scope"
	"github.com/xraph/relay/store"
)

// wireServices initializes the internal services after options have been applied.
func (r *Relay) wireServices() {
	r.catalog = catalog.NewCatalog(r.store, catalog.Config{
		CacheTTL: r.config.CacheTTL,
	}, r.logger)

	r.validator = catalog.NewValidator()

	r.endpointSvc = endpoint.NewService(r.store, r.logger)

	r.dlqSvc = dlq.NewService(r.store, r.store, dlq.Config{
		MaxAttempts: r.config.MaxRetries,
	}, r.logger)

	r.engine = delivery.NewEngine(r.store, r.dlqSvc, delivery.EngineConfig{
		Concurrency:     r.config.Concurrency,
		PollInterval:    r.config.PollInterval,
		MaxPollInterval: r.config.MaxPollInterval,
		BatchSize:       r.config.BatchSize,
		RequestTimeout:  r.config.RequestTimeout,
		RetrySchedule:   r.config.RetrySchedule,
		Metrics:         r.metrics,
		HTTPClient:      r.config.HTTPClient,
	}, r.logger)
}

// Start begins the delivery engine. Stores that support push notifications
// (store.WakeNotifier, e.g. Postgres LISTEN/NOTIFY) additionally wake the
// engine on cross-instance enqueues so deliveries are picked up without
// waiting out the idle poll backoff.
//
// The engine and the listener keep ctx's values and cancellation but not its
// span. Under Forge, ctx carries the "start relay" boot span, and every poll
// and delivery after it would otherwise land in the boot trace.
func (r *Relay) Start(ctx context.Context) {
	ctx = tracing.Detach(ctx)
	r.engine.Start(ctx)

	if wn, ok := r.store.(store.WakeNotifier); ok {
		stop, err := wn.StartWakeListener(ctx, r.engine.Wake)
		if err != nil {
			r.logger.Warn("wake listener unavailable; relying on polling",
				log.Any("error", err))
		} else {
			r.wakeStop = stop
		}
	}
}

// Stop gracefully shuts down the delivery engine.
func (r *Relay) Stop(ctx context.Context) {
	if r.wakeStop != nil {
		r.wakeStop()
		r.wakeStop = nil
	}
	r.engine.Stop(ctx)
}

// RegisterEventType registers a webhook event type definition in the catalog.
func (r *Relay) RegisterEventType(ctx context.Context, def catalog.WebhookDefinition, opts ...catalog.RegisterOption) (*catalog.EventType, error) {
	return r.catalog.RegisterType(ctx, def, opts...)
}

// Send validates an event and fans out deliveries. Memory and PostgreSQL commit
// event and fanout atomically. Other stores retain the legacy sequential path.
// Idempotency keys remain key-only no-ops; use SendReliable for content-bound
// receipts and recovery across catalog changes and event retention.
func (r *Relay) Send(ctx context.Context, evt *event.Event) (err error) {
	defer r.engine.SyncPending(ctx)

	ctx, span := tracing.Start(ctx, "send event",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("relay.event.type", evt.Type)),
	)
	defer func() {
		if err != nil {
			failSpan(span, err)
		}
		span.End()
	}()

	// 1. Validate event type exists.
	et, err := r.catalog.GetType(ctx, evt.Type)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrEventTypeNotFound, evt.Type)
	}

	// 2. Reject deprecated event types.
	if et.IsDeprecated {
		return fmt.Errorf("%w: %s", ErrEventTypeDeprecated, evt.Type)
	}

	// 3. Validate payload against schema (if defined).
	if len(et.Definition.Schema) > 0 {
		if validateErr := r.validator.Validate(et.Definition.Schema, evt.Data); validateErr != nil {
			return fmt.Errorf("%w: %s", ErrPayloadValidationFailed, validateErr.Error())
		}
	}

	// 4. Assign ID, capture scope, set entity timestamps.
	evt.Entity = entity.New()
	evt.ID = id.NewEventID()
	appID, orgID := scope.Capture(ctx)
	evt.ScopeAppID = appID
	evt.ScopeOrgID = orgID
	span.SetAttributes(attribute.String("relay.event.id", evt.ID.String()))

	if atomic, ok := r.store.(store.AtomicSender); ok {
		count, sendErr := atomic.SendEvent(ctx, evt, r.config.MaxRetries)
		if sendErr != nil {
			return fmt.Errorf("relay: atomic send: %w", sendErr)
		}
		span.SetAttributes(attribute.Int("relay.fanout.count", count))
		r.engine.Wake()
		if r.metrics != nil && count > 0 {
			r.metrics.EventsSentTotal.Inc()
		}
		return nil
	}

	// Persist the event. Idempotency key conflicts return a no-op success.
	if createErr := r.store.CreateEvent(ctx, evt); createErr != nil {
		if errors.Is(createErr, ErrDuplicateIdempotencyKey) {
			span.SetAttributes(attribute.Bool("relay.event.duplicate", true))
			return nil // idempotent: already processed
		}
		return fmt.Errorf("relay: persist event: %w", createErr)
	}

	// 5. Resolve matching endpoints.
	endpoints, err := r.store.Resolve(ctx, evt.TenantID, evt.Type)
	if err != nil {
		return fmt.Errorf("relay: resolve endpoints: %w", err)
	}

	span.SetAttributes(attribute.Int("relay.fanout.count", len(endpoints)))
	if len(endpoints) == 0 {
		return nil // no matching endpoints — nothing to deliver
	}

	// 6. Fan out: create one delivery per endpoint.
	now := time.Now().UTC()
	deliveries := make([]*delivery.Delivery, 0, len(endpoints))
	for _, ep := range endpoints {
		d := &delivery.Delivery{
			Entity:        entity.New(),
			ID:            id.NewDeliveryID(),
			EventID:       evt.ID,
			EndpointID:    ep.ID,
			EventType:     evt.Type,
			TenantID:      evt.TenantID,
			State:         delivery.StatePending,
			AttemptCount:  0,
			MaxAttempts:   r.config.MaxRetries,
			NextAttemptAt: now,
		}
		deliveries = append(deliveries, d)
	}

	if err := r.store.EnqueueBatch(ctx, deliveries); err != nil {
		return fmt.Errorf("relay: enqueue deliveries: %w", err)
	}

	// Nudge the delivery engine so in-process enqueues are picked up
	// immediately instead of waiting out the idle poll backoff.
	r.engine.Wake()

	if r.metrics != nil {
		r.metrics.EventsSentTotal.Inc()
	}

	r.logger.Debug("event sent",
		log.String("event_id", evt.ID.String()),
		log.String("type", evt.Type),
		log.Int("endpoints", len(endpoints)),
	)

	return nil
}

// spanErrors name the failures a caller can cause, for the span status and
// exception.type. Anything else (a store or driver error) is recorded by its
// Go type alone, because its message may quote the payload.
var spanErrors = []struct {
	err   error
	class string
	name  string
}{
	{ErrEventTypeNotFound, "event type not found", "relay.ErrEventTypeNotFound"},
	{ErrEventTypeDeprecated, "event type deprecated", "relay.ErrEventTypeDeprecated"},
	{ErrPayloadValidationFailed, "payload validation failed", "relay.ErrPayloadValidationFailed"},
	{acceptance.ErrInvalid, "invalid request", "acceptance.ErrInvalid"},
	{acceptance.ErrConflict, "content conflict", "acceptance.ErrConflict"},
	{acceptance.ErrUnsupported, "unsupported store", "acceptance.ErrUnsupported"},
	{acceptance.ErrTooManyRecipients, "too many recipients", "acceptance.ErrTooManyRecipients"},
}

// failSpan marks span failed by err without quoting err.
func failSpan(span trace.Span, err error) {
	for _, known := range spanErrors {
		if errors.Is(err, known.err) {
			tracing.Fail(span, known.class, known.name)
			return
		}
	}
	typ := tracing.ErrorType(err)
	tracing.Fail(span, typ, typ)
}

// Endpoints returns the endpoint management service.
func (r *Relay) Endpoints() *endpoint.Service {
	return r.endpointSvc
}

// Catalog returns the event type catalog.
func (r *Relay) Catalog() *catalog.Catalog {
	return r.catalog
}

// Health checks the health of the Relay by pinging its store.
func (r *Relay) Health(ctx context.Context) error {
	return r.store.Ping(ctx)
}

// Store returns the underlying store.
func (r *Relay) Store() store.Store {
	return r.store
}

// DLQ returns the DLQ service.
func (r *Relay) DLQ() *dlq.Service {
	return r.dlqSvc
}

// Config returns the configuration Relay was built with. It is fixed at
// construction; there is no setter.
func (r *Relay) Config() Config {
	return r.config
}
