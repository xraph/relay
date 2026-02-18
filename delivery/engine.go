package delivery

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
)

// EngineStore is the interface the engine needs for delivery operations.
type EngineStore interface {
	Dequeue(ctx context.Context, limit int) ([]*Delivery, error)
	UpdateDelivery(ctx context.Context, d *Delivery) error
	GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error)
	GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error)
	SetEnabled(ctx context.Context, epID id.ID, enabled bool) error
}

// DLQPusher pushes permanently failed deliveries to the dead letter queue.
type DLQPusher interface {
	PushFailed(ctx context.Context, d *Delivery, ep *endpoint.Endpoint, evt *event.Event, lastError string, lastStatusCode int) error
}

// EngineConfig holds engine configuration.
type EngineConfig struct {
	Concurrency    int
	PollInterval   time.Duration
	BatchSize      int
	RequestTimeout time.Duration
	RetrySchedule  []time.Duration
}

// Engine is the delivery worker pool that dequeues and processes deliveries.
type Engine struct {
	store   EngineStore
	sender  *Sender
	retrier *Retrier
	dlq     DLQPusher
	config  EngineConfig
	logger  *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewEngine creates a delivery engine.
func NewEngine(store EngineStore, dlq DLQPusher, cfg EngineConfig, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		store:   store,
		sender:  NewSender(cfg.RequestTimeout),
		retrier: NewRetrier(cfg.RetrySchedule),
		dlq:     dlq,
		config:  cfg,
		logger:  logger,
	}
}

// Start begins the delivery workers and poll loop.
func (e *Engine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.pollLoop(ctx)
	}()
}

// Stop cancels the poll loop and waits for in-flight deliveries to complete.
func (e *Engine) Stop(_ context.Context) {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

// pollLoop periodically dequeues pending deliveries and dispatches them to workers.
func (e *Engine) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(e.config.PollInterval)
	defer ticker.Stop()

	sem := make(chan struct{}, e.config.Concurrency)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			batch, err := e.store.Dequeue(ctx, e.config.BatchSize)
			if err != nil {
				e.logger.ErrorContext(ctx, "dequeue failed", "error", err)
				continue
			}

			for _, d := range batch {
				select {
				case <-ctx.Done():
					return
				case sem <- struct{}{}:
				}

				e.wg.Add(1)
				go func(del *Delivery) {
					defer e.wg.Done()
					defer func() { <-sem }()
					e.process(ctx, del)
				}(d)
			}
		}
	}
}

// process handles a single delivery: fetch endpoint + event, send, decide, update.
func (e *Engine) process(ctx context.Context, d *Delivery) {
	ep, err := e.store.GetEndpoint(ctx, d.EndpointID)
	if err != nil {
		e.logger.ErrorContext(ctx, "get endpoint failed",
			"delivery_id", d.ID, "endpoint_id", d.EndpointID, "error", err)
		return
	}

	evt, err := e.store.GetEvent(ctx, d.EventID)
	if err != nil {
		e.logger.ErrorContext(ctx, "get event failed",
			"delivery_id", d.ID, "event_id", d.EventID, "error", err)
		return
	}

	// Perform the HTTP delivery.
	d.AttemptCount++
	result := e.sender.Send(ctx, ep, evt, d)

	// Record result on delivery.
	d.LastError = result.Error
	d.LastStatusCode = result.StatusCode
	d.LastResponse = result.Response
	d.LastLatencyMs = result.LatencyMs

	// Decide what to do next.
	decision := e.retrier.Decide(result, d)

	switch decision {
	case Delivered:
		now := time.Now().UTC()
		d.State = StateDelivered
		d.CompletedAt = &now
		e.logger.DebugContext(ctx, "delivered",
			"delivery_id", d.ID, "status", result.StatusCode, "latency_ms", result.LatencyMs)

	case Retry:
		d.NextAttemptAt = e.retrier.ComputeNextAttempt(d.AttemptCount)
		e.logger.DebugContext(ctx, "retry scheduled",
			"delivery_id", d.ID, "attempt", d.AttemptCount, "next_at", d.NextAttemptAt)

	case DLQ:
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		if e.dlq != nil {
			if dlqErr := e.dlq.PushFailed(ctx, d, ep, evt, result.Error, result.StatusCode); dlqErr != nil {
				e.logger.ErrorContext(ctx, "push to DLQ failed",
					"delivery_id", d.ID, "error", dlqErr)
			}
		}
		e.logger.WarnContext(ctx, "delivery failed permanently",
			"delivery_id", d.ID, "status", result.StatusCode, "error", result.Error)

	case DisableEndpoint:
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		if disableErr := e.store.SetEnabled(ctx, d.EndpointID, false); disableErr != nil {
			e.logger.ErrorContext(ctx, "disable endpoint failed",
				"endpoint_id", d.EndpointID, "error", disableErr)
		}
		if e.dlq != nil {
			if dlqErr := e.dlq.PushFailed(ctx, d, ep, evt, result.Error, result.StatusCode); dlqErr != nil {
				e.logger.ErrorContext(ctx, "push to DLQ failed",
					"delivery_id", d.ID, "error", dlqErr)
			}
		}
		e.logger.WarnContext(ctx, "endpoint disabled (410 Gone)",
			"endpoint_id", d.EndpointID, "delivery_id", d.ID)
	}

	if updateErr := e.store.UpdateDelivery(ctx, d); updateErr != nil {
		e.logger.ErrorContext(ctx, "update delivery failed",
			"delivery_id", d.ID, "error", updateErr)
	}
}
