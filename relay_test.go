package relay_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/store/memory"
)

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func ctx() context.Context { return context.Background() }

func setup(t *testing.T) (*relay.Relay, *memory.Store) {
	t.Helper()
	s := memory.New()
	r, err := relay.New(relay.WithStore(s))
	if err != nil {
		t.Fatal(err)
	}
	return r, s
}

func registerType(t *testing.T, r *relay.Relay, name string) {
	t.Helper()
	_, err := r.RegisterEventType(ctx(), catalog.WebhookDefinition{
		Name: name,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func createEndpoint(t *testing.T, r *relay.Relay, tenantID string, patterns []string) {
	t.Helper()
	_, err := r.Endpoints().Create(ctx(), endpoint.Input{
		TenantID:   tenantID,
		URL:        "https://example.com/webhook",
		EventTypes: patterns,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendHappyPath(t *testing.T) {
	r, s := setup(t)

	registerType(t, r, "invoice.created")
	createEndpoint(t, r, "t1", []string{"invoice.*"})
	createEndpoint(t, r, "t1", []string{"*"})

	evt := &event.Event{
		Type:     "invoice.created",
		TenantID: "t1",
		Data:     map[string]any{"amount": 100},
	}

	if err := r.Send(ctx(), evt); err != nil {
		t.Fatal(err)
	}

	// Event should be persisted.
	if evt.ID.String() == "" {
		t.Fatal("expected event ID to be assigned")
	}

	// 2 endpoints matched → 2 deliveries.
	pending, _ := s.CountPending(ctx())
	if pending != 2 {
		t.Fatalf("expected 2 pending deliveries, got %d", pending)
	}

	// Deliveries should reference the event.
	deliveries, _ := s.ListByEvent(ctx(), evt.ID)
	if len(deliveries) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(deliveries))
	}
	for _, d := range deliveries {
		if d.State != delivery.StatePending {
			t.Fatalf("expected pending, got %s", d.State)
		}
	}
}

func TestSendUnknownEventType(t *testing.T) {
	r, _ := setup(t)

	evt := &event.Event{
		Type:     "does.not.exist",
		TenantID: "t1",
		Data:     map[string]any{},
	}

	err := r.Send(ctx(), evt)
	if !errors.Is(err, relay.ErrEventTypeNotFound) {
		t.Fatalf("expected ErrEventTypeNotFound, got %v", err)
	}
}

func TestSendDeprecatedEventType(t *testing.T) {
	r, _ := setup(t)

	registerType(t, r, "old.event")

	// Deprecate it via the catalog.
	if err := r.Catalog().DeleteType(ctx(), "old.event"); err != nil {
		t.Fatal(err)
	}

	evt := &event.Event{
		Type:     "old.event",
		TenantID: "t1",
		Data:     map[string]any{},
	}

	err := r.Send(ctx(), evt)
	if !errors.Is(err, relay.ErrEventTypeDeprecated) {
		t.Fatalf("expected ErrEventTypeDeprecated, got %v", err)
	}
}

func TestSendSchemaValidationFailure(t *testing.T) {
	r, _ := setup(t)

	_, err := r.RegisterEventType(ctx(), catalog.WebhookDefinition{
		Name: "validated.event",
		Schema: mustJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"amount": map[string]any{"type": "number"},
			},
			"required": []any{"amount"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Missing required field.
	evt := &event.Event{
		Type:     "validated.event",
		TenantID: "t1",
		Data:     map[string]any{"other": "value"},
	}

	err = r.Send(ctx(), evt)
	if !errors.Is(err, relay.ErrPayloadValidationFailed) {
		t.Fatalf("expected ErrPayloadValidationFailed, got %v", err)
	}
}

func TestSendSchemaValidationSuccess(t *testing.T) {
	r, _ := setup(t)

	_, err := r.RegisterEventType(ctx(), catalog.WebhookDefinition{
		Name: "validated.event",
		Schema: mustJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"amount": map[string]any{"type": "number"},
			},
			"required": []any{"amount"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	createEndpoint(t, r, "t1", []string{"validated.event"})

	evt := &event.Event{
		Type:     "validated.event",
		TenantID: "t1",
		Data:     map[string]any{"amount": 42.5},
	}

	if err := r.Send(ctx(), evt); err != nil {
		t.Fatal(err)
	}
}

func TestSendIdempotencyKeyNoOp(t *testing.T) {
	r, s := setup(t)

	registerType(t, r, "invoice.created")
	createEndpoint(t, r, "t1", []string{"*"})

	evt1 := &event.Event{
		Type:           "invoice.created",
		TenantID:       "t1",
		Data:           map[string]any{"v": 1},
		IdempotencyKey: "idem-1",
	}

	if err := r.Send(ctx(), evt1); err != nil {
		t.Fatal(err)
	}

	// First send should create 1 delivery.
	count1, _ := s.CountPending(ctx())
	if count1 != 1 {
		t.Fatalf("expected 1, got %d", count1)
	}

	// Second send with same key → no-op (no additional deliveries).
	evt2 := &event.Event{
		Type:           "invoice.created",
		TenantID:       "t1",
		Data:           map[string]any{"v": 2},
		IdempotencyKey: "idem-1",
	}

	if err := r.Send(ctx(), evt2); err != nil {
		t.Fatal("expected no-op, got:", err)
	}

	count2, _ := s.CountPending(ctx())
	if count2 != 1 {
		t.Fatalf("expected still 1 (idempotent), got %d", count2)
	}
}

func TestSendNoMatchingEndpoints(t *testing.T) {
	r, s := setup(t)

	registerType(t, r, "invoice.created")
	// No endpoints created.

	evt := &event.Event{
		Type:     "invoice.created",
		TenantID: "t1",
		Data:     map[string]any{},
	}

	if err := r.Send(ctx(), evt); err != nil {
		t.Fatal(err)
	}

	// Event should be persisted even with no endpoints.
	got, err := s.GetEvent(ctx(), evt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "invoice.created" {
		t.Fatalf("expected persisted event")
	}

	pending, _ := s.CountPending(ctx())
	if pending != 0 {
		t.Fatalf("expected 0 pending, got %d", pending)
	}
}

func TestSendFanout(t *testing.T) {
	r, s := setup(t)

	registerType(t, r, "order.completed")

	// Create 5 endpoints matching order.*.
	for i := 0; i < 5; i++ {
		createEndpoint(t, r, "t1", []string{"order.*"})
	}

	evt := &event.Event{
		Type:     "order.completed",
		TenantID: "t1",
		Data:     map[string]any{"order_id": "abc"},
	}

	if err := r.Send(ctx(), evt); err != nil {
		t.Fatal(err)
	}

	pending, _ := s.CountPending(ctx())
	if pending != 5 {
		t.Fatalf("expected 5 deliveries (fan-out), got %d", pending)
	}
}

func TestSendTenantIsolation(t *testing.T) {
	r, s := setup(t)

	registerType(t, r, "invoice.created")
	createEndpoint(t, r, "t1", []string{"*"})
	createEndpoint(t, r, "t2", []string{"*"})

	// Send for tenant t1 should only match t1's endpoint.
	evt := &event.Event{
		Type:     "invoice.created",
		TenantID: "t1",
		Data:     map[string]any{},
	}
	if err := r.Send(ctx(), evt); err != nil {
		t.Fatal(err)
	}

	pending, _ := s.CountPending(ctx())
	if pending != 1 {
		t.Fatalf("expected 1 delivery (tenant isolation), got %d", pending)
	}
}
