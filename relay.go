package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
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

	r.dlqSvc = dlq.NewService(r.store, r.logger)

	r.engine = delivery.NewEngine(r.store, r.dlqSvc, delivery.EngineConfig{
		Concurrency:    r.config.Concurrency,
		PollInterval:   r.config.PollInterval,
		BatchSize:      r.config.BatchSize,
		RequestTimeout: r.config.RequestTimeout,
		RetrySchedule:  r.config.RetrySchedule,
		Metrics:        r.metrics,
		Tracer:         r.tracer,
	}, r.logger)
}

// Start begins the delivery engine.
func (r *Relay) Start(ctx context.Context) {
	r.engine.Start(ctx)
}

// Stop gracefully shuts down the delivery engine.
func (r *Relay) Stop(ctx context.Context) {
	r.engine.Stop(ctx)
}

// RegisterEventType registers a webhook event type definition in the catalog.
func (r *Relay) RegisterEventType(ctx context.Context, def catalog.WebhookDefinition, opts ...catalog.RegisterOption) (*catalog.EventType, error) {
	return r.catalog.RegisterType(ctx, def, opts...)
}

// Send validates and persists an event, then fans out deliveries to matching endpoints.
//
// The critical path:
//  1. Look up event type from the catalog (reject unknown types).
//  2. Check if the event type is deprecated (reject if so).
//  3. Validate the event payload against the JSON Schema (if configured).
//  4. Persist the event (idempotency key dedup is handled here).
//  5. Resolve matching endpoints for this tenant + event type.
//  6. Enqueue one delivery per matched endpoint.
func (r *Relay) Send(ctx context.Context, evt *event.Event) error {
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

	// Persist the event. Idempotency key conflicts return a no-op success.
	if createErr := r.store.CreateEvent(ctx, evt); createErr != nil {
		if errors.Is(createErr, ErrDuplicateIdempotencyKey) {
			return nil // idempotent: already processed
		}
		return fmt.Errorf("relay: persist event: %w", createErr)
	}

	// 5. Resolve matching endpoints.
	endpoints, err := r.store.Resolve(ctx, evt.TenantID, evt.Type)
	if err != nil {
		return fmt.Errorf("relay: resolve endpoints: %w", err)
	}

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

	if r.metrics != nil {
		r.metrics.EventsSentTotal.Inc()
		r.metrics.PendingDeliveries.Add(float64(len(deliveries)))
	}

	r.logger.DebugContext(ctx, "event sent",
		"event_id", evt.ID,
		"type", evt.Type,
		"endpoints", len(endpoints),
	)

	return nil
}

// Endpoints returns the endpoint management service.
func (r *Relay) Endpoints() *endpoint.Service {
	return r.endpointSvc
}

// Catalog returns the event type catalog.
func (r *Relay) Catalog() *catalog.Catalog {
	return r.catalog
}

// Store returns the underlying store.
func (r *Relay) Store() store.Store {
	return r.store
}

// DLQ returns the DLQ service.
func (r *Relay) DLQ() *dlq.Service {
	return r.dlqSvc
}
