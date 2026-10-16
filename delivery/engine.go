package delivery

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	log "github.com/xraph/go-utils/log"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/errs"
	"github.com/xraph/relay/internal/tracing"
	"github.com/xraph/relay/observability"
	"github.com/xraph/relay/ratelimit"
)

// EngineStore is the interface the engine needs for delivery operations.
type EngineStore interface {
	Dequeue(ctx context.Context, limit int) ([]*Delivery, error)
	UpdateDelivery(ctx context.Context, d *Delivery) error
	GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error)
	GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error)
	SetEnabled(ctx context.Context, epID id.ID, enabled bool) error
	RecordAttempt(ctx context.Context, a *Attempt) error
}

// DLQPusher pushes permanently failed deliveries to the dead letter queue.
type DLQPusher interface {
	PushFailed(ctx context.Context, d *Delivery, ep *endpoint.Endpoint, evt *event.Event, lastError string, lastStatusCode int) error
}

// EngineConfig holds engine configuration.
type EngineConfig struct {
	Concurrency  int
	PollInterval time.Duration
	// MaxPollInterval caps the idle backoff. When polls come back empty the
	// poll interval doubles from PollInterval up to this value, so an idle
	// engine stops hammering the store every PollInterval. Defaults to 30s.
	MaxPollInterval time.Duration
	BatchSize       int
	RequestTimeout  time.Duration
	RetrySchedule   []time.Duration
	Metrics         *observability.Metrics

	// Deprecated: Tracer is ignored. The engine traces every delivery
	// through the global tracer provider, which forge.New installs.
	Tracer *observability.Tracer //nolint:staticcheck // the deprecated type, kept so old callers compile
	// HTTPClient sends deliveries. Nil means a plain client with RequestTimeout.
	HTTPClient *http.Client
}

// Engine is the delivery worker pool that dequeues and processes deliveries.
type Engine struct {
	store   EngineStore
	sender  *Sender
	retrier *Retrier
	dlq     DLQPusher
	limiter *ratelimit.Limiter
	config  EngineConfig
	logger  log.Logger

	wakeCh chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// inFlight counts the deliveries being processed. It changes under
	// inFlightMu together with the gauge it feeds, so the gauge is always set
	// from the exact count, never nudged up and down.
	inFlightMu sync.Mutex
	inFlight   int
}

// Why a dequeued delivery was not attempted. Each is the reason label on
// relay_deliveries_skipped_total.
const (
	// skipRateLimited: the endpoint's rate limit held it back, and it went
	// back in the queue for when a token is due.
	skipRateLimited = "rate_limited"
	// skipOrphaned: its endpoint or event no longer exists, so it failed.
	skipOrphaned = "orphaned"
	// skipStoreError: loading its endpoint or event failed, and it went back
	// in the queue.
	skipStoreError = "store_error"
)

// statusStoreError describes a failed span whose store call went wrong. The
// error itself is logged; the span carries its Go type.
const statusStoreError = "store error"

// NewEngine creates a delivery engine.
func NewEngine(store EngineStore, dlq DLQPusher, cfg EngineConfig, logger log.Logger) *Engine {
	if logger == nil {
		logger = log.NewNoopLogger()
	}
	if cfg.MaxPollInterval <= 0 {
		cfg.MaxPollInterval = 30 * time.Second
	}
	if cfg.MaxPollInterval < cfg.PollInterval {
		cfg.MaxPollInterval = cfg.PollInterval
	}
	sender := NewSender(cfg.RequestTimeout)
	if cfg.HTTPClient != nil {
		sender = NewSenderWithClient(cfg.HTTPClient, cfg.RequestTimeout)
	}
	return &Engine{
		store:   store,
		sender:  sender,
		retrier: NewRetrier(cfg.RetrySchedule),
		dlq:     dlq,
		limiter: ratelimit.New(),
		config:  cfg,
		logger:  logger,
		wakeCh:  make(chan struct{}, 1),
	}
}

