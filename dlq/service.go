package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Service manages the dead letter queue.
type Service struct {
	store  Store
	logger *slog.Logger
}

// NewService creates a new DLQ service.
func NewService(store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		logger: logger,
	}
}

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
