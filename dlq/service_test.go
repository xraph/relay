package dlq_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/store/memory"
)

func ctx() context.Context { return context.Background() }

// seedEntry pushes an un-replayed DLQ entry and returns it.
func seedEntry(t *testing.T, store *memory.Store) *dlq.Entry {
	t.Helper()
	e := &dlq.Entry{
		Entity:       entity.New(),
		ID:           id.NewDLQID(),
		DeliveryID:   id.NewDeliveryID(),
		EventID:      id.NewEventID(),
		EndpointID:   id.NewEndpointID(),
		EventType:    "invoice.created",
		TenantID:     "tenant-1",
		URL:          "https://receiver.example/hook",
		Error:        "connection refused",
		AttemptCount: 5,
		FailedAt:     time.Now().UTC().Add(-time.Hour),
	}
	if err := store.Push(ctx(), e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return e
}

func newService() (*dlq.Service, *memory.Store) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 5}, nil)
	return svc, store
}

func TestPushFailed(t *testing.T) {
	svc, store := newService()

	d := &delivery.Delivery{
		Entity:         entity.New(),
		ID:             id.NewDeliveryID(),
		EventID:        id.NewEventID(),
		EndpointID:     id.NewEndpointID(),
		AttemptCount:   5,
		LastStatusCode: 500,
	}
	ep := &endpoint.Endpoint{
		Entity:   entity.New(),
		ID:       d.EndpointID,
		TenantID: "tenant-1",
		URL:      "https://example.com/webhook",
	}
	evt := &event.Event{
		Entity: entity.New(),
		ID:     d.EventID,
		Type:   "invoice.created",
		Data:   json.RawMessage(`{"amount":100}`),
	}

	err := svc.PushFailed(ctx(), d, ep, evt, "server error", 500)
	if err != nil {
		t.Fatal(err)
	}

	// Verify entry was stored.
	entries, err := store.ListDLQ(ctx(), dlq.ListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.DeliveryID != d.ID {
		t.Fatalf("delivery ID mismatch: got %v, want %v", entry.DeliveryID, d.ID)
	}
	if entry.EventID != d.EventID {
		t.Fatalf("event ID mismatch")
	}
	if entry.EndpointID != d.EndpointID {
		t.Fatalf("endpoint ID mismatch")
	}
	if entry.EventType != "invoice.created" {
		t.Fatalf("event type: got %q, want %q", entry.EventType, "invoice.created")
	}
	if entry.TenantID != "tenant-1" {
		t.Fatalf("tenant ID: got %q, want %q", entry.TenantID, "tenant-1")
	}
	if entry.URL != "https://example.com/webhook" {
		t.Fatalf("URL mismatch")
	}
	if entry.Error != "server error" {
		t.Fatalf("error: got %q, want %q", entry.Error, "server error")
	}
	if entry.AttemptCount != 5 {
		t.Fatalf("attempt count: got %d, want 5", entry.AttemptCount)
	}
	if entry.LastStatusCode != 500 {
		t.Fatalf("status code: got %d, want 500", entry.LastStatusCode)
	}
}

func TestPushMultipleAndList(t *testing.T) {
	svc, _ := newService()

	for range 3 {
		d := &delivery.Delivery{
			Entity:     entity.New(),
			ID:         id.NewDeliveryID(),
			EventID:    id.NewEventID(),
			EndpointID: id.NewEndpointID(),
		}
		ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
		evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}
		if err := svc.PushFailed(ctx(), d, ep, evt, "err", 500); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := svc.List(ctx(), dlq.ListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestGetDLQEntry(t *testing.T) {
	svc, _ := newService()

	d := &delivery.Delivery{
		Entity:     entity.New(),
		ID:         id.NewDeliveryID(),
		EventID:    id.NewEventID(),
		EndpointID: id.NewEndpointID(),
	}
	ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
	evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}

	if err := svc.PushFailed(ctx(), d, ep, evt, "err", 500); err != nil {
		t.Fatal(err)
	}

	entries, _ := svc.List(ctx(), dlq.ListOpts{Limit: 1})
	if len(entries) == 0 {
		t.Fatal("expected at least 1 entry")
	}

	got, err := svc.Get(ctx(), entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != entries[0].ID {
		t.Fatal("ID mismatch on Get")
	}
}

func TestCount(t *testing.T) {
	svc, _ := newService()

	count, err := svc.Count(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}

	for range 5 {
		d := &delivery.Delivery{
			Entity:     entity.New(),
			ID:         id.NewDeliveryID(),
			EventID:    id.NewEventID(),
			EndpointID: id.NewEndpointID(),
		}
		ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
		evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}
		svc.PushFailed(ctx(), d, ep, evt, "err", 500)
	}

	count, err = svc.Count(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("expected 5, got %d", count)
	}
}

func TestReplay(t *testing.T) {
	svc, store := newService()

	d := &delivery.Delivery{
		Entity:     entity.New(),
		ID:         id.NewDeliveryID(),
		EventID:    id.NewEventID(),
		EndpointID: id.NewEndpointID(),
	}
	ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
	evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}

	svc.PushFailed(ctx(), d, ep, evt, "err", 500)

	entries, _ := svc.List(ctx(), dlq.ListOpts{Limit: 1})
	if len(entries) == 0 {
		t.Fatal("expected entry")
	}

	// Replay should mark the entry.
	err := svc.Replay(ctx(), entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	// Verify replayed_at is set.
	got, _ := store.GetDLQ(ctx(), entries[0].ID)
	if got.ReplayedAt == nil {
		t.Fatal("expected replayed_at to be set")
	}
}

func TestPurge(t *testing.T) {
	svc, _ := newService()

	for range 3 {
		d := &delivery.Delivery{
			Entity:     entity.New(),
			ID:         id.NewDeliveryID(),
			EventID:    id.NewEventID(),
			EndpointID: id.NewEndpointID(),
		}
		ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
		evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}
		svc.PushFailed(ctx(), d, ep, evt, "err", 500)
	}

	// Purge entries before "now + 1 second" should remove all.
	purged, err := svc.Purge(ctx(), time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if purged != 3 {
		t.Fatalf("expected 3 purged, got %d", purged)
	}

	count, _ := svc.Count(ctx())
	if count != 0 {
		t.Fatalf("expected 0 after purge, got %d", count)
	}
}

// A zero MaxAttempts is the exact value that caused the replay bug: the
// retrier evaluates `1 < 0` and sends the delivery straight back to the DLQ.
// So the service refuses to honour it and falls back to a sane default.
func TestConfigDefaultsMaxAttempts(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 0}, nil)
	if got := svc.MaxAttempts(); got != dlq.DefaultMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, dlq.DefaultMaxAttempts)
	}
}

