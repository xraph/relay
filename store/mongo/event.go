package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongod "go.mongodb.org/mongo-driver/v2/mongo"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
)

// CreateEvent persists an event.
func (s *Store) CreateEvent(ctx context.Context, evt *event.Event) error {
	m := toEventModel(evt)

	_, err := s.mdb.NewInsert(m).Exec(ctx)
	if err != nil {
		if mongod.IsDuplicateKeyError(err) {
			return relay.ErrDuplicateIdempotencyKey
		}

		return fmt.Errorf("relay/mongo: create event: %w", err)
	}

	return nil
}

// GetEvent returns an event by ID.
func (s *Store) GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error) {
	var m eventModel

	err := s.mdb.NewFind(&m).
		Filter(bson.M{"_id": evtID.String()}).
		Scan(ctx)
	if err != nil {
		if isNoDocuments(err) {
			return nil, relay.ErrEventNotFound
		}

		return nil, fmt.Errorf("relay/mongo: get event: %w", err)
	}

	return fromEventModel(&m)
}

// ListEvents returns events, optionally filtered by type, tenant, or time range.
func (s *Store) ListEvents(ctx context.Context, opts event.ListOpts) ([]*event.Event, error) {
	var models []eventModel

	filter := bson.M{}
	if opts.Type != "" {
		filter["type"] = opts.Type
	}

	if opts.From != nil || opts.To != nil {
		dateFilter := bson.M{}
		if opts.From != nil {
			dateFilter["$gte"] = *opts.From
		}

		if opts.To != nil {
			dateFilter["$lte"] = *opts.To
		}

		filter["created_at"] = dateFilter
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
		return nil, fmt.Errorf("relay/mongo: list events: %w", err)
	}

	result := make([]*event.Event, 0, len(models))

	for i := range models {
		evt, err := fromEventModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, evt)
	}

	return result, nil
}

// ListEventsByTenant returns events for a specific tenant.
func (s *Store) ListEventsByTenant(ctx context.Context, tenantID string, opts event.ListOpts) ([]*event.Event, error) {
	var models []eventModel

	filter := bson.M{"tenant_id": tenantID}
	if opts.Type != "" {
		filter["type"] = opts.Type
	}

	if opts.From != nil || opts.To != nil {
		dateFilter := bson.M{}
		if opts.From != nil {
			dateFilter["$gte"] = *opts.From
		}

		if opts.To != nil {
			dateFilter["$lte"] = *opts.To
		}

		filter["created_at"] = dateFilter
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
		return nil, fmt.Errorf("relay/mongo: list events by tenant: %w", err)
	}

	result := make([]*event.Event, 0, len(models))

	for i := range models {
		evt, err := fromEventModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, evt)
	}

	return result, nil
}
