package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// catalogModel is the JSON representation stored in Redis.
type catalogModel struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	GroupName     string            `json:"group_name"`
	Schema        []byte            `json:"schema,omitempty"`
	SchemaVersion string            `json:"schema_version"`
	Version       string            `json:"version"`
	Example       []byte            `json:"example,omitempty"`
	IsDeprecated  bool              `json:"is_deprecated"`
	DeprecatedAt  *time.Time        `json:"deprecated_at,omitempty"`
	ScopeAppID    string            `json:"scope_app_id"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func toCatalogModel(et *catalog.EventType) *catalogModel {
	return &catalogModel{
		ID:            et.ID.String(),
		Name:          et.Definition.Name,
		Description:   et.Definition.Description,
		GroupName:     et.Definition.Group,
		Schema:        et.Definition.Schema,
		SchemaVersion: et.Definition.SchemaVersion,
		Version:       et.Definition.Version,
		Example:       et.Definition.Example,
		IsDeprecated:  et.IsDeprecated,
		DeprecatedAt:  et.DeprecatedAt,
		ScopeAppID:    et.ScopeAppID,
		Metadata:      et.Metadata,
		CreatedAt:     et.CreatedAt,
		UpdatedAt:     et.UpdatedAt,
	}
}

func fromCatalogModel(m *catalogModel) (*catalog.EventType, error) {
	etID, err := id.ParseEventTypeID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse event type ID %q: %w", m.ID, err)
	}
	return &catalog.EventType{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID: etID,
		Definition: catalog.WebhookDefinition{
			Name:          m.Name,
			Description:   m.Description,
			Group:         m.GroupName,
			Schema:        m.Schema,
			SchemaVersion: m.SchemaVersion,
			Version:       m.Version,
			Example:       m.Example,
		},
		IsDeprecated: m.IsDeprecated,
		DeprecatedAt: m.DeprecatedAt,
		ScopeAppID:   m.ScopeAppID,
		Metadata:     m.Metadata,
	}, nil
}

func (s *Store) RegisterType(ctx context.Context, et *catalog.EventType) error {
	m := toCatalogModel(et)
	key := entityKey(prefixEventType, m.ID)

	// Check if a type with this name already exists (upsert).
	existingID, lookupErr := s.rdb.Get(ctx, uniqueEventTypeName+m.Name).Result()
	if lookupErr == nil && existingID != "" && existingID != m.ID {
		// Name already registered with a different ID — update existing.
		var existing catalogModel
		if getErr := s.getEntity(ctx, entityKey(prefixEventType, existingID), &existing); getErr == nil {
			existing.Description = m.Description
			existing.GroupName = m.GroupName
			existing.Schema = m.Schema
			existing.SchemaVersion = m.SchemaVersion
			existing.Version = m.Version
			existing.Example = m.Example
			existing.ScopeAppID = m.ScopeAppID
			existing.Metadata = m.Metadata
			existing.IsDeprecated = false
			existing.DeprecatedAt = nil
			existing.UpdatedAt = now()
			return s.setEntity(ctx, entityKey(prefixEventType, existingID), &existing)
		}
	}

	if err := s.setEntity(ctx, key, m); err != nil {
		return fmt.Errorf("relay/redis: register type: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, uniqueEventTypeName+m.Name, m.ID, 0)
	pipe.ZAdd(ctx, zEventTypeAll, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	pipe.SAdd(ctx, sEventTypeActive, m.ID)
	if m.GroupName != "" {
		pipe.ZAdd(ctx, zEventTypeGroup+m.GroupName, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("relay/redis: register type indexes: %w", err)
	}
	return nil
}

func (s *Store) GetType(ctx context.Context, name string) (*catalog.EventType, error) {
	entryID, err := s.rdb.Get(ctx, uniqueEventTypeName+name).Result()
	if err != nil {
		if isRedisNil(err) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, fmt.Errorf("relay/redis: get type lookup: %w", err)
	}

	var m catalogModel
	if err := s.getEntity(ctx, entityKey(prefixEventType, entryID), &m); err != nil {
		if isNotFound(err) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, fmt.Errorf("relay/redis: get type: %w", err)
	}
	return fromCatalogModel(&m)
}

func (s *Store) GetTypeByID(ctx context.Context, etID id.ID) (*catalog.EventType, error) {
	var m catalogModel
	if err := s.getEntity(ctx, entityKey(prefixEventType, etID.String()), &m); err != nil {
		if isNotFound(err) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, fmt.Errorf("relay/redis: get type by id: %w", err)
	}
	return fromCatalogModel(&m)
}

func (s *Store) ListTypes(ctx context.Context, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	zKey := zEventTypeAll
	if opts.Group != "" {
		zKey = zEventTypeGroup + opts.Group
	}

	ids, err := s.rdb.ZRange(ctx, zKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list types: %w", err)
	}

	result := make([]*catalog.EventType, 0, len(ids))
	for _, entryID := range ids {
		var m catalogModel
		if err := s.getEntity(ctx, entityKey(prefixEventType, entryID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		if !opts.IncludeDeprecated && m.IsDeprecated {
			continue
		}
		et, err := fromCatalogModel(&m)
		if err != nil {
			return nil, err
		}
		result = append(result, et)
	}

	return applyPagination(result, opts.Offset, opts.Limit), nil
}

func (s *Store) DeleteType(ctx context.Context, name string) error {
	entryID, err := s.rdb.Get(ctx, uniqueEventTypeName+name).Result()
	if err != nil {
		if isRedisNil(err) {
			return relay.ErrEventTypeNotFound
		}
		return fmt.Errorf("relay/redis: delete type lookup: %w", err)
	}

	key := entityKey(prefixEventType, entryID)
	var m catalogModel
	if err := s.getEntity(ctx, key, &m); err != nil {
		if isNotFound(err) {
			return relay.ErrEventTypeNotFound
		}
		return fmt.Errorf("relay/redis: delete type get: %w", err)
	}

	t := now()
	m.IsDeprecated = true
	m.DeprecatedAt = &t
	m.UpdatedAt = t

	if err := s.setEntity(ctx, key, &m); err != nil {
		return fmt.Errorf("relay/redis: delete type update: %w", err)
	}
	s.rdb.SRem(ctx, sEventTypeActive, entryID)
	return nil
}

func (s *Store) MatchTypes(ctx context.Context, pattern string) ([]*catalog.EventType, error) {
	ids, err := s.rdb.SMembers(ctx, sEventTypeActive).Result()
	if err != nil {
		return nil, fmt.Errorf("relay/redis: match types: %w", err)
	}

	var result []*catalog.EventType
	for _, entryID := range ids {
		var m catalogModel
		if err := s.getEntity(ctx, entityKey(prefixEventType, entryID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		et, err := fromCatalogModel(&m)
		if err != nil {
			return nil, err
		}
		if catalog.Match(pattern, et.Definition.Name) {
			result = append(result, et)
		}
	}
	return result, nil
}
