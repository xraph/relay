package mongo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/id"
)

// RegisterType creates or updates an event type definition.
func (s *Store) RegisterType(ctx context.Context, et *catalog.EventType) error {
	m := toEventTypeModel(et)

	_, err := s.mdb.NewUpdate(m).
		Filter(bson.M{"name": m.Name}).
		SetUpdate(bson.M{"$setOnInsert": bson.M{
			"_id":            m.ID,
			"name":           m.Name,
			"description":    m.Description,
			"group_name":     m.GroupName,
			"schema":         m.Schema,
			"schema_version": m.SchemaVersion,
			"version":        m.Version,
			"example":        m.Example,
			"is_deprecated":  m.IsDeprecated,
			"deprecated_at":  m.DeprecatedAt,
			"scope_app_id":   m.ScopeAppID,
			"metadata":       m.Metadata,
			"created_at":     m.CreatedAt,
			"updated_at":     m.UpdatedAt,
		}}).
		Upsert().
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: register type: %w", err)
	}

	return nil
}

// GetType returns an event type by name.
func (s *Store) GetType(ctx context.Context, name string) (*catalog.EventType, error) {
	var m eventTypeModel

	err := s.mdb.NewFind(&m).
		Filter(bson.M{"name": name}).
		Scan(ctx)
	if err != nil {
		if isNoDocuments(err) {
			return nil, relay.ErrEventTypeNotFound
		}

		return nil, fmt.Errorf("relay/mongo: get type: %w", err)
	}

	return fromEventTypeModel(&m)
}

// GetTypeByID returns an event type by its TypeID.
func (s *Store) GetTypeByID(ctx context.Context, etID id.ID) (*catalog.EventType, error) {
	var m eventTypeModel

	err := s.mdb.NewFind(&m).
		Filter(bson.M{"_id": etID.String()}).
		Scan(ctx)
	if err != nil {
		if isNoDocuments(err) {
			return nil, relay.ErrEventTypeNotFound
		}

		return nil, fmt.Errorf("relay/mongo: get type by id: %w", err)
	}

	return fromEventTypeModel(&m)
}

// ListTypes returns all registered event types, optionally filtered.
func (s *Store) ListTypes(ctx context.Context, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	var models []eventTypeModel

	q := s.mdb.NewFind(&models)

	filter := bson.M{}
	if !opts.IncludeDeprecated {
		filter["is_deprecated"] = false
	}

	if opts.Group != "" {
		filter["group_name"] = opts.Group
	}

	q = q.Filter(filter).
		Sort(bson.D{{Key: "created_at", Value: -1}})

	if opts.Limit > 0 {
		q = q.Limit(int64(opts.Limit))
	}

	if opts.Offset > 0 {
		q = q.Skip(int64(opts.Offset))
	}

	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list types: %w", err)
	}

	result := make([]*catalog.EventType, 0, len(models))

	for i := range models {
		et, err := fromEventTypeModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, et)
	}

	return result, nil
}

// DeleteType soft-deletes (deprecates) an event type.
func (s *Store) DeleteType(ctx context.Context, name string) error {
	t := now()

	res, err := s.mdb.NewUpdate((*eventTypeModel)(nil)).
		Filter(bson.M{"name": name}).
		Set("is_deprecated", true).
		Set("deprecated_at", t).
		Set("updated_at", t).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: delete type: %w", err)
	}

	if res.MatchedCount() == 0 {
		return relay.ErrEventTypeNotFound
	}

	return nil
}

// MatchTypes returns non-deprecated event types matching a glob pattern.
func (s *Store) MatchTypes(ctx context.Context, pattern string) ([]*catalog.EventType, error) {
	var models []eventTypeModel

	if err := s.mdb.NewFind(&models).
		Filter(bson.M{"is_deprecated": false}).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: match types: %w", err)
	}

	var result []*catalog.EventType

	for i := range models {
		if catalog.Match(pattern, models[i].Name) {
			et, err := fromEventTypeModel(&models[i])
			if err != nil {
				return nil, err
			}

			result = append(result, et)
		}
	}

	return result, nil
}

// isNoDocuments checks if an error wraps mongo.ErrNoDocuments.
func isNoDocuments(err error) bool {
	return errors.Is(err, mongo.ErrNoDocuments)
}