// Start begins the delivery workers and poll loop. The engine keeps ctx's
// values and cancellation but not its span: each delivery starts a trace of
// its own, and an empty poll starts none.
func (e *Engine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(tracing.Detach(ctx))
	e.SyncPending(ctx)

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

// Wake nudges the poll loop to check for pending deliveries immediately,
// resetting any idle backoff. It is non-blocking and safe to call from any
// goroutine, including before Start.
func (e *Engine) Wake() {
	select {
	case e.wakeCh <- struct{}{}:
	default:
	}
}

// pollLoop dequeues pending deliveries and dispatches them to workers. Empty
// polls double the wait up to MaxPollInterval so an idle engine doesn't issue
// a dequeue (an UPDATE/findAndModify against the store) every PollInterval;
// any dequeued work or a Wake call resets the cadence to PollInterval.
func (e *Engine) pollLoop(ctx context.Context) {
	interval := e.config.PollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	sem := make(chan struct{}, e.config.Concurrency)

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wakeCh:
			interval = e.config.PollInterval
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}

		batch, err := e.store.Dequeue(ctx, e.config.BatchSize)
		if err != nil {
			e.logger.Error("dequeue failed", log.Any("error", err))
		}

		e.SyncPending(ctx)
		if len(batch) > 0 {
			interval = e.config.PollInterval
		} else {
			// Back off on empty polls and on errors alike; errors hot-
			// spinning at PollInterval would only pile onto a struggling
			// store.
			interval = min(interval*2, e.config.MaxPollInterval)
		}

		for _, d := range batch {
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}

			e.wg.Add(1)
			e.trackInFlight(1)
			go func(del *Delivery) {
				defer e.wg.Done()
				defer func() {
					e.trackInFlight(-1)
					<-sem
				}()
				e.process(ctx, del)
			}(d)
		}

		timer.Reset(interval)
	}
}

