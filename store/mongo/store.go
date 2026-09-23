package mongo

import (
	"context"
	"errors"
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
	// Earlier versions created a sparse unique index on idempotency_key,
	// which refused every un-keyed event after the first. It has to go before
	// the partial one can take its place on the same key.
	if err := s.mdb.Collection(colEvents).Indexes().DropOne(ctx, legacyIdempotencyIndex); err != nil && !isIndexNotFound(err) {
		return fmt.Errorf("relay/mongo: drop legacy idempotency index: %w", err)
	}

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

	return backfillDeliveryEventFields(ctx, s.mdb.Database())
}

// backfillDeliveryEventFields copies event_type and tenant_id from each
// delivery's event onto deliveries written before those fields existed, so a
// tenant filter on the delivery log does not hide them. It runs on every
// Migrate and touches only rows still missing the event type, found through
// the event_type index. A delivery whose event is gone keeps the empty value.
func backfillDeliveryEventFields(ctx context.Context, db *mongo.Database) error {
	dels := db.Collection(colDeliveries)
	cur, err := dels.Find(ctx, bson.M{"event_type": bson.M{"$in": bson.A{nil, ""}}},
		options.Find().SetProjection(bson.M{"_id": 1, "event_id": 1}))
	if err != nil {
		return fmt.Errorf("relay/mongo: backfill deliveries: %w", err)
	}
	defer cur.Close(ctx)

	evts := db.Collection(colEvents)
	for cur.Next(ctx) {
		var row struct {
			ID      string `bson:"_id"`
			EventID string `bson:"event_id"`
		}
		if err := cur.Decode(&row); err != nil {
			return fmt.Errorf("relay/mongo: backfill decode: %w", err)
		}
		var evt struct {
			Type     string `bson:"type"`
			TenantID string `bson:"tenant_id"`
		}
		if err := evts.FindOne(ctx, bson.M{"_id": row.EventID}).Decode(&evt); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				continue
			}
			return fmt.Errorf("relay/mongo: backfill event %s: %w", row.EventID, err)
		}
		if _, err := dels.UpdateByID(ctx, row.ID, bson.M{"$set": bson.M{
			"event_type": evt.Type, "tenant_id": evt.TenantID,
		}}); err != nil {
			return fmt.Errorf("relay/mongo: backfill delivery %s: %w", row.ID, err)
		}
	}
	return cur.Err()
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
			idempotencyIndex(),
		},
		colDeliveries: {
			{Keys: bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}},
			{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "created_at", Value: -1}}},
			{Keys: bson.D{{Key: "event_type", Value: 1}, {Key: "created_at", Value: -1}}},
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
