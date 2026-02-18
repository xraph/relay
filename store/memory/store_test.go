package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

func ctx() context.Context { return context.Background() }

// ──────────────────────────────────────────────────
// Lifecycle
// ──────────────────────────────────────────────────

func TestLifecycle(t *testing.T) {
	s := New()

	if err := s.Migrate(ctx()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(ctx()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(ctx()); !errors.Is(err, relay.ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed, got %v", err)
	}
}

// ──────────────────────────────────────────────────
// catalog.Store
// ──────────────────────────────────────────────────

func TestCatalogCRUD(t *testing.T) {
	s := New()

	et := &catalog.EventType{
		Entity: entity.New(),
		ID:     id.NewEventTypeID(),
		Definition: catalog.WebhookDefinition{
			Name:        "invoice.created",
			Description: "Invoice was created",
			Group:       "invoice",
		},
	}

	// Register
	if err := s.RegisterType(ctx(), et); err != nil {
		t.Fatal(err)
	}

	// Get by name
	got, err := s.GetType(ctx(), "invoice.created")
	if err != nil {
		t.Fatal(err)
	}
	if got.Definition.Name != "invoice.created" {
		t.Fatalf("got name %q", got.Definition.Name)
	}

	// Get by ID
	got, err = s.GetTypeByID(ctx(), et.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Definition.Name != "invoice.created" {
		t.Fatalf("got name %q", got.Definition.Name)
	}

	// Get not found
	_, err = s.GetType(ctx(), "does.not.exist")
	if !errors.Is(err, relay.ErrEventTypeNotFound) {
		t.Fatalf("expected ErrEventTypeNotFound, got %v", err)
	}

	// List
	list, err := s.ListTypes(ctx(), catalog.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 type, got %d", len(list))
	}

	// Upsert (re-register same name)
	et2 := &catalog.EventType{
		Entity: entity.New(),
		ID:     id.NewEventTypeID(),
		Definition: catalog.WebhookDefinition{
			Name:        "invoice.created",
			Description: "Updated description",
			Group:       "invoice",
		},
	}
	if err := s.RegisterType(ctx(), et2); err != nil {
		t.Fatal(err)
	}

	got, _ = s.GetType(ctx(), "invoice.created")
	if got.Definition.Description != "Updated description" {
		t.Fatalf("expected updated description, got %q", got.Definition.Description)
	}
	// ID should be preserved from original registration.
	if et2.ID != et.ID {
		t.Fatalf("expected ID to be preserved on upsert")
	}

	// Delete (soft-delete)
	if err := s.DeleteType(ctx(), "invoice.created"); err != nil {
		t.Fatal(err)
	}

	// Listed without IncludeDeprecated → empty
	list, _ = s.ListTypes(ctx(), catalog.ListOpts{})
	if len(list) != 0 {
		t.Fatalf("expected 0 types after delete, got %d", len(list))
	}

	// Listed with IncludeDeprecated → 1
	list, _ = s.ListTypes(ctx(), catalog.ListOpts{IncludeDeprecated: true})
	if len(list) != 1 {
		t.Fatalf("expected 1 type with IncludeDeprecated, got %d", len(list))
	}

	// Delete not found
	if err := s.DeleteType(ctx(), "does.not.exist"); !errors.Is(err, relay.ErrEventTypeNotFound) {
		t.Fatalf("expected ErrEventTypeNotFound, got %v", err)
	}
}

func TestCatalogListWithGroupFilter(t *testing.T) {
	s := New()

	for _, name := range []string{"invoice.created", "invoice.paid", "user.created"} {
		group := "invoice"
		if name == "user.created" {
			group = "user"
		}
		et := &catalog.EventType{
			Entity: entity.New(),
			ID:     id.NewEventTypeID(),
			Definition: catalog.WebhookDefinition{
				Name:  name,
				Group: group,
			},
		}
		if err := s.RegisterType(ctx(), et); err != nil {
			t.Fatal(err)
		}
	}

	list, _ := s.ListTypes(ctx(), catalog.ListOpts{Group: "invoice"})
	if len(list) != 2 {
		t.Fatalf("expected 2 invoice types, got %d", len(list))
	}
}

func TestCatalogListPagination(t *testing.T) {
	s := New()

	for _, name := range []string{"a.type", "b.type", "c.type", "d.type"} {
		et := &catalog.EventType{
			Entity:     entity.New(),
			ID:         id.NewEventTypeID(),
			Definition: catalog.WebhookDefinition{Name: name},
		}
		if err := s.RegisterType(ctx(), et); err != nil {
			t.Fatal(err)
		}
	}

	list, _ := s.ListTypes(ctx(), catalog.ListOpts{Offset: 1, Limit: 2})
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	if list[0].Definition.Name != "b.type" || list[1].Definition.Name != "c.type" {
		t.Fatalf("unexpected pagination results: %q, %q", list[0].Definition.Name, list[1].Definition.Name)
	}
}

func TestCatalogMatchTypes(t *testing.T) {
	s := New()

	for _, name := range []string{"invoice.created", "invoice.paid", "user.created"} {
		et := &catalog.EventType{
			Entity:     entity.New(),
			ID:         id.NewEventTypeID(),
			Definition: catalog.WebhookDefinition{Name: name},
		}
		_ = s.RegisterType(ctx(), et)
	}

	result, _ := s.MatchTypes(ctx(), "invoice.*")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}

	result, _ = s.MatchTypes(ctx(), "*")
	if len(result) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(result))
	}
}