// process handles a single delivery: fetch endpoint + event, send, decide,
// update. That is one unit of work, so it gets a root span of its own.
func (e *Engine) process(ctx context.Context, d *Delivery) {
	// Outside the span: a count after every delivery is bookkeeping, and
	// would put a query under every delivery trace.
	defer e.SyncPending(ctx)

	attrs := []attribute.KeyValue{
		attribute.String("relay.delivery.id", d.ID.String()),
		attribute.String("relay.event.id", d.EventID.String()),
		attribute.String("relay.endpoint.id", d.EndpointID.String()),
	}
	if d.EventType != "" {
		attrs = append(attrs, attribute.String("relay.event.type", d.EventType))
	}
	ctx, span := tracing.Start(ctx, "deliver webhook",
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
	defer span.End()

	ep, err := e.store.GetEndpoint(ctx, d.EndpointID)
	if err != nil {
		e.logger.Error("get endpoint failed",
			log.String("delivery_id", d.ID.String()), log.String("endpoint_id", d.EndpointID.String()), log.Any("error", err))
		e.release(ctx, span, d, err, errs.ErrEndpointNotFound, "endpoint no longer exists")
		return
	}

	evt, err := e.store.GetEvent(ctx, d.EventID)
	if err != nil {
		e.logger.Error("get event failed",
			log.String("delivery_id", d.ID.String()), log.String("event_id", d.EventID.String()), log.Any("error", err))
		e.release(ctx, span, d, err, errs.ErrEventNotFound, "event no longer exists")
		return
	}

	// Held back by the endpoint's rate limit: back in the queue for when a
	// token is due, and no attempt counted, because nothing was sent.
	if !e.limiter.Allow(ep.ID.String(), ep.RateLimit) {
		d.State = StatePending
		d.NextAttemptAt = time.Now().UTC().Add(time.Second / time.Duration(ep.RateLimit))
		e.recordSkip(span, skipRateLimited)
		if updateErr := e.store.UpdateDelivery(ctx, d); updateErr != nil {
			e.logger.Error("defer rate-limited delivery failed",
				log.String("delivery_id", d.ID.String()), log.Any("error", updateErr))
			storeFailed(span, updateErr)
		}
		return
	}

	// Perform the HTTP delivery.
	attemptedAt := time.Now().UTC()
	d.AttemptCount++
	result := e.sender.Send(ctx, ep, evt, d)

	// Record result on delivery.
	d.LastError = result.Error
	d.LastStatusCode = result.StatusCode
	d.LastResponse = result.Response
	d.LastLatencyMs = result.LatencyMs

	// Decide what to do next.
	decision := e.retrier.Decide(result, d)
	e.recordAttempt(span, d.AttemptCount, result, outcomeOf(decision))

	switch decision {
	case Delivered:
		now := time.Now().UTC()
		d.State = StateDelivered
		d.CompletedAt = &now
		e.logger.Debug("delivered",
			log.String("delivery_id", d.ID.String()), log.Int("status", result.StatusCode), log.Int("latency_ms", result.LatencyMs))

	case Retry:
		// Dequeue hands the engine a claimed row, and every persistent
		// backend marks the claim in State ("delivering", or "delivered" on
		// redis). Only pending rows are dequeued, so a retry that kept the
		// claimed state was never attempted again.
		d.State = StatePending
		d.NextAttemptAt = e.retrier.ComputeNextAttempt(d.AttemptCount)
		e.logger.Debug("retry scheduled",
			log.String("delivery_id", d.ID.String()), log.Int("attempt", d.AttemptCount), log.Any("next_at", d.NextAttemptAt))

	case DLQ:
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		e.pushDLQ(ctx, span, d, ep, evt, result)
		e.logger.Warn("delivery failed permanently",
			log.String("delivery_id", d.ID.String()), log.Int("status", result.StatusCode), log.String("error", result.Error))

	case DisableEndpoint:
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		if disableErr := e.store.SetEnabled(ctx, d.EndpointID, false); disableErr != nil {
			e.logger.Error("disable endpoint failed",
				log.String("endpoint_id", d.EndpointID.String()), log.Any("error", disableErr))
			storeFailed(span, disableErr)
		}
		e.pushDLQ(ctx, span, d, ep, evt, result)
		e.logger.Warn("endpoint disabled (410 Gone)",
			log.String("endpoint_id", d.EndpointID.String()), log.String("delivery_id", d.ID.String()))
	}

	// One row per attempt, written before the delivery update. A failed
	// write is logged and does not stop the update: a gap in the history is
	// better than a delivery left claimed.
	attempt := &Attempt{
		ID:          id.NewAttemptID(),
		DeliveryID:  d.ID,
		AttemptNum:  d.AttemptCount,
		StatusCode:  result.StatusCode,
		Error:       result.Error,
		Response:    result.Response,
		LatencyMs:   result.LatencyMs,
		Outcome:     outcomeOf(decision),
		AttemptedAt: attemptedAt,
	}
	if decision == Retry {
		next := d.NextAttemptAt
		attempt.NextAttemptAt = &next
	}
	if recErr := e.store.RecordAttempt(ctx, attempt); recErr != nil {
		e.logger.Error("record attempt failed",
			log.String("delivery_id", d.ID.String()), log.Any("error", recErr))
		storeFailed(span, recErr)
	}

	if updateErr := e.store.UpdateDelivery(ctx, d); updateErr != nil {
		e.logger.Error("update delivery failed",
			log.String("delivery_id", d.ID.String()), log.Any("error", updateErr))
		storeFailed(span, updateErr)
	}
}

// release hands back a delivery the engine claimed but could not attempt.
// Returning without writing leaves the row claimed, and a claimed row is
// never dequeued again.
//
// A missing endpoint or event will not come back, so the delivery fails.
// That is not an error on the span: nothing went wrong in relay. Anything
// else is a store hiccup, so it goes back to pending for a later attempt
// without counting one: nothing was sent.
func (e *Engine) release(ctx context.Context, span trace.Span, d *Delivery, err, gone error, reason string) {
	if errors.Is(err, gone) {
		now := time.Now().UTC()
		d.State = StateFailed
		d.CompletedAt = &now
		d.LastError = reason
		e.recordSkip(span, skipOrphaned)
	} else {
		d.State = StatePending
		d.NextAttemptAt = e.retrier.ComputeNextAttempt(1)
		e.recordSkip(span, skipStoreError)
		storeFailed(span, err)
	}
	if updateErr := e.store.UpdateDelivery(ctx, d); updateErr != nil {
		e.logger.Error("release delivery failed",
			log.String("delivery_id", d.ID.String()), log.Any("error", updateErr))
		storeFailed(span, updateErr)
	}
}

// storeFailed marks span failed by a store call. The call site logs the
// error; the span gets only its Go type, because driver messages quote
// values.
func storeFailed(span trace.Span, err error) {
	tracing.Fail(span, statusStoreError, tracing.ErrorType(err))
}

// pushDLQ parks a delivery that failed for good in the dead letter queue.
func (e *Engine) pushDLQ(ctx context.Context, span trace.Span, d *Delivery, ep *endpoint.Endpoint, evt *event.Event, result Result) {
	if e.dlq == nil {
		return
	}
	err := e.dlq.PushFailed(ctx, d, ep, evt, result.Error, result.StatusCode)
	if err != nil {
		e.logger.Error("push to DLQ failed",
			log.String("delivery_id", d.ID.String()), log.Any("error", err))
		storeFailed(span, err)
	}
	if e.config.Metrics != nil {
		e.config.Metrics.RecordDLQPush(err)
	}
}

// recordAttempt puts an attempt's outcome on its span, counts it and, when a
// request went out, times it by status class or, with no response, by error
// class. Any outcome but delivered fails the span. Its status names the
// response status or the error class, never the error text: a transport
// error quotes the endpoint's URL.
func (e *Engine) recordAttempt(span trace.Span, attemptNum int, result Result, outcome Outcome) {
	span.SetAttributes(
		attribute.Int("relay.delivery.attempt", attemptNum),
		attribute.String("relay.delivery.outcome", string(outcome)),
	)
	if result.StatusCode > 0 {
		span.SetAttributes(semconv.HTTPResponseStatusCode(result.StatusCode))
	}
	if outcome != OutcomeDelivered {
		class := failureClass(result)
		span.SetAttributes(semconv.ErrorTypeKey.String(class))
		tracing.Fail(span, class, result.errType)
	}

	m := e.config.Metrics
	if m == nil {
		return
	}
	m.RecordAttempt(string(outcome))
	if !result.sent {
		return
	}
	errType := "none"
	if result.StatusCode == 0 {
		errType = result.errClass
	}
	m.ObserveDelivery(statusClass(result.StatusCode), errType, result.duration.Seconds())
}

// failureClass is the error.type of a failed attempt: the response status as
// a string when there was one, as the HTTP conventions have it, or else the
// error class.
func failureClass(result Result) string {
	switch {
	case result.StatusCode > 0:
		return strconv.Itoa(result.StatusCode)
	case result.errClass != "":
		return result.errClass
	default:
		return errClassOther
	}
}

// recordSkip puts the reason a dequeued delivery was not attempted on its
// span, and counts it.
func (e *Engine) recordSkip(span trace.Span, reason string) {
	span.SetAttributes(attribute.String("relay.delivery.outcome", reason))
	if e.config.Metrics != nil {
		e.config.Metrics.RecordSkip(reason)
	}
}

// trackInFlight moves the in-flight count by delta and publishes it.
func (e *Engine) trackInFlight(delta int) {
	if e.config.Metrics == nil {
		return
	}
	e.inFlightMu.Lock()
	defer e.inFlightMu.Unlock()
	e.inFlight += delta
	e.config.Metrics.DeliveriesInFlight.Set(float64(e.inFlight))
}

// statusClass is the status_class label for a response status. Zero means no
// response came back.
func statusClass(code int) string {
	switch {
	case code == 0:
		return "none"
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}

// SyncPending refreshes the pending gauge when the store exposes its counter.
// Custom engine stores without this optional capability leave it unchanged.
func (e *Engine) SyncPending(ctx context.Context) {
	if e.config.Metrics == nil {
		return
	}
	counter, ok := e.store.(observability.PendingCounter)
	if !ok {
		return
	}
	if err := e.config.Metrics.SyncPending(ctx, counter); err != nil {
		e.logger.Debug("pending delivery count unavailable", log.Any("error", err))
	}
}
