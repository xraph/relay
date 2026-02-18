package delivery_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/signature"
)

func newTestEndpoint(url string) *endpoint.Endpoint {
	return &endpoint.Endpoint{
		Entity:     entity.New(),
		ID:         id.NewEndpointID(),
		TenantID:   "tenant-1",
		URL:        url,
		Secret:     "whsec_test_secret_1234567890abcdef1234567890abcdef",
		EventTypes: []string{"test.event"},
		Enabled:    true,
	}
}

func newTestEvent() *event.Event {
	return &event.Event{
		Entity:   entity.New(),
		ID:       id.NewEventID(),
		Type:     "test.event",
		TenantID: "tenant-1",
		Data:     json.RawMessage(`{"hello":"world"}`),
	}
}

func newTestDelivery(epID, evtID id.ID) *delivery.Delivery {
	return &delivery.Delivery{
		Entity:      entity.New(),
		ID:          id.NewDeliveryID(),
		EventID:     evtID,
		EndpointID:  epID,
		State:       delivery.StatePending,
		MaxAttempts: 5,
	}
}

func TestSenderHappyPath(t *testing.T) {
	var receivedHeaders http.Header
	var receivedBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		receivedBody = string(bodyBytes)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sender := delivery.NewSender(5 * time.Second)
	ep := newTestEndpoint(srv.URL)
	evt := newTestEvent()
	del := newTestDelivery(ep.ID, evt.ID)

	result := sender.Send(context.Background(), ep, evt, del)

	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", result.StatusCode)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Response != `{"ok":true}` {
		t.Fatalf("unexpected response: %s", result.Response)
	}
	if result.LatencyMs < 0 {
		t.Fatal("latency should be non-negative")
	}

	// Verify body is marshaled event data.
	expectedBody := `{"hello":"world"}`
	if receivedBody != expectedBody {
		t.Fatalf("body: got %q, want %q", receivedBody, expectedBody)
	}

	// Verify standard headers.
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Fatal("missing Content-Type")
	}
	if receivedHeaders.Get("User-Agent") != "Relay/1.0" {
		t.Fatal("missing User-Agent")
	}
	if receivedHeaders.Get("X-Relay-Event-ID") != evt.ID.String() {
		t.Fatal("missing X-Relay-Event-ID")
	}
	if receivedHeaders.Get("X-Relay-Event-Type") != "test.event" {
		t.Fatal("missing X-Relay-Event-Type")
	}
	if receivedHeaders.Get("X-Relay-Delivery-ID") != del.ID.String() {
		t.Fatal("missing X-Relay-Delivery-ID")
	}

	// Verify HMAC signature.
	sig := receivedHeaders.Get("X-Relay-Signature")
	ts := receivedHeaders.Get("X-Relay-Timestamp")
	if sig == "" || ts == "" {
		t.Fatal("missing signature headers")
	}
	if !strings.HasPrefix(sig, "v1=") {
		t.Fatal("signature should start with v1=")
	}
}

func TestSenderVerifiesSignature(t *testing.T) {
	var receivedSig string
	var receivedTS string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Relay-Signature")
		receivedTS = r.Header.Get("X-Relay-Timestamp")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := delivery.NewSender(5 * time.Second)
	ep := newTestEndpoint(srv.URL)
	evt := newTestEvent()
	del := newTestDelivery(ep.ID, evt.ID)

	sender.Send(context.Background(), ep, evt, del)

	// Parse the timestamp and verify using the signature package.
	var ts int64
	for _, c := range receivedTS {
		ts = ts*10 + int64(c-'0')
	}

	if !signature.Verify(receivedBody, ep.Secret, ts, receivedSig) {
		t.Fatal("signature verification failed")
	}
}

func TestSenderCustomHeaders(t *testing.T) {
	var receivedHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := delivery.NewSender(5 * time.Second)
	ep := newTestEndpoint(srv.URL)
	ep.Headers = map[string]string{
		"X-Custom-Header": "custom-value",
		"Authorization":   "Bearer token123",
	}
	evt := newTestEvent()
	del := newTestDelivery(ep.ID, evt.ID)

	result := sender.Send(context.Background(), ep, evt, del)

	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", result.StatusCode)
	}
	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Fatal("missing custom header")
	}
	if receivedHeaders.Get("Authorization") != "Bearer token123" {
		t.Fatal("missing Authorization header")
	}
}

func TestSenderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Very short timeout.
	sender := delivery.NewSender(50 * time.Millisecond)
	ep := newTestEndpoint(srv.URL)
	evt := newTestEvent()
	del := newTestDelivery(ep.ID, evt.ID)

	result := sender.Send(context.Background(), ep, evt, del)

	if result.StatusCode != 0 {
		t.Fatalf("expected status 0 on timeout, got %d", result.StatusCode)
	}
	if result.Error == "" {
		t.Fatal("expected error on timeout")
	}
	if result.LatencyMs <= 0 {
		t.Fatal("expected positive latency")
	}
}

func TestSenderConnectionRefused(t *testing.T) {
	sender := delivery.NewSender(5 * time.Second)
	ep := newTestEndpoint("http://127.0.0.1:1") // port 1 should refuse connections
	evt := newTestEvent()
	del := newTestDelivery(ep.ID, evt.ID)

	result := sender.Send(context.Background(), ep, evt, del)

	if result.StatusCode != 0 {
		t.Fatalf("expected status 0 on connection refused, got %d", result.StatusCode)
	}
	if result.Error == "" {
		t.Fatal("expected error on connection refused")
	}
}

func TestSenderServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	sender := delivery.NewSender(5 * time.Second)
	ep := newTestEndpoint(srv.URL)
	evt := newTestEvent()
	del := newTestDelivery(ep.ID, evt.ID)

	result := sender.Send(context.Background(), ep, evt, del)

	if result.StatusCode != 500 {
		t.Fatalf("expected 500, got %d", result.StatusCode)
	}
	if result.Response != "internal error" {
		t.Fatalf("unexpected response: %s", result.Response)
	}
}
