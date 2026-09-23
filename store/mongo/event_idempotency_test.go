package mongo_test

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongod "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/xraph/relay"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// An event with no idempotency key must never collide with another. The
// unique index was sparse, but the empty key is stored as "", which a sparse
// index still indexes, so the second un-keyed event came back as a duplicate
// and relay.Send reported it delivered while dropping it.
func TestEventsWithoutIdempotencyKeyDoNotCollide(t *testing.T) {
	s := openStore(t, startMongo(t))
	ctx := context.Background()
	newEvent := func(key string) *event.Event {
		return &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.created",
			TenantID: "t", Data: map[string]any{"n": 1}, IdempotencyKey: key}
	}
	for i := range 3 {
		if err := s.CreateEvent(ctx, newEvent("")); err != nil {
			t.Fatalf("un-keyed event %d: %v", i, err)
		}
	}
	if err := s.CreateEvent(ctx, newEvent("k-1")); err != nil {
		t.Fatalf("keyed event: %v", err)
	}
	if err := s.CreateEvent(ctx, newEvent("k-1")); !errors.Is(err, relay.ErrDuplicateIdempotencyKey) {
		t.Fatalf("second event with the same key: %v, want ErrDuplicateIdempotencyKey", err)
	}
}

// A database migrated by an earlier version still carries the sparse index.
// Migrate has to take it away, or the fix only helps fresh installs.
func TestMigrateReplacesTheLegacySparseIdempotencyIndex(t *testing.T) {
	uri := startMongo(t)
	ctx := context.Background()
	raw := rawDatabase(t, uri)
	_ = raw.Collection("relay_events").Drop(ctx)
	if _, err := raw.Collection("relay_events").Indexes().CreateOne(ctx, mongod.IndexModel{
		Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
		Options: options.Index().SetUnique(true).SetSparse(true),
	}); err != nil {
		t.Fatalf("plant legacy index: %v", err)
	}

	s := openStore(t, uri) // runs Migrate
	for i := range 2 {
		evt := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.created",
			TenantID: "t", Data: map[string]any{"n": i}}
		if err := s.CreateEvent(ctx, evt); err != nil {
			t.Fatalf("un-keyed event %d after migrating a legacy database: %v", i, err)
		}
	}
}
