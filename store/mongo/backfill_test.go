package mongo_test

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// See the postgres test of the same name.
func TestMigrationBackfillsEventTypeAndTenantOntoOldDeliveries(t *testing.T) {
	uri := startMongo(t)
	s := openStore(t, uri)
	ctx := context.Background()

	evt := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.paid",
		TenantID: "tenant-backfill", Data: map[string]any{"n": 1}}
	if err := s.CreateEvent(ctx, evt); err != nil {
		t.Fatalf("create event: %v", err)
	}
	delID := id.NewDeliveryID()
	now := time.Now().UTC()
	// Written the way an older version wrote it: neither field present.
	if _, err := rawDatabase(t, uri).Collection("relay_deliveries").InsertOne(ctx, bson.M{
		"_id": delID.String(), "event_id": evt.ID.String(), "endpoint_id": id.NewEndpointID().String(),
		"state": "pending", "next_attempt_at": now, "created_at": now, "updated_at": now,
	}); err != nil {
		t.Fatalf("insert old-style delivery: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := s.GetDelivery(ctx, delID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EventType != "invoice.paid" || got.TenantID != "tenant-backfill" {
		t.Errorf("backfilled (%q, %q), want (%q, %q)", got.EventType, got.TenantID, "invoice.paid", "tenant-backfill")
	}
}
