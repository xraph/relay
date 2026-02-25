package redis

import (
	"context"
	"fmt"
	"math"
	"time"

	goredis "github.com/redis/go-redis/v9"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// eventModel is the JSON representation stored in Redis.
type eventModel struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	TenantID       string    `json:"tenant_id"`
	Data           any       `json:"data,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	ScopeAppID     string    `json:"scope_app_id"`
	ScopeOrgID     string    `json:"scope_org_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toEventModel(evt *event.Event) *eventModel {
	return &eventModel{
		ID:             evt.ID.String(),
		Type:           evt.Type,
		TenantID:       evt.TenantID,
		Data:           evt.Data,
		IdempotencyKey: evt.IdempotencyKey,
		ScopeAppID:     evt.ScopeAppID,
		ScopeOrgID:     evt.ScopeOrgID,
		CreatedAt:      evt.CreatedAt,
		UpdatedAt:      evt.UpdatedAt,
	}
}

func fromEventModel(m *eventModel) (*event.Event, error) {
	evtID, err := id.ParseEventID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse event ID %q: %w", m.ID, err)
	}
	return &event.Event{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             evtID,
		Type:           m.Type,
		TenantID:       m.TenantID,
		Data:           m.Data,
		IdempotencyKey: m.IdempotencyKey,
		ScopeAppID:     m.ScopeAppID,
		ScopeOrgID:     m.ScopeOrgID,
	}, nil
}

func (s *Store) CreateEvent(ctx context.Context, evt *event.Event) error {
	m := toEventModel(evt)
	key := entityKey(prefixEvent, m.ID)

	// Idempotency check via SET NX.
	if m.IdempotencyKey != "" {
		ok, err := s.rdb.SetNX(ctx, uniqueEventIdem+m.IdempotencyKey, m.ID, 0).Result()
		if err != nil {
			return fmt.Errorf("relay/redis: create event idem check: %w", err)
		}
		if !ok {
			return relay.ErrDuplicateIdempotencyKey
		}
	}

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: create event: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, zEventAll, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	if m.TenantID != "" {
		pipe.ZAdd(ctx, zEventTenant+m.TenantID, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/redis: create event indexes: %w", err)
	}
	return nil
}

func (s *Store) GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error) {
	var m eventModel
	if err := s.getEntity(ctx, entityKey(prefixEvent, evtID.String()), &m); err != nil {
		if isNotFound(err) {
			return nil, relay.ErrEventNotFound
		}
		return nil, fmt.Errorf("relay/redis: get event: %w", err)
	}
	return fromEventModel(&m)
}

func (s *Store) ListEvents(ctx context.Context, opts event.ListOpts) ([]*event.Event, error) {
	minScore := math.Inf(-1)
	maxScore := math.Inf(1)
	if opts.From != nil {
		minScore = scoreFromTime(*opts.From)
	}
	if opts.To != nil {
		maxScore = scoreFromTime(*opts.To)
	}

	ids, err := s.zRangeByScoreIDs(ctx, zEventAll, minScore, maxScore)
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list events: %w", err)
	}

	result := make([]*event.Event, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- { // reverse for DESC order
		var m eventModel
		if err := s.getEntity(ctx, entityKey(prefixEvent, ids[i]), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		if opts.Type != "" && m.Type != opts.Type {
			continue
		}
		evt, err := fromEventModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, evt)
	}

	return applyPagination(result, opts.Offset, opts.Limit), nil
}

func (s *Store) ListEventsByTenant(ctx context.Context, tenantID string, opts event.ListOpts) ([]*event.Event, error) {
	ids, err := s.rdb.ZRange(ctx, zEventTenant+tenantID, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list events by tenant: %w", err)
	}

	result := make([]*event.Event, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- { // reverse for DESC order
		var m eventModel
		if err := s.getEntity(ctx, entityKey(prefixEvent, ids[i]), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		if opts.Type != "" && m.Type != opts.Type {
			continue
		}
		evt, err := fromEventModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, evt)
	}

	return applyPagination(result, opts.Offset, opts.Limit), nil
}