func TestConfigRefusesANegativeMaxAttempts(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: -3}, nil)
	if got := svc.MaxAttempts(); got != dlq.DefaultMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, dlq.DefaultMaxAttempts)
	}
}

func TestConfigHonoursMaxAttempts(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 3}, nil)
	if got := svc.MaxAttempts(); got != 3 {
		t.Fatalf("MaxAttempts() = %d, want 3", got)
	}
}

// The budget must come from config. 7 is deliberate: the old memory store
// hardcoded 5 and the other backends left it at 0, so a test at 5 would pass
// on the old code by coincidence and prove nothing.
func TestReplayEnqueuesWithTheConfiguredBudget(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 7}, nil)
	e := seedEntry(t, store)

	if err := svc.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}

	dels := store.AllDeliveries()
	if len(dels) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(dels))
	}
	if dels[0].MaxAttempts != 7 {
		t.Fatalf("MaxAttempts = %d, want 7 from config", dels[0].MaxAttempts)
	}
	if dels[0].AttemptCount != 0 {
		t.Fatalf("AttemptCount = %d, want 0: a replay starts over", dels[0].AttemptCount)
	}
	if dels[0].State != delivery.StatePending {
		t.Fatalf("State = %q, want pending", dels[0].State)
	}
	if dels[0].EventID != e.EventID || dels[0].EndpointID != e.EndpointID {
		t.Fatal("the replayed delivery does not target the original event and endpoint")
	}
}

// Regression guard. The old memory store already kept and marked the row, so
// this passes before and after on memory; the conformance suite is what proves
// row-keeping on the real backends.
func TestReplayKeepsAndMarksTheEntry(t *testing.T) {
	svc, store := newService()
	e := seedEntry(t, store)

	if err := svc.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := store.GetDLQ(ctx(), e.ID)
	if err != nil {
		t.Fatalf("the entry must survive a replay: %v", err)
	}
	if got.ReplayedAt == nil {
		t.Fatal("ReplayedAt is nil after a replay")
	}
}

// Replaying sends a real webhook. A second replay of the same entry must be
// refused before anything is enqueued, not after.
func TestReplayTwiceIsRefused(t *testing.T) {
	svc, store := newService()
	e := seedEntry(t, store)

	if err := svc.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("first replay: %v", err)
	}
	err := svc.Replay(ctx(), e.ID)
	if !errors.Is(err, relay.ErrAlreadyReplayed) {
		t.Fatalf("second replay error = %v, want ErrAlreadyReplayed", err)
	}
	if !errors.Is(err, dlq.ErrAlreadyReplayed) {
		t.Fatal("relay.ErrAlreadyReplayed and dlq.ErrAlreadyReplayed are not the same value")
	}
	if n := len(store.AllDeliveries()); n != 1 {
		t.Fatalf("got %d deliveries after a refused replay, want 1: "+
			"the refusal must happen before the enqueue", n)
	}
}

func TestReplayMissingEntry(t *testing.T) {
	svc, _ := newService()
	err := svc.Replay(ctx(), id.NewDLQID())
	if !errors.Is(err, relay.ErrDLQNotFound) {
		t.Fatalf("error = %v, want ErrDLQNotFound", err)
	}
}
