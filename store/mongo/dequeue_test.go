package mongo_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongod "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/mongodriver"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	mongostore "github.com/xraph/relay/store/mongo"
)

const testDBName = "relay_dequeue_test"

// startMongo launches a disposable mongod and returns its URI. Skips when
// -short is set or no container runtime is available.
func startMongo(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping container-backed integration test in -short mode")
	}

	ctx := context.Background()
	ctr, err := tcmongo.Run(ctx, "mongo:7")
	if err != nil {
		t.Skipf("container runtime unavailable: %v", err)
	}
	t.Cleanup(func() {
		if termErr := testcontainers.TerminateContainer(ctr); termErr != nil {
			t.Errorf("terminate mongo container: %v", termErr)
		}
	})

	uri, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mongo connection string: %v", err)
	}
	return uri
}

func openStore(t *testing.T, uri string) *mongostore.Store {
	t.Helper()
	ctx := context.Background()

	drv := mongodriver.New()
	if err := drv.Open(ctx, uri, mongodriver.WithDatabase(testDBName)); err != nil {
		t.Fatalf("open mongodriver: %v", err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatalf("grove open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := mongostore.New(db)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// rawDatabase returns a direct mongo client handle for profiler control,
// independent of the store under test.
func rawDatabase(t *testing.T, uri string) *mongod.Database {
	t.Helper()
	client, err := mongod.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("raw mongo connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return client.Database(testDBName)
}

// countWriteCommands returns the number of profiled write operations
// (findAndModify, update, insert, delete) recorded against the deliveries
// collection.
func countWriteCommands(t *testing.T, mdb *mongod.Database) int {
	t.Helper()
	ctx := context.Background()

	cursor, err := mdb.Collection("system.profile").Find(ctx, bson.M{
		"$or": bson.A{
			bson.M{"op": bson.M{"$in": bson.A{"update", "insert", "remove"}}},
			bson.M{"command.findAndModify": bson.M{"$exists": true}},
			bson.M{"command.findandmodify": bson.M{"$exists": true}},
		},
		"ns": testDBName + ".relay_deliveries",
	})
	if err != nil {
		t.Fatalf("query system.profile: %v", err)
	}
	var entries []bson.M
	if err := cursor.All(ctx, &entries); err != nil {
		t.Fatalf("decode system.profile: %v", err)
	}
	return len(entries)
}

// TestDequeueIdleIssuesNoWriteCommands locks in the read-gate: polling an
// empty queue must not send write commands (findAndModify) to the server,
// otherwise an idle relay generates constant write traffic.
func TestDequeueIdleIssuesNoWriteCommands(t *testing.T) {
	uri := startMongo(t)
	s := openStore(t, uri)
	mdb := rawDatabase(t, uri)
	ctx := context.Background()

	// Profile every operation from here on.
	if err := mdb.RunCommand(ctx, bson.D{{Key: "profile", Value: 2}}).Err(); err != nil {
		t.Fatalf("enable profiling: %v", err)
	}

	for range 5 {
		batch, err := s.Dequeue(ctx, 10)
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if len(batch) != 0 {
			t.Fatalf("expected empty dequeue, got %d", len(batch))
		}
	}

	if n := countWriteCommands(t, mdb); n != 0 {
		t.Fatalf("idle Dequeue issued %d write commands against deliveries; want 0", n)
	}
}

// TestDequeueClaimsPendingDelivery proves the gate doesn't break the claim
// path: a pending delivery is still dequeued and transitioned.
func TestDequeueClaimsPendingDelivery(t *testing.T) {
	uri := startMongo(t)
	s := openStore(t, uri)
	ctx := context.Background()

	d := &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       id.NewEventID(),
		EndpointID:    id.NewEndpointID(),
		State:         delivery.StatePending,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().UTC().Add(-time.Second),
	}
	if err := s.Enqueue(ctx, d); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	batch, err := s.Dequeue(ctx, 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("expected 1 dequeued delivery, got %d", len(batch))
	}
	if batch[0].ID.String() != d.ID.String() {
		t.Fatalf("dequeued wrong delivery: %s", batch[0].ID)
	}
	if string(batch[0].State) != "delivering" {
		t.Fatalf("expected state delivering, got %s", batch[0].State)
	}
}
