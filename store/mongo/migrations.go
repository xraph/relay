package mongo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/xraph/grove/drivers/mongodriver/mongomigrate"
	"github.com/xraph/grove/migrate"
)

// Migrations is the grove migration group for the Relay mongo store.
// It can be registered with the grove extension for orchestrated migration
// management (locking, version tracking, rollback support).
var Migrations = migrate.NewGroup("relay")

func init() {
	Migrations.MustRegister(
		&migrate.Migration{
			Name:    "create_relay_event_types",
			Version: "20240101000001",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}

				if err := mexec.CreateCollection(ctx, (*eventTypeModel)(nil)); err != nil {
					return err
				}

				return mexec.CreateIndexes(ctx, colEventTypes, []mongo.IndexModel{
					{
						Keys:    bson.D{{Key: "name", Value: 1}},
						Options: options.Index().SetUnique(true),
					},
					{Keys: bson.D{{Key: "group_name", Value: 1}}},
					{Keys: bson.D{{Key: "created_at", Value: -1}}},
				})
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				return mexec.DropCollection(ctx, (*eventTypeModel)(nil))
			},
		},
		&migrate.Migration{
			Name:    "create_relay_endpoints",
			Version: "20240101000002",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}

				if err := mexec.CreateCollection(ctx, (*endpointModel)(nil)); err != nil {
					return err
				}

				return mexec.CreateIndexes(ctx, colEndpoints, []mongo.IndexModel{
					{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "enabled", Value: 1}}},
					{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "created_at", Value: -1}}},
				})
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				return mexec.DropCollection(ctx, (*endpointModel)(nil))
			},
		},
		&migrate.Migration{
			Name:    "create_relay_events",
			Version: "20240101000003",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}

				if err := mexec.CreateCollection(ctx, (*eventModel)(nil)); err != nil {
					return err
				}

				return mexec.CreateIndexes(ctx, colEvents, []mongo.IndexModel{
					{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "type", Value: 1}, {Key: "created_at", Value: -1}}},
					idempotencyIndex(),
				})
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				return mexec.DropCollection(ctx, (*eventModel)(nil))
			},
		},
		&migrate.Migration{
			Name:    "create_relay_deliveries",
			Version: "20240101000004",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}

				if err := mexec.CreateCollection(ctx, (*deliveryModel)(nil)); err != nil {
					return err
				}

				return mexec.CreateIndexes(ctx, colDeliveries, []mongo.IndexModel{
					{Keys: bson.D{{Key: "state", Value: 1}, {Key: "next_attempt_at", Value: 1}}},
					{Keys: bson.D{{Key: "endpoint_id", Value: 1}, {Key: "created_at", Value: -1}}},
					{Keys: bson.D{{Key: "event_id", Value: 1}}},
				})
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				return mexec.DropCollection(ctx, (*deliveryModel)(nil))
			},
		},
		&migrate.Migration{
			Name:    "create_relay_dlq",
			Version: "20240101000005",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}

				if err := mexec.CreateCollection(ctx, (*dlqEntryModel)(nil)); err != nil {
					return err
				}

				return mexec.CreateIndexes(ctx, colDLQ, []mongo.IndexModel{
					{Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "failed_at", Value: -1}}},
					{Keys: bson.D{{Key: "endpoint_id", Value: 1}}},
				})
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				return mexec.DropCollection(ctx, (*dlqEntryModel)(nil))
			},
		},
		// The events migration made idempotency_key unique and sparse, but an
		// event with no key is stored with "", which a sparse index still
		// indexes. The second un-keyed event was refused as a duplicate, and
		// relay.Send reads a duplicate as "already processed" and returns
		// success, so every un-keyed event after the first was dropped. A
		// partial index covers only the keys somebody actually set.
		&migrate.Migration{
			Name:    "relay_events_idempotency_partial",
			Version: "20260923000001",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				idx := mexec.DB().Collection(colEvents).Indexes()
				if err := idx.DropOne(ctx, legacyIdempotencyIndex); err != nil && !isIndexNotFound(err) {
					return fmt.Errorf("drop sparse idempotency index: %w", err)
				}
				return mexec.CreateIndexes(ctx, colEvents, []mongo.IndexModel{idempotencyIndex()})
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				mexec, ok := exec.(*mongomigrate.Executor)
				if !ok {
					return fmt.Errorf("expected mongomigrate executor, got %T", exec)
				}
				return mexec.DB().Collection(colEvents).Indexes().DropOne(ctx, idempotencyIndexName)
			},
		},
	)
}

// isIndexNotFound reports that there was no old index to drop: IndexNotFound
// (27) when the collection exists without it, NamespaceNotFound (26) when the
// collection does not exist yet.
func isIndexNotFound(err error) bool {
	var ce mongo.CommandError
	return errors.As(err, &ce) && (ce.Code == 27 || ce.Code == 26)
}

// legacyIdempotencyIndex is the name mongo gave the old sparse index.
const legacyIdempotencyIndex = "idempotency_key_1"

// idempotencyIndexName names the partial index that replaced it.
const idempotencyIndexName = "idempotency_key_set"

// idempotencyIndex is unique over the keys somebody set and ignores the rest.
// An un-keyed event is stored with "", so sparse was never enough.
func idempotencyIndex() mongo.IndexModel {
	return mongo.IndexModel{
		Keys: bson.D{{Key: "idempotency_key", Value: 1}},
		Options: options.Index().
			SetName(idempotencyIndexName).
			SetUnique(true).
			SetPartialFilterExpression(bson.D{{Key: "idempotency_key",
				Value: bson.D{{Key: "$type", Value: "string"}, {Key: "$gt", Value: ""}}}}),
	}
}
