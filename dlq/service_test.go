package dlq_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/store/memory"
)

func ctx() context.Context { return context.Background() }

func newService() (*dlq.Service, *memory.Store) {
	store := memory.New()
	svc := dlq.NewService(store, nil)
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
