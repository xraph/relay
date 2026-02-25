package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// deliveryModel is the JSON representation stored in Redis.
type deliveryModel struct {
	ID             string     `json:"id"`
	EventID        string     `json:"event_id"`
	EndpointID     string     `json:"endpoint_id"`
	State          string     `json:"state"`
	AttemptCount   int        `json:"attempt_count"`
	MaxAttempts    int        `json:"max_attempts"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	LastError      string     `json:"last_error"`
	LastStatusCode int        `json:"last_status_code"`
	LastResponse   string     `json:"last_response"`
	LastLatencyMs  int        `json:"last_latency_ms"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toDeliveryModel(d *delivery.Delivery) *deliveryModel {
	return &deliveryModel{
		ID:             d.ID.String(),
		EventID:        d.EventID.String(),
		EndpointID:     d.EndpointID.String(),
		State:          string(d.State),
		AttemptCount:   d.AttemptCount,
		MaxAttempts:    d.MaxAttempts,
		NextAttemptAt:  d.NextAttemptAt,
		LastError:      d.LastError,
		LastStatusCode: d.LastStatusCode,
		LastResponse:   d.LastResponse,
		LastLatencyMs:  d.LastLatencyMs,
		CompletedAt:    d.CompletedAt,
		CreatedAt:      d.CreatedAt,
		UpdatedAt:      d.UpdatedAt,
	}
}

func fromDeliveryModel(m *deliveryModel) (*delivery.Delivery, error) {
	delID, err := id.ParseDeliveryID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse delivery ID %q: %w", m.ID, err)
	}
	evtID, err := id.ParseEventID(m.EventID)
	if err != nil {
		return nil, fmt.Errorf("parse event ID %q: %w", m.EventID, err)
	}
	epID, err := id.ParseEndpointID(m.EndpointID)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint ID %q: %w", m.EndpointID, err)
	}
	return &delivery.Delivery{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             delID,
		EventID:        evtID,
		EndpointID:     epID,
		State:          delivery.State(m.State),
		AttemptCount:   m.AttemptCount,
		MaxAttempts:    m.MaxAttempts,
		NextAttemptAt:  m.NextAttemptAt,
		LastError:      m.LastError,
		LastStatusCode: m.LastStatusCode,
		LastResponse:   m.LastResponse,
		LastLatencyMs:  m.LastLatencyMs,
		CompletedAt:    m.CompletedAt,
	}, nil
}

// dequeueScript atomically claims pending deliveries from the sorted set.
// KEYS[1] = relay:z:del:pending
// ARGV[1] = current unix timestamp (score threshold)
// ARGV[2] = limit
var dequeueScript = goredis.NewScript(`
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
if #ids == 0 then return {} end
for i, id in ipairs(ids) do
    redis.call('ZREM', KEYS[1], id)
end
return ids
`)

func (s *Store) Enqueue(ctx context.Context, d *delivery.Delivery) error {
	m := toDeliveryModel(d)
	key := entityKey(prefixDelivery, m.ID)

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: enqueue delivery: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, zDeliveryPend, goredis.Z{Score: scoreFromTime(m.NextAttemptAt), Member: m.ID})
	pipe.ZAdd(ctx, zDeliveryEP+m.EndpointID, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	pipe.ZAdd(ctx, zDeliveryEvt+m.EventID, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/redis: enqueue delivery indexes: %w", err)
	}
	return nil
}

func (s *Store) EnqueueBatch(ctx context.Context, ds []*delivery.Delivery) error {
	if len(ds) == 0 {
		return nil
	}

	pipe := s.rdb.Pipeline()
	for _, d := range ds {
		m := toDeliveryModel(d)
		key := entityKey(prefixDelivery, m.ID)

		raw, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("relay/redis: enqueue batch marshal: %w", err)
		}
		pipe.Set(ctx, key, raw, 0)
		pipe.ZAdd(ctx, zDeliveryPend, goredis.Z{Score: scoreFromTime(m.NextAttemptAt), Member: m.ID})
		pipe.ZAdd(ctx, zDeliveryEP+m.EndpointID, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
		pipe.ZAdd(ctx, zDeliveryEvt+m.EventID, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/redis: enqueue batch: %w", err)
	}
	return nil
}

func (s *Store) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	// Atomically claim pending delivery IDs using Lua script.
	nowScore := fmt.Sprintf("%f", scoreFromTime(now()))
	result, err := dequeueScript.Run(ctx, s.rdb, []string{zDeliveryPend}, nowScore, limit).StringSlice()
	if err != nil {
		if isRedisNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("relay/redis: dequeue script: %w", err)
	}
	if len(result) == 0 {
		return nil, nil
	}

	// Fetch and update each claimed delivery.
	deliveries := make([]*delivery.Delivery, 0, len(result))
	for _, entryID := range result {
		key := entityKey(prefixDelivery, entryID)
		var m deliveryModel
		if err := s.getEntity(ctx, key, &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("relay/redis: dequeue get: %w", err)
		}

		m.State = string(delivery.StateDelivered) // temporarily mark as delivering
		m.UpdatedAt = now()
		if err := s.setEntity(ctx, key, &m); err != nil {
			return nil, fmt.Errorf("relay/redis: dequeue update: %w", err)
		}

		d, err := fromDeliveryModel(&m)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}

	return deliveries, nil
}

func (s *Store) UpdateDelivery(ctx context.Context, d *delivery.Delivery) error {
	m := toDeliveryModel(d)
	m.UpdatedAt = now()
	key := entityKey(prefixDelivery, m.ID)

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: update delivery: %w", err)
	}

	// If state is back to pending, re-add to the pending sorted set.
	if d.State == delivery.StatePending {
		s.rdb.ZAdd(ctx, zDeliveryPend, goredis.Z{Score: scoreFromTime(m.NextAttemptAt), Member: m.ID})
	}
	return nil
}

func (s *Store) GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error) {
	var m deliveryModel
	if err := s.getEntity(ctx, entityKey(prefixDelivery, delID.String()), &m); err != nil {
		if isNotFound(err) {
			return nil, relay.ErrDeliveryNotFound
		}
		return nil, fmt.Errorf("relay/redis: get delivery: %w", err)
	}
	return fromDeliveryModel(&m)
}

func (s *Store) ListByEndpoint(ctx context.Context, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	ids, err := s.rdb.ZRange(ctx, zDeliveryEP+epID.String(), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list by endpoint: %w", err)
	}

	result := make([]*delivery.Delivery, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- { // reverse for DESC order
		var m deliveryModel
		if err := s.getEntity(ctx, entityKey(prefixDelivery, ids[i]), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		if opts.State != nil && delivery.State(m.State) != *opts.State {
			continue
		}
		d, err := fromDeliveryModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}

	return applyPagination(result, opts.Offset, opts.Limit), nil
}

func (s *Store) ListByEvent(ctx context.Context, evtID id.ID) ([]*delivery.Delivery, error) {
	ids, err := s.rdb.ZRange(ctx, zDeliveryEvt+evtID.String(), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list by event: %w", err)
	}

	result := make([]*delivery.Delivery, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- { // reverse for DESC order
		var m deliveryModel
		if err := s.getEntity(ctx, entityKey(prefixDelivery, ids[i]), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		d, err := fromDeliveryModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}

	return result, nil
}

func (s *Store) CountPending(ctx context.Context) (int64, error) {
	count, err := s.rdb.ZCard(ctx, zDeliveryPend).Result()
	if err != nil {
		return 0, fmt.Errorf("relay/redis: count pending: %w", err)
	}
	return count, nil
}
