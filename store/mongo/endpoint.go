package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
)

// CreateEndpoint persists a new endpoint.
func (s *Store) CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	m := toEndpointModel(ep)

	_, err := s.mdb.NewInsert(m).Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: create endpoint: %w", err)
	}

	return nil
}

// GetEndpoint returns an endpoint by ID.
func (s *Store) GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error) {
	var m endpointModel

	err := s.mdb.NewFind(&m).
		Filter(bson.M{"_id": epID.String()}).
		Scan(ctx)
	if err != nil {
		if isNoDocuments(err) {
			return nil, relay.ErrEndpointNotFound
		}

		return nil, fmt.Errorf("relay/mongo: get endpoint: %w", err)
	}

	return fromEndpointModel(&m)
}

// UpdateEndpoint modifies an existing endpoint.
func (s *Store) UpdateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	m := toEndpointModel(ep)
	m.UpdatedAt = now()

	res, err := s.mdb.NewUpdate(m).
		Filter(bson.M{"_id": m.ID}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: update endpoint: %w", err)
	}

	if res.MatchedCount() == 0 {
		return relay.ErrEndpointNotFound
	}

	return nil
}

// DeleteEndpoint removes an endpoint.
func (s *Store) DeleteEndpoint(ctx context.Context, epID id.ID) error {
	res, err := s.mdb.NewDelete((*endpointModel)(nil)).
		Filter(bson.M{"_id": epID.String()}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: delete endpoint: %w", err)
	}

	if res.DeletedCount() == 0 {
		return relay.ErrEndpointNotFound
	}

	return nil
}

// ListEndpoints returns endpoints for a tenant, optionally filtered.
func (s *Store) ListEndpoints(ctx context.Context, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	var models []endpointModel

	filter := bson.M{"tenant_id": tenantID}
	if opts.Enabled != nil {
		filter["enabled"] = *opts.Enabled
	}

	q := s.mdb.NewFind(&models).
		Filter(filter).
		Sort(bson.D{{Key: "created_at", Value: -1}})

	if opts.Limit > 0 {
		q = q.Limit(int64(opts.Limit))
	}

	if opts.Offset > 0 {
		q = q.Skip(int64(opts.Offset))
	}

	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list endpoints: %w", err)
	}

	result := make([]*endpoint.Endpoint, 0, len(models))

	for i := range models {
		ep, err := fromEndpointModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, ep)
	}

	return result, nil
}

// Resolve finds all active endpoints matching an event type for a tenant.
func (s *Store) Resolve(ctx context.Context, tenantID, eventType string) ([]*endpoint.Endpoint, error) {
	var models []endpointModel

	if err := s.mdb.NewFind(&models).
		Filter(bson.M{
			"tenant_id": tenantID,
			"enabled":   true,
		}).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: resolve: %w", err)
	}

	var result []*endpoint.Endpoint

	for i := range models {
		for _, pattern := range models[i].EventTypes {
			if catalog.Match(pattern, eventType) {
				ep, err := fromEndpointModel(&models[i])
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

// SetEnabled enables or disables an endpoint.
func (s *Store) SetEnabled(ctx context.Context, epID id.ID, enabled bool) error {
	res, err := s.mdb.NewUpdate((*endpointModel)(nil)).
		Filter(bson.M{"_id": epID.String()}).
		Set("enabled", enabled).
		Set("updated_at", now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: set enabled: %w", err)
	}

	if res.MatchedCount() == 0 {
		return relay.ErrEndpointNotFound
	}

	return nil
}
