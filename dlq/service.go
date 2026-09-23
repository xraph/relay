package dlq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	log "github.com/xraph/go-utils/log"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// ErrAlreadyReplayed is returned when a DLQ entry that has already been
// replayed is replayed again. Replaying re-sends a real webhook, so the second
// call is refused rather than silently duplicating the delivery.
// relay.ErrAlreadyReplayed is the same value.
var ErrAlreadyReplayed = errors.New("relay: dlq entry already replayed")

// DefaultMaxAttempts is the retry budget a replayed delivery receives when
// the configured value is unusable. Zero is unusable: it makes the retrier
// evaluate `1 < 0` and send the delivery straight back to the DLQ.
const DefaultMaxAttempts = 5

// Config tunes the DLQ service.
type Config struct {
	// MaxAttempts is the retry budget given to a replayed delivery. Values
	// below 1 fall back to DefaultMaxAttempts.
	MaxAttempts int
}

// Enqueuer is the delivery-side capability replay needs. The composite store
// satisfies it, so relay wires the same value in as both dependencies.
type Enqueuer interface {
	Enqueue(ctx context.Context, d *delivery.Delivery) error
}

// Service manages the dead letter queue.
type Service struct {
	store       Store
	enq         Enqueuer
	maxAttempts int
	logger      log.Logger
}

// NewService creates a new DLQ service.
func NewService(store Store, enq Enqueuer, cfg Config, logger log.Logger) *Service {
	if logger == nil {
		logger = log.NewNoopLogger()
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Service{
		store:       store,
		enq:         enq,
		maxAttempts: maxAttempts,
		logger:      logger,
	}
}

// MaxAttempts reports the retry budget a replayed delivery will receive.
func (svc *Service) MaxAttempts() int { return svc.maxAttempts }

// PushFailed creates a DLQ entry from a failed delivery. Implements delivery.DLQPusher.
func (svc *Service) PushFailed(ctx context.Context, d *delivery.Delivery, ep *endpoint.Endpoint, evt *event.Event, lastError string, lastStatusCode int) error {
	payload, marshalErr := json.Marshal(evt.Data)
	if marshalErr != nil {
		return fmt.Errorf("dlq: marshal payload: %w", marshalErr)
	}

	entry := &Entry{
		Entity:         entity.New(),
		ID:             id.NewDLQID(),
		DeliveryID:     d.ID,
		EventID:        d.EventID,
		EndpointID:     d.EndpointID,
		EventType:      evt.Type,
		TenantID:       ep.TenantID,
		URL:            ep.URL,
		Payload:        payload,
		Error:          lastError,
		AttemptCount:   d.AttemptCount,
		LastStatusCode: lastStatusCode,
		FailedAt:       time.Now().UTC(),
	}

	return svc.store.Push(ctx, entry)
}

// List returns DLQ entries matching the given options.
func (svc *Service) List(ctx context.Context, opts ListOpts) ([]*Entry, error) {
	return svc.store.ListDLQ(ctx, opts)
}

// Get returns a DLQ entry by ID.
func (svc *Service) Get(ctx context.Context, dlqID id.ID) (*Entry, error) {
	return svc.store.GetDLQ(ctx, dlqID)
}

// Replay re-enqueues a single DLQ entry for redelivery.
func (svc *Service) Replay(ctx context.Context, dlqID id.ID) error {
	return svc.store.Replay(ctx, dlqID)
}

// ReplayBulk re-enqueues all DLQ entries within a time range.
func (svc *Service) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	return svc.store.ReplayBulk(ctx, from, to)
}

// Purge removes old DLQ entries.
func (svc *Service) Purge(ctx context.Context, before time.Time) (int64, error) {
	return svc.store.Purge(ctx, before)
}

// Count returns the total number of DLQ entries.
func (svc *Service) Count(ctx context.Context) (int64, error) {
	return svc.store.CountDLQ(ctx)
}
