package redis

import (
	"context"
	"fmt"
	"math"
	"time"

	goredis "github.com/redis/go-redis/v9"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// dlqEntryModel is the JSON representation stored in Redis.
type dlqEntryModel struct {
	ID             string     `json:"id"`
	DeliveryID     string     `json:"delivery_id"`
	EventID        string     `json:"event_id"`
	EndpointID     string     `json:"endpoint_id"`
	TenantID       string     `json:"tenant_id"`
	EventType      string     `json:"event_type"`
	URL            string     `json:"url"`
	Payload        any        `json:"payload,omitempty"`
	Error          string     `json:"error"`
	AttemptCount   int        `json:"attempt_count"`
	LastStatusCode int        `json:"last_status_code"`
	ReplayedAt     *time.Time `json:"replayed_at,omitempty"`
	FailedAt       time.Time  `json:"failed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toDLQEntryModel(e *dlq.Entry) *dlqEntryModel {
	return &dlqEntryModel{
		ID:             e.ID.String(),
		DeliveryID:     e.DeliveryID.String(),
		EventID:        e.EventID.String(),
		EndpointID:     e.EndpointID.String(),
		TenantID:       e.TenantID,
		EventType:      e.EventType,
		URL:            e.URL,
		Payload:        e.Payload,
		Error:          e.Error,
		AttemptCount:   e.AttemptCount,
		LastStatusCode: e.LastStatusCode,
		ReplayedAt:     e.ReplayedAt,
		FailedAt:       e.FailedAt,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
	}
}

func fromDLQEntryModel(m *dlqEntryModel) (*dlq.Entry, error) {
	dlqID, err := id.ParseDLQID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse DLQ ID %q: %w", m.ID, err)
	}
	delID, err := id.ParseDeliveryID(m.DeliveryID)
	if err != nil {
		return nil, fmt.Errorf("parse delivery ID %q: %w", m.DeliveryID, err)
	}
	evtID, err := id.ParseEventID(m.EventID)
	if err != nil {
		return nil, fmt.Errorf("parse event ID %q: %w", m.EventID, err)
	}
	epID, err := id.ParseEndpointID(m.EndpointID)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint ID %q: %w", m.EndpointID, err)
	}
	return &dlq.Entry{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             dlqID,
		DeliveryID:     delID,
		EventID:        evtID,
		EndpointID:     epID,
		TenantID:       m.TenantID,
		EventType:      m.EventType,
		URL:            m.URL,
		Payload:        m.Payload,
		Error:          m.Error,
		AttemptCount:   m.AttemptCount,
		LastStatusCode: m.LastStatusCode,
		ReplayedAt:     m.ReplayedAt,
		FailedAt:       m.FailedAt,
	}, nil
}

func (s *Store) Push(ctx context.Context, entry *dlq.Entry) error {
	m := toDLQEntryModel(entry)
	key := entityKey(prefixDLQ, m.ID)

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: push dlq: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, zDLQAll, goredis.Z{Score: scoreFromTime(m.FailedAt), Member: m.ID})
	if m.TenantID != "" {
		pipe.ZAdd(ctx, zDLQTenant+m.TenantID, goredis.Z{Score: scoreFromTime(m.FailedAt), Member: m.ID})
	}
	if m.EndpointID != "" {
		pipe.ZAdd(ctx, zDLQEndpoint+m.EndpointID, goredis.Z{Score: scoreFromTime(m.FailedAt), Member: m.ID})
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/redis: push dlq indexes: %w", err)
	}
	return nil
}

func (s *Store) ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	zKey := zDLQAll
	if opts.TenantID != "" {
		zKey = zDLQTenant + opts.TenantID
	}
	if opts.EndpointID != nil {
		zKey = zDLQEndpoint + opts.EndpointID.String()
	}

	minScore := math.Inf(-1)
	maxScore := math.Inf(1)
	if opts.From != nil {
		minScore = scoreFromTime(*opts.From)
	}
	if opts.To != nil {
		maxScore = scoreFromTime(*opts.To)
	}

	ids, err := s.zRangeByScoreIDs(ctx, zKey, minScore, maxScore)
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list dlq: %w", err)
	}

	result := make([]*dlq.Entry, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- { // reverse for DESC order
		var m dlqEntryModel
		if err := s.getEntity(ctx, entityKey(prefixDLQ, ids[i]), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		entry, err := fromDLQEntryModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}

	return applyPagination(result, opts.Offset, opts.Limit), nil
}

func (s *Store) GetDLQ(ctx context.Context, dlqID id.ID) (*dlq.Entry, error) {
	var m dlqEntryModel
	if err := s.getEntity(ctx, entityKey(prefixDLQ, dlqID.String()), &m); err != nil {
		if isNotFound(err) {
			return nil, relay.ErrDLQNotFound
		}
		return nil, fmt.Errorf("relay/redis: get dlq: %w", err)
	}
	return fromDLQEntryModel(&m)
}

func (s *Store) Replay(ctx context.Context, dlqID id.ID) error {
	entry, err := s.GetDLQ(ctx, dlqID)
	if err != nil {
		return err
	}

	// Re-enqueue a new delivery.
	d := &delivery.Delivery{
		ID:            id.NewDeliveryID(),
		EventID:       entry.EventID,
		EndpointID:    entry.EndpointID,
		State:         delivery.StatePending,
		NextAttemptAt: now(),
	}
	d.CreatedAt = now()
	d.UpdatedAt = d.CreatedAt

	if enqueueErr := s.Enqueue(ctx, d); enqueueErr != nil {
		return enqueueErr
	}

	// Remove from DLQ.
	return s.deleteDLQEntry(ctx, dlqID.String(), entry.TenantID, entry.EndpointID.String())
}

func (s *Store) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	minScore := scoreFromTime(from)
	maxScore := scoreFromTime(to)

	ids, err := s.zRangeByScoreIDs(ctx, zDLQAll, minScore, maxScore)
	if err != nil {
		return 0, fmt.Errorf("relay/redis: replay bulk list: %w", err)
	}

	var count int64
	for _, entryID := range ids {
		var m dlqEntryModel
		if err := s.getEntity(ctx, entityKey(prefixDLQ, entryID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return count, err
		}

		entry, err := fromDLQEntryModel(&m)
		if err != nil {
			return count, err
		}

		d := &delivery.Delivery{
			ID:            id.NewDeliveryID(),
			EventID:       entry.EventID,
			EndpointID:    entry.EndpointID,
			State:         delivery.StatePending,
			NextAttemptAt: now(),
		}
		d.CreatedAt = now()
		d.UpdatedAt = d.CreatedAt

		if enqueueErr := s.Enqueue(ctx, d); enqueueErr != nil {
			return count, enqueueErr
		}

		if err := s.deleteDLQEntry(ctx, entryID, m.TenantID, m.EndpointID); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	maxScore := scoreFromTime(before)
	ids, err := s.zRangeByScoreIDs(ctx, zDLQAll, math.Inf(-1), maxScore)
	if err != nil {
		return 0, fmt.Errorf("relay/redis: purge list: %w", err)
	}

	var count int64
	for _, entryID := range ids {
		var m dlqEntryModel
		if err := s.getEntity(ctx, entityKey(prefixDLQ, entryID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return count, err
		}

		if err := s.deleteDLQEntry(ctx, entryID, m.TenantID, m.EndpointID); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func (s *Store) CountDLQ(ctx context.Context) (int64, error) {
	count, err := s.rdb.ZCard(ctx, zDLQAll).Result()
	if err != nil {
		return 0, fmt.Errorf("relay/redis: count dlq: %w", err)
	}
	return count, nil
}

// deleteDLQEntry removes a DLQ entry and its index entries.
func (s *Store) deleteDLQEntry(ctx context.Context, entryID, tenantID, endpointID string) error {
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, entityKey(prefixDLQ, entryID))
	pipe.ZRem(ctx, zDLQAll, entryID)
	if tenantID != "" {
		pipe.ZRem(ctx, zDLQTenant+tenantID, entryID)
	}
	if endpointID != "" {
		pipe.ZRem(ctx, zDLQEndpoint+endpointID, entryID)
	}
	_, err := pipe.Exec(ctx)
	return err
}
