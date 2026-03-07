package delivery

import (
	"context"
	"sync"
	"time"

	log "github.com/xraph/go-utils/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/observability"
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
	Metrics        *observability.Metrics
	Tracer         *observability.Tracer
}

// Engine is the delivery worker pool that dequeues and processes deliveries.
type Engine struct {
	store   EngineStore
	sender  *Sender
	retrier *Retrier
	dlq     DLQPusher
	config  EngineConfig
	logger  log.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewEngine creates a delivery engine.
func NewEngine(store EngineStore, dlq DLQPusher, cfg EngineConfig, logger log.Logger) *Engine {
	if logger == nil {
		logger = log.NewNoopLogger()
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
				e.logger.Error("dequeue failed", log.Any("error", err))
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
	// Start a tracing span for this delivery attempt.
	var span trace.Span
	if e.config.Tracer != nil {
		ctx, span = e.config.Tracer.StartDeliverySpan(ctx, d.ID.String(), d.EventID.String(), d.EndpointID.String())
	}

	ep, err := e.store.GetEndpoint(ctx, d.EndpointID)
	if err != nil {
		e.logger.Error("get endpoint failed",
			log.String("delivery_id", d.ID.String()), log.String("endpoint_id", d.EndpointID.String()), log.Any("error", err))
		if span != nil {
			e.config.Tracer.EndDeliverySpan(span, 0, 0, err.Error())
		}
		return
	}

	evt, err := e.store.GetEvent(ctx, d.EventID)
	if err != nil {
		e.logger.Error("get event failed",
			log.String("delivery_id", d.ID.String()), log.String("event_id", d.EventID.String()), log.Any("error", err))
		if span != nil {
			e.config.Tracer.EndDeliverySpan(span, 0, 0, err.Error())
		}
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

	latencySeconds := float64(result.LatencyMs) / 1000.0

	// Decide what to do next.
	decision := e.retrier.Decide(result, d)

	switch decision {
	case Delivered:
		now := time.Now().UTC()
		d.State = StateDelivered
		d.CompletedAt = &now
		if e.config.Metrics != nil {
			e.config.Metrics.RecordDelivery("delivered", latencySeconds)
			e.config.Metrics.PendingDeliveries.Dec()
		}
		e.logger.Debug("delivered",
			log.String("delivery_id", d.ID.String()), log.Int("status", result.StatusCode), log.Int("latency_ms", result.LatencyMs))

	case Retry:
		d.NextAttemptAt = e.retrier.ComputeNextAttempt(d.AttemptCount)
		if e.config.Metrics != nil {
			e.config.Metrics.RecordDelivery("retried", latencySeconds)
		}
		e.logger.Debug("retry scheduled",
			log.String("delivery_id", d.ID.String()), log.Int("attempt", d.AttemptCount), log.Any("next_at", d.NextAttemptAt))

	case DLQ:
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		if e.dlq != nil {
			if dlqErr := e.dlq.PushFailed(ctx, d, ep, evt, result.Error, result.StatusCode); dlqErr != nil {
				e.logger.Error("push to DLQ failed",
					log.String("delivery_id", d.ID.String()), log.Any("error", dlqErr))
			}
		}
		if e.config.Metrics != nil {
			e.config.Metrics.RecordDelivery("failed", latencySeconds)
			e.config.Metrics.PendingDeliveries.Dec()
			e.config.Metrics.DLQSize.Inc()
		}
		e.logger.Warn("delivery failed permanently",
			log.String("delivery_id", d.ID.String()), log.Int("status", result.StatusCode), log.String("error", result.Error))

	case DisableEndpoint:
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		if disableErr := e.store.SetEnabled(ctx, d.EndpointID, false); disableErr != nil {
			e.logger.Error("disable endpoint failed",
				log.String("endpoint_id", d.EndpointID.String()), log.Any("error", disableErr))
		}
		if e.dlq != nil {
			if dlqErr := e.dlq.PushFailed(ctx, d, ep, evt, result.Error, result.StatusCode); dlqErr != nil {
				e.logger.Error("push to DLQ failed",
					log.String("delivery_id", d.ID.String()), log.Any("error", dlqErr))
			}
		}
		if e.config.Metrics != nil {
			e.config.Metrics.RecordDelivery("failed", latencySeconds)
			e.config.Metrics.PendingDeliveries.Dec()
			e.config.Metrics.DLQSize.Inc()
		}
		e.logger.Warn("endpoint disabled (410 Gone)",
			log.String("endpoint_id", d.EndpointID.String()), log.String("delivery_id", d.ID.String()))
	}

	// End the tracing span with the final result.
	if span != nil {
		e.config.Tracer.EndDeliverySpan(span, d.LastStatusCode, d.LastLatencyMs, d.LastError)
	}

	if updateErr := e.store.UpdateDelivery(ctx, d); updateErr != nil {
		e.logger.Error("update delivery failed",
			log.String("delivery_id", d.ID.String()), log.Any("error", updateErr))
	}
}
