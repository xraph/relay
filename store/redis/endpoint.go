package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// endpointModel is the JSON representation stored in Redis.
type endpointModel struct {
	ID          string            `json:"id"`
	TenantID    string            `json:"tenant_id"`
	URL         string            `json:"url"`
	Description string            `json:"description"`
	Secret      string            `json:"secret"`
	EventTypes  []string          `json:"event_types"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     bool              `json:"enabled"`
	RateLimit   int               `json:"rate_limit"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

func toEndpointModel(ep *endpoint.Endpoint) *endpointModel {
	return &endpointModel{
		ID:          ep.ID.String(),
		TenantID:    ep.TenantID,
		URL:         ep.URL,
		Description: ep.Description,
		Secret:      ep.Secret,
		EventTypes:  ep.EventTypes,
		Headers:     ep.Headers,
		Enabled:     ep.Enabled,
		RateLimit:   ep.RateLimit,
		Metadata:    ep.Metadata,
		CreatedAt:   ep.CreatedAt,
		UpdatedAt:   ep.UpdatedAt,
	}
}

func fromEndpointModel(m *endpointModel) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint ID %q: %w", m.ID, err)
	}
	return &endpoint.Endpoint{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:          epID,
		TenantID:    m.TenantID,
		URL:         m.URL,
		Description: m.Description,
		Secret:      m.Secret,
		EventTypes:  m.EventTypes,
		Headers:     m.Headers,
		Enabled:     m.Enabled,
		RateLimit:   m.RateLimit,
		Metadata:    m.Metadata,
	}, nil
}

func (s *Store) CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	m := toEndpointModel(ep)
	key := entityKey(prefixEndpoint, m.ID)

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: create endpoint: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, zEndpointTenant+m.TenantID, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	if m.Enabled {
		pipe.SAdd(ctx, enabledSetKey(m.TenantID), m.ID)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/redis: create endpoint indexes: %w", err)
	}
	return nil
}

func (s *Store) GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error) {
	var m endpointModel
	if err := s.getEntity(ctx, entityKey(prefixEndpoint, epID.String()), &m); err != nil {
		if isNotFound(err) {
			return nil, relay.ErrEndpointNotFound
		}
		return nil, fmt.Errorf("relay/redis: get endpoint: %w", err)
	}
	return fromEndpointModel(&m)
}

func (s *Store) UpdateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	key := entityKey(prefixEndpoint, ep.ID.String())

	// Verify existence.
	var existing endpointModel
	if err := s.getEntity(ctx, key, &existing); err != nil {
		if isNotFound(err) {
			return relay.ErrEndpointNotFound
		}
		return fmt.Errorf("relay/redis: update endpoint get: %w", err)
	}

	m := toEndpointModel(ep)
	m.UpdatedAt = now()

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: update endpoint: %w", err)
	}

	// Update enabled set.
	if m.Enabled {
		s.rdb.SAdd(ctx, enabledSetKey(m.TenantID), m.ID)
	} else {
		s.rdb.SRem(ctx, enabledSetKey(m.TenantID), m.ID)
	}
	return nil
}

func (s *Store) DeleteEndpoint(ctx context.Context, epID id.ID) error {
	key := entityKey(prefixEndpoint, epID.String())

	var m endpointModel
	if err := s.getEntity(ctx, key, &m); err != nil {
		if isNotFound(err) {
			return relay.ErrEndpointNotFound
		}
		return fmt.Errorf("relay/redis: delete endpoint get: %w", err)
	}

	if err := s.kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("relay/redis: delete endpoint: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.ZRem(ctx, zEndpointTenant+m.TenantID, m.ID)
	pipe.SRem(ctx, enabledSetKey(m.TenantID), m.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("relay/redis: delete endpoint indexes: %w", err)
	}
	return nil
}

func (s *Store) ListEndpoints(ctx context.Context, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	ids, err := s.rdb.ZRange(ctx, zEndpointTenant+tenantID, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list endpoints: %w", err)
	}

	result := make([]*endpoint.Endpoint, 0, len(ids))
	for _, entryID := range ids {
		var m endpointModel
		if err := s.getEntity(ctx, entityKey(prefixEndpoint, entryID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		if opts.Enabled != nil && m.Enabled != *opts.Enabled {
			continue
		}
		ep, err := fromEndpointModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, ep)
	}

	return applyPagination(result, opts.Offset, opts.Limit), nil
}

func (s *Store) Resolve(ctx context.Context, tenantID, eventType string) ([]*endpoint.Endpoint, error) {
	ids, err := s.rdb.SMembers(ctx, enabledSetKey(tenantID)).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: resolve: %w", err)
	}

	var result []*endpoint.Endpoint
	for _, entryID := range ids {
		var m endpointModel
		if err := s.getEntity(ctx, entityKey(prefixEndpoint, entryID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		for _, pattern := range m.EventTypes {
			if catalog.Match(pattern, eventType) {
				ep, err := fromEndpointModel(&m)
				if err != nil {
					return nil, err
				}
				result = append(result, ep)
				break
			}
		}
	}
	return result, nil
}

func (s *Store) SetEnabled(ctx context.Context, epID id.ID, enabled bool) error {
	key := entityKey(prefixEndpoint, epID.String())

	var m endpointModel
	if err := s.getEntity(ctx, key, &m); err != nil {
		if isNotFound(err) {
			return relay.ErrEndpointNotFound
		}
		return fmt.Errorf("relay/redis: set enabled get: %w", err)
	}

	m.Enabled = enabled
	m.UpdatedAt = now()

	if err := s.setEntity(ctx, key, &m); err != nil {
		return fmt.Errorf("relay/redis: set enabled: %w", err)
	}

	if enabled {
		s.rdb.SAdd(ctx, enabledSetKey(m.TenantID), m.ID)
	} else {
		s.rdb.SRem(ctx, enabledSetKey(m.TenantID), m.ID)
	}
	return nil
}