// ──────────────────────────────────────────────────
// endpoint.Store
// ──────────────────────────────────────────────────

func newEndpoint(tenantID string, eventTypes []string) *endpoint.Endpoint {
	return &endpoint.Endpoint{
		Entity:     entity.New(),
		ID:         id.NewEndpointID(),
		TenantID:   tenantID,
		URL:        "https://example.com/webhook",
		Secret:     "whsec_test",
		EventTypes: eventTypes,
		Enabled:    true,
	}
}

func TestEndpointCRUD(t *testing.T) {
	s := New()

	ep := newEndpoint("tenant1", []string{"invoice.*"})

	// Create
	if err := s.CreateEndpoint(ctx(), ep); err != nil {
		t.Fatal(err)
	}

	// Get
	got, err := s.GetEndpoint(ctx(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TenantID != "tenant1" {
		t.Fatalf("got tenant %q", got.TenantID)
	}

	// Get not found
	_, err = s.GetEndpoint(ctx(), id.NewEndpointID())
	if !errors.Is(err, relay.ErrEndpointNotFound) {
		t.Fatalf("expected ErrEndpointNotFound, got %v", err)
	}

	// Update
	ep.Description = "Updated"
	err = s.UpdateEndpoint(ctx(), ep)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetEndpoint(ctx(), ep.ID)
	if got.Description != "Updated" {
		t.Fatalf("expected updated description")
	}

	// Update not found
	fake := newEndpoint("tenant1", nil)
	err = s.UpdateEndpoint(ctx(), fake)
	if !errors.Is(err, relay.ErrEndpointNotFound) {
		t.Fatalf("expected ErrEndpointNotFound, got %v", err)
	}

	// List
	list, err := s.ListEndpoints(ctx(), "tenant1", endpoint.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}

	// Delete
	err = s.DeleteEndpoint(ctx(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GetEndpoint(ctx(), ep.ID)
	if !errors.Is(err, relay.ErrEndpointNotFound) {
		t.Fatalf("expected deleted")
	}
}

func TestEndpointSetEnabled(t *testing.T) {
	s := New()

	ep := newEndpoint("t1", []string{"*"})
	_ = s.CreateEndpoint(ctx(), ep)

	if err := s.SetEnabled(ctx(), ep.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEndpoint(ctx(), ep.ID)
	if got.Enabled {
		t.Fatal("expected disabled")
	}

	if err := s.SetEnabled(ctx(), id.NewEndpointID(), true); !errors.Is(err, relay.ErrEndpointNotFound) {
		t.Fatalf("expected ErrEndpointNotFound, got %v", err)
	}
}

func TestEndpointResolve(t *testing.T) {
	s := New()

	ep1 := newEndpoint("t1", []string{"invoice.*"})
	ep2 := newEndpoint("t1", []string{"user.*"})
	ep3 := newEndpoint("t1", []string{"*"})
	epDisabled := newEndpoint("t1", []string{"*"})
	epDisabled.Enabled = false
	epOtherTenant := newEndpoint("t2", []string{"*"})

	for _, ep := range []*endpoint.Endpoint{ep1, ep2, ep3, epDisabled, epOtherTenant} {
		_ = s.CreateEndpoint(ctx(), ep)
	}

	// invoice.created → ep1 + ep3 (not ep2, not disabled, not other tenant)
	result, err := s.Resolve(ctx(), "t1", "invoice.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 resolved, got %d", len(result))
	}
}

func TestEndpointListFilters(t *testing.T) {
	s := New()

	ep1 := newEndpoint("t1", []string{"*"})
	ep2 := newEndpoint("t1", []string{"*"})
	ep2.Enabled = false
	_ = s.CreateEndpoint(ctx(), ep1)
	_ = s.CreateEndpoint(ctx(), ep2)

	enabled := true
	list, _ := s.ListEndpoints(ctx(), "t1", endpoint.ListOpts{Enabled: &enabled})
	if len(list) != 1 {
		t.Fatalf("expected 1 enabled, got %d", len(list))
	}
}

// ──────────────────────────────────────────────────
// event.Store
// ──────────────────────────────────────────────────

func newEvent(tenantID, eventType string) *event.Event {
	return &event.Event{
		Entity:   entity.New(),
		ID:       id.NewEventID(),
		Type:     eventType,
		TenantID: tenantID,
		Data:     map[string]string{"key": "value"},
	}
}

func TestEventCRUD(t *testing.T) {
	s := New()

	evt := newEvent("t1", "invoice.created")

	// Create
	if err := s.CreateEvent(ctx(), evt); err != nil {
		t.Fatal(err)
	}

	// Get
	got, err := s.GetEvent(ctx(), evt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "invoice.created" {
		t.Fatalf("got type %q", got.Type)
	}

	// Get not found
	_, err = s.GetEvent(ctx(), id.NewEventID())
	if !errors.Is(err, relay.ErrEventNotFound) {
		t.Fatalf("expected ErrEventNotFound, got %v", err)
	}
}

func TestEventIdempotencyKey(t *testing.T) {
	s := New()

	evt := newEvent("t1", "invoice.created")
	evt.IdempotencyKey = "unique-key-1"

	if err := s.CreateEvent(ctx(), evt); err != nil {
		t.Fatal(err)
	}

	// Duplicate idempotency key
	evt2 := newEvent("t1", "invoice.created")
	evt2.IdempotencyKey = "unique-key-1"
	if err := s.CreateEvent(ctx(), evt2); !errors.Is(err, relay.ErrDuplicateIdempotencyKey) {
		t.Fatalf("expected ErrDuplicateIdempotencyKey, got %v", err)
	}

	// Empty idempotency key is fine
	evt3 := newEvent("t1", "invoice.created")
	if err := s.CreateEvent(ctx(), evt3); err != nil {
		t.Fatal(err)
	}
}

func TestEventListFilters(t *testing.T) {
	s := New()

	for _, typ := range []string{"invoice.created", "invoice.paid", "user.created"} {
		evt := newEvent("t1", typ)
		_ = s.CreateEvent(ctx(), evt)
	}

	// Filter by type
	list, _ := s.ListEvents(ctx(), event.ListOpts{Type: "invoice.created"})
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}

	// All
	list, _ = s.ListEvents(ctx(), event.ListOpts{})
	if len(list) != 3 {
		t.Fatalf("expected 3, got %d", len(list))
	}
}

func TestEventListByTenant(t *testing.T) {
	s := New()

	_ = s.CreateEvent(ctx(), newEvent("t1", "a"))
	_ = s.CreateEvent(ctx(), newEvent("t1", "b"))
	_ = s.CreateEvent(ctx(), newEvent("t2", "c"))

	list, _ := s.ListEventsByTenant(ctx(), "t1", event.ListOpts{})
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestEventListTimeFilter(t *testing.T) {
	s := New()

	evt := newEvent("t1", "a")
	_ = s.CreateEvent(ctx(), evt)

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	list, _ := s.ListEvents(ctx(), event.ListOpts{From: &past, To: &future})
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}

	list, _ = s.ListEvents(ctx(), event.ListOpts{From: &future})
	if len(list) != 0 {
		t.Fatalf("expected 0, got %d", len(list))
	}
}

// ──────────────────────────────────────────────────
// delivery.Store
// ──────────────────────────────────────────────────

func newDelivery(evtID, epID id.ID) *delivery.Delivery {
	return &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       evtID,
		EndpointID:    epID,
		State:         delivery.StatePending,
		AttemptCount:  0,
		MaxAttempts:   5,
		NextAttemptAt: time.Now().Add(-time.Second), // ready for dequeue
	}
}

func TestDeliveryCRUD(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	epID := id.NewEndpointID()
	d := newDelivery(evtID, epID)

	// Enqueue
	if err := s.Enqueue(ctx(), d); err != nil {
		t.Fatal(err)
	}

	// Get
	got, err := s.GetDelivery(ctx(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != delivery.StatePending {
		t.Fatalf("expected pending, got %s", got.State)
	}

	// Update
	d.State = delivery.StateDelivered
	err = s.UpdateDelivery(ctx(), d)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDelivery(ctx(), d.ID)
	if got.State != delivery.StateDelivered {
		t.Fatalf("expected delivered, got %s", got.State)
	}

	// Get not found
	_, err = s.GetDelivery(ctx(), id.NewDeliveryID())
	if !errors.Is(err, relay.ErrDeliveryNotFound) {
		t.Fatalf("expected ErrDeliveryNotFound, got %v", err)
	}
}

func TestDeliveryEnqueueBatch(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	ds := []*delivery.Delivery{
		newDelivery(evtID, id.NewEndpointID()),
		newDelivery(evtID, id.NewEndpointID()),
		newDelivery(evtID, id.NewEndpointID()),
	}

	if err := s.EnqueueBatch(ctx(), ds); err != nil {
		t.Fatal(err)
	}

	count, _ := s.CountPending(ctx())
	if count != 3 {
		t.Fatalf("expected 3 pending, got %d", count)
	}
}

func TestDeliveryDequeue(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	for i := 0; i < 5; i++ {
		_ = s.Enqueue(ctx(), newDelivery(evtID, id.NewEndpointID()))
	}

	// Dequeue with limit
	batch, err := s.Dequeue(ctx(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 3 {
		t.Fatalf("expected 3, got %d", len(batch))
	}

	// Second dequeue should get remaining 2 (first 3 are locked)
	batch2, _ := s.Dequeue(ctx(), 10)
	if len(batch2) != 2 {
		t.Fatalf("expected 2, got %d", len(batch2))
	}

	// Third dequeue should get 0 (all locked)
	batch3, _ := s.Dequeue(ctx(), 10)
	if len(batch3) != 0 {
		t.Fatalf("expected 0, got %d", len(batch3))
	}

	// Update (release lock) on first batch item, then dequeue again
	batch[0].State = delivery.StateDelivered
	_ = s.UpdateDelivery(ctx(), batch[0])

	batch4, _ := s.Dequeue(ctx(), 10)
	// The delivered item shouldn't be dequeued (state != pending)
	if len(batch4) != 0 {
		t.Fatalf("expected 0 (delivered items not dequeued), got %d", len(batch4))
	}
}

func TestDeliveryDequeueRespectsNextAttemptAt(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	d := newDelivery(evtID, id.NewEndpointID())
	d.NextAttemptAt = time.Now().Add(time.Hour) // future
	_ = s.Enqueue(ctx(), d)

	batch, _ := s.Dequeue(ctx(), 10)
	if len(batch) != 0 {
		t.Fatalf("expected 0 (not ready), got %d", len(batch))
	}
}

func TestDeliveryListByEndpoint(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	epID := id.NewEndpointID()

	d1 := newDelivery(evtID, epID)
	d2 := newDelivery(evtID, epID)
	d3 := newDelivery(evtID, id.NewEndpointID()) // different endpoint

	_ = s.Enqueue(ctx(), d1)
	_ = s.Enqueue(ctx(), d2)
	_ = s.Enqueue(ctx(), d3)

	list, _ := s.ListByEndpoint(ctx(), epID, delivery.ListOpts{})
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestDeliveryListByEvent(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	d1 := newDelivery(evtID, id.NewEndpointID())
	d2 := newDelivery(evtID, id.NewEndpointID())
	d3 := newDelivery(id.NewEventID(), id.NewEndpointID()) // different event

	_ = s.Enqueue(ctx(), d1)
	_ = s.Enqueue(ctx(), d2)
	_ = s.Enqueue(ctx(), d3)

	list, _ := s.ListByEvent(ctx(), evtID)
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestDeliveryCountPending(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	d1 := newDelivery(evtID, id.NewEndpointID())
	d2 := newDelivery(evtID, id.NewEndpointID())
	_ = s.Enqueue(ctx(), d1)
	_ = s.Enqueue(ctx(), d2)

	// Mark one as delivered
	d1.State = delivery.StateDelivered
	_ = s.UpdateDelivery(ctx(), d1)

	count, _ := s.CountPending(ctx())
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}
}

// ──────────────────────────────────────────────────
// dlq.Store
// ──────────────────────────────────────────────────

func newDLQEntry(evtID, epID id.ID) *dlq.Entry {
	return &dlq.Entry{
		Entity:         entity.New(),
		ID:             id.NewDLQID(),
		DeliveryID:     id.NewDeliveryID(),
		EventID:        evtID,
		EndpointID:     epID,
		TenantID:       "t1",
		Payload:        []byte(`{"test":true}`),
		Error:          "connection refused",
		LastStatusCode: 500,
		FailedAt:       time.Now().UTC(),
	}
}

func TestDLQCRUD(t *testing.T) {
	s := New()

	evtID := id.NewEventID()
	epID := id.NewEndpointID()
	entry := newDLQEntry(evtID, epID)

	// Push
	if err := s.Push(ctx(), entry); err != nil {
		t.Fatal(err)
	}

	// Get
	got, err := s.GetDLQ(ctx(), entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != "connection refused" {
		t.Fatalf("got error %q", got.Error)
	}

	// Get not found
	_, err = s.GetDLQ(ctx(), id.NewDLQID())
	if !errors.Is(err, relay.ErrDLQNotFound) {
		t.Fatalf("expected ErrDLQNotFound, got %v", err)
	}

	// Count
	count, _ := s.CountDLQ(ctx())
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}
}

func TestDLQList(t *testing.T) {
	s := New()

	epID := id.NewEndpointID()
	_ = s.Push(ctx(), newDLQEntry(id.NewEventID(), epID))
	_ = s.Push(ctx(), newDLQEntry(id.NewEventID(), id.NewEndpointID()))

	// List all for tenant
	list, _ := s.ListDLQ(ctx(), dlq.ListOpts{TenantID: "t1"})
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}

	// Filter by endpoint
	list, _ = s.ListDLQ(ctx(), dlq.ListOpts{EndpointID: &epID})
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}
}

func TestDLQReplay(t *testing.T) {
	s := New()

	entry := newDLQEntry(id.NewEventID(), id.NewEndpointID())
	_ = s.Push(ctx(), entry)

	// Before replay, 0 pending deliveries
	count, _ := s.CountPending(ctx())
	if count != 0 {
		t.Fatalf("expected 0 pending, got %d", count)
	}

	// Replay
	if err := s.Replay(ctx(), entry.ID); err != nil {
		t.Fatal(err)
	}

	// After replay, 1 pending delivery
	count, _ = s.CountPending(ctx())
	if count != 1 {
		t.Fatalf("expected 1 pending, got %d", count)
	}

	// Entry should have ReplayedAt set
	got, _ := s.GetDLQ(ctx(), entry.ID)
	if got.ReplayedAt == nil {
		t.Fatal("expected ReplayedAt to be set")
	}

	// Replay not found
	if err := s.Replay(ctx(), id.NewDLQID()); !errors.Is(err, relay.ErrDLQNotFound) {
		t.Fatalf("expected ErrDLQNotFound, got %v", err)
	}
}

func TestDLQReplayBulk(t *testing.T) {
	s := New()

	_ = s.Push(ctx(), newDLQEntry(id.NewEventID(), id.NewEndpointID()))
	_ = s.Push(ctx(), newDLQEntry(id.NewEventID(), id.NewEndpointID()))

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)

	count, err := s.ReplayBulk(ctx(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}

	// Replaying again should return 0 (already replayed)
	count, _ = s.ReplayBulk(ctx(), from, to)
	if count != 0 {
		t.Fatalf("expected 0 on second replay, got %d", count)
	}
}

func TestDLQPurge(t *testing.T) {
	s := New()

	_ = s.Push(ctx(), newDLQEntry(id.NewEventID(), id.NewEndpointID()))
	_ = s.Push(ctx(), newDLQEntry(id.NewEventID(), id.NewEndpointID()))

	// Purge entries created before "far future" → all
	count, _ := s.Purge(ctx(), time.Now().Add(time.Hour))
	if count != 2 {
		t.Fatalf("expected 2 purged, got %d", count)
	}

	remaining, _ := s.CountDLQ(ctx())
	if remaining != 0 {
		t.Fatalf("expected 0, got %d", remaining)
	}
}
