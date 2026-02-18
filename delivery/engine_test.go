package delivery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/store/memory"
)

// stubDLQ is a simple DLQ pusher that records pushed entries.
type stubDLQ struct {
	pushed []*delivery.Delivery
	count  atomic.Int32
}

func (s *stubDLQ) PushFailed(_ context.Context, d *delivery.Delivery, _ *endpoint.Endpoint, _ *event.Event, _ string, _ int) error {
	s.pushed = append(s.pushed, d)
	s.count.Add(1)
	return nil
}

func setupEngine(t *testing.T, handler http.Handler, dlq delivery.DLQPusher) (*memory.Store, *delivery.Engine, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)

	store := memory.New()
	cfg := delivery.EngineConfig{
		Concurrency:    2,
		PollInterval:   50 * time.Millisecond,
		BatchSize:      10,
		RequestTimeout: 5 * time.Second,
		RetrySchedule:  []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
	}

	engine := delivery.NewEngine(store, dlq, cfg, nil)
	return store, engine, srv
}

func createTestData(t *testing.T, store *memory.Store, url string) (*endpoint.Endpoint, *delivery.Delivery) {
	t.Helper()
	ctx := context.Background()

	ep := &endpoint.Endpoint{
		Entity:     entity.New(),
		ID:         id.NewEndpointID(),
		TenantID:   "tenant-1",
		URL:        url,
		Secret:     "whsec_test_secret_1234567890abcdef1234567890abcdef",
		EventTypes: []string{"test.event"},
		Enabled:    true,
	}
	if err := store.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}

	evt := &event.Event{
		Entity:   entity.New(),
		ID:       id.NewEventID(),
		Type:     "test.event",
		TenantID: "tenant-1",
		Data:     json.RawMessage(`{"hello":"world"}`),
	}
	if err := store.CreateEvent(ctx, evt); err != nil {
		t.Fatal(err)
	}

	del := &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       evt.ID,
		EndpointID:    ep.ID,
		State:         delivery.StatePending,
		AttemptCount:  0,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().UTC(),
	}
	if err := store.Enqueue(ctx, del); err != nil {
		t.Fatal(err)
	}

	return ep, del
}

func TestEngineDeliversSuccessfully(t *testing.T) {
	var delivered atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delivered.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	dlq := &stubDLQ{}
	store, engine, srv := setupEngine(t, handler, dlq)
	defer srv.Close()

	_, del := createTestData(t, store, srv.URL)

	ctx := context.Background()
	engine.Start(ctx)

	// Wait for delivery to complete.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery")
		default:
		}

		got, err := store.GetDelivery(ctx, del.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == delivery.StateDelivered {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	engine.Stop(ctx)

	if delivered.Load() != 1 {
		t.Fatalf("expected 1 delivery, got %d", delivered.Load())
	}
	if dlq.count.Load() != 0 {
		t.Fatal("expected no DLQ pushes")
	}
}

func TestEngineRetriesAndSucceeds(t *testing.T) {
	var attempts atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	dlq := &stubDLQ{}
	store, engine, srv := setupEngine(t, handler, dlq)
	defer srv.Close()

	_, del := createTestData(t, store, srv.URL)

	ctx := context.Background()
	engine.Start(ctx)

	// Wait for delivery to complete.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery")
		default:
		}

		got, err := store.GetDelivery(ctx, del.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == delivery.StateDelivered {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	engine.Stop(ctx)

	if attempts.Load() < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", attempts.Load())
	}
	if dlq.count.Load() != 0 {
		t.Fatal("expected no DLQ pushes")
	}
}

func TestEngineExhaustsRetriesAndDLQs(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	dlqPusher := &stubDLQ{}
	store, engine, srv := setupEngine(t, handler, dlqPusher)
	defer srv.Close()

	_, del := createTestData(t, store, srv.URL)

	ctx := context.Background()
	engine.Start(ctx)

	// Wait for delivery to fail permanently.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery to fail")
		default:
		}

		got, err := store.GetDelivery(ctx, del.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == delivery.StateFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	engine.Stop(ctx)

	if dlqPusher.count.Load() != 1 {
		t.Fatalf("expected 1 DLQ push, got %d", dlqPusher.count.Load())
	}
}

func TestEngine410DisablesEndpoint(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})

	dlqPusher := &stubDLQ{}
	store, engine, srv := setupEngine(t, handler, dlqPusher)
	defer srv.Close()

	ep, del := createTestData(t, store, srv.URL)

	ctx := context.Background()
	engine.Start(ctx)

	// Wait for delivery to fail.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery to fail")
		default:
		}

		got, err := store.GetDelivery(ctx, del.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == delivery.StateFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	engine.Stop(ctx)

	// Verify endpoint was disabled.
	epGot, err := store.GetEndpoint(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if epGot.Enabled {
		t.Fatal("expected endpoint to be disabled after 410")
	}

	if dlqPusher.count.Load() != 1 {
		t.Fatalf("expected 1 DLQ push for 410, got %d", dlqPusher.count.Load())
	}
}

func TestEngineGracefulShutdown(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	store, engine, srv := setupEngine(t, handler, nil)
	defer srv.Close()

	ctx := context.Background()

	// Create multiple deliveries.
	for range 5 {
		createTestData(t, store, srv.URL)
	}

	engine.Start(ctx)

	// Give engine a moment to start processing.
	time.Sleep(200 * time.Millisecond)

	// Stop should wait for in-flight work.
	engine.Stop(ctx)

	// After stop, pending count should be lower (some or all delivered).
	pending, err := store.CountPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pending after shutdown: %d", pending)
}

func TestEngineNilDLQ(t *testing.T) {
	// Ensure engine works without a DLQ pusher.
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	store, engine, srv := setupEngine(t, handler, nil)
	defer srv.Close()

	_, del := createTestData(t, store, srv.URL)

	ctx := context.Background()
	engine.Start(ctx)

	// Wait for delivery to fail.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery to fail")
		default:
		}

		got, err := store.GetDelivery(ctx, del.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == delivery.StateFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	engine.Stop(ctx)
}
