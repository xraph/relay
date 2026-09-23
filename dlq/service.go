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
//
// This is a destructive read: it sends a real webhook to a real receiver,
// which cannot tell it apart from the original. Each entry is sent at most
// once, however many callers race for it: the entry is claimed atomically
// before anything is sent, and every caller that loses the claim gets
// ErrAlreadyReplayed having sent nothing.
//
// The replayed delivery takes its retry budget from config. The per-backend
// Replay this replaced left MaxAttempts at 0 on postgres, sqlite and redis,
// so the retrier evaluated `1 < 0` and sent the delivery straight back here.
func (svc *Service) Replay(ctx context.Context, dlqID id.ID) error {
	entry, err := svc.store.GetDLQ(ctx, dlqID)
	if err != nil {
		return err
	}
	return svc.replayEntry(ctx, entry)
}

// replayEntry claims entry, then sends it. The order is the whole point.
//
// Claiming first means two callers can never both send: the store lets
// exactly one claim through. The cost is that a send can fail after its claim
// succeeded, so a failed send releases the claim, leaving the entry exactly as
// replayable as before. If the release fails too, the entry stays claimed with
// nothing sent; the caller gets both errors and can clear it by hand. That
// takes two failures in a row, which is far rarer than two operators pressing
// replay at the same time.
func (svc *Service) replayEntry(ctx context.Context, entry *Entry) error {
	now := time.Now().UTC()
	if err := svc.store.MarkReplayed(ctx, entry.ID, now); err != nil {
		return err
	}

	d := &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       entry.EventID,
		EndpointID:    entry.EndpointID,
		State:         delivery.StatePending,
		AttemptCount:  0,
		MaxAttempts:   svc.maxAttempts,
		NextAttemptAt: now,
	}
	if err := svc.enq.Enqueue(ctx, d); err != nil {
		enqErr := fmt.Errorf("dlq: replay enqueue: %w", err)
		if relErr := svc.store.ReleaseReplay(ctx, entry.ID); relErr != nil {
			return errors.Join(enqErr, fmt.Errorf(
				"dlq: release claim on %s after a failed send (it is marked "+
					"replayed but nothing was sent): %w", entry.ID, relErr))
		}
		return enqErr
	}
	return nil
}

// ReplayBulk re-enqueues every un-replayed DLQ entry that failed inside the
// window. Entries already replayed are skipped, so calling it twice over the
// same window does not send every webhook in it twice.
//
// It returns the number replayed. A failure partway through returns the count
// achieved so far along with the error: those deliveries have been enqueued
// and will be sent, and the caller needs to know how many.
//
// The window is on failed_at, as it always was: ListDLQ filters From and To on
// that column on every backend. A zero Limit means every matching entry on
// every backend, so the window is never silently truncated to a page.
func (svc *Service) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	entries, err := svc.store.ListDLQ(ctx, ListOpts{From: &from, To: &to})
	if err != nil {
		return 0, err
	}

	var count int64
	for _, e := range entries {
		if e.ReplayedAt != nil {
			continue
		}
		// The listed entry already carries what replayEntry needs, so this
		// skips the extra read Replay would make. ReplayedAt was checked above
		// only as a shortcut; the claim inside replayEntry is what decides.
		if err := svc.replayEntry(ctx, e); err != nil {
			// Another caller claimed it between the list and now. Not a
			// failure: it was sent once, which is the point.
			if errors.Is(err, ErrAlreadyReplayed) {
				continue
			}
			return count, err
		}
		count++
	}
	return count, nil
}

// Purge removes old DLQ entries.
func (svc *Service) Purge(ctx context.Context, before time.Time) (int64, error) {
	return svc.store.Purge(ctx, before)
}

// Count returns the total number of DLQ entries.
func (svc *Service) Count(ctx context.Context) (int64, error) {
	return svc.store.CountDLQ(ctx)
}
