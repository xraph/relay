package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/relay/api"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/store/memory"
)

// testServer creates a Handler backed by a memory store and returns the test server.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()

	s := memory.New()
	logger := slog.Default()
	cat := catalog.NewCatalog(s, catalog.Config{}, logger)
	epSvc := endpoint.NewService(s, logger)
	dlqSvc := dlq.NewService(s, logger)

	h := api.NewHandler(s, cat, epSvc, dlqSvc, logger)
	return httptest.NewServer(h)
}

func doJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

// --- Event Types ---

func TestEventTypes_CRUD(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create
	resp := doJSON(t, "POST", srv.URL+"/event-types", map[string]any{
		"name":        "order.created",
		"description": "Fired when an order is created",
		"version":     "2025-01-01",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}
	var et map[string]any
	decodeBody(t, resp, &et)
	def, _ := et["definition"].(map[string]any)
	if def == nil || def["name"] != "order.created" {
		t.Fatalf("expected definition.name order.created, got %v", et)
	}

	// Get by name
	resp = doJSON(t, "GET", srv.URL+"/event-types/order.created", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// List
	resp = doJSON(t, "GET", srv.URL+"/event-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", resp.StatusCode)
	}
	var list []map[string]any
	decodeBody(t, resp, &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 event type, got %d", len(list))
	}

	// Delete (soft-delete marks as deprecated)
	resp = doJSON(t, "DELETE", srv.URL+"/event-types/order.created", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Get after soft-delete returns 200 with deprecated=true
	resp = doJSON(t, "GET", srv.URL+"/event-types/order.created", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get after delete: expected 200, got %d", resp.StatusCode)
	}
	var deletedET map[string]any
	decodeBody(t, resp, &deletedET)
	if deletedET["deprecated"] != true {
		t.Fatalf("expected deprecated=true, got %v", deletedET["deprecated"])
	}
}

func TestEventTypes_CreateMissingName(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/event-types", map[string]any{
		"description": "no name",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Endpoints ---

func TestEndpoints_CRUD(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create
	resp := doJSON(t, "POST", srv.URL+"/endpoints", map[string]any{
		"tenant_id":   "tenant-1",
		"url":         "https://example.com/webhook",
		"event_types": []string{"order.*"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}
	var ep map[string]any
	decodeBody(t, resp, &ep)
	epID, ok := ep["id"].(string)
	if !ok || epID == "" {
		t.Fatal("expected non-empty endpoint ID")
	}

	// Get
	resp = doJSON(t, "GET", srv.URL+"/endpoints/"+epID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// List
	resp = doJSON(t, "GET", srv.URL+"/endpoints?tenant_id=tenant-1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", resp.StatusCode)
	}
	var eps []map[string]any
	decodeBody(t, resp, &eps)
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}

	// Update
	resp = doJSON(t, "PUT", srv.URL+"/endpoints/"+epID, map[string]any{
		"url":         "https://example.com/updated",
		"event_types": []string{"order.*", "invoice.*"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", resp.StatusCode)
	}
	var updated map[string]any
	decodeBody(t, resp, &updated)
	if updated["url"] != "https://example.com/updated" {
		t.Fatalf("expected updated URL, got %v", updated["url"])
	}

	// Disable
	resp = doJSON(t, "PATCH", srv.URL+"/endpoints/"+epID+"/disable", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("disable: expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Enable
	resp = doJSON(t, "PATCH", srv.URL+"/endpoints/"+epID+"/enable", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("enable: expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Rotate secret
	resp = doJSON(t, "POST", srv.URL+"/endpoints/"+epID+"/rotate-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate: expected 200, got %d", resp.StatusCode)
	}
	var secretResp map[string]string
	decodeBody(t, resp, &secretResp)
	if secretResp["secret"] == "" {
		t.Fatal("expected non-empty secret")
	}

	// Delete
	resp = doJSON(t, "DELETE", srv.URL+"/endpoints/"+epID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Get deleted → 404
	resp = doJSON(t, "GET", srv.URL+"/endpoints/"+epID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted: expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestEndpoints_ListRequiresTenantID(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/endpoints", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Events ---

func TestEvents_CreateAndGet(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create
	resp := doJSON(t, "POST", srv.URL+"/events", map[string]any{
		"type":      "order.created",
		"tenant_id": "tenant-1",
		"data":      map[string]any{"order_id": "123"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create: expected 202, got %d", resp.StatusCode)
	}
	var evt map[string]any
	decodeBody(t, resp, &evt)
	evtID, ok := evt["id"].(string)
	if !ok || evtID == "" {
		t.Fatal("expected non-empty event ID")
	}

	// Get
	resp = doJSON(t, "GET", srv.URL+"/events/"+evtID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// List
	resp = doJSON(t, "GET", srv.URL+"/events", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", resp.StatusCode)
	}
	var events []map[string]any
	decodeBody(t, resp, &events)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestEvents_CreateMissingFields(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Missing type
	resp := doJSON(t, "POST", srv.URL+"/events", map[string]any{
		"tenant_id": "tenant-1",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing type, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Missing tenant_id
	resp = doJSON(t, "POST", srv.URL+"/events", map[string]any{
		"type": "order.created",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing tenant_id, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Stats ---

func TestStats(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/stats", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats: expected 200, got %d", resp.StatusCode)
	}
	var stats map[string]any
	decodeBody(t, resp, &stats)

	if _, ok := stats["pending_deliveries"]; !ok {
		t.Fatal("expected pending_deliveries in response")
	}
	if _, ok := stats["dlq_size"]; !ok {
		t.Fatal("expected dlq_size in response")
	}
}

// --- DLQ ---

func TestDLQ_ListEmpty(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/dlq", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list dlq: expected 200, got %d", resp.StatusCode)
	}
	var entries []map[string]any
	decodeBody(t, resp, &entries)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestDLQ_ReplayNotFound(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/dlq/dlq_nonexistent/replay", nil)
	// The ID will fail parsing since it's not a valid DLQ ID format.
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("replay nonexistent: expected 400 or 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDLQ_BulkReplayBadBody(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/dlq/replay", map[string]any{
		"from": "not-a-date",
		"to":   "2025-01-01T00:00:00Z",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Deliveries ---

func TestDeliveries_ListEmpty(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// First create an endpoint to list deliveries for.
	resp := doJSON(t, "POST", srv.URL+"/endpoints", map[string]any{
		"tenant_id":   "tenant-1",
		"url":         "https://example.com/webhook",
		"event_types": []string{"*"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create endpoint: expected 201, got %d", resp.StatusCode)
	}
	var ep map[string]any
	decodeBody(t, resp, &ep)
	epID := ep["id"].(string)

	resp = doJSON(t, "GET", srv.URL+"/endpoints/"+epID+"/deliveries", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list deliveries: expected 200, got %d", resp.StatusCode)
	}
	var deliveries []map[string]any
	decodeBody(t, resp, &deliveries)
	if len(deliveries) != 0 {
		t.Fatalf("expected 0 deliveries, got %d", len(deliveries))
	}
}

// --- Invalid IDs ---

func TestEndpoint_InvalidID(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/endpoints/not-a-valid-id", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestEvent_InvalidID(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/events/not-a-valid-id", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
