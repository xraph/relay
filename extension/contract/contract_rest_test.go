package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// sent registers invoice.paid, creates an endpoint for tenant, and sends one
// event through events.send. It returns the event id.
func (h *harness) sent() string {
	const tenant = "acme"
	h.t.Helper()
	if _, err := h.r.RegisterEventType(context.Background(), catalog.WebhookDefinition{
		Name: "invoice.paid", Version: "1",
	}); err != nil {
		h.t.Fatalf("register type: %v", err)
	}
	h.create(tenant, "receiver")
	out := h.command("events.send", map[string]any{
		"type": "invoice.paid", "tenantId": tenant, "data": map[string]any{"amount": 42},
	})
	evtID, _ := out["id"].(string)
	if evtID == "" {
		h.t.Fatalf("events.send returned no id: %v", out)
	}
	return evtID
}

func details(err error) map[string]any {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Details
	}
	return nil
}

func TestDeliveriesListReturnsTheLogWithTheFieldsThePageReads(t *testing.T) {
	h := newHarness(t)
	h.sent()

	out := h.query("deliveries.list", map[string]any{"tenantId": "acme"})
	rows, _ := out["deliveries"].([]any)
	if len(rows) != 1 {
		t.Fatalf("got %d deliveries, want 1: %v", len(rows), out)
	}
	row := rows[0].(map[string]any)
	for field, want := range map[string]any{
		"eventType": "invoice.paid", "tenantId": "acme", "state": "pending",
		"endpointUrl": "https://receiver.example/hook", "attemptCount": float64(0),
	} {
		if row[field] != want {
			t.Errorf("%s = %v, want %v", field, row[field], want)
		}
	}
	if out["complete"] != true {
		t.Errorf("complete = %v, want true on the memory store", out["complete"])
	}
	if _, ok := out["nextCursor"]; ok {
		t.Errorf("a single page carries a cursor: %v", out["nextCursor"])
	}
}

func TestDeliveriesListRefusesWhatItCannotRead(t *testing.T) {
	h := newHarness(t)
	for name, params := range map[string]map[string]any{
		"status class": {"statusClass": "3xx"},
		"cursor":       {"cursor": "not-a-cursor"},
		"endpoint id":  {"endpointId": "not-an-id"},
	} {
		if _, err := h.call(contract.KindQuery, "deliveries.list", params); codeOf(err) != contract.CodeBadRequest {
			t.Errorf("%s: code %q (%v), want BAD_REQUEST", name, codeOf(err), err)
		}
	}
}

