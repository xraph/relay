package storetest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// EngineBackend is what the delivery engine and the DLQ service need from a
// store. Every backend's aggregate Store satisfies it.
type EngineBackend interface {
	delivery.EngineStore
	dlq.Store
	Enqueue(ctx context.Context, d *delivery.Delivery) error
	GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error)
	CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error
	CreateEvent(ctx context.Context, evt *event.Event) error
	ListAttempts(ctx context.Context, delID id.ID) ([]*delivery.Attempt, error)
}

// RunEngineSuite runs the real delivery engine against a backend.
//
// The engine's own tests run on the memory store, which keeps a claimed row
// `pending` and so hid that every other backend hands the engine a row in some
// other state. What the engine writes back after a retry decides whether the
// delivery is ever picked up again, and only a real backend can say.
func RunEngineSuite(t *testing.T, open func(t *testing.T) EngineBackend) {
	t.Run("retries a failed attempt until it is delivered", func(t *testing.T) {
		s := open(t)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		d := seedDelivery(t, s, srv.URL, 3)
		runEngine(t, s)

		got := waitForState(t, s, d.ID, delivery.StateDelivered)
		if got.AttemptCount != 2 {
			t.Errorf("attempt count %d, want 2", got.AttemptCount)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("receiver saw %d requests, want 2", n)
		}

		// The history the retry sequence is drawn from.
		attempts, err := s.ListAttempts(context.Background(), d.ID)
		if err != nil {
			t.Fatalf("list attempts: %v", err)
		}
		if len(attempts) != 2 {
			t.Fatalf("got %d attempts, want 2", len(attempts))
		}
		if a := attempts[0]; a.StatusCode != 500 || a.Outcome != delivery.OutcomeRetry || a.NextAttemptAt == nil {
			t.Errorf("first attempt (%d, %q, next %v), want (500, retry, set)", a.StatusCode, a.Outcome, a.NextAttemptAt)
		}
		if a := attempts[1]; a.StatusCode != 200 || a.Outcome != delivery.OutcomeDelivered || a.NextAttemptAt != nil {
			t.Errorf("second attempt (%d, %q, next %v), want (200, delivered, nil)", a.StatusCode, a.Outcome, a.NextAttemptAt)
		}
	})

	t.Run("records a 410 as the attempt that disabled the endpoint", func(t *testing.T) {
		s := open(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusGone)
		}))
		t.Cleanup(srv.Close)

		d := seedDelivery(t, s, srv.URL, 3)
		runEngine(t, s)

		waitForState(t, s, d.ID, delivery.StateFailed)
		attempts, err := s.ListAttempts(context.Background(), d.ID)
		if err != nil {
			t.Fatalf("list attempts: %v", err)
		}
		if len(attempts) != 1 || attempts[0].Outcome != delivery.OutcomeEndpointDisabled {
			t.Errorf("attempts %v, want one endpoint_disabled", attempts)
		}
	})

	t.Run("gives up into the DLQ after max attempts", func(t *testing.T) {
		s := open(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)

		d := seedDelivery(t, s, srv.URL, 2)
		runEngine(t, s)

		got := waitForState(t, s, d.ID, delivery.StateFailed)
		if got.AttemptCount != 2 {
			t.Errorf("attempt count %d, want 2", got.AttemptCount)
		}
		entries, err := s.ListDLQ(context.Background(), dlq.ListOpts{EndpointID: &d.EndpointID})
		if err != nil {
			t.Fatalf("list dlq: %v", err)
		}
		if len(entries) != 1 || entries[0].DeliveryID != d.ID {
			t.Errorf("dlq entries %v, want one for delivery %s", entries, d.ID)
		}
	})

	// Before the fix the engine returned early and left the row claimed,
	// which on a SQL backend means `delivering` forever.
	t.Run("fails a delivery whose endpoint is gone rather than leaving it claimed", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		evt := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.created",
			TenantID: uniqueTenant(t), Data: map[string]any{"n": 1}}
		if err := s.CreateEvent(ctx, evt); err != nil {
			t.Fatalf("create event: %v", err)
		}
		d := &delivery.Delivery{Entity: entity.New(), ID: id.NewDeliveryID(), EventID: evt.ID,
			EndpointID: id.NewEndpointID(), State: delivery.StatePending, MaxAttempts: 3,
			NextAttemptAt: time.Now().UTC()}
		if err := s.Enqueue(ctx, d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		runEngine(t, s)

		got := waitForState(t, s, d.ID, delivery.StateFailed)
		if !strings.Contains(got.LastError, "endpoint") {
			t.Errorf("last error %q, want it to name the endpoint", got.LastError)
		}
	})
}

func seedDelivery(t *testing.T, s EngineBackend, url string, maxAttempts int) *delivery.Delivery {
	t.Helper()
	ctx := context.Background()
	tenant := uniqueTenant(t)
	ep := &endpoint.Endpoint{Entity: entity.New(), ID: id.NewEndpointID(), TenantID: tenant,
		URL: url, Secret: "whsec_storetest", EventTypes: []string{"*"}, Enabled: true}
	if err := s.CreateEndpoint(ctx, ep); err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	evt := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.created",
		TenantID: tenant, Data: map[string]any{"n": 1}}
	if err := s.CreateEvent(ctx, evt); err != nil {
		t.Fatalf("create event: %v", err)
	}
	d := &delivery.Delivery{Entity: entity.New(), ID: id.NewDeliveryID(), EventID: evt.ID,
		EndpointID: ep.ID, State: delivery.StatePending, MaxAttempts: maxAttempts,
		NextAttemptAt: time.Now().UTC()}
	if err := s.Enqueue(ctx, d); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return d
}

func runEngine(t *testing.T, s EngineBackend) {
	t.Helper()
	svc := dlq.NewService(s, s, dlq.Config{MaxAttempts: 3}, nil)
	e := delivery.NewEngine(s, svc, delivery.EngineConfig{
		Concurrency:     4,
		PollInterval:    10 * time.Millisecond,
		MaxPollInterval: 20 * time.Millisecond,
		BatchSize:       10,
		RequestTimeout:  2 * time.Second,
		RetrySchedule:   []time.Duration{20 * time.Millisecond},
	}, nil)
	e.Start(context.Background())
	t.Cleanup(func() { e.Stop(context.Background()) })
}

func waitForState(t *testing.T, s EngineBackend, delID id.ID, want delivery.State) *delivery.Delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last *delivery.Delivery
	for time.Now().Before(deadline) {
		d, err := s.GetDelivery(context.Background(), delID)
		if err == nil {
			last = d
			if d.State == want {
				return d
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("delivery %s stuck in state %q after %d attempts (last error %q), want %q",
			delID, last.State, last.AttemptCount, last.LastError, want)
	}
	t.Fatalf("delivery %s never readable", delID)
	return nil
}
