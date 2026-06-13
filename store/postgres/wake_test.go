package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the pg migrate executor

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	pgstore "github.com/xraph/relay/store/postgres"
)

// startPostgres launches a disposable postgres container and returns its
// DSN. Skips when -short is set or no container runtime is available.
func startPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping container-backed integration test in -short mode")
	}

	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("relay"),
		tcpostgres.WithUsername("relay"),
		tcpostgres.WithPassword("relay"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("container runtime unavailable: %v", err)
	}
	t.Cleanup(func() {
		if termErr := testcontainers.TerminateContainer(ctr); termErr != nil {
			t.Errorf("terminate postgres container: %v", termErr)
		}
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return dsn
}

func openPgStore(t *testing.T, dsn string) *pgstore.Store {
	t.Helper()
	ctx := context.Background()

	drv := pgdriver.New()
	if err := drv.Open(ctx, dsn); err != nil {
		t.Fatalf("open pgdriver: %v", err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatalf("grove open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := pgstore.New(db)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// TestWakeListenerFiresOnEnqueue proves the LISTEN/NOTIFY path: a wake
// listener started on one store connection fires when a delivery is
// enqueued through another connection (i.e. another app instance), without
// waiting for any poll interval.
func TestWakeListenerFiresOnEnqueue(t *testing.T) {
	dsn := startPostgres(t)
	listenerStore := openPgStore(t, dsn)
	enqueuerStore := openPgStore(t, dsn)
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

	// Let the LISTEN subscription establish before the NOTIFY fires.
	time.Sleep(300 * time.Millisecond)

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

// TestWakeListenerStopTerminates verifies stop returns and the listener
// goroutine shuts down cleanly (the deferred db.Close in openPgStore hangs
// if the dedicated connection is never released).
func TestWakeListenerStopTerminates(t *testing.T) {
	dsn := startPostgres(t)
	s := openPgStore(t, dsn)

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