func TestDeliveriesDetailCarriesItsAttemptsInOrder(t *testing.T) {
	h := newHarness(t)
	evtID := h.sent()
	rows := h.query("deliveries.list", map[string]any{"tenantId": "acme"})["deliveries"].([]any)
	delID := rows[0].(map[string]any)["id"].(string)
	parsed, _ := id.ParseDeliveryID(delID)
	next := time.Now().UTC().Add(time.Minute)
	for _, a := range []*delivery.Attempt{
		{ID: id.NewAttemptID(), DeliveryID: parsed, AttemptNum: 2, StatusCode: 200, Outcome: delivery.OutcomeDelivered, AttemptedAt: time.Now().UTC()},
		{ID: id.NewAttemptID(), DeliveryID: parsed, AttemptNum: 1, StatusCode: 503, Outcome: delivery.OutcomeRetry,
			NextAttemptAt: &next, Response: "busy", AttemptedAt: time.Now().UTC().Add(-time.Minute)},
	} {
		if err := h.store.RecordAttempt(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}

	out := h.query("deliveries.detail", map[string]any{"id": delID})
	if out["eventId"] != evtID || out["endpointEnabled"] != true {
		t.Errorf("detail header wrong: %v", out)
	}
	atts, _ := out["attempts"].([]any)
	if len(atts) != 2 {
		t.Fatalf("got %d attempts, want 2", len(atts))
	}
	first, second := atts[0].(map[string]any), atts[1].(map[string]any)
	if first["attemptNum"] != float64(1) || first["outcome"] != "retry" || first["response"] != "busy" || first["nextAttemptAt"] == nil {
		t.Errorf("first attempt %v", first)
	}
	if second["outcome"] != "delivered" || second["nextAttemptAt"] != nil {
		t.Errorf("second attempt %v", second)
	}

	if _, err := h.call(contract.KindQuery, "deliveries.detail", map[string]any{"id": id.NewDeliveryID().String()}); codeOf(err) != contract.CodeNotFound {
		t.Errorf("missing delivery: %q, want NOT_FOUND", codeOf(err))
	}
}

func TestEventsSendNamesTheFieldItRefuses(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(contract.KindCommand, "events.send", map[string]any{"type": "never.registered", "tenantId": "acme"})
	if codeOf(err) != contract.CodeBadRequest || details(err)["field"] != "type" {
		t.Fatalf("unregistered type: %v (details %v), want BAD_REQUEST on type", err, details(err))
	}
	_, err = h.call(contract.KindCommand, "events.send", map[string]any{"type": "x"})
	if details(err)["field"] != "tenant_id" {
		t.Errorf("missing tenant: details %v, want field tenant_id", details(err))
	}
}

// relay.Send reports a reused key as success without storing anything, so the
// id it assigned names nothing. The response says so.
func TestEventsSendReportsADuplicateKey(t *testing.T) {
	h := newHarness(t)
	h.sent()
	send := func() map[string]any {
		return h.command("events.send", map[string]any{"type": "invoice.paid", "tenantId": "acme", "idempotencyKey": "k-1"})
	}
	if first := send(); first["duplicate"] == true || first["id"] == nil {
		t.Fatalf("first send %v, want a stored event", first)
	}
	if second := send(); second["duplicate"] != true || second["id"] != nil {
		t.Errorf("second send %v, want duplicate with no id", second)
	}
}

func TestEventsDetailListsTheDeliveriesItFannedOut(t *testing.T) {
	h := newHarness(t)
	evtID := h.sent()
	out := h.query("events.detail", map[string]any{"id": evtID})
	if dels, _ := out["deliveries"].([]any); len(dels) != 1 {
		t.Errorf("got %v deliveries, want 1", out["deliveries"])
	}
	if data, _ := out["data"].(map[string]any); data["amount"] != float64(42) {
		t.Errorf("data %v, want the payload that was sent", out["data"])
	}
	list := h.query("events.list", map[string]any{"type": "invoice.paid"})
	if evs, _ := list["events"].([]any); len(evs) != 1 {
		t.Errorf("events.list %v", list)
	}
}

func TestEventTypesRegisterMatchAndDeprecate(t *testing.T) {
	h := newHarness(t)
	schema := json.RawMessage(`{"type":"object","required":["amount"]}`)
	h.command("eventTypes.register", map[string]any{"name": "invoice.paid", "version": "1", "schema": schema, "example": map[string]any{"amount": 1}})
	h.command("eventTypes.register", map[string]any{"name": "customer.created", "version": "1"})

	detail := h.query("eventTypes.detail", map[string]any{"name": "invoice.paid"})
	if detail["hasSchema"] != true || detail["schema"] == nil || detail["example"] == nil {
		t.Errorf("detail %v, want schema and example", detail)
	}
	matched := h.query("eventTypes.match", map[string]any{"pattern": "invoice.*"})["types"].([]any)
	if len(matched) != 1 || matched[0].(map[string]any)["name"] != "invoice.paid" {
		t.Errorf("match %v, want only invoice.paid", matched)
	}

	// The schema is enforced on send.
	h.create("acme", "r")
	if _, err := h.call(contract.KindCommand, "events.send", map[string]any{"type": "invoice.paid", "tenantId": "acme", "data": map[string]any{}}); codeOf(err) != contract.CodeBadRequest {
		t.Errorf("payload missing a required field: %q, want BAD_REQUEST", codeOf(err))
	}

	h.command("eventTypes.deprecate", map[string]any{"name": "customer.created"})
	if n := len(h.query("eventTypes.list", map[string]any{})["types"].([]any)); n != 1 {
		t.Errorf("active types %d, want 1 after deprecating one", n)
	}
	if n := len(h.query("eventTypes.list", map[string]any{"includeDeprecated": true})["types"].([]any)); n != 2 {
		t.Errorf("all types %d, want 2", n)
	}
	_, err := h.call(contract.KindCommand, "eventTypes.register", map[string]any{"name": "bad", "schema": "not an object"})
	if codeOf(err) != contract.CodeBadRequest || details(err)["field"] != "schema" {
		t.Errorf("a schema that is not an object: %v, want BAD_REQUEST on schema", err)
	}
}

func (h *harness) pushDLQ(failedAt time.Time) *dlq.Entry {
	const tenant = "acme"
	h.t.Helper()
	e := &dlq.Entry{Entity: entity.New(), ID: id.NewDLQID(), DeliveryID: id.NewDeliveryID(), EventID: id.NewEventID(),
		EndpointID: id.NewEndpointID(), EventType: "invoice.paid", TenantID: tenant, URL: "https://receiver.example/hook",
		Payload: json.RawMessage(`{"amount":42}`), Error: "503", AttemptCount: 5, LastStatusCode: 503, FailedAt: failedAt}
	if err := h.store.Push(context.Background(), e); err != nil {
		h.t.Fatal(err)
	}
	return e
}

func TestDLQReplayIsRefusedTheSecondTime(t *testing.T) {
	h := newHarness(t)
	e := h.pushDLQ(time.Now().UTC().Add(-time.Hour))

	detail := h.query("dlq.detail", map[string]any{"id": e.ID.String()})
	if p, _ := detail["payload"].(map[string]any); p["amount"] != float64(42) {
		t.Errorf("payload %v, want the JSON that would be sent", detail["payload"])
	}
	h.command("dlq.replay", map[string]any{"id": e.ID.String()})
	if _, err := h.call(contract.KindCommand, "dlq.replay", map[string]any{"id": e.ID.String()}); codeOf(err) != contract.CodeConflict {
		t.Errorf("second replay: %q, want CONFLICT", codeOf(err))
	}
	replayed := true
	rows := h.query("dlq.list", map[string]any{"replayed": replayed})["entries"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["replayedAt"] == nil {
		t.Errorf("replayed filter %v, want the one entry, marked", rows)
	}
}

func TestDLQBulkReplayShowsItsCountFirst(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	h.pushDLQ(now.Add(-2 * time.Hour))
	h.pushDLQ(now.Add(-90 * time.Minute))
	old := h.pushDLQ(now.Add(-72 * time.Hour)) // outside the window
	window := map[string]any{"from": now.Add(-3 * time.Hour), "to": now}

	preview := h.query("dlq.bulkPreview", window)
	if preview["replayable"] != float64(2) || preview["alreadyReplayed"] != float64(0) {
		t.Fatalf("preview %v, want 2 replayable", preview)
	}
	if got := h.command("dlq.replayBulk", window); got["replayed"] != float64(2) {
		t.Errorf("replayBulk %v, want 2", got)
	}
	if again := h.query("dlq.bulkPreview", window); again["replayable"] != float64(0) || again["alreadyReplayed"] != float64(2) {
		t.Errorf("preview after replay %v", again)
	}
	if _, err := h.call(contract.KindCommand, "dlq.replayBulk", map[string]any{"from": now}); codeOf(err) != contract.CodeBadRequest {
		t.Errorf("half a window: %q, want BAD_REQUEST", codeOf(err))
	}

	purged := h.command("dlq.purge", map[string]any{"before": now.Add(-24 * time.Hour)})
	if purged["purged"] != float64(1) {
		t.Errorf("purge %v, want 1", purged)
	}
	if _, err := h.call(contract.KindQuery, "dlq.detail", map[string]any{"id": old.ID.String()}); codeOf(err) != contract.CodeNotFound {
		t.Errorf("purged entry: %q, want NOT_FOUND", codeOf(err))
	}
}

func TestOverviewAndSettings(t *testing.T) {
	h := newHarness(t)
	h.sent()
	h.pushDLQ(time.Now().UTC())
	stats := h.query("overview.stats", map[string]any{})
	for field, want := range map[string]float64{"eventTypes": 1, "endpoints": 1, "pending": 1, "deadLetters": 1} {
		if stats[field] != want {
			t.Errorf("%s = %v, want %v", field, stats[field], want)
		}
	}
	cfg := h.query("settings.config", map[string]any{})
	if cfg["maxRetries"] == nil || cfg["retryScheduleMs"] == nil {
		t.Errorf("settings %v", cfg)
	}
}
