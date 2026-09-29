package mongo_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/xraph/relay/id"
)

// Before payloads were stored as JSON text, PushFailed's bytes went to mongo
// as a BSON binary. The bytes were the right JSON; reading them back has to
// unwrap them rather than hand the page a {"Subtype":0,"Data":...} object.
func TestADeadLetterWrittenAsBinaryReadsBackAsJSON(t *testing.T) {
	uri := startMongo(t)
	s := openStore(t, uri)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	dlqID := id.NewDLQID()
	now := time.Now().UTC()
	const want = `{"id":"inv_1"}`
	if _, err := rawDatabase(t, uri).Collection("relay_dlq").InsertOne(ctx, bson.M{
		"_id": dlqID.String(), "delivery_id": id.NewDeliveryID().String(),
		"event_id": id.NewEventID().String(), "endpoint_id": id.NewEndpointID().String(),
		"tenant_id": "tenant-legacy", "event_type": "invoice.created", "url": "https://receiver.example/hook",
		"payload": bson.Binary{Subtype: 0, Data: []byte(want)},
		"error":   "connection refused", "attempt_count": 5, "last_status_code": 0,
		"failed_at": now, "created_at": now, "updated_at": now,
	}); err != nil {
		t.Fatalf("insert old-style entry: %v", err)
	}

	got, err := s.GetDLQ(ctx, dlqID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	b, err := json.Marshal(got.Payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != want {
		t.Fatalf("payload read back as %s, want %s", b, want)
	}
}
