package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/mongodriver"

	"github.com/xraph/relay/store"
)

// Collection name constants.
const (
	colEventTypes = "relay_event_types"
	colEndpoints  = "relay_endpoints"
	colEvents     = "relay_events"
	colDeliveries = "relay_deliveries"
	colDLQ        = "relay_dlq"
)

// Compile-time interface check.
var _ store.Store = (*Store)(nil)

// Store implements store.Store using MongoDB via Grove ORM.
type Store struct {
	db  *grove.DB
	mdb *mongodriver.MongoDB
}

// New creates a new MongoDB store backed by Grove ORM.
func New(db *grove.DB) *Store {
	return &Store{
		db:  db,
		mdb: mongodriver.Unwrap(db),
	}
}

// DB returns the underlying grove database for direct access.
func (s *Store) DB() *grove.DB { return s.db }

// Migrate creates indexes for all relay collections.
func (s *Store) Migrate(ctx context.Context) error {
	indexes := migrationIndexes()

	for col, models := range indexes {
		if len(models) == 0 {
			continue
		}

		_, err := s.mdb.Collection(col).Indexes().CreateMany(ctx, models)
		if err != nil {
			return fmt.Errorf("relay/mongo: migrate %s indexes: %w", col, err)
		}
	}

	return nil
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// now returns the current UTC time.
func now() time.Time {
	return time.Now().UTC()
}

// migrationIndexes returns the index definitions for all relay collections.
func migrationIndexes() map[string][]mongo.IndexModel {
	return map[string][]mongo.IndexModel{
		colEventTypes: {
			{
				Keys:    bson.D{{Key: "name", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
			{Keys: bson.D{{Key: "group_name", Value: 1}}},
			{Keys: bson.D{{Key: "created_at", Value: -1}}},
		},
		colEndpoints: {
			{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "enabled", Value: 1}}},
			{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "created_at", Value: -1}}},
		},
		colEvents: {
			{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "type", Value: 1}, {Key: "created_at", Value: -1}}},
			{
				Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
				Options: options.Index().SetUnique(true).SetSparse(true),
			},
		},
		colDeliveries: {
			{Keys: bson.D{{Key: "state", Value: 1}, {Key: "next_attempt_at", Value: 1}}},
			{Keys: bson.D{{Key: "endpoint_id", Value: 1}, {Key: "created_at", Value: -1}}},
			{Keys: bson.D{{Key: "event_id", Value: 1}}},
		},
		colDLQ: {
			{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "failed_at", Value: -1}}},
			{Keys: bson.D{{Key: "endpoint_id", Value: 1}}},
		},
	}
}
