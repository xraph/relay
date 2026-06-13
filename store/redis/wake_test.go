package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	redismodule "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/xraph/grove/kv"
	"github.com/xraph/grove/kv/drivers/redisdriver"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	redisstore "github.com/xraph/relay/store/redis"
)

// startRedis launches a disposable redis container and returns its
// connection string. Skips when -short is set or no container runtime is
// available.
func startRedis(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping container-backed integration test in -short mode")
	}

	ctx := context.Background()
	ctr, err := redismodule.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Skipf("container runtime unavailable: %v", err)
	}
	t.Cleanup(func() {
		if termErr := testcontainers.TerminateContainer(ctr); termErr != nil {
			t.Errorf("terminate redis container: %v", termErr)
		}
	})

	connStr, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	return connStr
}

func openRedisStore(t *testing.T, connStr string) *redisstore.Store {
	t.Helper()
	ctx := context.Background()

	rdb := redisdriver.New()
	if err := rdb.Open(ctx, connStr); err != nil {
		t.Fatalf("open redisdriver: %v", err)
	}
	kvStore, err := kv.Open(rdb)
	if err != nil {
		t.Fatalf("kv open: %v", err)
	}
	t.Cleanup(func() { _ = kvStore.Close() })

	return redisstore.New(kvStore)
}

// TestWakeListenerFiresOnEnqueue proves the pub/sub wake path: a wake
// listener started on one store connection fires when a delivery is
// enqueued through another connection (i.e. another app instance), without
// waiting for any poll interval.
func TestWakeListenerFiresOnEnqueue(t *testing.T) {
	connStr := startRedis(t)
	listenerStore := openRedisStore(t, connStr)
	enqueuerStore := openRedisStore(t, connStr)
	ctx := context.Background()

	woke := make(chan struct{}, 8)
	stop, err := listenerStore.StartWakeListener(ctx, func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("start wake listener: %v", err)
	}
	t.Cleanup(stop)

	d := &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       id.NewEventID(),
		EndpointID:    id.NewEndpointID(),
		State:         delivery.StatePending,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().UTC(),
	}
	if err := enqueuerStore.Enqueue(ctx, d); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("wake listener did not fire after cross-connection enqueue")
	}
}

// TestWakeListenerStopTerminates verifies stop returns and shuts the
// subscriber down cleanly.
func TestWakeListenerStopTerminates(t *testing.T) {
	s := openRedisStore(t, startRedis(t))

	stop, err := s.StartWakeListener(context.Background(), func() {})
	if err != nil {
		t.Fatalf("start wake listener: %v", err)
	}

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return")
	}
}
